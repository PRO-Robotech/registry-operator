// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package iamproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Глаголы модели прав, которые спрашивает прокси.
const (
	VerbGet    = "v_get"
	VerbList   = "v_list"
	VerbUpdate = "v_update"
	VerbDelete = "v_delete"
)

// actionOf — действие каталога прав для глагола и типа объекта: kaname
// требует его в запросе, а отношение берёт из required_relation.
func actionOf(verb, objType string) string {
	res := "repositories"
	if objType == "registry_registry" {
		res = "registries"
	}
	return "registry." + res + "." + strings.TrimPrefix(verb, "v_")
}

var (
	// ErrUnauthenticated — kaname не признал токен клиента.
	ErrUnauthenticated = errors.New("kaname did not authenticate the token")
	// ErrCheckUnavailable — решения нет: kaname не ответил по существу.
	ErrCheckUnavailable = errors.New("authorization check is unavailable")
)

// Checker — вопрос о праве субъекта токеном самого субъекта.
type Checker interface {
	Check(ctx context.Context, id Identity, verb, object string) (bool, error)
}

// checkPath — AuthorizeService.Check публичного REST-фронта kaname: та же
// дверь, что у всякого клиента API с одним токеном. Публичный gRPC kaname
// требует проверенный клиентский лист (corelib grpcsrv/tls.go,
// RequireAndVerifyClientCert), и личность там — лист, а не токен.
const checkPath = "/iam/v1/authorize:check"

// KanameChecker — публичный AuthorizeService.Check kaname. Спрашивает
// всегда о субъекте токена и его же токеном: своих прав у прокси нет, и
// спросить о чужом субъекте он не может по построению.
type KanameChecker struct {
	url     string
	http    *http.Client
	timeout time.Duration
}

// NewKanameChecker — клиент публичного REST-фронта kaname; base — https
// без пути, client доверяет центру kaname.
func NewKanameChecker(base string, client *http.Client, timeout time.Duration) (*KanameChecker, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return nil, fmt.Errorf("kaname url %q must be an https URL without a path", base)
	}
	if client == nil || timeout <= 0 {
		return nil, errors.New("kaname checker needs an http client and a deadline")
	}
	return &KanameChecker{url: strings.TrimSuffix(base, "/") + checkPath, http: client, timeout: timeout}, nil
}

type checkRequest struct {
	Subject          string      `json:"subject"`
	Resource         resourceRef `json:"resource"`
	Action           string      `json:"action"`
	RequiredRelation string      `json:"requiredRelation"`
}

type resourceRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Check — право субъекта токена на объект.
func (k *KanameChecker) Check(ctx context.Context, id Identity, verb, object string) (bool, error) {
	typ, oid, ok := strings.Cut(object, ":")
	if !ok {
		return false, fmt.Errorf("object %q is not <type>:<id>", object)
	}
	body, err := json.Marshal(checkRequest{
		Subject: id.Subject, Resource: resourceRef{Type: typ, ID: oid},
		Action: actionOf(verb, typ), RequiredRelation: verb,
	})
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, k.timeout)
	defer cancel()
	// Адрес — флаг запуска (только https), а не вход запроса.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.url, bytes.NewReader(body)) //nolint:gosec // см. выше.
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+id.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.http.Do(req) //nolint:gosec // адрес — флаг запуска, только https.
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrCheckUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		var out struct {
			Allowed bool `json:"allowed"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
			return false, fmt.Errorf("%w: undecodable answer", ErrCheckUnavailable)
		}
		return out.Allowed, nil
	case http.StatusUnauthorized:
		return false, ErrUnauthenticated
	case http.StatusForbidden:
		// Вопрос о себе отвергнут только тогда, когда kaname видит в токене
		// другого субъекта: решения о праве нет, есть отказ.
		return false, nil
	default:
		return false, fmt.Errorf("%w: status %d", ErrCheckUnavailable, resp.StatusCode)
	}
}

// CachedChecker — кэш разрешений чтения: ключ — хэш токена, объект и
// глагол, срок не больше TTL. Глаголы записи идут мимо кэша: отзыв права
// на запись действует со следующего запроса. Отказы не кэшируются: новая
// выдача и только что записанный репозиторий видны со следующего запроса,
// а отказ ничего не стоит переспросить.
type CachedChecker struct {
	Inner Checker
	TTL   time.Duration
	// MaxEntries — предел записей: переполнение снимает кэш целиком.
	MaxEntries int
	Now        func() time.Time

	mu      sync.Mutex
	entries map[string]cached
}

type cached struct {
	allowed bool
	until   time.Time
}

// cacheable — глагол чтения.
func cacheable(verb string) bool { return verb == VerbGet || verb == VerbList }

// Check — вердикт из кэша либо от Inner.
func (c *CachedChecker) Check(ctx context.Context, id Identity, verb, object string) (bool, error) {
	if !cacheable(verb) || c.TTL <= 0 {
		return c.Inner.Check(ctx, id, verb, object)
	}
	sum := sha256.Sum256([]byte(id.Token))
	key := hex.EncodeToString(sum[:]) + "|" + verb + "|" + object
	now := c.Now()
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && now.Before(e.until) {
		c.mu.Unlock()
		return e.allowed, nil
	}
	c.mu.Unlock()
	allowed, err := c.Inner.Check(ctx, id, verb, object)
	if err != nil || !allowed {
		return false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= c.MaxEntries {
		c.entries = map[string]cached{}
	}
	c.entries[key] = cached{allowed: allowed, until: now.Add(c.TTL)}
	return allowed, nil
}
