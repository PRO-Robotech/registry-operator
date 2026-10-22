// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package ocidigest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const manifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`

func manifestDigest() string {
	h := sha256.Sum256([]byte(manifest))
	return "sha256:" + hex.EncodeToString(h[:])
}

// registry — двойник реестра OCI: манифест чарта по тегу, по желанию —
// анонимный токен по вызову Bearer и заголовок digest.
type registry struct {
	repo       string
	tag        string
	digest     string
	withHeader bool
	bearer     bool
	scope      string
	heads      atomic.Int32
	srv        *httptest.Server
}

func (r *registry) handler(w http.ResponseWriter, req *http.Request) {
	switch {
	case req.URL.Path == "/token":
		if req.URL.Query().Get("scope") != "repository:"+r.repo+":pull" {
			http.Error(w, "scope", http.StatusForbidden)
			return
		}
		_, _ = fmt.Fprint(w, `{"token":"t0k"}`)
		return
	case req.URL.Path != "/v2/"+r.repo+"/manifests/"+r.tag:
		http.NotFound(w, req)
		return
	}
	if r.bearer && req.Header.Get("Authorization") != "Bearer t0k" {
		scope := r.scope
		if scope == "" {
			scope = "repository:" + r.repo + ":pull"
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="reg",scope="%s"`, r.srv.URL, scope))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if req.Method == http.MethodHead {
		r.heads.Add(1)
	}
	if r.withHeader {
		w.Header().Set("Docker-Content-Digest", r.digest)
	}
	if req.Method == http.MethodGet {
		_, _ = fmt.Fprint(w, manifest)
	}
}

func newRegistry(t *testing.T, mod func(*registry)) (*registry, *HTTPVerifier, Chart) {
	t.Helper()
	r := &registry{repo: "pro-robotech/charts/registry-instance", tag: "1.0.0", digest: manifestDigest(), withHeader: true}
	if mod != nil {
		mod(r)
	}
	r.srv = httptest.NewServer(http.HandlerFunc(r.handler))
	t.Cleanup(r.srv.Close)
	host := strings.TrimPrefix(r.srv.URL, "http://")
	v := NewHTTPVerifier(2*time.Second, time.Minute, []string{host})
	c := Chart{RepoURL: "oci://" + host + "/pro-robotech/charts", Name: "registry-instance", Version: "1.0.0", Digest: manifestDigest()}
	return r, v, c
}

func TestVerify_PinnedDigestMatches(t *testing.T) {
	_, v, c := newRegistry(t, nil)
	if err := v.Verify(context.Background(), c); err != nil {
		t.Fatalf("matching digest refused: %v", err)
	}
}

func TestVerify_MovedTagIsAMismatch(t *testing.T) {
	_, v, c := newRegistry(t, func(r *registry) { r.digest = "sha256:" + strings.Repeat("a", 64) })
	if err := v.Verify(context.Background(), c); !errors.Is(err, ErrMismatch) {
		t.Fatalf("moved tag: %v", err)
	}
}

func TestVerify_MissingTagIsAMismatch(t *testing.T) {
	_, v, c := newRegistry(t, nil)
	c.Version = "9.9.9"
	if err := v.Verify(context.Background(), c); !errors.Is(err, ErrMismatch) {
		t.Fatalf("missing tag: %v", err)
	}
}

func TestVerify_DigestFromTheBodyWhenTheHeaderIsAbsent(t *testing.T) {
	_, v, c := newRegistry(t, func(r *registry) { r.withHeader = false })
	if err := v.Verify(context.Background(), c); err != nil {
		t.Fatalf("body digest refused: %v", err)
	}
	_, v2, c2 := newRegistry(t, func(r *registry) { r.withHeader = false })
	c2.Digest = "sha256:" + strings.Repeat("b", 64)
	if err := v2.Verify(context.Background(), c2); !errors.Is(err, ErrMismatch) {
		t.Fatalf("body digest mismatch: %v", err)
	}
}

func TestVerify_AnonymousBearerTokenOfTheSameRepository(t *testing.T) {
	_, v, c := newRegistry(t, func(r *registry) { r.bearer = true })
	if err := v.Verify(context.Background(), c); err != nil {
		t.Fatalf("anonymous bearer flow refused: %v", err)
	}
	// Вызов на чужую область — отказ: токен шире чтения этого чарта не берётся.
	_, v2, c2 := newRegistry(t, func(r *registry) { r.bearer = true; r.scope = "repository:*:push" })
	if err := v2.Verify(context.Background(), c2); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("foreign bearer scope: %v", err)
	}
}

func TestVerify_PlainHTTPOnlyForListedHosts(t *testing.T) {
	r, _, c := newRegistry(t, nil)
	v := NewHTTPVerifier(2*time.Second, time.Minute, nil)
	if err := v.Verify(context.Background(), c); err == nil {
		t.Fatal("unlisted host was read over plain http")
	}
	if r.heads.Load() != 0 {
		t.Fatalf("plain http request reached the registry %d times", r.heads.Load())
	}
}

func TestVerify_SuccessIsRememberedForTheTTL(t *testing.T) {
	r, v, c := newRegistry(t, nil)
	now := time.Unix(1_800_000_000, 0)
	v.Now = func() time.Time { return now }
	for range 3 {
		if err := v.Verify(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.heads.Load(); got != 1 {
		t.Fatalf("registry asked %d times within the TTL, want 1", got)
	}
	now = now.Add(2 * time.Minute)
	if err := v.Verify(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if got := r.heads.Load(); got != 2 {
		t.Fatalf("registry asked %d times after the TTL, want 2", got)
	}
}

func TestReference_AppendsTheChartName(t *testing.T) {
	for _, c := range []struct{ repo, name, host, path string }{
		{"oci://ghcr.io/pro-robotech/charts", "registry-instance", "ghcr.io", "pro-robotech/charts/registry-instance"},
		{"oci://ghcr.io/pro-robotech/charts/registry-instance", "registry-instance", "ghcr.io", "pro-robotech/charts/registry-instance"},
	} {
		h, p, err := Reference(c.repo, c.name)
		if err != nil || h != c.host || p != c.path {
			t.Fatalf("%s: %s %s %v", c.repo, h, p, err)
		}
	}
	if _, _, err := Reference("https://ghcr.io/x", "y"); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("non-oci repo: %v", err)
	}
}
