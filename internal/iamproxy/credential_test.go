// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package iamproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/credsecret"
)

// verifierProbe позволяет заметить ошибочный fallback из opaque в JWT.
type verifierProbe struct{ calls int }

func (v *verifierProbe) Verify(context.Context, string) (Identity, error) {
	v.calls++
	return Identity{Subject: "user:jwt-user"}, nil
}

func TestOpaqueCredentialAuthority(t *testing.T) {
	raw, _, err := credsecret.Mint("credential-one")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"service-account", 200, `{"subject":"service_account:principal-two"}`, nil},
		{"user", 200, `{"subject":"user:principal-two"}`, nil},
		{"unauthorized", 401, raw, ErrToken},
		{"forbidden", 403, raw, ErrToken},
		{"unavailable", 503, raw, ErrIdentityUnavailable},
		{"empty", 200, `{}`, ErrIdentityUnavailable},
		{"system", 200, `{"subject":"system:bootstrap"}`, ErrIdentityUnavailable},
		{"wildcard", 200, `{"subject":"user:*"}`, ErrIdentityUnavailable},
		{"empty-id", 200, `{"subject":"user:"}`, ErrIdentityUnavailable},
		{"foreign-separator", 200, `{"subject":"user:abc#admin"}`, ErrIdentityUnavailable},
		{"malformed", 200, `{`, ErrIdentityUnavailable},
		{"trailing", 200, `{"subject":"user:ok"} {}`, ErrIdentityUnavailable},
		{"oversize", 200, strings.Repeat(" ", 65537), ErrIdentityUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/iam/v1/me" || r.Header.Get("Authorization") != "Bearer "+raw {
					t.Error("wrong authority request")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer s.Close()
			jwt := &verifierProbe{}
			v, err := NewCredentialVerifier(jwt, s.URL, s.Client(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				id, got := v.Verify(context.Background(), raw)
				if !errors.Is(got, tc.want) {
					t.Fatalf("error class: got %v, want %v", got, tc.want)
				}
				if got != nil && strings.Contains(got.Error(), raw) {
					t.Fatal("credential in error")
				}
				if tc.want == nil && (!id.Opaque || id.Token != raw || !strings.HasSuffix(id.Subject, ":principal-two")) {
					t.Fatal("wrong verified identity")
				}
			}
			if calls != 2 || jwt.calls != 0 {
				t.Fatal("cached authority or fallback to JWT")
			}
		})
	}
}

func TestOpaqueLaneAndBasicUsername(t *testing.T) {
	raw, _, err := credsecret.Mint("credential-one")
	if err != nil {
		t.Fatal(err)
	}
	jwt := &verifierProbe{}
	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected authority request") }))
	defer s.Close()
	v, err := NewCredentialVerifier(jwt, s.URL, s.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), "kacho_malformed"); !errors.Is(err, ErrToken) {
		t.Fatal("malformed mark accepted")
	}
	if jwt.calls != 0 {
		t.Fatal("opaque fallback")
	}
	if _, err := v.Verify(context.Background(), "jwt-input"); err != nil || jwt.calls != 1 {
		t.Fatal("JWT branch lost")
	}
	for _, tc := range []struct {
		name, password string
		want           bool
	}{
		{"credential-one", raw, true}, {"principal-two", raw, false}, {"", raw, false}, {"arbitrary", "jwt-input", true},
	} {
		r := httptest.NewRequest(http.MethodGet, "/v2/", nil)
		r.SetBasicAuth(tc.name, tc.password)
		_, ok := presentedToken(r)
		if ok != tc.want {
			t.Errorf("Basic username %q: got %t", tc.name, ok)
		}
	}
}

func TestCredentialRedirectsAndCancellation(t *testing.T) {
	raw, _, err := credsecret.Mint("credential-one")
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []int{301, 302, 303, 307, 308} {
		targetCalls := 0
		s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/target" {
				targetCalls++
				return
			}
			w.Header().Set("Location", "/target")
			w.WriteHeader(code)
		}))
		v, err := NewCredentialVerifier(&verifierProbe{}, s.URL, s.Client(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := v.Verify(context.Background(), raw); !errors.Is(err, ErrIdentityUnavailable) {
			t.Fatal("redirect accepted")
		}
		checker, err := NewKanameChecker(s.URL, s.Client(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := checker.Check(context.Background(), Identity{Subject: "user:one", Token: raw}, VerbGet, "registry_registry:one"); !errors.Is(err, ErrCheckUnavailable) {
			t.Fatal("Check redirect accepted")
		}
		if targetCalls != 0 {
			t.Fatal("redirect target received credentials")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := v.Verify(ctx, raw); !errors.Is(err, ErrIdentityUnavailable) {
			t.Fatal("cancel misclassified")
		}
		s.Close()
	}
}

// Проверяется связка двух HTTP запросов: principal не равен CredentialID,
// но субъект Check и предъявленное удостоверение остаются авторитетными.
func TestOpaqueWhoAmIAndSelfCheckBinding(t *testing.T) {
	raw, _, err := credsecret.Mint("credential-one")
	if err != nil {
		t.Fatal(err)
	}
	const subject = "service_account:principal-two"
	meCalls, checkCalls := 0, 0
	revoked := false
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+raw {
			t.Error("credential changed")
		}
		switch r.URL.Path {
		case "/iam/v1/me":
			meCalls++
			if revoked {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"subject":"` + subject + `","systemAdmin":true}`))
		case checkPath:
			checkCalls++
			var in checkRequest
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.Subject != subject || in.Resource.Type != "registry_repository" || in.Resource.ID != "one/team/app" || in.Action != "registry.repositories.get" || in.RequiredRelation != VerbGet {
				t.Error("wrong self-check payload")
			}
			// Сам факт успешного WhoAmI и systemAdmin не разрешает pull.
			_, _ = w.Write([]byte(`{"allowed":false}`))
		default:
			t.Error("unexpected endpoint")
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	v, err := NewCredentialVerifier(&verifierProbe{}, s.URL, s.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	checker, err := NewKanameChecker(s.URL, s.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	id, err := v.Verify(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, err := checker.Check(context.Background(), id, VerbGet, "registry_repository:one/team/app"); allowed || err != nil {
		t.Fatal("identity snapshot overrode resource denial", err)
	}
	revoked = true
	if _, err := v.Verify(context.Background(), raw); !errors.Is(err, ErrToken) {
		t.Fatal("revoked credential reused")
	}
	if meCalls != 2 || checkCalls != 1 {
		t.Fatal("wrong request census")
	}
}

func TestCredentialAuthorityURLRestrictions(t *testing.T) {
	for _, base := range []string{"http://example.test", "https://user:pass@example.test", "https://example.test/path", "https://example.test#fragment", "https://example.test?"} {
		if _, err := NewCredentialVerifier(&verifierProbe{}, base, &http.Client{}, time.Second); err == nil {
			t.Errorf("accepted untrusted origin %q", base)
		}
	}
}

// Docker login получает отказ удостоверению отдельно от отказа авторитета;
// сырое тело авторитета с секретом не становится телом ответа proxy.
func TestOpaqueDockerLoginHTTPBoundary(t *testing.T) {
	raw, _, err := credsecret.Mint("credential-one")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ status, want int }{{200, 200}, {401, 401}, {403, 401}, {500, 503}} {
		s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			if tc.status == 200 {
				_, _ = w.Write([]byte(`{"subject":"service_account:principal-two"}`))
				return
			}
			_, _ = w.Write([]byte(raw))
		}))
		v, err := NewCredentialVerifier(&verifierProbe{}, s.URL, s.Client(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		p := &Proxy{o: Options{Verifier: v}}
		r := httptest.NewRequest(http.MethodGet, "/v2/", nil)
		r.SetBasicAuth("credential-one", raw)
		out := httptest.NewRecorder()
		p.ServeHTTP(out, r)
		if out.Code != tc.want {
			t.Errorf("authority %d: proxy %d", tc.status, out.Code)
		}
		if strings.Contains(out.Body.String(), raw) {
			t.Fatal("authority body leaked credential")
		}
		s.Close()
	}
}

// redirect на другой origin не получает даже первого запроса с удостоверением.
func TestCredentialCrossOriginRedirect(t *testing.T) {
	raw, _, err := credsecret.Mint("credential-one")
	if err != nil {
		t.Fatal(err)
	}
	var contacts atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { contacts.Add(1) }))
	defer target.Close()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL+"/capture")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer s.Close()
	v, err := NewCredentialVerifier(&verifierProbe{}, s.URL, s.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), raw); !errors.Is(err, ErrIdentityUnavailable) {
		t.Fatal("foreign redirect accepted")
	}
	checker, err := NewKanameChecker(s.URL, s.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checker.Check(context.Background(), Identity{Subject: "user:one", Token: raw}, VerbGet, "registry_registry:one"); !errors.Is(err, ErrCheckUnavailable) {
		t.Fatal("foreign Check redirect accepted")
	}
	if contacts.Load() != 0 {
		t.Fatal("credential reached foreign origin")
	}
}

// enrollProbe обнаруживает неожиданный переход read-запроса в регистрацию.
type enrollProbe struct{ calls int }

func (e *enrollProbe) Enroll(context.Context, string, string) error { e.calls++; return nil }

func TestOpaqueProtectedRequestRevocation(t *testing.T) {
	raw, _, err := credsecret.Mint("credential-one")
	if err != nil {
		t.Fatal(err)
	}
	var revoked, denied atomic.Bool
	var meCalls, checkCalls, upstreamCalls atomic.Int32
	authority := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+raw {
			t.Error("authority credential changed")
		}
		if r.URL.Path == "/iam/v1/me" {
			meCalls.Add(1)
			if revoked.Load() {
				w.WriteHeader(401)
				_, _ = w.Write([]byte(raw))
				return
			}
			_, _ = w.Write([]byte(`{"subject":"service_account:principal-two"}`))
			return
		}
		if r.URL.Path != checkPath {
			t.Error("unexpected authority path")
			w.WriteHeader(404)
			return
		}
		checkCalls.Add(1)
		var in checkRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		if in.Subject != "service_account:principal-two" {
			t.Error("untrusted subject")
		}
		allow := !denied.Load() && in.Resource.Type == "registry_repository" && in.Resource.ID == "one/team/app" && in.Action == "registry.repositories.get" && in.RequiredRelation == VerbGet
		_ = json.NewEncoder(w).Encode(map[string]bool{"allowed": allow})
	}))
	defer authority.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("client credential reached zot")
		}
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewCredentialVerifier(&verifierProbe{}, authority.URL, authority.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	check, err := NewKanameChecker(authority.URL, authority.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	enroll := &enrollProbe{}
	var logs bytes.Buffer
	p, err := New(Options{RegistryID: "one", Upstream: u, Verifier: v, Checker: &CachedChecker{Inner: check, TTL: 10 * time.Second, MaxEntries: 8, Now: time.Now}, Enroller: enroll, Log: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	request := func(want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/v2/one/team/app/manifests/latest", nil)
		r.SetBasicAuth("credential-one", raw)
		r.Header.Set("Cookie", "credential="+raw)
		out := httptest.NewRecorder()
		p.ServeHTTP(out, r)
		if out.Code != want {
			t.Fatalf("status %d, want %d", out.Code, want)
		}
		if strings.Contains(out.Body.String(), raw) || strings.Contains(logs.String(), raw) {
			t.Fatal("credential leaked")
		}
	}
	request(200)
	denied.Store(true)
	request(403) // позитивный read-кэш не скрывает отзыв разрешения.
	revoked.Store(true)
	request(401) // authN снова спрашивается, до Check и zot дело не доходит.
	if meCalls.Load() != 3 || checkCalls.Load() != 3 || upstreamCalls.Load() != 1 || enroll.calls != 0 {
		t.Fatal("unexpected request/enrollment census")
	}
}

func TestOpaqueAuthorityDeadline(t *testing.T) {
	raw, _, err := credsecret.Mint("credential-one")
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	v, err := NewCredentialVerifier(&verifierProbe{}, s.URL, s.Client(), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), raw); !errors.Is(err, ErrIdentityUnavailable) {
		t.Fatal("timeout misclassified")
	}
}
