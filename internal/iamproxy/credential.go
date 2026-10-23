// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package iamproxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PRO-Robotech/corelib/credsecret"

	"github.com/PRO-Robotech/registry-operator/internal/kaname"
)

// ErrIdentityUnavailable — авторитет не дал пригодного ответа; это не отказ удостоверению.
var ErrIdentityUnavailable = errors.New("credential authority is unavailable")

// CredentialVerifier выбирает полосу по марке удостоверения, не по отказу JWT.
// Opaque проверяется публичным standalone Kaname на каждом запросе; собственных прав нет.
type CredentialVerifier struct {
	jwt     TokenVerifier
	url     string
	http    *http.Client
	timeout time.Duration
}

// NewCredentialVerifier использует тот же доверенный origin, что public Check.
func NewCredentialVerifier(jwt TokenVerifier, base string, client *http.Client, timeout time.Duration) (*CredentialVerifier, error) {
	checker, err := NewKanameChecker(base, client, timeout)
	if err != nil {
		return nil, err
	}
	if jwt == nil {
		return nil, errors.New("credential verifier needs a JWT verifier")
	}
	return &CredentialVerifier{jwt: jwt, url: strings.TrimSuffix(checker.url, checkPath) + "/iam/v1/me", http: checker.http, timeout: timeout}, nil
}

// withoutRedirects не позволяет ни одному перенаправлению унести удостоверение.
func withoutRedirects(client *http.Client) *http.Client {
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}

// Verify возвращает только субъект, названный аутентифицировавшим клиента авторитетом.
// WhoAmI не публикует expiry; срок проверяет Kaname, локального кэша здесь нет.
func (v *CredentialVerifier) Verify(ctx context.Context, raw string) (Identity, error) {
	if !credsecret.HasMark(raw) {
		return v.jwt.Verify(ctx, raw)
	}
	if _, err := credsecret.Parse(raw); err != nil {
		return Identity{}, ErrToken
	}
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.url, nil)
	if err != nil {
		return Identity{}, ErrIdentityUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+raw)
	resp, err := v.http.Do(req) //nolint:gosec // фиксированный доверенный HTTPS origin, без redirect.
	if err != nil {
		return Identity{}, ErrIdentityUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Identity{}, ErrToken
	}
	if resp.StatusCode != http.StatusOK {
		return Identity{}, ErrIdentityUnavailable
	}
	const maxIdentityBody = 64 << 10
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIdentityBody+1))
	if err != nil || len(body) > maxIdentityBody {
		return Identity{}, ErrIdentityUnavailable
	}
	var out struct {
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Identity{}, ErrIdentityUnavailable
	}
	typ, id, ok := strings.Cut(out.Subject, ":")
	if !ok || (typ != "user" && typ != "service_account") || !kaname.ValidID(id) {
		return Identity{}, ErrIdentityUnavailable
	}
	return Identity{Subject: out.Subject, Token: raw, Opaque: true}, nil
}
