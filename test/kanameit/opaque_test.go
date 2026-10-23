//go:build kanameit

// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package kanameit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/credsecret"
	"github.com/PRO-Robotech/registry-operator/internal/iamproxy"
	"github.com/PRO-Robotech/registry-operator/internal/kaname"
)

// Проба использует настоящий standalone Kaname: harness направляет REST_URL
// прямо в Service/http-rest, без Kacho gateway. Клиентские SA/key/binding здесь
// создаёт тестовый администратор изолированного стенда, не Registry operator.
func TestKaname_OpaqueDirectIdentityAndPermissions(t *testing.T) {
	w := newWorld(t)
	key := issueOpaque(t, w.adm, w.sa.id)
	secret, _ := key["secret"].(string)
	keyID, _ := key["keyId"].(string)
	login, _ := key["clientId"].(string)
	if secret == "" || keyID == "" || login == "" {
		t.Fatal("issued opaque credential is incomplete")
	}
	revoked := false
	keyPath := "/iam/v1/serviceAccounts/" + w.sa.id + "/keys/" + keyID
	t.Cleanup(func() {
		if !revoked {
			w.adm.call(http.MethodDelete, keyPath, nil)
		}
	})
	parsed, err := credsecret.Parse(secret)
	if err != nil || parsed.CredentialID != login {
		t.Fatal("issued Docker login does not match credential ID")
	}
	if login == w.sa.id {
		t.Fatal("fixture must distinguish key identity from principal identity")
	}
	client, err := iamproxy.TLSClient(tlsPath("server-ca.crt"), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// JWT часть берётся из обычной proxy fixture; здесь напрямую проверяем
	// ответ /me и public self-check реального Kaname, без фиктивного PDP.
	req, err := http.NewRequest(http.MethodGet, env["REGOP_IT_REST_URL"]+"/iam/v1/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal("direct WhoAmI transport failed")
	}
	var who struct {
		Subject string `json:"subject"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&who)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || decodeErr != nil || who.Subject != w.sa.principal() {
		t.Fatal("direct WhoAmI did not identify the issuing service account")
	}
	id := iamproxy.Identity{Subject: who.Subject, Token: secret, Opaque: true}
	checker := restChecker(t)
	wrong := id
	wrong.Subject = w.human.principal()
	if allowed, err := checker.Check(context.Background(), wrong, iamproxy.VerbGet, kaname.RepositoryObject(w.reg, "team/app")); err != nil || allowed {
		t.Fatal("direct self gate did not produce a definite denial for another subject")
	}

	s := subject{name: "opaque service account", typ: w.sa.typ, id: w.sa.id, token: secret}
	p := w.newProxy(t, w.reg, false, iamproxy.MaxCacheTTL)
	path := "/v2/" + w.reg + "/team/app/manifests/latest"
	p.expect(t, s, false, step{"no grant", http.MethodGet, path, 403})
	revokeGrant := w.grant(t, s, "registry.puller", w.onRepository(w.reg, "team/app"))
	if revokeGrant == nil {
		t.Fatal("opaque fixture grant failed")
	}
	p.expect(t, s, true, step{"pull with exact grant", http.MethodGet, path, 200})
	p.expect(t, s, false,
		step{"neighbour denied", http.MethodGet, "/v2/" + w.reg + "/team/empty/manifests/latest", 403},
		step{"puller cannot push", http.MethodPut, "/v2/" + w.reg + "/team/app/manifests/latest", 403})
	revokeGrant()
	p.settled(t, s, path) // ограниченное ожидание материализации отзыва в IAM.
	p.expect(t, s, false, step{"revoked grant", http.MethodGet, path, 403})
	w.adm.operation(http.MethodDelete, keyPath, nil, "revoke opaque credential")
	revoked = true
	// После завершённого удаления прямой Kaname не кэширует opaque identity.
	p.expect(t, s, false, step{"revoked credential", http.MethodGet, path, 401})
}

// issueOpaque читает одноразовый Secret только из исходного завершённого Issue.
// GET операции уже не содержит Secret; ни polling, ни повторной выдачи здесь нет.
// Диагностика фиксированная: envelope может содержать секрет и не печатается.
func issueOpaque(t *testing.T, a *admin, accountID string) map[string]any {
	t.Helper()
	code, op := a.call(http.MethodPost, "/iam/v1/serviceAccounts/"+accountID+"/keys", map[string]any{
		"description": "registry opaque direct contract", "credentialKind": "CREDENTIAL_KIND_SECRET", "ttlSeconds": 3600,
	})
	operationID, _ := op["id"].(string)
	done, _ := op["done"].(bool)
	if code != http.StatusOK || operationID == "" || !done || op["error"] != nil {
		t.Fatal("opaque Issue did not return a completed successful initial operation")
	}
	key, ok := op["response"].(map[string]any)
	if !ok || key == nil {
		t.Fatal("opaque Issue initial response is absent or malformed")
	}
	return key
}
