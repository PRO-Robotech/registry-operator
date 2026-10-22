//go:build integration

// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Проба iam-proxy против настоящего zot (контейнер на loopback исполнителя):
// push нового репозитория по протоколу Distribution — проверка блоба,
// загрузка, манифест; pull тем же субъектом; отказы постороннему и
// анонимному; закрытый каталог. kaname и оператор — двойники с их
// контрактом; zot — настоящий.
package integration

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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

const regID = "abc123"

type enroller struct {
	mu   sync.Mutex
	fake *kanametest.Fake
	by   map[string]string
}

func (e *enroller) Enroll(_ context.Context, repo, subject string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	obj := kaname.RepositoryObject(regID, repo)
	if _, ok := e.fake.Objects()[obj]; ok {
		if e.by[repo] == subject {
			return nil
		}
		return iamproxy.ErrEnrollConflict
	}
	e.fake.RegisterDirect(obj, kanametest.Object{Subject: kaname.RegistryObject(regID), Relation: "parent",
		ParentChain: []string{kaname.RegistryObject(regID), "project:prj1"}, ParentProjectID: "prj1"})
	e.by[repo] = subject
	return nil
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

type client struct {
	t     *testing.T
	base  string
	token string
}

func (c client) do(method, path string, body []byte, hdr ...string) *http.Response {
	c.t.Helper()
	u := path
	if !strings.HasPrefix(path, "http") {
		u = c.base + path
	}
	req, err := http.NewRequest(method, u, bytes.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(b))
	return resp
}

// pushBlob — монолитная загрузка блоба: POST, затем PUT по Location.
func (c client) pushBlob(repo string, blob []byte) string {
	c.t.Helper()
	d := digest(blob)
	resp := c.do(http.MethodPost, "/v2/"+regID+"/"+repo+"/blobs/uploads/", nil)
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		c.t.Fatalf("upload start: %d %s", resp.StatusCode, b)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		c.t.Fatal(err)
	}
	// Абсолютный адрес увёл бы клиента мимо прокси прямо в zot.
	if loc.IsAbs() || loc.Host != "" {
		c.t.Fatalf("upload location %q leads past the proxy", loc)
	}
	base, _ := url.Parse(c.base)
	target := base.ResolveReference(loc)
	q := target.Query()
	q.Set("digest", d)
	target.RawQuery = q.Encode()
	resp = c.do(http.MethodPut, target.String(), blob, "Content-Type", "application/octet-stream")
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		c.t.Fatalf("upload finish: %d %s", resp.StatusCode, b)
	}
	return d
}

func TestProxyInFrontOfZot_PushAndPullThroughRights(t *testing.T) {
	zotURL := os.Getenv("ZOT_URL")
	if zotURL == "" {
		t.Fatal("ZOT_URL is required: the probe runs against a real zot")
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	jwks := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pub, _ := key.PublicKey.Bytes()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{
			"kty": "EC", "crv": "P-256", "kid": "k1", "alg": "ES256",
			"x": base64.RawURLEncoding.EncodeToString(pub[1:33]), "y": base64.RawURLEncoding.EncodeToString(pub[33:65]),
		}}})
	}))
	defer jwks.Close()
	verifier, err := iamproxy.NewVerifier(iamproxy.TokenConfig{
		JWKSURL: jwks.URL, HTTPClient: jwks.Client(), Issuer: "https://iam.example.test", Audience: "registry",
		Algorithms: []string{"ES256"}, Types: []string{"at+jwt"}, KeysTTL: time.Minute, RefreshEvery: time.Second, Leeway: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := kanametest.NewCA("ca")
	leaf, _ := ca.Issue("kaname", []string{"kaname.test"}, nil)
	fake := kanametest.New("unused")
	srv, err := fake.Serve(leaf.TLS, ca.Pool())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	conn, err := grpc.NewClient(srv.PublicAddr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12, RootCAs: ca.Pool(), ServerName: "kaname.test",
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	fake.RegisterDirect(kaname.RegistryObject(regID), kanametest.Object{Subject: "project:prj1", Relation: "project",
		ParentChain: []string{"project:prj1"}, ParentProjectID: "prj1", ParentAccountID: "acc1"})
	fake.AddGrant(kanametest.Grant{Subject: "service_account:sva1", Role: kanametest.RolePusher, Registry: regID})

	upstream, _ := url.Parse(zotURL)
	p, err := iamproxy.New(iamproxy.Options{
		RegistryID: regID, Upstream: upstream, Verifier: verifier,
		Checker:  &iamproxy.CachedChecker{Inner: iamproxy.NewKanameChecker(conn, 5*time.Second), TTL: 10 * time.Second, MaxEntries: 64, Now: time.Now},
		Enroller: &enroller{fake: fake, by: map[string]string{}},
		Realm:    "https://registry.example.test/iam/token", Service: "registry", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(p)
	defer proxy.Close()

	mint := func(subject string) string {
		typ, id, _ := strings.Cut(subject, ":")
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
			"iss": "https://iam.example.test", "aud": "registry", "sub": id, "exp": time.Now().Add(time.Hour).Unix(),
			"kaname_principal_type": typ, "kaname_principal_id": id,
		})
		tok.Header["kid"], tok.Header["typ"] = "k1", "at+jwt"
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		fake.AddToken(s, subject)
		return s
	}
	pusher := client{t: t, base: proxy.URL, token: mint("service_account:sva1")}

	// Push нового репозитория.
	cfgBlob := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	layer := []byte("layer of the integration probe " + time.Now().String())
	if resp := pusher.do(http.MethodHead, "/v2/"+regID+"/team/app/blobs/"+digest(layer), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("blob check of a new repository: %d", resp.StatusCode)
	}
	cfgDigest := pusher.pushBlob("team/app", cfgBlob)
	layerDigest := pusher.pushBlob("team/app", layer)
	manifest, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": cfgDigest, "size": len(cfgBlob)},
		"layers": []any{map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": layerDigest, "size": len(layer)}},
	})
	if resp := pusher.do(http.MethodPut, "/v2/"+regID+"/team/app/manifests/v1", manifest, "Content-Type", "application/vnd.oci.image.manifest.v1+json"); resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("manifest: %d %s", resp.StatusCode, b)
	}

	// Pull тем же субъектом.
	resp := pusher.do(http.MethodGet, "/v2/"+regID+"/team/app/manifests/v1", nil, "Accept", "application/vnd.oci.image.manifest.v1+json")
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, manifest) {
		t.Fatalf("pull manifest: %d %s", resp.StatusCode, got)
	}
	resp = pusher.do(http.MethodGet, "/v2/"+regID+"/team/app/blobs/"+layerDigest, nil)
	got, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, layer) {
		t.Fatalf("pull blob: %d", resp.StatusCode)
	}
	if resp := pusher.do(http.MethodGet, "/v2/"+regID+"/team/app/tags/list", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("tags: %d", resp.StatusCode)
	}

	// Отказы: посторонний, анонимный, каталог.
	stranger := client{t: t, base: proxy.URL, token: mint("service_account:sva9")}
	if resp := stranger.do(http.MethodGet, "/v2/"+regID+"/team/app/manifests/v1", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stranger pull: %d", resp.StatusCode)
	}
	if resp := stranger.do(http.MethodPut, "/v2/"+regID+"/team/app/manifests/v2", manifest); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stranger push: %d", resp.StatusCode)
	}
	anon := client{t: t, base: proxy.URL}
	if resp := anon.do(http.MethodGet, "/v2/"+regID+"/team/app/manifests/v1", nil); resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("anonymous pull: %d", resp.StatusCode)
	}
	if resp := pusher.do(http.MethodGet, "/v2/_catalog", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("catalog through the proxy: %d", resp.StatusCode)
	}
	// Данные действительно в zot.
	direct := client{t: t, base: zotURL}
	if resp := direct.do(http.MethodGet, "/v2/"+regID+"/team/app/manifests/v1", nil, "Accept", "application/vnd.oci.image.manifest.v1+json"); resp.StatusCode != http.StatusOK {
		t.Fatalf("zot does not hold the pushed manifest: %d", resp.StatusCode)
	}
}
