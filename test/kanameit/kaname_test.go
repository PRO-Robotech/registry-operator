//go:build kanameit

// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Проба iam-proxy и регистрации оператора против НАСТОЯЩЕГО kaname (стенд в
// kind, hack/it-kaname.sh probe). Токен клиента — ОБЫЧНЫЙ токен kaname, тот
// же, что для API: служебной учётки (обмен подписанного утверждения ключом
// учётки на /iam/v1/token) и человека (своя церемония службы, посев стенда).
// Прокси собран из тех же частей, что в поде (проверка токена по набору
// ключей kaname, Check «о себе» тем же токеном, кэш вердиктов), перед
// двойником zot на loopback; регистри и репозитории записаны в kaname
// личностью модуля registry по mTLS. Проект, учётку и выдачи заводит сама
// проба через API администратора стенда (бутстрап-удостоверение), аккаунт —
// kacho-system: машина аккаунт не заводит (аккаунт принадлежит человеку,
// kaname account/create.go). Всё заведённое снимается t.Cleanup и на
// падении. Адресов и учётных данных в дереве нет: всё приходит из окружения
// скрипта. Окружения нет — проба не выполнилась (код 3 TestMain), а не
// прошла.
package kanameit

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	iamv1 "github.com/PRO-Robotech/kaname/pkg/api/kaname/cloud/iam/v1"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/PRO-Robotech/registry-operator/internal/iamproxy"
	"github.com/PRO-Robotech/registry-operator/internal/kaname"
	"github.com/PRO-Robotech/registry-operator/internal/tlsfile"
)

// Окружение пробы (ставит hack/it-kaname.sh probe).
var envNames = []string{
	"REGOP_IT_INTERNAL_ADDR", "REGOP_IT_INTERNAL_SERVER_NAME",
	"REGOP_IT_PUBLIC_ADDR", "REGOP_IT_PUBLIC_SERVER_NAME",
	"REGOP_IT_REST_URL", "REGOP_IT_TOKEN_URL", "REGOP_IT_JWKS_URL",
	"REGOP_IT_ISSUER", "REGOP_IT_AUDIENCE", "REGOP_IT_SYSTEM_ACCOUNT", "REGOP_IT_TLS_DIR",
}

var env = map[string]string{}

func TestMain(m *testing.M) {
	for _, n := range envNames {
		v := os.Getenv(n)
		if v == "" {
			fmt.Fprintf(os.Stderr, "kanameit: NOT EXECUTED: %s is not set (run hack/it-kaname.sh probe)\n", n)
			os.Exit(3)
		}
		env[n] = v
	}
	os.Exit(m.Run())
}

func tlsPath(name string) string { return filepath.Join(env["REGOP_IT_TLS_DIR"], name) }

func readSecret(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(tlsPath(name))
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		t.Fatalf("%s is not readable: %v", name, err)
	}
	return strings.TrimSpace(string(b))
}

// runID — признак прогона в именах заводимого.
func runID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func caPool(t *testing.T) *x509.CertPool {
	t.Helper()
	pool, err := tlsfile.CAPool(tlsPath("server-ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

// ---------------------------------------------------------------- admin

// admin — REST API kaname под бутстрап-удостоверением администратора стенда.
type admin struct {
	t     *testing.T
	http  *http.Client
	token string
}

// bootstrap — машинный system_admin стенда: чеканка на внутреннем gRPC
// листом службы (единственный круг чеканки).
func bootstrap(t *testing.T) *admin {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(tlsPath("service.crt"), tlsPath("service.key"))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(env["REGOP_IT_INTERNAL_ADDR"], grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12, RootCAs: caPool(t), ServerName: env["REGOP_IT_INTERNAL_SERVER_NAME"], Certificates: []tls.Certificate{cert},
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := iamv1.NewInternalBootstrapTokenServiceClient(conn).MintBootstrapToken(ctx, &iamv1.MintBootstrapTokenRequest{})
	if err != nil || resp.GetAccessToken() == "" {
		t.Fatalf("bootstrap mint: %v", err)
	}
	client, err := iamproxy.TLSClient(tlsPath("server-ca.crt"), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return &admin{t: t, http: client, token: resp.GetAccessToken()}
}

// call — запрос REST; тело ответа — JSON-объект.
func (a *admin) call(method, path string, body any) (int, map[string]any) {
	a.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, env["REGOP_IT_REST_URL"]+path, rd)
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// operation — мутация, её операция до завершения и ответ операции.
func (a *admin) operation(method, path string, body any, what string) map[string]any {
	a.t.Helper()
	code, op := a.call(method, path, body)
	id, _ := op["id"].(string)
	if code != http.StatusOK || id == "" {
		a.t.Fatalf("%s: %d %v", what, code, op)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		code, cur := a.call(http.MethodGet, "/operations/"+id, nil)
		if code != http.StatusOK {
			a.t.Fatalf("%s: operation %s is not readable: %d %v", what, id, code, cur)
		}
		if done, _ := cur["done"].(bool); done {
			if e, ok := cur["error"]; ok && e != nil {
				a.t.Fatalf("%s: operation %s failed: %v", what, id, e)
			}
			resp, _ := cur["response"].(map[string]any)
			return resp
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("%s: operation %s did not finish: %v", what, id, cur)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func str(m map[string]any, key, what string, t *testing.T) string {
	t.Helper()
	v, _ := m[key].(string)
	if v == "" {
		t.Fatalf("%s: no %s in %v", what, key, m)
	}
	return v
}

// ---------------------------------------------------------------- world

// world — общее для таблицы: проект, учётка, люди, регистри, роли.
type world struct {
	adm     *admin
	account string
	project string
	scope   kaname.Scope
	kn      *kaname.Client
	roles   map[string]string
	sa      subject
	human   subject
	// reg — регистри пробы с репозиториями app и empty; other — другой
	// регистри того же проекта, без выдач.
	reg, other string
}

type subject struct {
	name, typ, id, token string
}

func (s subject) principal() string { return s.typ + ":" + s.id }

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{adm: bootstrap(t), roles: map[string]string{}}
	run := runID(t)

	code, accs := w.adm.call(http.MethodGet, "/iam/v1/accounts?pageSize=1000", nil)
	if code != http.StatusOK {
		t.Fatalf("accounts: %d %v", code, accs)
	}
	list, _ := accs["accounts"].([]any)
	for _, a := range list {
		if m, ok := a.(map[string]any); ok && m["name"] == env["REGOP_IT_SYSTEM_ACCOUNT"] {
			w.account, _ = m["id"].(string)
		}
	}
	if w.account == "" {
		t.Fatalf("account %s is not among %d accounts", env["REGOP_IT_SYSTEM_ACCOUNT"], len(list))
	}

	prj := w.adm.operation(http.MethodPost, "/iam/v1/projects", map[string]any{
		"accountId": w.account, "name": "regop-it-" + run, "description": "registry-operator kanameit " + run,
	}, "project")
	w.project = str(prj, "id", "project", t)
	t.Cleanup(func() { w.adm.call(http.MethodDelete, "/iam/v1/projects/"+w.project, nil) })
	w.scope = kaname.Scope{Project: w.project, Account: w.account}

	code, roles := w.adm.call(http.MethodGet, "/iam/v1/roles?pageSize=1000", nil)
	if code != http.StatusOK {
		t.Fatalf("roles: %d", code)
	}
	rl, _ := roles["roles"].([]any)
	for _, want := range []string{"registry.puller", "registry.pusher"} {
		for _, r := range rl {
			m, _ := r.(map[string]any)
			if m == nil || m["accountId"] != nil && m["accountId"] != "" {
				continue
			}
			if m["id"] == want || m["name"] == want {
				w.roles[want], _ = m["id"].(string)
			}
		}
		if w.roles[want] == "" {
			t.Fatalf("role %s of the registry module manifest is not among %d roles", want, len(rl))
		}
	}

	sva := w.adm.operation(http.MethodPost, "/iam/v1/serviceAccounts", map[string]any{
		"accountId": w.account, "name": "regop-it-" + run, "description": "registry-operator kanameit " + run,
	}, "service account")
	w.sa = subject{name: "service account", typ: "service_account", id: str(sva, "id", "service account", t)}
	t.Cleanup(func() { w.adm.call(http.MethodDelete, "/iam/v1/serviceAccounts/"+w.sa.id, nil) })
	w.sa.token = w.saToken(t, env["REGOP_IT_AUDIENCE"])
	w.human = subject{name: "human", typ: "user", id: readSecret(t, "human.id"), token: readSecret(t, "human.token")}

	w.kn = registrar(t)
	w.reg, w.other = "it"+run, "io"+run
	w.register(t, kaname.RegistryRegistration(w.reg, w.scope, kaname.Version(time.Now(), 1)))
	w.register(t, kaname.RegistryRegistration(w.other, w.scope, kaname.Version(time.Now(), 1)))
	for _, repo := range []string{"team/app", "team/empty"} {
		w.register(t, kaname.RepositoryRegistration(w.reg, repo, w.scope, kaname.Version(time.Now(), 1)))
	}
	return w
}

func registrar(t *testing.T) *kaname.Client {
	t.Helper()
	c, err := kaname.Dial(kaname.ClientConfig{
		Address: env["REGOP_IT_INTERNAL_ADDR"], ServerName: env["REGOP_IT_INTERNAL_SERVER_NAME"],
		CertFile: tlsPath("registry.crt"), KeyFile: tlsPath("registry.key"), CAFile: tlsPath("server-ca.crt"),
		Timeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// register — запись личностью модуля; снятие — в t.Cleanup и на падении.
func (w *world) register(t *testing.T, r kaname.Registration) {
	t.Helper()
	t.Cleanup(func() {
		r.Version = time.Now().Add(time.Second)
		_ = w.kn.Unregister(context.Background(), r)
	})
	if err := w.kn.Register(context.Background(), r); err != nil {
		t.Fatalf("register %s: %v", r.Object, err)
	}
}

// saToken — обычный токен kaname учётки: ключ учётки, подписанное им
// утверждение клиента, обмен на /iam/v1/token с адресатом audience.
func (w *world) saToken(t *testing.T, audience string) string {
	t.Helper()
	key := w.adm.operation(http.MethodPost, "/iam/v1/serviceAccounts/"+w.sa.id+"/keys", map[string]any{
		"description": "registry-operator kanameit",
	}, "service account key")
	clientID, keyID := str(key, "clientId", "key", t), str(key, "keyId", "key", t)
	block, _ := pem.Decode([]byte(str(key, "privateKeyPem", "key", t)))
	if block == nil {
		t.Fatal("the issued key is not PEM")
	}
	var priv *ecdsa.PrivateKey
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		priv, _ = k.(*ecdsa.PrivateKey)
	} else if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		priv = k
	}
	if priv == nil {
		t.Fatal("the issued key is not an EC private key")
	}
	now := time.Now()
	a := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": clientID, "sub": clientID, "aud": env["REGOP_IT_ISSUER"],
		"iat": now.Unix(), "exp": now.Add(2 * time.Minute).Unix(), "jti": runID(t) + runID(t),
	})
	a.Header["kid"], a.Header["typ"] = keyID, "client-authentication+jwt"
	assertion, err := a.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"grant_type":            {"client_credentials"},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion},
		"audience":              {audience},
	}
	resp, err := w.adm.http.PostForm(env["REGOP_IT_TOKEN_URL"], form)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out)
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		t.Fatalf("token exchange of the service account (audience %s): %d %s", audience, resp.StatusCode, out.Error)
	}
	return out.AccessToken
}

// grant — выдача роли субъекту; цель — регистри, репозиторий либо аккаунт
// (каскад). Снятие — revoke (и t.Cleanup).
type grantTarget struct {
	scopeType, scopeID string
	target             map[string]any
}

func (w *world) onRegistry(id string) grantTarget {
	return grantTarget{"iam.project", w.project, map[string]any{"resources": map[string]any{
		"resources": []any{map[string]any{"type": "registry.registries", "id": id}}}}}
}

func (w *world) onRepository(id, repo string) grantTarget {
	return grantTarget{"iam.project", w.project, map[string]any{"resources": map[string]any{
		"resources": []any{map[string]any{"type": "registry.repositories", "id": id + "/" + repo}}}}}
}

func (w *world) onAccount() grantTarget {
	return grantTarget{"iam.account", w.account, map[string]any{"allInScope": map[string]any{}}}
}

func (w *world) grant(t *testing.T, s subject, role string, g grantTarget) func() {
	t.Helper()
	b := w.adm.operation(http.MethodPost, "/iam/v1/accessBindings", map[string]any{
		"subjectType": s.typ, "subjectId": s.id, "roleId": w.roles[role],
		"scopeType": g.scopeType, "scopeId": g.scopeID, "target": g.target,
	}, "grant "+role+" to "+s.principal())
	id := str(b, "id", "grant", t)
	if b["status"] != "ACTIVE" {
		t.Fatalf("grant %s is %v, not ACTIVE", id, b["status"])
	}
	revoked := false
	revoke := func() {
		if !revoked {
			revoked = true
			w.adm.operation(http.MethodDelete, "/iam/v1/accessBindings/"+id, nil, "revoke "+id)
		}
	}
	t.Cleanup(func() {
		if !revoked {
			revoked = true
			w.adm.call(http.MethodDelete, "/iam/v1/accessBindings/"+id, nil)
		}
	})
	return revoke
}

// ---------------------------------------------------------------- proxy

// zot — двойник zot на loopback: отвечает 200 и считает дошедшие запросы.
type zot struct {
	mu sync.Mutex
	n  int
}

func (z *zot) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	z.mu.Lock()
	z.n++
	z.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (z *zot) count() int {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.n
}

// enroller — заявка нового репозитория: запись в kaname личностью модуля
// (то, что делает эндпоинт оператора по принятой заявке).
type enroller struct {
	w        *world
	registry string
	mu       sync.Mutex
	done     []kaname.Registration
}

func (e *enroller) Enroll(ctx context.Context, repo, _ string) error {
	r := kaname.RepositoryRegistration(e.registry, repo, e.w.scope, kaname.Version(time.Now(), 1))
	if err := e.w.kn.Register(ctx, r); err != nil {
		// Не отказ заявки, а сбой записи: прокси ответит 503, и проба
		// назовёт его своим кодом.
		return fmt.Errorf("register %s: %w", r.Object, err)
	}
	e.mu.Lock()
	e.done = append(e.done, r)
	e.mu.Unlock()
	return nil
}

type proxy struct {
	srv *httptest.Server
	zot *zot
}

// newProxy — прокси одного регистри из частей пода: проверка токена ключами
// kaname с адресатом публичного API, Check «о себе» тем же токеном, кэш
// вердиктов cacheTTL (0 — без кэша).
func (w *world) newProxy(t *testing.T, registry string, public bool, cacheTTL time.Duration) *proxy {
	t.Helper()
	jwksClient, err := iamproxy.TLSClient(tlsPath("server-ca.crt"), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	v, err := iamproxy.NewVerifier(iamproxy.TokenConfig{
		JWKSURL: env["REGOP_IT_JWKS_URL"], HTTPClient: jwksClient, Issuer: env["REGOP_IT_ISSUER"], Audience: env["REGOP_IT_AUDIENCE"],
		Algorithms: []string{"ES256"}, Types: []string{"at+jwt"}, KeysTTL: 5 * time.Minute, RefreshEvery: time.Second, Leeway: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(env["REGOP_IT_PUBLIC_ADDR"], grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(caPool(t), env["REGOP_IT_PUBLIC_SERVER_NAME"])))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	z := &zot{}
	zs := httptest.NewServer(z)
	t.Cleanup(zs.Close)
	up, _ := url.Parse(zs.URL)
	enr := &enroller{w: w, registry: registry}
	t.Cleanup(func() {
		enr.mu.Lock()
		defer enr.mu.Unlock()
		for _, r := range enr.done {
			r.Version = time.Now().Add(time.Second)
			_ = w.kn.Unregister(context.Background(), r)
		}
	})
	p, err := iamproxy.New(iamproxy.Options{
		RegistryID: registry, Public: public, Upstream: up, Verifier: v,
		Checker:  &iamproxy.CachedChecker{Inner: iamproxy.NewKanameChecker(conn, 10*time.Second), TTL: cacheTTL, MaxEntries: 1024, Now: time.Now},
		Enroller: enr, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ps := httptest.NewServer(p)
	t.Cleanup(ps.Close)
	return &proxy{srv: ps, zot: z}
}

// send — запрос к прокси; scheme — "bearer", "basic" (пароль docker login)
// либо "" (без кредов).
func (p *proxy) send(t *testing.T, scheme, method, path, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, p.srv.URL+path, nil)
	switch scheme {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+token)
	case "basic":
		req.SetBasicAuth("robot", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp
}

type step struct {
	what, method, path string
	want               int
}

var schemes = []string{"bearer", "basic"}

// expect — каждый шаг обеими схемами; дошёл ли запрос до zot — ровно при
// 200. settle — ограниченный повтор положительного шага на только что
// выданном праве (выдача материализуется асинхронно); отрицательный шаг не
// повторяется никогда.
func (p *proxy) expect(t *testing.T, s subject, settle bool, steps ...step) {
	t.Helper()
	for _, st := range steps {
		for _, scheme := range schemes {
			deadline := time.Now().Add(60 * time.Second)
			for {
				before := p.zot.count()
				resp := p.send(t, scheme, st.method, st.path, s.token)
				reached := p.zot.count() > before
				if resp.StatusCode == st.want && reached == (st.want == http.StatusOK) {
					break
				}
				if !settle || st.want != http.StatusOK || time.Now().After(deadline) {
					t.Fatalf("%s %s (%s, %s): %d (zot reached %v), want %d", s.name, st.what, scheme, st.method+" "+st.path, resp.StatusCode, reached, st.want)
				}
				time.Sleep(time.Second)
			}
		}
	}
}

// settled — право снято: чтение снова 403 (после снятия выдачи kaname
// снимает права асинхронно; ожидание ограничено).
func (p *proxy) settled(t *testing.T, s subject, path string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for p.send(t, "bearer", http.MethodGet, path, s.token).StatusCode != http.StatusForbidden {
		if time.Now().After(deadline) {
			t.Fatalf("%s: the revoked right still reads %s", s.name, path)
		}
		time.Sleep(time.Second)
	}
}

// ---------------------------------------------------------------- probes

// Таблица прав одна для обычного токена служебной учётки и человека, обеими
// схемами предъявления: нет выдачи, puller и pusher на регистри, pusher
// точечно на репозиторий (F3: записанный пустой репозиторий правом на
// другой не открывается, нового не завести), pusher на аккаунт (каскад),
// чужой регистри — 403.
func TestKaname_RightsTableWithTheOrdinaryToken(t *testing.T) {
	w := newWorld(t)
	pull := func(reg string) string { return "/v2/" + reg + "/team/app/manifests/latest" }
	push := func(reg string) string { return "/v2/" + reg + "/team/app/manifests/v1" }
	empty := "/v2/" + w.reg + "/team/empty/blobs/uploads/"
	n := 0
	fresh := func() string { n++; return fmt.Sprintf("/v2/%s/fresh-%d/blobs/uploads/", w.reg, n) }

	for _, s := range []subject{w.sa, w.human} {
		t.Run(s.name, func(t *testing.T) {
			p := w.newProxy(t, w.reg, false, 0)
			foreign := w.newProxy(t, w.other, false, 0)

			p.expect(t, s, false,
				step{"no grant: pull", http.MethodGet, pull(w.reg), 403},
				step{"no grant: push", http.MethodPut, push(w.reg), 403},
				step{"no grant: new repository", http.MethodPost, fresh(), 403})

			revoke := w.grant(t, s, "registry.puller", w.onRegistry(w.reg))
			p.expect(t, s, true,
				step{"puller: pull", http.MethodGet, pull(w.reg), 200},
				step{"puller: push", http.MethodPut, push(w.reg), 403},
				step{"puller: new repository", http.MethodPost, fresh(), 403})
			revoke()
			p.settled(t, s, pull(w.reg))

			revoke = w.grant(t, s, "registry.pusher", w.onRegistry(w.reg))
			p.expect(t, s, true,
				step{"pusher: pull", http.MethodGet, pull(w.reg), 200},
				step{"pusher: push", http.MethodPut, push(w.reg), 200},
				step{"pusher: new repository", http.MethodPost, fresh(), 200})
			foreign.expect(t, s, false,
				step{"pusher elsewhere: pull from another registry", http.MethodGet, pull(w.other), 403},
				step{"pusher elsewhere: push to another registry", http.MethodPut, push(w.other), 403})
			revoke()
			p.settled(t, s, pull(w.reg))

			revoke = w.grant(t, s, "registry.pusher", w.onRepository(w.reg, "team/app"))
			p.expect(t, s, true,
				step{"pusher on the repository: pull", http.MethodGet, pull(w.reg), 200},
				step{"pusher on the repository: push", http.MethodPut, push(w.reg), 200})
			p.expect(t, s, false,
				step{"F3: the registered empty repository", http.MethodPost, empty, 403},
				step{"pusher on the repository: new repository", http.MethodPost, fresh(), 403})
			revoke()
			p.settled(t, s, pull(w.reg))

			revoke = w.grant(t, s, "registry.pusher", w.onAccount())
			p.expect(t, s, true,
				step{"pusher on the account (cascade): pull", http.MethodGet, pull(w.reg), 200},
				step{"pusher on the account (cascade): push", http.MethodPut, push(w.reg), 200},
				step{"pusher on the account (cascade): new repository", http.MethodPost, fresh(), 200})
			revoke()
			p.settled(t, s, pull(w.reg))
		})
	}
}

// Без кредов — 401 с вызовом Basic, а не выдача токена; --public открывает
// анонимный pull и только его; проверка версии отвечает годному токену
// любой схемой.
func TestKaname_AnonymousAndPublic(t *testing.T) {
	w := newWorld(t)
	private := w.newProxy(t, w.reg, false, 0)
	public := w.newProxy(t, w.reg, true, 0)
	for _, c := range []struct {
		what   string
		p      *proxy
		method string
		path   string
		want   int
	}{
		{"anonymous pull from a private registry", private, http.MethodGet, "/v2/" + w.reg + "/team/app/manifests/latest", 401},
		{"anonymous version check of a private registry", private, http.MethodGet, "/v2/", 401},
		{"anonymous push to a public registry", public, http.MethodPut, "/v2/" + w.reg + "/team/app/manifests/v1", 401},
		{"anonymous pull from a public registry (--public)", public, http.MethodGet, "/v2/" + w.reg + "/team/app/manifests/latest", 200},
	} {
		resp := c.p.send(t, "", c.method, c.path, "")
		if resp.StatusCode != c.want {
			t.Fatalf("%s: %d, want %d", c.what, resp.StatusCode, c.want)
		}
		if ch := resp.Header.Get("WWW-Authenticate"); c.want == 401 && ch != `Basic realm="registry"` {
			t.Fatalf("%s: challenge %q", c.what, ch)
		}
	}
	for _, s := range []subject{w.sa, w.human} {
		for _, scheme := range schemes {
			if code := private.send(t, scheme, http.MethodGet, "/v2/", s.token).StatusCode; code != http.StatusOK {
				t.Fatalf("version check with the ordinary token of the %s (%s): %d", s.name, scheme, code)
			}
		}
	}
}

// Токен kaname для другого адресата (докерной полосы) прокси не принимает:
// адресат — публичный API kaname, как у самого API. Токен без срока kaname
// не выпускает — его отказ держит unit-проба TestProxy_RefusesInvalidTokens.
func TestKaname_TokenOfAnotherAudienceIsRefused(t *testing.T) {
	w := newWorld(t)
	w.grant(t, w.sa, "registry.pusher", w.onRegistry(w.reg))
	p := w.newProxy(t, w.reg, false, 0)
	p.expect(t, w.sa, true, step{"pusher: pull", http.MethodGet, "/v2/" + w.reg + "/team/app/manifests/latest", 200})
	foreignAud := os.Getenv("REGOP_IT_FOREIGN_AUDIENCE")
	if foreignAud == "" || foreignAud == env["REGOP_IT_AUDIENCE"] {
		t.Fatalf("REGOP_IT_FOREIGN_AUDIENCE %q names no other audience", foreignAud)
	}
	other := subject{name: "service account, another audience", typ: w.sa.typ, id: w.sa.id, token: w.saToken(t, foreignAud)}
	for _, scheme := range schemes {
		if code := p.send(t, scheme, http.MethodGet, "/v2/"+w.reg+"/team/app/manifests/latest", other.token).StatusCode; code != http.StatusUnauthorized {
			t.Fatalf("token for %s (%s): %d, want 401", foreignAud, scheme, code)
		}
	}
	if p.zot.count() != 2 {
		t.Fatalf("zot reached %d times, want 2 (the twin pull only)", p.zot.count())
	}
}

// Отзыв права чтения доходит до прокси не позже окна кэша (10 с) после
// того, как его увидел kaname; вопрос о другом субъекте тем же токеном —
// отказ.
func TestKaname_RevocationReachesTheProxyWithinTheCacheTTL(t *testing.T) {
	w := newWorld(t)
	path := "/v2/" + w.reg + "/team/app/manifests/latest"
	cached := w.newProxy(t, w.reg, false, iamproxy.MaxCacheTTL)
	direct := w.newProxy(t, w.reg, false, 0)
	revoke := w.grant(t, w.sa, "registry.puller", w.onRegistry(w.reg))
	cached.expect(t, w.sa, true, step{"puller: pull", http.MethodGet, path, 200})

	revoke()
	start := time.Now()
	var atKaname, atProxy time.Duration
	for atKaname == 0 || atProxy == 0 {
		if time.Since(start) > 2*time.Minute {
			t.Fatalf("the revoked right still reads: kaname after %v, proxy after %v", atKaname, atProxy)
		}
		if atKaname == 0 && direct.send(t, "bearer", http.MethodGet, path, w.sa.token).StatusCode == http.StatusForbidden {
			atKaname = time.Since(start)
		}
		if atProxy == 0 && cached.send(t, "bearer", http.MethodGet, path, w.sa.token).StatusCode == http.StatusForbidden {
			atProxy = time.Since(start)
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Logf("revocation: kaname refuses after %v, the proxy with a %v cache after %v", atKaname, iamproxy.MaxCacheTTL, atProxy)
	if atProxy > atKaname+iamproxy.MaxCacheTTL+time.Second {
		t.Fatalf("the proxy refused %v after kaname, beyond its cache window %v", atProxy-atKaname, iamproxy.MaxCacheTTL)
	}

	ck := iamproxy.NewKanameChecker(mustPublicConn(t), 10*time.Second)
	other := iamproxy.Identity{Subject: "user:usr0000000000000000000", Token: w.sa.token}
	if ok, err := ck.Check(context.Background(), other, iamproxy.VerbGet, kaname.RepositoryObject(w.reg, "team/app")); err == nil && ok {
		t.Fatal("the ordinary token answered a Check about another subject")
	}
}

func mustPublicConn(t *testing.T) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(env["REGOP_IT_PUBLIC_ADDR"], grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(caPool(t), env["REGOP_IT_PUBLIC_SERVER_NAME"])))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}
