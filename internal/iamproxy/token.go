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
	"strings"
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
	Leeway       time.Duration
	Now          func() time.Time
}

// Verifier — проверка токенов клиентов.
type Verifier struct {
	cfg TokenConfig

	mu        sync.Mutex
	keys      map[string]*ecdsa.PublicKey
	fetchedAt time.Time
	forcedAt  time.Time
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

// subjectOf — субъект IAM токена: вид и идентификатор принципала kaname;
// без них — по виду идентификатора sub (sva… — учётка, usr… — человек).
// Идентификатор с разделителями модели прав субъектом не становится.
func subjectOf(c jwt.MapClaims) (string, bool) {
	typ, _ := c[claimPrincipalType].(string)
	id, _ := c[claimPrincipalID].(string)
	if typ == "" && id == "" {
		sub, _ := c["sub"].(string)
		switch {
		case strings.HasPrefix(sub, "sva"):
			typ, id = "service_account", sub
		case strings.HasPrefix(sub, "usr"):
			typ, id = "user", sub
		}
	}
	if (typ != "user" && typ != "service_account") || !kaname.ValidID(id) {
		return "", false
	}
	return typ + ":" + id, true
}

// errKeyNotPublished — набор ключей получен, но такого kid в нём нет:
// порок токена, а не недоступность издателя.
var errKeyNotPublished = errors.New("key is not published by the issuer")

// key — открытый ключ по kid: из снимка, при неизвестном kid — внеочередное
// обновление не чаще RefreshEvery (ротация ключей издателя).
func (v *Verifier) key(ctx context.Context, kid string) (*ecdsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.cfg.Now()
	stale := v.keys == nil || now.Sub(v.fetchedAt) >= v.cfg.KeysTTL
	if !stale {
		if k, ok := v.keys[kid]; ok {
			return k, nil
		}
		if now.Sub(v.forcedAt) < v.cfg.RefreshEvery {
			return nil, fmt.Errorf("%w: %q", errKeyNotPublished, kid)
		}
		v.forcedAt = now
	}
	keys, err := v.fetch(ctx)
	if err != nil {
		if v.keys != nil {
			// Издатель не ответил на обновление: действует прежний снимок.
			if k, ok := v.keys[kid]; ok {
				return k, nil
			}
			if !stale {
				return nil, fmt.Errorf("%w: %q", errKeyNotPublished, kid)
			}
		}
		return nil, err
	}
	v.keys, v.fetchedAt = keys, now
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("%w: %q", errKeyNotPublished, kid)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch key set: %w", err)
	}
	defer resp.Body.Close()
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
