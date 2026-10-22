// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package iamproxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PRO-Robotech/registry-operator/internal/enroll/api"
	"github.com/PRO-Robotech/registry-operator/internal/iamproxy"
)

// Заявка уходит с projected-токеном пода и телом контракта; каждый код
// эндпоинта даёт свой исход, неизвестный — недоступность, а не успех.
func TestOperatorEnroller_OutcomesFollowTheEndpointCodes(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody api.Request
	status := http.StatusCreated
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(status)
	}))
	defer srv.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("projected-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := iamproxy.NewOperatorEnroller(srv.URL, "abc123", tokenFile, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Enroll(context.Background(), "team/app", "service_account:sva1"); err != nil {
		t.Fatalf("created: %v", err)
	}
	if gotAuth != "Bearer projected-token" || gotPath != "/v1/registries/abc123/repositories" ||
		gotBody.Repository != "team/app" || gotBody.Subject != "service_account:sva1" {
		t.Fatalf("request %q %q %+v", gotAuth, gotPath, gotBody)
	}
	for code, want := range map[int]error{
		http.StatusOK:                  nil,
		http.StatusConflict:            iamproxy.ErrEnrollConflict,
		http.StatusTooManyRequests:     iamproxy.ErrEnrollLimited,
		http.StatusGone:                iamproxy.ErrEnrollClosed,
		http.StatusServiceUnavailable:  iamproxy.ErrEnrollUnavailable,
		http.StatusForbidden:           iamproxy.ErrEnrollUnavailable,
		http.StatusInternalServerError: iamproxy.ErrEnrollUnavailable,
	} {
		status = code
		err := e.Enroll(context.Background(), "team/app", "service_account:sva1")
		if (want == nil) != (err == nil) || (want != nil && !errors.Is(err, want)) {
			t.Fatalf("status %d: %v, want %v", code, err, want)
		}
	}
}

func TestNewOperatorEnroller_OnlyHTTPS(t *testing.T) {
	for _, u := range []string{"http://registry-operator.registry-system.svc:8443", "https://h/path", "registry-operator:8443"} {
		if _, err := iamproxy.NewOperatorEnroller(u, "abc123", "/tok", http.DefaultClient); err == nil {
			t.Fatalf("%s accepted", u)
		}
	}
	if _, err := iamproxy.NewOperatorEnroller("https://registry-operator.registry-system.svc:8443", "abc123", "/tok", http.DefaultClient); err != nil {
		t.Fatalf("lawful twin refused: %v", err)
	}
}

func validConfig() iamproxy.Config {
	return iamproxy.Config{
		Listen: ":8080", HealthListen: ":8081", Upstream: "http://127.0.0.1:5000", UpstreamReadyPath: "/readyz", RegistryID: "abc123",
		JWKSURL: "https://iam.example.test/jwks", JWKSCAFile: "/ca", Issuer: "https://iam.example.test", Audience: "registry", TokenTypes: "at+jwt",
		KanameAddress: "kaname.iam-system.svc:9090", KanameServerName: "kaname.iam-system.svc", KanameCAFile: "/ca",
		CheckTimeout: 2e9, CacheTTL: 10e9,
		EnrollURL: "https://registry-operator.registry-system.svc:8443", EnrollCAFile: "/ca", EnrollTokenFile: "/tok", EnrollTimeout: 15e9,
	}
}

// Каждая порча одного значения — отказ старта с названным полем; близнец
// — исходная конфигурация.
func TestConfig_RefusesAnIncompleteOrUnsafeConfiguration(t *testing.T) {
	ok := validConfig()
	if err := ok.Validate(); err != nil {
		t.Fatalf("lawful twin refused: %v", err)
	}
	for name, c := range map[string]struct {
		edit func(*iamproxy.Config)
		want string
	}{
		"plaintext key set":       {func(c *iamproxy.Config) { c.JWKSURL = "http://iam.example.test/jwks" }, "jwks-url"},
		"key set without CA":      {func(c *iamproxy.Config) { c.JWKSCAFile = "" }, "jwks-ca"},
		"no audience":             {func(c *iamproxy.Config) { c.Audience = "" }, "audience"},
		"cache longer than 10s":   {func(c *iamproxy.Config) { c.CacheTTL = 11e9 }, "cache-ttl"},
		"plaintext enrollment":    {func(c *iamproxy.Config) { c.EnrollURL = "http://registry-operator:8443" }, "enroll-url"},
		"no projected token":      {func(c *iamproxy.Config) { c.EnrollTokenFile = "" }, "enroll-token-file"},
		"kaname without CA":       {func(c *iamproxy.Config) { c.KanameCAFile = "" }, "kaname-ca"},
		"registry id with colon":  {func(c *iamproxy.Config) { c.RegistryID = "abc:123" }, "registry-id"},
		"no token types":          {func(c *iamproxy.Config) { c.TokenTypes = " " }, "token-types"},
		"check without deadline":  {func(c *iamproxy.Config) { c.CheckTimeout = 0 }, "check-timeout"},
		"relative ready path":     {func(c *iamproxy.Config) { c.UpstreamReadyPath = "readyz" }, "upstream-ready-path"},
		"ready path with a query": {func(c *iamproxy.Config) { c.UpstreamReadyPath = "/readyz?x=1" }, "upstream-ready-path"},
	} {
		t.Run(name, func(t *testing.T) {
			bad := validConfig()
			c.edit(&bad)
			err := bad.Validate()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want a refusal naming %q, got %v", c.want, err)
			}
		})
	}
}

// Интерфейс флагов, на который опирается чарт registry-instance (его
// README): каждый флаг рендера чарта разбирается, и собранная из них
// конфигурация проходит проверку старта.
func TestConfig_TheFlagsOfTheInstanceChartAreUnderstood(t *testing.T) {
	var c iamproxy.Config
	fs := flag.NewFlagSet("iam-proxy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	c.BindFlags(fs)
	args := []string{
		"--listen=:8080", "--health-listen=:8081", "--upstream=http://127.0.0.1:5000", "--upstream-ready-path=/readyz",
		"--registry-id=abc123", "--public=false",
		"--jwks-url=https://iam.example.test/jwks", "--jwks-ca=/ca", "--issuer=https://iam.example.test", "--audience=registry",
		"--kaname-address=kaname.iam-system.svc:9090", "--kaname-server-name=kaname.iam-system.svc", "--kaname-ca=/ca",
		"--check-timeout=2s", "--cache-ttl=10s",
		"--enroll-url=https://registry-operator.registry-system.svc:8443", "--enroll-ca=/ca", "--enroll-token-file=/tok", "--enroll-timeout=15s",
	}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("the flags of the instance chart are not understood: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("the configuration of the instance chart is refused: %v", err)
	}
}

// Выдачи токена у прокси нет: клиент входит тем же токеном kaname, что и в
// API (пароль Basic либо Bearer), а вызов без кредов — Basic. Флаги области
// выдачи (--realm, --service) чарт не передаёт, и прокси их не знает:
// принятый и не действующий флаг выглядел бы настройкой, которой нет.
func TestConfig_NoTokenIssuanceFlags(t *testing.T) {
	for _, flagArg := range []string{"--realm=https://registry.example.test/iam/token", "--service=registry"} {
		var c iamproxy.Config
		fs := flag.NewFlagSet("iam-proxy", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		c.BindFlags(fs)
		if err := fs.Parse([]string{flagArg}); err == nil {
			t.Fatalf("%s is understood: the proxy announces a token issuance it does not have", flagArg)
		}
	}
}
