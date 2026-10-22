// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package ocidigest сверяет, что версия чарта в реестре OCI разрешается в
// закреплённый digest манифеста. Пин digest заменяет неизменяемость тега:
// оператор не пишет Addon, чья версия чарта указывает не на то содержимое,
// которое приняла платформа.
package ocidigest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Chart — закреплённая версия чарта.
type Chart struct {
	RepoURL string
	Name    string
	Version string
	Digest  string
}

// Verifier сверяет версию чарта с пином.
type Verifier interface {
	Verify(ctx context.Context, c Chart) error
}

var (
	// ErrMismatch — версия разрешается в другой digest. Повтор не поможет.
	ErrMismatch = errors.New("chart version resolves to a different digest")
	// ErrUnreachable — реестр не ответил по существу; повтор возможен.
	ErrUnreachable = errors.New("chart registry is unreachable")
	// ErrInvalidReference — ссылка на чарт не разбирается.
	ErrInvalidReference = errors.New("invalid chart reference")
)

// manifestAccept — виды манифеста, которыми публикуется чарт Helm в OCI.
const manifestAccept = "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

// maxManifestBytes — предел тела манифеста при вычислении digest.
const maxManifestBytes = 4 << 20

var digestRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// HTTPVerifier — сверка по протоколу Distribution: HEAD манифеста версии,
// при 401 — анонимный токен по WWW-Authenticate (Bearer), повтор с ним.
type HTTPVerifier struct {
	Client *http.Client
	// PlainHTTPHosts — хосты реестров, доступные только по HTTP (реестр
	// внутри кластера). Остальные — только HTTPS.
	PlainHTTPHosts map[string]bool
	// TTL — сколько помнится успешная сверка.
	TTL time.Duration
	// Now — часы (подменяются в пробах).
	Now func() time.Time

	mu    sync.Mutex
	cache map[Chart]time.Time
}

// NewHTTPVerifier — сверка с таймаутом запроса и сроком памяти успеха.
func NewHTTPVerifier(timeout, ttl time.Duration, plainHTTPHosts []string) *HTTPVerifier {
	hosts := map[string]bool{}
	for _, h := range plainHTTPHosts {
		hosts[h] = true
	}
	return &HTTPVerifier{
		Client:         &http.Client{Timeout: timeout},
		PlainHTTPHosts: hosts,
		TTL:            ttl,
		Now:            time.Now,
		cache:          map[Chart]time.Time{},
	}
}

// Reference — хост и путь репозитория OCI чарта: путь repoURL, к которому
// дописано имя чарта, если последний сегмент пути — не оно.
func Reference(repoURL, name string) (host, repo string, err error) {
	if !strings.HasPrefix(repoURL, "oci://") {
		return "", "", fmt.Errorf("%w: repoURL %q is not oci://", ErrInvalidReference, repoURL)
	}
	rest := strings.TrimPrefix(repoURL, "oci://")
	host, path, ok := strings.Cut(rest, "/")
	if !ok || host == "" || path == "" {
		return "", "", fmt.Errorf("%w: repoURL %q has no repository path", ErrInvalidReference, repoURL)
	}
	path = strings.Trim(path, "/")
	if seg := path[strings.LastIndex(path, "/")+1:]; seg != name {
		path += "/" + name
	}
	return host, path, nil
}

// Verify сверяет версию с пином; успех помнится TTL.
func (v *HTTPVerifier) Verify(ctx context.Context, c Chart) error {
	if !digestRe.MatchString(c.Digest) {
		return fmt.Errorf("%w: digest %q", ErrInvalidReference, c.Digest)
	}
	now := v.Now()
	v.mu.Lock()
	if until, ok := v.cache[c]; ok && now.Before(until) {
		v.mu.Unlock()
		return nil
	}
	v.mu.Unlock()

	got, err := v.resolve(ctx, c)
	if err != nil {
		return err
	}
	if got != c.Digest {
		return fmt.Errorf("%w: %s:%s resolves to %s, pinned %s", ErrMismatch, c.Name, c.Version, got, c.Digest)
	}
	v.mu.Lock()
	v.cache[c] = now.Add(v.TTL)
	v.mu.Unlock()
	return nil
}

func (v *HTTPVerifier) scheme(host string) string {
	if v.PlainHTTPHosts[host] {
		return "http"
	}
	return "https"
}

func (v *HTTPVerifier) resolve(ctx context.Context, c Chart) (string, error) {
	host, repo, err := Reference(c.RepoURL, c.Name)
	if err != nil {
		return "", err
	}
	manifestURL := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", v.scheme(host), host, repo, url.PathEscape(c.Version))

	token := ""
	resp, err := v.do(ctx, http.MethodHead, manifestURL, token)
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		drain(resp)
		if token, err = v.anonymousToken(ctx, challenge, repo); err != nil {
			return "", err
		}
		if resp, err = v.do(ctx, http.MethodHead, manifestURL, token); err != nil {
			return "", err
		}
	}
	defer drain(resp)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", fmt.Errorf("%w: %s:%s not found", ErrMismatch, repo, c.Version)
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("%w: manifest HEAD returned %d", ErrUnreachable, resp.StatusCode)
	}
	if d := resp.Header.Get("Docker-Content-Digest"); digestRe.MatchString(d) {
		return d, nil
	}
	// Реестр без заголовка digest: digest — sha256 тела манифеста.
	get, err := v.do(ctx, http.MethodGet, manifestURL, token)
	if err != nil {
		return "", err
	}
	defer drain(get)
	if get.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: manifest GET returned %d", ErrUnreachable, get.StatusCode)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(get.Body, maxManifestBytes+1))
	if err != nil {
		return "", fmt.Errorf("%w: read manifest: %w", ErrUnreachable, err)
	}
	if n > maxManifestBytes {
		return "", fmt.Errorf("%w: manifest exceeds %d bytes", ErrInvalidReference, maxManifestBytes)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func (v *HTTPVerifier) do(ctx context.Context, method, u, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidReference, err)
	}
	req.Header.Set("Accept", manifestAccept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := v.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	return resp, nil
}

// anonymousToken — анонимный токен pull по вызову Bearer. Область — только
// pull этого репозитория; область вызова сверяется с ним.
func (v *HTTPVerifier) anonymousToken(ctx context.Context, challenge, repo string) (string, error) {
	params, ok := parseBearer(challenge)
	if !ok {
		return "", fmt.Errorf("%w: registry asks for unsupported authentication", ErrUnreachable)
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Host == "" {
		return "", fmt.Errorf("%w: bearer realm %q is not a URL", ErrUnreachable, params["realm"])
	}
	if realm.Scheme != "https" && (realm.Scheme != "http" || !v.PlainHTTPHosts[realm.Host]) {
		return "", fmt.Errorf("%w: bearer realm must be https", ErrUnreachable)
	}
	scope := "repository:" + repo + ":pull"
	if s, has := params["scope"]; has && s != scope {
		return "", fmt.Errorf("%w: bearer scope %q differs from %q", ErrUnreachable, s, scope)
	}
	q := realm.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	q.Set("scope", scope)
	realm.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	resp, err := v.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: token: %w", ErrUnreachable, err)
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: token endpoint returned %d", ErrUnreachable, resp.StatusCode)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", fmt.Errorf("%w: token body: %w", ErrUnreachable, err)
	}
	if body.Token != "" {
		return body.Token, nil
	}
	if body.AccessToken != "" {
		return body.AccessToken, nil
	}
	return "", fmt.Errorf("%w: token endpoint returned no token", ErrUnreachable)
}

// parseBearer разбирает `Bearer k="v",k2="v2"`.
func parseBearer(h string) (map[string]string, bool) {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(h), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return nil, false
	}
	out := map[string]string{}
	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")
		k, after, found := strings.Cut(rest, "=")
		if !found {
			break
		}
		k = strings.TrimSpace(k)
		var val string
		if strings.HasPrefix(after, `"`) {
			end := strings.Index(after[1:], `"`)
			if end < 0 {
				return nil, false
			}
			val = after[1 : 1+end]
			rest = after[2+end:]
		} else {
			val, rest, _ = strings.Cut(after, ",")
		}
		out[k] = val
	}
	_, hasRealm := out["realm"]
	return out, hasRealm
}

func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxManifestBytes))
	_ = resp.Body.Close()
}
