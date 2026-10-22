// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package iamproxy_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/PRO-Robotech/registry-operator/internal/iamproxy"
)

// keyIssuer — издатель ключей проб: отвечает набором, отказом либо молчит.
type keyIssuer struct {
	key     *ecdsa.PrivateKey
	hits    atomic.Int64
	mode    atomic.Int32 // issuerServes, issuerFails либо issuerSilent
	release chan struct{}
}

const (
	issuerServes = iota
	issuerFails
	issuerSilent
)

func newIssuer(t *testing.T) (*keyIssuer, *httptest.Server) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	is := &keyIssuer{key: key, release: make(chan struct{})}
	is.mode.Store(issuerServes)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		is.hits.Add(1)
		switch is.mode.Load() {
		case issuerFails:
			w.WriteHeader(http.StatusBadGateway)
			return
		case issuerSilent:
			select {
			case <-is.release:
			case <-r.Context().Done():
			}
			return
		}
		pub, _ := key.PublicKey.Bytes()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{
			"kty": "EC", "crv": "P-256", "kid": "k1", "use": "sig", "alg": "ES256",
			"x": base64.RawURLEncoding.EncodeToString(pub[1:33]), "y": base64.RawURLEncoding.EncodeToString(pub[33:65]),
		}}})
	}))
	t.Cleanup(func() {
		close(is.release)
		srv.Close()
	})
	return is, srv
}

func (is *keyIssuer) token(t *testing.T, now time.Time) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": issuer, "aud": []string{aud}, "sub": "usr1", "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
		"kaname_principal_type": "user", "kaname_principal_id": "usr1",
	})
	tok.Header["kid"] = "k1"
	tok.Header["typ"] = "at+jwt"
	s, err := tok.SignedString(is.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func verifierFor(t *testing.T, srv *httptest.Server, ck *clock) *iamproxy.Verifier {
	t.Helper()
	v, err := iamproxy.NewVerifier(iamproxy.TokenConfig{
		JWKSURL: srv.URL, HTTPClient: srv.Client(), Issuer: issuer, Audience: aud,
		Algorithms: []string{"ES256"}, Types: []string{"at+jwt"}, KeysTTL: time.Minute, RefreshEvery: time.Second,
		Leeway: 30 * time.Second, Now: ck.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Издатель молчит, снимок протух: запросы не ждут издателя и не
// выстраиваются за мьютексом — действует прежний снимок, обновление одно
// на всех и идёт в стороне.
func TestVerifier_SilentIssuerDoesNotSerializeRequests(t *testing.T) {
	is, srv := newIssuer(t)
	ck := &clock{t: time.Now()}
	v := verifierFor(t, srv, ck)
	tok := is.token(t, ck.Now())
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	is.mode.Store(issuerSilent)
	ck.Advance(2 * time.Minute)

	const n = 32
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := time.Now()
	for i := range n {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, errs[i] = v.Verify(ctx, tok)
		})
	}
	wg.Wait()
	elapsed := time.Since(start)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("request %d with a published key refused while the issuer is silent: %v", i, err)
		}
	}
	if elapsed > time.Second {
		t.Fatalf("%d requests took %v behind a silent issuer", n, elapsed)
	}
	if h := is.hits.Load(); h != 2 {
		t.Fatalf("issuer asked %d times, want the first fetch and one refresh", h)
	}
}

// Неудачное обновление откладывает следующее: протухший снимок не
// превращает каждый запрос в обращение к издателю.
func TestVerifier_FailedRefreshBacksOff(t *testing.T) {
	is, srv := newIssuer(t)
	ck := &clock{t: time.Now()}
	v := verifierFor(t, srv, ck)
	tok := is.token(t, ck.Now())
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	is.mode.Store(issuerFails)
	ck.Advance(2 * time.Minute)
	waitHits := func(want int64) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for is.hits.Load() < want && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatalf("stale snapshot refused: %v", err)
	}
	waitHits(2)
	for range 20 {
		if _, err := v.Verify(context.Background(), tok); err != nil {
			t.Fatalf("stale snapshot refused during the back-off: %v", err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if h := is.hits.Load(); h != 2 {
		t.Fatalf("issuer asked %d times within the back-off, want 2", h)
	}
	// Близнец: отступ истёк — следующее обновление идёт.
	ck.Advance(time.Minute)
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	waitHits(3)
	if h := is.hits.Load(); h != 3 {
		t.Fatalf("issuer asked %d times after the back-off, want 3", h)
	}
}
