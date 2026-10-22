//go:build kanameit

// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Проба оператора и iam-proxy против НАСТОЯЩЕГО kaname (стенд в kind,
// hack/it-kaname.sh): регистрация регистри и репозитория личностью модуля
// registry по mTLS на внутренний листенер; докер-токен человека с полосы
// /iam/token, проверенный ключами издателя; Check «о себе» этим токеном —
// разрешено на своём репозитории, отказ на вопросе о другом субъекте.
// Адресов и учётных данных в дереве нет: всё приходит из окружения
// скрипта. Окружения нет — проба не выполнилась (код 3 TestMain), а не
// прошла.
package kanameit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/PRO-Robotech/registry-operator/internal/iamproxy"
	"github.com/PRO-Robotech/registry-operator/internal/kaname"
	"github.com/PRO-Robotech/registry-operator/internal/tlsfile"
)

// Окружение пробы (ставит hack/it-kaname.sh).
var envNames = []string{
	"REGOP_IT_INTERNAL_ADDR", "REGOP_IT_INTERNAL_SERVER_NAME",
	"REGOP_IT_PUBLIC_ADDR", "REGOP_IT_PUBLIC_SERVER_NAME",
	"REGOP_IT_TOKEN_URL", "REGOP_IT_JWKS_URL", "REGOP_IT_ISSUER", "REGOP_IT_SERVICE",
	"REGOP_IT_TLS_DIR", "REGOP_IT_HUMAN_EMAIL", "REGOP_IT_HUMAN_PASSWORD", "REGOP_IT_PROJECT_ID",
}

var env = map[string]string{}

func TestMain(m *testing.M) {
	for _, n := range envNames {
		v := os.Getenv(n)
		if v == "" {
			fmt.Fprintf(os.Stderr, "kanameit: NOT EXECUTED: %s is not set (run hack/it-kaname.sh)\n", n)
			os.Exit(3)
		}
		env[n] = v
	}
	os.Exit(m.Run())
}

func tlsPath(name string) string { return filepath.Join(env["REGOP_IT_TLS_DIR"], name) }

// newID — id регистри прогона: свой на каждый прогон, снимается пробой.
func newID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "it" + hex.EncodeToString(b)
}

func registrar(t *testing.T) *kaname.Client {
	t.Helper()
	c, err := kaname.Dial(kaname.ClientConfig{
		Address: env["REGOP_IT_INTERNAL_ADDR"], ServerName: env["REGOP_IT_INTERNAL_SERVER_NAME"],
		CertFile: tlsPath("registry.crt"), KeyFile: tlsPath("registry.key"), CAFile: tlsPath("server-ca.crt"),
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// registered — регистри и репозиторий app записаны в kaname под проектом
// человека; снятие — в t.Cleanup и на падении.
func registered(t *testing.T) (string, string) {
	t.Helper()
	ctx := context.Background()
	kn := registrar(t)
	id := newID(t)
	scope := kaname.Scope{Project: env["REGOP_IT_PROJECT_ID"]}
	version := kaname.Version(time.Now(), 1)
	reg := kaname.RegistryRegistration(id, scope, version)
	repo := kaname.RepositoryRegistration(id, "app", scope, version)
	t.Cleanup(func() {
		tomb := kaname.Version(time.Now().Add(time.Second), 2)
		_ = kn.Unregister(ctx, kaname.RepositoryRegistration(id, "app", scope, tomb))
		_ = kn.Unregister(ctx, kaname.RegistryRegistration(id, scope, tomb))
	})
	if err := kn.Register(ctx, reg); err != nil {
		t.Fatalf("register %s: %v", reg.Object, err)
	}
	if err := kn.Register(ctx, repo); err != nil {
		t.Fatalf("register %s: %v", repo.Object, err)
	}
	return id, repo.Object
}

// dockerToken — токен полосы /iam/token для области репозитория (Basic
// человека, как у docker login).
func dockerToken(t *testing.T, id string) string {
	t.Helper()
	client, err := iamproxy.TLSClient(tlsPath("server-ca.crt"), 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	q := url.Values{"service": {env["REGOP_IT_SERVICE"]}, "scope": {"repository:" + id + "/app:pull,push"}}
	req, err := http.NewRequest(http.MethodGet, env["REGOP_IT_TOKEN_URL"]+"?"+q.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(env["REGOP_IT_HUMAN_EMAIL"], env["REGOP_IT_HUMAN_PASSWORD"])
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/iam/token: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("/iam/token body: %v", err)
	}
	if out.Token == "" {
		out.Token = out.AccessToken
	}
	if out.Token == "" {
		t.Fatal("/iam/token returned no token")
	}
	return out.Token
}

func checker(t *testing.T) iamproxy.Checker {
	t.Helper()
	pool, err := tlsfile.CAPool(tlsPath("server-ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(env["REGOP_IT_PUBLIC_ADDR"], grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(pool, env["REGOP_IT_PUBLIC_SERVER_NAME"])))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return iamproxy.NewKanameChecker(conn, 10*time.Second)
}

// Регистрация личностью модуля: регистри под проектом и репозиторий под
// регистри приняты настоящим kaname, снятие проходит.
func TestKaname_RegistrationOfARegistryAndItsRepository(t *testing.T) {
	registered(t)
}

// Докер-токен человека проходит проверку iam-proxy (ключи издателя, утверждения
// принципала) и Check о себе на своём репозитории разрешён; вопрос о другом
// субъекте этим токеном — отказ.
func TestKaname_DockerTokenChecksAboutOneself(t *testing.T) {
	id, repo := registered(t)
	tok := dockerToken(t, id)
	jwksClient, err := iamproxy.TLSClient(tlsPath("server-ca.crt"), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	v, err := iamproxy.NewVerifier(iamproxy.TokenConfig{
		JWKSURL: env["REGOP_IT_JWKS_URL"], HTTPClient: jwksClient, Issuer: env["REGOP_IT_ISSUER"], Audience: env["REGOP_IT_SERVICE"],
		Algorithms: []string{"ES256"}, Types: []string{"at+jwt", "JWT"}, KeysTTL: time.Minute, RefreshEvery: time.Second, Leeway: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	who, err := v.Verify(ctx, tok)
	if err != nil {
		t.Fatalf("the docker token of the human does not pass iam-proxy: %v", err)
	}
	ck := checker(t)
	var allowed bool
	deadline := time.Now().Add(30 * time.Second)
	for {
		// Права потомков материализуются асинхронно: ограниченный повтор
		// только на своём только что записанном ресурсе.
		allowed, err = ck.Check(ctx, who, iamproxy.VerbGet, repo)
		if (err == nil && allowed) || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil || !allowed {
		t.Fatalf("Check about oneself on the own repository: allowed=%v err=%v", allowed, err)
	}
	other := iamproxy.Identity{Subject: "user:usr0000000000000000000", Token: tok}
	if ok, err := ck.Check(ctx, other, iamproxy.VerbGet, repo); err == nil && ok {
		t.Fatal("the registry token answered a Check about another subject")
	}
}
