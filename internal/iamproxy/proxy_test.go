// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package iamproxy_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/PRO-Robotech/registry-operator/internal/iamproxy"
	"github.com/PRO-Robotech/registry-operator/internal/kaname"
	"github.com/PRO-Robotech/registry-operator/internal/kaname/kanametest"
)

const (
	issuer   = "https://iam.example.test"
	aud      = "registry"
	regID    = "abc123"
	otherReg = "def456"
)

// clock — управляемые часы проб.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// upstream — двойник zot: запоминает дошедшие запросы.
type upstream struct {
	mu       sync.Mutex
	requests []*http.Request
	status   int
	body     string
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.requests = append(u.requests, r.Clone(context.Background()))
	status, body := u.status, u.body
	u.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="zot"`)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (u *upstream) set(status int, body string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status, u.body = status, body
}

func (u *upstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.requests)
}

func (u *upstream) last() *http.Request {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.requests[len(u.requests)-1]
}

// enroller — двойник оператора: тот же исход заявки, что у эндпоинта.
type enroller struct {
	fake  *kanametest.Fake
	clock *clock
	mu    sync.Mutex
	by    map[string]string
	at    map[string]time.Time
	calls int
}

func (e *enroller) Enroll(_ context.Context, repo, subject string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	obj := kaname.RepositoryObject(regID, repo)
	if _, ok := e.fake.Objects()[obj]; ok {
		if e.by[repo] == subject && e.clock.Now().Sub(e.at[repo]) < 10*time.Minute {
			return nil
		}
		return iamproxy.ErrEnrollConflict
	}
	e.fake.RegisterDirect(obj, kanametest.Object{Subject: kaname.RegistryObject(regID), Relation: "parent",
		ParentChain: []string{kaname.RegistryObject(regID), "project:prj1"}, ParentProjectID: "prj1", ParentAccountID: "acc1"})
	e.by[repo], e.at[repo] = subject, e.clock.Now()
	return nil
}

type env struct {
	t     *testing.T
	fake  *kanametest.Fake
	zot   *upstream
	proxy *httptest.Server
	key   *ecdsa.PrivateKey
	clock *clock
	enr   *enroller
}

func newEnv(t *testing.T, public bool) *env {
	t.Helper()
	ck := &clock{t: time.Now()}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		x, y := key.PublicKey.X.FillBytes(make([]byte, 32)), key.PublicKey.Y.FillBytes(make([]byte, 32))
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{
			"kty": "EC", "crv": "P-256", "kid": "k1", "use": "sig", "alg": "ES256",
			"x": base64.RawURLEncoding.EncodeToString(x), "y": base64.RawURLEncoding.EncodeToString(y),
		}}})
	}))
	t.Cleanup(jwks.Close)
	verifier, err := iamproxy.NewVerifier(iamproxy.TokenConfig{
		JWKSURL: jwks.URL, HTTPClient: jwks.Client(), Issuer: issuer, Audience: aud,
		Algorithms: []string{"ES256"}, Types: []string{"at+jwt"}, KeysTTL: 5 * time.Minute, RefreshEvery: time.Second,
		Leeway: 30 * time.Second, Now: ck.Now,
	})
	if err != nil {
		t.Fatal(err)
	}

	ca, err := kanametest.NewCA("test-ca")
	if err != nil {
		t.Fatal(err)
	}
	serverLeaf, err := ca.Issue("kaname", []string{"kaname.test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := kanametest.New("unused")
	srv, err := fake.Serve(serverLeaf.TLS, ca.Pool())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(srv.PublicAddr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12, RootCAs: ca.Pool(), ServerName: "kaname.test",
	})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	checker := &iamproxy.CachedChecker{Inner: iamproxy.NewKanameChecker(conn, 5*time.Second), TTL: 10 * time.Second, MaxEntries: 1024, Now: ck.Now}

	for _, id := range []string{regID, otherReg} {
		fake.RegisterDirect(kaname.RegistryObject(id), kanametest.Object{Subject: "project:prj1", Relation: "project",
			ParentChain: []string{"project:prj1"}, ParentProjectID: "prj1", ParentAccountID: "acc1"})
	}
	zot := &upstream{}
	zs := httptest.NewServer(zot)
	t.Cleanup(zs.Close)
	u, _ := url.Parse(zs.URL)
	enr := &enroller{fake: fake, clock: ck, by: map[string]string{}, at: map[string]time.Time{}}
	p, err := iamproxy.New(iamproxy.Options{
		RegistryID: regID, Public: public, Upstream: u, Verifier: verifier, Checker: checker, Enroller: enr,
		Realm: "https://registry.example.test/iam/token", Service: "registry", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ps := httptest.NewServer(p)
	t.Cleanup(ps.Close)
	return &env{t: t, fake: fake, zot: zot, proxy: ps, key: key, clock: ck, enr: enr}
}

// token — токен издателя для субъекта; edit правит утверждения и заголовок.
func (e *env) token(subject string, edit func(jwt.MapClaims, map[string]any)) string {
	e.t.Helper()
	typ, id, _ := strings.Cut(subject, ":")
	claims := jwt.MapClaims{
		"iss": issuer, "aud": []string{aud}, "sub": id, "exp": e.clock.Now().Add(time.Hour).Unix(), "iat": e.clock.Now().Unix(),
		"kaname_principal_type": typ, "kaname_principal_id": id,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = "k1"
	tok.Header["typ"] = "at+jwt"
	if edit != nil {
		edit(claims, tok.Header)
	}
	s, err := tok.SignedString(e.key)
	if err != nil {
		e.t.Fatal(err)
	}
	e.fake.AddToken(s, subject)
	return s
}

func (e *env) do(method, path, token string, hdr ...string) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.proxy.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(strings.NewReader(string(b)))
	return resp
}

func (e *env) registerRepo(repo string) {
	e.fake.RegisterDirect(kaname.RepositoryObject(regID, repo), kanametest.Object{Subject: kaname.RegistryObject(regID), Relation: "parent",
		ParentChain: []string{kaname.RegistryObject(regID), "project:prj1"}, ParentProjectID: "prj1", ParentAccountID: "acc1"})
}

const (
	pullPath = "/v2/" + regID + "/team/app/manifests/latest"
	pushPath = "/v2/" + regID + "/team/app/manifests/v1"
	newPush  = "/v2/" + regID + "/team/fresh/blobs/uploads/"
)

// Таблица прав: на каждую выдачу — pull и push, и дошёл ли запрос до zot.
func TestProxy_RightsTable(t *testing.T) {
	const subject = "service_account:sva1"
	cases := []struct {
		name     string
		grant    *kanametest.Grant
		pull     int
		push     int
		newRepo  int
		deletion int
	}{
		{"no grant", nil, 403, 403, 403, 403},
		{"puller on the registry", &kanametest.Grant{Subject: subject, Role: kanametest.RolePuller, Registry: regID}, 200, 403, 403, 403},
		{"pusher on the registry", &kanametest.Grant{Subject: subject, Role: kanametest.RolePusher, Registry: regID}, 200, 200, 200, 403},
		{"admin on the registry", &kanametest.Grant{Subject: subject, Role: kanametest.RoleAdmin, Registry: regID}, 200, 200, 200, 200},
		{"pusher on the account (cascade)", &kanametest.Grant{Subject: subject, Role: kanametest.RolePusher, Account: "acc1"}, 200, 200, 200, 403},
		{"pusher on another account", &kanametest.Grant{Subject: subject, Role: kanametest.RolePusher, Account: "acc2"}, 403, 403, 403, 403},
		{"pusher on another registry", &kanametest.Grant{Subject: subject, Role: kanametest.RolePusher, Registry: otherReg}, 403, 403, 403, 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, false)
			e.registerRepo("team/app")
			if c.grant != nil {
				e.fake.AddGrant(*c.grant)
			}
			tok := e.token(subject, nil)
			for _, step := range []struct {
				method, path string
				want         int
			}{
				{http.MethodGet, pullPath, c.pull},
				{http.MethodPut, pushPath, c.push},
				{http.MethodPost, newPush, c.newRepo},
				{http.MethodDelete, pullPath, c.deletion},
			} {
				before := e.zot.count()
				resp := e.do(step.method, step.path, tok)
				if resp.StatusCode != step.want {
					t.Fatalf("%s %s: %d, want %d", step.method, step.path, resp.StatusCode, step.want)
				}
				reached := e.zot.count() > before
				if reached != (step.want == 200) {
					t.Fatalf("%s %s: zot reached=%v with status %d", step.method, step.path, reached, step.want)
				}
			}
		})
	}
}

// F3: записанный пустой репозиторий не «новый» — права создания мало.
func TestProxy_RegisteredEmptyRepositoryIsNotOpenedByCreateRight(t *testing.T) {
	e := newEnv(t, false)
	e.registerRepo("team/empty")
	e.fake.AddGrant(kanametest.Grant{Subject: "user:usr9", Role: kanametest.RoleCreateOnly, Registry: regID})
	tok := e.token("user:usr9", nil)
	resp := e.do(http.MethodPost, "/v2/"+regID+"/team/empty/blobs/uploads/", tok)
	if resp.StatusCode != http.StatusForbidden || e.zot.count() != 0 {
		t.Fatalf("create-only subject wrote into a registered repository: %d, zot %d", resp.StatusCode, e.zot.count())
	}
	// Близнец: незаписанный репозиторий тем же правом заводится.
	if resp := e.do(http.MethodPost, newPush, tok); resp.StatusCode != http.StatusOK {
		t.Fatalf("create-only subject could not start a new repository: %d", resp.StatusCode)
	}
	// Заявивший пишет в свой репозиторий в своё окно, посторонний — нет.
	if resp := e.do(http.MethodPost, newPush, tok); resp.StatusCode != http.StatusOK {
		t.Fatalf("enroller second request: %d", resp.StatusCode)
	}
	e.fake.AddGrant(kanametest.Grant{Subject: "user:usr8", Role: kanametest.RoleCreateOnly, Registry: regID})
	if resp := e.do(http.MethodPost, newPush, e.token("user:usr8", nil)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("another create-only subject wrote into the fresh repository: %d", resp.StatusCode)
	}
}

func TestProxy_AnonymousAccess(t *testing.T) {
	closed := newEnv(t, false)
	closed.registerRepo("team/app")
	resp := closed.do(http.MethodGet, pullPath, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous pull from a private registry: %d", resp.StatusCode)
	}
	if ch := resp.Header.Get("WWW-Authenticate"); !strings.Contains(ch, `scope="repository:abc123/team/app:pull"`) || !strings.Contains(ch, `service="registry"`) {
		t.Fatalf("challenge %q", ch)
	}
	if closed.zot.count() != 0 {
		t.Fatal("anonymous request reached zot")
	}

	open := newEnv(t, true)
	if resp := open.do(http.MethodGet, pullPath, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("anonymous pull from a public registry: %d", resp.StatusCode)
	}
	if resp := open.do(http.MethodGet, "/v2/"+regID+"/team/app/tags/list", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("anonymous tag list from a public registry: %d", resp.StatusCode)
	}
	if resp := open.do(http.MethodPut, pushPath, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous push to a public registry: %d", resp.StatusCode)
	}
	if n := open.fake.Checks(); n != 0 {
		t.Fatalf("public read asked kaname %d times", n)
	}
}

// Отзыв права чтения действует по истечении срока кэша; отзыв права
// записи — со следующего запроса.
func TestProxy_RevocationAfterTheCacheTTL(t *testing.T) {
	const subject = "service_account:sva1"
	e := newEnv(t, false)
	e.registerRepo("team/app")
	e.fake.AddGrant(kanametest.Grant{Subject: subject, Role: kanametest.RolePusher, Registry: regID})
	tok := e.token(subject, nil)
	if resp := e.do(http.MethodGet, pullPath, tok); resp.StatusCode != http.StatusOK {
		t.Fatalf("pull: %d", resp.StatusCode)
	}
	if resp := e.do(http.MethodPut, pushPath, tok); resp.StatusCode != http.StatusOK {
		t.Fatalf("push: %d", resp.StatusCode)
	}
	e.fake.Revoke(subject)
	if resp := e.do(http.MethodPut, pushPath, tok); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("push right after the revocation: %d", resp.StatusCode)
	}
	if resp := e.do(http.MethodGet, pullPath, tok); resp.StatusCode != http.StatusOK {
		t.Fatalf("pull inside the cache TTL: %d", resp.StatusCode)
	}
	e.clock.Advance(11 * time.Second)
	if resp := e.do(http.MethodGet, pullPath, tok); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("pull past the cache TTL: %d", resp.StatusCode)
	}
}

func TestProxy_RefusesInvalidTokens(t *testing.T) {
	e := newEnv(t, false)
	e.registerRepo("team/app")
	e.fake.AddGrant(kanametest.Grant{Subject: "service_account:sva1", Role: kanametest.RolePusher, Registry: regID})
	cases := map[string]func(jwt.MapClaims, map[string]any){
		"another audience": func(c jwt.MapClaims, _ map[string]any) { c["aud"] = []string{"console"} },
		"another issuer":   func(c jwt.MapClaims, _ map[string]any) { c["iss"] = "https://evil.example.test" },
		"expired":          func(c jwt.MapClaims, _ map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
		"no expiry":        func(c jwt.MapClaims, _ map[string]any) { delete(c, "exp") },
		"another type":     func(_ jwt.MapClaims, h map[string]any) { h["typ"] = "JWT" },
		"unknown key":      func(_ jwt.MapClaims, h map[string]any) { h["kid"] = "k9" },
		"no principal": func(c jwt.MapClaims, _ map[string]any) {
			delete(c, "kaname_principal_type")
			c["sub"] = "x"
			c["kaname_principal_id"] = ""
		},
		"principal with a separator": func(c jwt.MapClaims, _ map[string]any) {
			c["kaname_principal_id"] = "sva1#member"
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			tok := e.token("service_account:sva1", edit)
			if resp := e.do(http.MethodGet, pullPath, tok); resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("got %d", resp.StatusCode)
			}
		})
	}
	// Подпись другим ключом.
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"iss": issuer, "aud": aud, "exp": time.Now().Add(time.Hour).Unix(),
		"kaname_principal_type": "service_account", "kaname_principal_id": "sva1"})
	tok.Header["kid"], tok.Header["typ"] = "k1", "at+jwt"
	forged, _ := tok.SignedString(other)
	if resp := e.do(http.MethodGet, pullPath, forged); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token signed by another key: %d", resp.StatusCode)
	}
	// Подпись HMAC открытым материалом издателя не принимается.
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": issuer, "aud": aud, "exp": time.Now().Add(time.Hour).Unix(),
		"kaname_principal_type": "service_account", "kaname_principal_id": "sva1"})
	hs.Header["kid"], hs.Header["typ"] = "k1", "at+jwt"
	hsTok, _ := hs.SignedString([]byte("k1"))
	if resp := e.do(http.MethodGet, pullPath, hsTok); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("HS256 token: %d", resp.StatusCode)
	}
	if resp := e.do(http.MethodGet, pullPath, "", "Authorization", "Basic dXNlcjpwYXNz"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("basic credentials: %d", resp.StatusCode)
	}
	if e.zot.count() != 0 {
		t.Fatalf("an invalid token reached zot %d times", e.zot.count())
	}
}

func TestNewVerifier_RefusesAPlaintextKeySet(t *testing.T) {
	_, err := iamproxy.NewVerifier(iamproxy.TokenConfig{
		JWKSURL: "http://iam.example.test/jwks", HTTPClient: http.DefaultClient, Issuer: issuer, Audience: aud,
		Algorithms: []string{"ES256"}, Types: []string{"at+jwt"},
	})
	if err == nil {
		t.Fatal("a key set over plain http was accepted")
	}
}

// Гигиена: учётные данные клиента в zot не уходят, тела отказов zot наружу
// не выходят, служебные пути закрыты.
func TestProxy_Hygiene(t *testing.T) {
	e := newEnv(t, false)
	e.registerRepo("team/app")
	e.fake.AddGrant(kanametest.Grant{Subject: "service_account:sva1", Role: kanametest.RolePusher, Registry: regID})
	tok := e.token("service_account:sva1", nil)

	resp := e.do(http.MethodGet, pullPath, tok, "Cookie", "session=s3cr3t", "Proxy-Authorization", "Basic x")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pull: %d", resp.StatusCode)
	}
	got := e.zot.last()
	for _, h := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if v := got.Header.Get(h); v != "" {
			t.Fatalf("%s reached zot: %q", h, v)
		}
	}
	if resp.Header.Get("WWW-Authenticate") != "" {
		t.Fatal("the challenge of zot reached the client")
	}

	e.zot.set(http.StatusInternalServerError, "panic: s3 key AKIA-SECRET at /internal/path")
	resp = e.do(http.MethodGet, pullPath, tok)
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError || strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "/internal") {
		t.Fatalf("zot 5xx leaked: %d %s", resp.StatusCode, b)
	}
	e.zot.set(http.StatusUnauthorized, "zot auth details")
	resp = e.do(http.MethodGet, pullPath, tok)
	b, _ = io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized || strings.Contains(string(b), "zot auth") {
		t.Fatalf("zot 401 leaked: %d %s", resp.StatusCode, b)
	}
	e.zot.set(0, "")

	before := e.zot.count()
	for _, path := range []string{"/v2/_catalog", "/v2/_zot/ext/search", "/v2/" + regID + "/_zot/ext/x", "/v2/" + otherReg + "/team/app/manifests/latest",
		"/v2/" + regID + "/team%2Fapp/manifests/latest", "/v2/" + regID + "/team/../x/manifests/latest", "/v2/"} {
		if resp := e.do(http.MethodGet, path, tok); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
	}
	if e.zot.count() != before {
		t.Fatal("a closed path reached zot")
	}
}

func TestProxy_MountFromOutsideTheRegistryIsDropped(t *testing.T) {
	e := newEnv(t, false)
	e.registerRepo("team/app")
	e.fake.AddGrant(kanametest.Grant{Subject: "service_account:sva1", Role: kanametest.RolePusher, Registry: regID})
	tok := e.token("service_account:sva1", nil)
	resp := e.do(http.MethodPost, "/v2/"+regID+"/team/app/blobs/uploads/?mount=sha256:abc&from="+otherReg+"/secret", tok)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: %d", resp.StatusCode)
	}
	if q := e.zot.last().URL.Query(); q.Has("mount") || q.Has("from") {
		t.Fatalf("foreign mount reached zot: %v", q)
	}
	resp = e.do(http.MethodPost, "/v2/"+regID+"/team/app/blobs/uploads/?mount=sha256:abc&from="+regID+"/team/app", tok)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own mount: %d", resp.StatusCode)
	}
	if q := e.zot.last().URL.Query(); q.Get("from") != regID+"/team/app" {
		t.Fatalf("mount inside the registry was dropped: %v", q)
	}
}

func TestProxy_UnavailableIAMIsNotAnAllow(t *testing.T) {
	e := newEnv(t, false)
	e.registerRepo("team/app")
	tok := e.token("service_account:sva1", nil)
	p, err := iamproxy.New(iamproxy.Options{
		RegistryID: regID, Upstream: &url.URL{Scheme: "http", Host: "127.0.0.1:1"},
		Verifier: verifierFunc(func(context.Context, string) (iamproxy.Identity, error) {
			return iamproxy.Identity{Subject: "service_account:sva1", Token: tok}, nil
		}),
		Checker: checkerFunc(func(context.Context, iamproxy.Identity, string, string) (bool, error) {
			return false, iamproxy.ErrCheckUnavailable
		}),
		Enroller: e.enr, Realm: "r", Service: "registry", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, pullPath, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestNew_UpstreamOutsideThePodIsRefused(t *testing.T) {
	_, err := iamproxy.New(iamproxy.Options{
		RegistryID: regID, Upstream: &url.URL{Scheme: "http", Host: "zot.other-ns.svc:5000"},
		Verifier: verifierFunc(nil), Checker: checkerFunc(nil), Enroller: &enroller{}, Realm: "r", Service: "s",
	})
	if err == nil {
		t.Fatal("an upstream off the loopback was accepted")
	}
}

type verifierFunc func(context.Context, string) (iamproxy.Identity, error)

func (f verifierFunc) Verify(ctx context.Context, raw string) (iamproxy.Identity, error) {
	if f == nil {
		return iamproxy.Identity{}, errors.New("unused")
	}
	return f(ctx, raw)
}

type checkerFunc func(context.Context, iamproxy.Identity, string, string) (bool, error)

func (f checkerFunc) Check(ctx context.Context, id iamproxy.Identity, verb, object string) (bool, error) {
	if f == nil {
		return false, errors.New("unused")
	}
	return f(ctx, id, verb, object)
}
