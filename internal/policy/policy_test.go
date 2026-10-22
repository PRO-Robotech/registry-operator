// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package policy_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/yaml"

	"github.com/PRO-Robotech/registry-operator/internal/config"
	"github.com/PRO-Robotech/registry-operator/internal/kinds"
	"github.com/PRO-Robotech/registry-operator/internal/policy"
	"github.com/PRO-Robotech/registry-operator/internal/samples"
)

const samplesDir = "../../config/samples"

func sampleConfig(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Load(samples.ConfigPath(samplesDir))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func goldenObject(t *testing.T, step string) *unstructured.Unstructured {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "desired", "testdata", "golden", step+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	j, err := yaml.YAMLToJSONStrict(b)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := utiljson.Unmarshal(j, &m); err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: m}
}

func order() policy.Order {
	return policy.Order{
		ID: "abc123", Login: "cust01", UID: "6f1c2a3b-4d5e-4f60-8a71-92b3c4d5e6f7",
		ServiceAccountName: "zot-abc123-ci", RouteNamespace: "registry-edge",
		Chart: policy.Chart{
			RepoURL: "oci://ghcr.io/pro-robotech/charts", Name: "registry-instance", Version: "1.0.0",
			Digest: "sha256:acf4e9b3f3cace43e81cb7eae9c52553217f30b4578fd812ef55657286390304",
		},
	}
}

func set(t *testing.T, u *unstructured.Unstructured, value any, path ...string) {
	t.Helper()
	if err := unstructured.SetNestedField(u.Object, value, path...); err != nil {
		t.Fatal(err)
	}
}

// Каждый случай портит ровно один факт golden-объекта; сам golden-объект —
// законный близнец и проходит.
func TestValidate_EachRefusalHasALawfulTwin(t *testing.T) {
	cfg := sampleConfig(t)
	cases := []struct {
		name  string
		kind  kinds.Kind
		step  string
		edit  func(u *unstructured.Unstructured)
		field string
	}{
		{"s3 public bucket", kinds.S3BucketClaim, "storage", func(u *unstructured.Unstructured) { set(t, u, true, "spec", "bucket", "public") }, "spec.bucket.public"},
		{"s3 foreign vault config", kinds.S3BucketClaim, "storage", func(u *unstructured.Unstructured) { set(t, u, "admin", "spec", "vaultConfigRef", "name") }, "spec.vaultConfigRef.name"},
		{"s3 purge outside deletion", kinds.S3BucketClaim, "storage", func(u *unstructured.Unstructured) { set(t, u, "Purge", "spec", "deletionPolicy") }, "spec.deletionPolicy"},
		{"s3 customer of another tenant", kinds.S3BucketClaim, "storage", func(u *unstructured.Unstructured) { set(t, u, "other", "spec", "customerLogin") }, "spec.customerLogin"},
		{"s3 in another namespace", kinds.S3BucketClaim, "storage", func(u *unstructured.Unstructured) { u.SetNamespace("other") }, "metadata.namespace"},
		{"s3 region outside the list", kinds.S3BucketClaim, "storage", func(u *unstructured.Unstructured) { set(t, u, "far-away", "spec", "region") }, "spec.region"},

		{"vaultclaim on its own mount", kinds.VaultClaim, "secrets", func(u *unstructured.Unstructured) { set(t, u, "kubernetes-mine", "spec", "auth", "mountPath") }, "spec.auth.mountPath"},
		{"vaultclaim prunes exclusively", kinds.VaultClaim, "secrets", func(u *unstructured.Unstructured) { set(t, u, "Exclusive", "spec", "auth", "rolePruning") }, "spec.auth.rolePruning"},
		{"vaultclaim purges", kinds.VaultClaim, "secrets", func(u *unstructured.Unstructured) { set(t, u, "Purge", "spec", "deletionPolicy") }, "spec.deletionPolicy"},
		{"vaultclaim two roles", kinds.VaultClaim, "secrets", func(u *unstructured.Unstructured) {
			roles, _, _ := unstructured.NestedSlice(u.Object, "spec", "roles")
			set(t, u, append(roles, roles[0]), "spec", "roles")
		}, "spec.roles"},
		{"vaultclaim binds in another namespace", kinds.VaultClaim, "secrets", func(u *unstructured.Unstructured) {
			roles, _, _ := unstructured.NestedSlice(u.Object, "spec", "roles")
			r := roles[0].(map[string]any)
			r["boundServiceAccounts"] = map[string]any{"names": []any{"zot-abc123"}, "namespace": "other"}
			set(t, u, []any{r}, "spec", "roles")
		}, "spec.roles[0].boundServiceAccounts.namespace"},
		{"vaultclaim single-name binding form", kinds.VaultClaim, "secrets", func(u *unstructured.Unstructured) {
			roles, _, _ := unstructured.NestedSlice(u.Object, "spec", "roles")
			r := roles[0].(map[string]any)
			r["boundServiceAccounts"] = map[string]any{"name": "zot-abc123", "names": []any{"zot-abc123"}, "namespace": "cust01"}
			set(t, u, []any{r}, "spec", "roles")
		}, "spec"},
		{"vaultclaim role without its leaf policy", kinds.VaultClaim, "secrets", func(u *unstructured.Unstructured) {
			roles, _, _ := unstructured.NestedSlice(u.Object, "spec", "roles")
			r := roles[0].(map[string]any)
			r["policies"] = []any{"default"}
			set(t, u, []any{r}, "spec", "roles")
		}, "spec.roles[0].policies"},
		{"vaultclaim policy of another order", kinds.VaultClaim, "secrets", func(u *unstructured.Unstructured) {
			pols, _, _ := unstructured.NestedSlice(u.Object, "spec", "policies")
			p := pols[0].(map[string]any)
			p["name"] = "zot-other-leaf"
			set(t, u, []any{p}, "spec", "policies")
		}, "spec.policies[0].name"},

		{"service account second key to another app", kinds.IamServiceAccount, "identity", func(u *unstructured.Unstructured) {
			set(t, u, []any{map[string]any{"name": "push", "delivery": map[string]any{"vault": map[string]any{"app": "other", "instance": "zot-abc123"}}}}, "spec", "keys")
		}, "spec.keys[0].delivery.vault.app"},
		{"service account key too long", kinds.IamServiceAccount, "identity", func(u *unstructured.Unstructured) {
			set(t, u, []any{map[string]any{"name": "push", "ttlSeconds": int64(31536000), "delivery": map[string]any{"vault": map[string]any{"app": "docker-registry", "instance": "zot-abc123"}}}}, "spec", "keys")
		}, "spec.keys[0].ttlSeconds"},
		{"service account orphaned on deletion", kinds.IamServiceAccount, "identity", func(u *unstructured.Unstructured) { set(t, u, "Orphan", "spec", "deletionPolicy") }, "spec.deletionPolicy"},

		{"binding to another account", kinds.IamAccessBinding, "access", func(u *unstructured.Unstructured) {
			set(t, u, []any{map[string]any{"serviceAccountRef": map[string]any{"name": "zot-other-ci"}}}, "spec", "subjects")
		}, "spec.subjects[0].serviceAccountRef.name"},
		{"binding gives pusher to anonymous", kinds.IamAccessBinding, "access", func(u *unstructured.Unstructured) {
			set(t, u, []any{map[string]any{"systemClass": map[string]any{"name": "anonymous"}}}, "spec", "subjects")
		}, "spec.roleRef.name"},
		{"binding on the account scope", kinds.IamAccessBinding, "access", func(u *unstructured.Unstructured) { set(t, u, "iam.account", "spec", "scope", "type") }, "spec.scope.type"},
		{"binding with expiry", kinds.IamAccessBinding, "access", func(u *unstructured.Unstructured) { set(t, u, "2030-01-01T00:00:00Z", "spec", "expiresAt") }, "spec"},

		{"addon to another cluster", kinds.Addon, "workload", func(u *unstructured.Unstructured) { set(t, u, "https://elsewhere", "spec", "targetCluster") }, "spec.targetCluster"},
		{"addon without finalizer", kinds.Addon, "workload", func(u *unstructured.Unstructured) { set(t, u, false, "spec", "backend", "finalizer") }, "spec.backend.finalizer"},
		{"addon retries without limit", kinds.Addon, "workload", func(u *unstructured.Unstructured) {
			set(t, u, int64(100), "spec", "backend", "syncPolicy", "retry", "limit")
		}, "spec.backend.syncPolicy.retry.limit"},
		{"addon without self heal", kinds.Addon, "workload", func(u *unstructured.Unstructured) {
			set(t, u, false, "spec", "backend", "syncPolicy", "automated", "selfHeal")
		}, "spec.backend.syncPolicy.automated.selfHeal"},
		{"addon stale sync epoch", kinds.Addon, "workload", func(u *unstructured.Unstructured) { set(t, u, "7", "spec", "variables", "syncEpoch") }, "spec.variables.syncEpoch"},
		{"addon another release", kinds.Addon, "workload", func(u *unstructured.Unstructured) { set(t, u, "zot-other", "spec", "releaseName") }, "spec.releaseName"},
		{"addon unknown plugin", kinds.Addon, "workload", func(u *unstructured.Unstructured) { set(t, u, "my-plugin", "spec", "pluginName") }, "spec.pluginName"},
		{"addon in a namespace", kinds.Addon, "workload", func(u *unstructured.Unstructured) { u.SetNamespace("cust01") }, "metadata.name"},

		{"route in a namespace outside the list", kinds.HTTPRoute, "route", func(u *unstructured.Unstructured) { u.SetNamespace("kube-system") }, "metadata.namespace"},
		{"route foreign host", kinds.HTTPRoute, "route", func(u *unstructured.Unstructured) { set(t, u, []any{"evil.example.test"}, "spec", "hostnames") }, "spec.hostnames"},
		{"route with a filter", kinds.HTTPRoute, "route", func(u *unstructured.Unstructured) {
			rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
			r := rules[0].(map[string]any)
			r["filters"] = []any{map[string]any{"type": "RequestRedirect"}}
			set(t, u, []any{r}, "spec", "rules")
		}, "spec.rules[0].filters"},
		{"route on another port", kinds.HTTPRoute, "route", func(u *unstructured.Unstructured) {
			rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
			r := rules[0].(map[string]any)
			b := r["backendRefs"].([]any)[0].(map[string]any)
			b["port"] = int64(9091)
			set(t, u, []any{r}, "spec", "rules")
		}, "spec.rules[0].backendRefs[0].port"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			twin := goldenObject(t, c.step)
			if err := cfg.Policy.Check(c.kind, order(), twin); err != nil {
				t.Fatalf("lawful twin refused: %v", err)
			}
			bad := goldenObject(t, c.step)
			c.edit(bad)
			err := cfg.Policy.Check(c.kind, order(), bad)
			if err == nil {
				t.Fatal("policy accepted the forbidden object")
			}
			if !strings.Contains(err.Error(), " "+c.field+":") {
				t.Fatalf("refusal does not name %s: %v", c.field, err)
			}
		})
	}
}

// Одна учётка в names[] — законная привязка; вторая учётка — отказ.
func TestValidate_VaultClaimBindsExactlyTheWorkloadAccount(t *testing.T) {
	cfg := sampleConfig(t)
	u := goldenObject(t, "secrets")
	if err := cfg.Policy.Check(kinds.VaultClaim, order(), u); err != nil {
		t.Fatalf("names[] with the workload account refused: %v", err)
	}
	roles, _, _ := unstructured.NestedSlice(u.Object, "spec", "roles")
	r := roles[0].(map[string]any)
	r["boundServiceAccounts"] = map[string]any{"names": []any{"zot-abc123", "default"}, "namespace": "cust01"}
	set(t, u, []any{r}, "spec", "roles")
	err := cfg.Policy.Check(kinds.VaultClaim, order(), u)
	if err == nil || !strings.Contains(err.Error(), "spec.roles[0].boundServiceAccounts.names:") {
		t.Fatalf("names[] with a second account accepted: %v", err)
	}
}

// Системный класс идёт только за публичностью заказа: anonymous с ролью
// публичного чтения законен лишь на публичном заказе, authenticated — не
// законен никогда (иначе любой вошедший читает частный регистри). Каждый
// отказ стоит рядом с законным близнецом, отличным одним фактом.
func TestValidate_SystemClassFollowsThePublicityOfTheOrder(t *testing.T) {
	cfg := sampleConfig(t)
	binding := func(class string) *unstructured.Unstructured {
		u := goldenObject(t, "access")
		set(t, u, []any{map[string]any{"systemClass": map[string]any{"name": class}}}, "spec", "subjects")
		set(t, u, "registry.public.read", "spec", "roleRef", "name")
		return u
	}
	public := order()
	public.Public = true
	private := order()

	if err := cfg.Policy.Check(kinds.IamAccessBinding, public, binding("anonymous")); err != nil {
		t.Fatalf("public read for anonymous on a public order refused: %v", err)
	}
	err := cfg.Policy.Check(kinds.IamAccessBinding, private, binding("anonymous"))
	if err == nil || !strings.Contains(err.Error(), "spec.subjects[0].systemClass.name:") {
		t.Fatalf("public read for anonymous on a private order accepted: %v", err)
	}
	for _, o := range []policy.Order{public, private} {
		err := cfg.Policy.Check(kinds.IamAccessBinding, o, binding("authenticated"))
		if err == nil || !strings.Contains(err.Error(), "spec.subjects[0].systemClass.name:") {
			t.Fatalf("public read for every authenticated subject accepted (public=%v): %v", o.Public, err)
		}
	}
	// Учётка заказа рядом с анонимом на частном заказе — отказ по анониму,
	// а не по учётке.
	u := binding("anonymous")
	set(t, u, []any{
		map[string]any{"serviceAccountRef": map[string]any{"name": "zot-abc123-ci"}},
		map[string]any{"systemClass": map[string]any{"name": "anonymous"}},
	}, "spec", "subjects")
	err = cfg.Policy.Check(kinds.IamAccessBinding, private, u)
	if err == nil || !strings.Contains(err.Error(), "spec.subjects[1].systemClass.name:") || strings.Contains(err.Error(), "spec.subjects[0]") {
		t.Fatalf("anonymous beside the order account on a private order: %v", err)
	}
}

func TestValidate_ChartPinOutsideThePlatformList(t *testing.T) {
	cfg := sampleConfig(t)
	o := order()
	o.Chart.Version = "1.0.1"
	u := goldenObject(t, "workload")
	set(t, u, "1.0.1", "spec", "version")
	if err := cfg.Policy.Check(kinds.Addon, o, u); err == nil || !strings.Contains(err.Error(), "not an allowed platform pin") {
		t.Fatalf("unpinned chart version accepted: %v", err)
	}
}

func TestCheckRendered_OperatorKeysAreRefusedPerKind(t *testing.T) {
	for _, c := range []struct {
		kind kinds.Kind
		key  string
	}{
		{kinds.S3BucketClaim, "deletionPolicy"},
		{kinds.VaultClaim, "deletionPolicy"},
		{kinds.IamServiceAccount, "name"},
		{kinds.IamAccessBinding, "deletionPolicy"},
		{kinds.Addon, "repoURL"},
		{kinds.Addon, "initDependencies"},
	} {
		if err := policy.CheckRendered(c.kind, map[string]any{c.key: "x"}); err == nil {
			t.Fatalf("%s: rendered %q accepted", c.kind.GVK.Kind, c.key)
		}
		if err := policy.CheckRendered(c.kind, map[string]any{"other": "x"}); err != nil {
			t.Fatalf("%s: twin refused: %v", c.kind.GVK.Kind, err)
		}
	}
	if err := policy.CheckRendered(kinds.Deployment, map[string]any{}); err == nil {
		t.Fatal("a template for a read-only kind accepted: the operator does not write it")
	}
}
