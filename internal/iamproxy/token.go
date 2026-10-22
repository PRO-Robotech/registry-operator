// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package iamproxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/PRO-Robotech/registry-operator/internal/kaname"
)

// Утверждения kaname, называющие принципала токена.
const (
	claimPrincipalType = "kaname_principal_type"
	claimPrincipalID   = "kaname_principal_id"
)

var (
	// ErrToken — токен не прошёл проверку: подпись, издатель, аудитория,
	// срок, тип либо принципал.
	ErrToken = errors.New("token is not valid for this registry")
	// ErrKeys — набор ключей издателя недоступен: решения нет.
	ErrKeys = errors.New("issuer key set is unavailable")
)

// Identity — проверенный токен: субъект IAM и сам токен (его прокси
// предъявляет kaname, спрашивая о праве субъекта).
type Identity struct {
	Subject string
	Token   string
}

// TokenConfig — проверка токенов клиентов.
type TokenConfig struct {
	// JWKSURL — набор ключей издателя; только https.
	JWKSURL string
	// HTTPClient — клиент с центром доверия набора ключей.
	HTTPClient *http.Client
	Issuer     string
	Audience   string
	// Algorithms — допустимые подписи (ES256).
	Algorithms []string
	// Types — допустимые значения заголовка typ.
	Types []string
	// KeysTTL — срок снимка набора ключей; RefreshEvery — не чаще этого
	// внеочередное обновление по неизвестному kid.
	KeysTTL      time.Duration
	RefreshEvery time.Duration
	// FetchTimeout — срок одного обращения за набором ключей (0 — 10 с).
	FetchTimeout time.Duration
	Leeway       time.Duration
	Now          func() time.Time
}

// Отступ обновления набора ключей после неудачи: удвоение от RefreshEvery
// до потолка.
const maxKeysBackoff = 5 * time.Minute

// Verifier — проверка токенов клиентов. Набор ключей обновляется одним
// обращением на всех и вне мьютекса: запрос с ключом из прежнего снимка
// обновления не ждёт, запрос с неизвестным ключом ждёт его в пределах своего
// срока; после неудачи следующее обновление — не раньше отступа.
type Verifier struct {
	cfg TokenConfig

	mu        sync.Mutex
	keys      map[string]*ecdsa.PublicKey
	fetchedAt time.Time
	// nextAt — не раньше этого — следующее обращение к издателю.
	nextAt   time.Time
	failures int
	lastErr  error
	// inflight — идущее обновление; закрывается по его исходу.
	inflight chan struct{}
}

// NewVerifier — проверка с объявленной конфигурацией; набор ключей только
// по https.
func NewVerifier(cfg TokenConfig) (*Verifier, error) {
	u, err := url.Parse(cfg.JWKSURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("jwks url %q must be an https URL", cfg.JWKSURL)
	}
	if cfg.HTTPClient == nil || cfg.Issuer == "" || cfg.Audience == "" || len(cfg.Algorithms) == 0 || len(cfg.Types) == 0 {
		return nil, errors.New("token verification needs an http client, issuer, audience, algorithms and token types")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.FetchTimeout <= 0 {
		cfg.FetchTimeout = 10 * time.Second
	}
	return &Verifier{cfg: cfg}, nil
}

// Verify — проверенная личность токена либо ErrToken / ErrKeys.
func (v *Verifier) Verify(ctx context.Context, raw string) (Identity, error) {
	claims := jwt.MapClaims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods(v.cfg.Algorithms),
		jwt.WithExpirationRequired(),
		jwt.WithIssuer(v.cfg.Issuer),
		jwt.WithAudience(v.cfg.Audience),
		jwt.WithLeeway(v.cfg.Leeway),
		jwt.WithTimeFunc(v.cfg.Now),
	)
	var keysErr error
	tok, err := parser.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("token names no key")
		}
		k, err := v.key(ctx, kid)
		if err != nil && !errors.Is(err, errKeyNotPublished) {
			keysErr = err
		}
		return k, err
	})
	if keysErr != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrKeys, keysErr)
	}
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrToken, err)
	}
	if typ, _ := tok.Header["typ"].(string); !slices.Contains(v.cfg.Types, typ) {
		return Identity{}, fmt.Errorf("%w: token type %q", ErrToken, typ)
	}
	sub, ok := subjectOf(claims)
	if !ok {
		return Identity{}, fmt.Errorf("%w: token names no principal", ErrToken)
	}
	return Identity{Subject: sub, Token: raw}, nil
}

// subjectOf — субъект IAM токена: вид и идентификатор принципала kaname,
// те же утверждения, по которым kaname называет вызывающего на своём API.
// Токен без них kaname не примет, и прокси не выводит субъекта из sub:
// аутентификация одна на API и на прокси. Идентификатор с разделителями
// модели прав субъектом не становится.
func subjectOf(c jwt.MapClaims) (string, bool) {
	typ, _ := c[claimPrincipalType].(string)
	id, _ := c[claimPrincipalID].(string)
	if (typ != "user" && typ != "service_account") || !kaname.ValidID(id) {
		return "", false
	}
	return typ + ":" + id, true
}

// Ready — набор ключей издателя получен хотя бы раз; пока нет — попытка
// получить его (не раньше отступа). Прокси без ключей не проверит ни одного
// токена, и под не должен принимать трафик.
func (v *Verifier) Ready(ctx context.Context) bool {
	v.mu.Lock()
	if v.keys != nil {
		v.mu.Unlock()
		return true
	}
	if v.cfg.Now().Before(v.nextAt) {
		v.mu.Unlock()
		return false
	}
	wait := v.refreshLocked(ctx)
	v.mu.Unlock()
	select {
	case <-wait:
	case <-ctx.Done():
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.keys != nil
}

// errKeyNotPublished — набор ключей получен, но такого kid в нём нет:
// порок токена, а не недоступность издателя.
var errKeyNotPublished = errors.New("key is not published by the issuer")

// key — открытый ключ по kid. Снимок свеж и ключ в нём — сразу. Иначе —
// обновление (протухший снимок либо неизвестный kid), если отступ истёк:
// ключ из прежнего снимка отдаётся, не дожидаясь обновления (оно идёт в
// стороне), неизвестный kid ждёт исхода обновления. После обновления —
// решение по тому, что есть, без второго обращения.
func (v *Verifier) key(ctx context.Context, kid string) (*ecdsa.PublicKey, error) {
	for refreshed := false; ; refreshed = true {
		v.mu.Lock()
		now := v.cfg.Now()
		stale := v.keys == nil || now.Sub(v.fetchedAt) >= v.cfg.KeysTTL
		k, known := v.keys[kid]
		if known && !stale {
			v.mu.Unlock()
			return k, nil
		}
		if refreshed || now.Before(v.nextAt) {
			err := v.verdictLocked(kid, stale, known)
			v.mu.Unlock()
			return k, err
		}
		wait := v.refreshLocked(ctx)
		v.mu.Unlock()
		if known {
			// Издатель обновляется в стороне; прежний снимок действует.
			return k, nil
		}
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for the key set: %w", ctx.Err())
		}
	}
}

// verdictLocked — решение без нового обращения к издателю.
func (v *Verifier) verdictLocked(kid string, stale, known bool) error {
	switch {
	case known:
		// Издатель не ответил на обновление: действует прежний снимок.
		return nil
	case v.keys != nil && !stale:
		return fmt.Errorf("%w: %q", errKeyNotPublished, kid)
	case v.lastErr != nil:
		return v.lastErr
	case v.keys != nil:
		return fmt.Errorf("%w: %q", errKeyNotPublished, kid)
	}
	return errors.New("key set is not fetched yet")
}

// refreshLocked — канал исхода обновления; обновления нет — запускает его.
// Обращение идёт вне мьютекса, в контексте без отмены запроса (запрос,
// ушедший раньше, не обрывает обновление для остальных) со своим сроком.
func (v *Verifier) refreshLocked(ctx context.Context) <-chan struct{} {
	if v.inflight != nil {
		return v.inflight
	}
	done := make(chan struct{})
	v.inflight = done
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v.cfg.FetchTimeout)
	go func() {
		defer cancel()
		keys, err := v.fetch(fctx)
		v.mu.Lock()
		defer v.mu.Unlock()
		now := v.cfg.Now()
		if err != nil {
			v.failures++
			v.lastErr = err
			v.nextAt = now.Add(backoff(v.cfg.RefreshEvery, v.failures))
		} else {
			v.keys, v.fetchedAt = keys, now
			v.failures, v.lastErr = 0, nil
			v.nextAt = now.Add(v.cfg.RefreshEvery)
		}
		v.inflight = nil
		close(done)
	}()
	return done
}

// backoff — отступ после n неудач подряд: base, 2·base, 4·base… до
// maxKeysBackoff.
func backoff(base time.Duration, n int) time.Duration {
	d := max(base, time.Second)
	for i := 1; i < n && d < maxKeysBackoff; i++ {
		d *= 2
	}
	return min(d, maxKeysBackoff)
}

type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (v *Verifier) fetch(ctx context.Context) (map[string]*ecdsa.PublicKey, error) {
	// Адрес набора ключей — флаг запуска (только https), а не вход запроса.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil) //nolint:gosec // см. выше.
	if err != nil {
		return nil, err
	}
	resp, err := v.cfg.HTTPClient.Do(req) //nolint:gosec // адрес — флаг запуска, только https.
	if err != nil {
		return nil, fmt.Errorf("fetch key set: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch key set: status %d", resp.StatusCode)
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("decode key set: %w", err)
	}
	out := map[string]*ecdsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" || k.Kid == "" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != "ES256") {
			continue
		}
		x, errX := base64.RawURLEncoding.DecodeString(k.X)
		y, errY := base64.RawURLEncoding.DecodeString(k.Y)
		if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
			continue
		}
		// Разбор проверяет, что точка лежит на кривой.
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			continue
		}
		out[k.Kid] = pub
	}
	if len(out) == 0 {
		return nil, errors.New("key set holds no usable ES256 key")
	}
	return out, nil
}
