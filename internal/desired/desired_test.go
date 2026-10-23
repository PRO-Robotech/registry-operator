// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package desired

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/yaml"

	observedv1alpha1 "github.com/PRO-Robotech/registry-operator/api/observed/v1alpha1"
	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/config"
	"github.com/PRO-Robotech/registry-operator/internal/kinds"
	"github.com/PRO-Robotech/registry-operator/internal/render"
	"github.com/PRO-Robotech/registry-operator/internal/samples"
)

const samplesDir = "../../config/samples"

type fixture struct {
	set *samples.Set
	cfg *config.Config
	in  Input
}

// readyStatus — статусы готовых шагов заказа abc123/cust01: то, что вывели
// бы их контроллеры.
func readyStatus() map[string]render.Child {
	return map[string]render.Child{
		kinds.S3BucketClaim.StatusKey: {Name: "zot-abc123", Namespace: "cust01", Status: map[string]any{
			"phase": "Ready", "bucketName": "cust01-zot-abc123", "endpoint": "https://s3.example.test",
			"writtenPath": "namespaces/cust01/apps/docker-registry/zot-abc123",
		}},
		kinds.IamServiceAccount.StatusKey: {Name: "zot-abc123-ci", Namespace: "cust01", Status: map[string]any{"id": "sva-1"}},
		kinds.VaultClaim.StatusKey: {Name: "zot-abc123", Namespace: "cust01", Status: map[string]any{
			"phase": "Ready", "vault": map[string]any{"authMountPath": "kubernetes-site-a", "appliedRoles": []any{"zot-abc123"}},
		}},
		kinds.IamAccessBinding.StatusKey: {Name: "zot-abc123-ci-pusher", Namespace: "cust01", Status: map[string]any{"id": "acb-1"}},
		kinds.Addon.StatusKey:            {Name: "reg-abc123", Status: map[string]any{"deployed": true}},
	}
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	set, err := samples.Load(samplesDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(samples.ConfigPath(samplesDir))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := samples.LoadRegistry(samplesDir)
	if err != nil {
		t.Fatal(err)
	}
	// Golden-набор сохраняет явно запрошенный прежний шестишаговый режим.
	reg.Spec.TemplateRef = registryv1alpha1.TemplateRef{Name: set.RegistryTemplate.Spec.TemplateName, Version: set.RegistryTemplate.Spec.Version}
	reg.UID = types.UID("6f1c2a3b-4d5e-4f60-8a71-92b3c4d5e6f7")
	params, err := render.StrictYAMLMap([]byte(set.RegistryTemplate.Spec.Params))
	if err != nil {
		t.Fatal(err)
	}
	in := Input{
		Registry: reg,
		Template: reg.Spec.TemplateRef,
		Params:   params,
		Status:   readyStatus(),
		Names:    NamesFor(&cfg.Policy, reg.Spec.ID, reg.Namespace, set.IamServiceAccount, set.IamAccessBinding, set.HTTPRoute),
		Policy:   &cfg.Policy,
	}
	return fixture{set: set, cfg: cfg, in: in}
}

func normalize(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	if err := utiljson.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func golden(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "golden", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	j, err := yaml.YAMLToJSONStrict(b)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	if err := utiljson.Unmarshal(j, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertGolden(t *testing.T, name string, u *unstructured.Unstructured) {
	t.Helper()
	got := normalize(t, u.Object)
	want := golden(t, name)
	if !reflect.DeepEqual(got, want) {
		g, _ := yaml.Marshal(got)
		w, _ := yaml.Marshal(want)
		t.Fatalf("%s differs from golden\n--- got\n%s\n--- want\n%s", name, g, w)
	}
}

// Шесть шаблонов ревизии zot-v1 на заказе abc123/cust01 дают ровно
// golden-объекты: имена, namespace, метки владения, поля оператора и пин
// чарта с digest.
func TestBuild_SixTemplatesRenderTheGoldenObjectsForAbc123(t *testing.T) {
	f := newFixture(t)
	for _, k := range []kinds.Kind{kinds.S3BucketClaim, kinds.IamServiceAccount, kinds.VaultClaim, kinds.IamAccessBinding, kinds.Addon, kinds.HTTPRoute} {
		t.Run(string(k.Step), func(t *testing.T) {
			u, err := Build(k, f.set.ForKind(k), f.in)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			assertGolden(t, string(k.Step), u)
		})
	}
}

func TestBuild_AddonCarriesTheChartPinWithDigest(t *testing.T) {
	f := newFixture(t)
	u, err := Build(kinds.Addon, f.set.Addon, f.in)
	if err != nil {
		t.Fatal(err)
	}
	pin := f.set.Addon.Spec.Chart
	if v, _, _ := unstructured.NestedString(u.Object, "spec", "version"); v != pin.Version {
		t.Fatalf("version %q, pin %q", v, pin.Version)
	}
	if d := u.GetAnnotations()[registryv1alpha1.AnnotationChartDigest]; d != pin.Digest {
		t.Fatalf("digest annotation %q, pin %q", d, pin.Digest)
	}
	// Пин вне перечня площадки — отказ, даже если шаблон принят.
	other := f.set.Addon.DeepCopy()
	other.Spec.Chart.Digest = "sha256:" + strings.Repeat("0", 64)
	if _, err := Build(kinds.Addon, other, f.in); !IsPolicy(err) {
		t.Fatalf("a chart pin outside the platform list built: %v", err)
	}
}

func TestBuild_AddonCarriesThePublicFlagOfTheOrder(t *testing.T) {
	f := newFixture(t)
	f.in.Registry = f.in.Registry.DeepCopy()
	f.in.Registry.Spec.Public = true
	u, err := Build(kinds.Addon, f.set.Addon, f.in)
	if err != nil {
		t.Fatal(err)
	}
	if v, _, _ := unstructured.NestedString(u.Object, "spec", "variables", "public"); v != "true" {
		t.Fatalf("public variable %q on a public order", v)
	}
}

// Отказы сборки: каждая порча шаблона — пара с законным близнецом (тот же
// шаблон без порчи собирается).
func TestBuild_RefusesARenderedSpecThatOverstepsThePolicy(t *testing.T) {
	cases := []struct {
		name   string
		kind   kinds.Kind
		edit   func(v string) string
		render bool
		field  string
	}{
		{"s3 writes deletionPolicy itself", kinds.S3BucketClaim, func(v string) string { return v + "deletionPolicy: Purge\n" }, false, "spec.deletionPolicy"},
		{"s3 destination of another tenant", kinds.S3BucketClaim, func(v string) string {
			return strings.Replace(v, `"namespaces/%s/apps/docker-registry/zot-%s" .Login .ID`, `"namespaces/other/apps/docker-registry/zot-%s" .ID`, 1)
		}, false, "spec.vault.destinationPath"},
		{"s3 adopts a named bucket", kinds.S3BucketClaim, func(v string) string {
			return strings.Replace(v, "  public: false\n", "  public: false\n  name: someones-bucket\n", 1)
		}, false, "spec"},
		{"vaultclaim manages the platform mount", kinds.VaultClaim, func(v string) string {
			return strings.Replace(v, "manageConfig: false", "manageConfig: true", 1)
		}, false, "spec.auth.manageConfig"},
		{"vaultclaim binds another service account", kinds.VaultClaim, func(v string) string {
			return strings.Replace(v, "names: [{{ printf \"zot-%s\" .ID | toJson }}]", "names: [default]", 1)
		}, false, "spec.roles[0].boundServiceAccounts.names"},
		{"vaultclaim names an admin policy", kinds.VaultClaim, func(v string) string {
			return strings.Replace(v, `policies: [{{ printf "zot-%s-leaf" .ID | toJson }}]`, `policies: [root]`, 1)
		}, false, "spec.roles[0].policies"},
		{"vaultclaim policy with raw rules", kinds.VaultClaim, func(v string) string {
			return strings.Replace(v, "  identityPaths:\n", "  rules: 'path \"secret/*\" { capabilities = [\"read\"] }'\n  identityPaths:\n", 1)
		}, false, "spec.policies[0].rules"},
		{"vaultclaim policy reads every leaf of the tenant", kinds.VaultClaim, func(v string) string {
			return strings.Replace(v, "/{serviceAccountName}", "/zot-shared-{serviceAccountName}", 1)
		}, false, "spec.policies[0].identityPaths[0].path"},
		{"vaultclaim policy writes the leaf", kinds.VaultClaim, func(v string) string {
			return strings.Replace(v, "capabilities: [read]", "capabilities: [read, update]", 1)
		}, false, "spec.policies[0].identityPaths[0].capabilities"},
		{"vaultclaim role without an audience", kinds.VaultClaim, func(v string) string {
			return strings.Replace(v, "  audience: {{ .Params.secrets.audience | toJson }}\n", "", 1)
		}, false, "spec.roles[0].audience"},
		{"vaultclaim declares transit", kinds.VaultClaim, func(v string) string { return v + "transit:\n  keys: [{name: k}]\n" }, false, "spec"},
		{"service account sets its own name", kinds.IamServiceAccount, func(v string) string { return v + "name: other\n" }, false, "spec.name"},
		{"service account key delivered to another instance", kinds.IamServiceAccount, func(v string) string {
			return strings.Replace(v, `instance: {{ printf "zot-%s" .ID | toJson }}`, `instance: zot-other`, 1)
		}, false, "spec.keys[0].delivery.vault.instance"},
		{"binding grants an admin role", kinds.IamAccessBinding, func(v string) string {
			return strings.Replace(v, "name: registry.pusher", "name: registry.admin", 1)
		}, false, "spec.roleRef.name"},
		{"binding targets another registry", kinds.IamAccessBinding, func(v string) string {
			return strings.Replace(v, `id: {{ .ID | toJson }}`, `id: zzz999`, 1)
		}, false, "spec.target.resources"},
		{"binding names a user", kinds.IamAccessBinding, func(v string) string {
			return strings.Replace(v, "subjects:\n", "subjects:\n- user:\n    id: usr1\n", 1)
		}, false, "spec"},
		{"addon writes the chart itself", kinds.Addon, func(v string) string { return v + "version: 9.9.9\n" }, false, "spec.version"},
		{"addon writes an order variable", kinds.Addon, func(v string) string {
			return strings.Replace(v, "  vaultRole:", "  id: other\n  vaultRole:", 1)
		}, false, "spec.variables.id"},
		{"addon writes the public flag", kinds.Addon, func(v string) string {
			return strings.Replace(v, "  vaultRole:", "  public: \"true\"\n  vaultRole:", 1)
		}, false, "spec.variables.public"},
		{"addon omits a variable of the order layer", kinds.Addon, func(v string) string {
			return strings.Replace(v, "  region: {{ .Params.storage.region | toJson }}\n", "", 1)
		}, false, "spec.variables.region"},
		{"addon selects an order layer", kinds.Addon, func(v string) string {
			return strings.Replace(v, "addons.in-cloud.io/addon: {{ .Params.addon.valuesAddon | toJson }}\n    addons.in-cloud.io/values: order",
				"addons.in-cloud.io/addon: reg-other\n    addons.in-cloud.io/values: order", 1)
		}, false, "spec.valuesSelectors[1].matchLabels"},
		{"addon in another project", kinds.Addon, func(v string) string {
			return strings.Replace(v, `project: {{ printf "%s%s" .Params.addon.projectPrefix .Login | toJson }}`, `project: default`, 1)
		}, false, "spec.backend.project"},
		{"addon creates its namespace", kinds.Addon, func(v string) string {
			return strings.Replace(v, "syncOptions: [ServerSideApply=true, FailOnSharedResource=true]", "syncOptions: [ServerSideApply=true, FailOnSharedResource=true, CreateNamespace=true]", 1)
		}, false, "spec.backend.syncPolicy.syncOptions"},
		{"addon reads cluster objects", kinds.Addon, func(v string) string { return v + "valuesSources: []\n" }, false, "spec.valuesSources"},
		{"addon variable outside the order layer", kinds.Addon, func(v string) string {
			return strings.Replace(v, "  vaultRole:", "  extra: x\n  vaultRole:", 1)
		}, false, "spec.variables.extra"},
		{"addon variable with a template", kinds.Addon, func(v string) string {
			// Значение с {{ печатается через toJson и потому проходит рендер;
			// отвергает его политика переменных.
			return strings.Replace(v, `  bucket: {{ .Status.S3BucketClaim.Status.bucketName | toJson }}`, "  bucket: {{ printf \"%s x\" \"{{\" | toJson }}", 1)
		}, false, "spec.variables.bucket"},
		{"route on a foreign gateway", kinds.HTTPRoute, func(v string) string {
			return strings.Replace(v, `name: {{ .Params.route.gatewayName | toJson }}`, `name: other-gw`, 1)
		}, false, "spec.parentRefs"},
		{"route path of another registry", kinds.HTTPRoute, func(v string) string {
			return strings.Replace(v, `value: {{ printf "/v2/%s/" .ID | toJson }}`, `value: /v2/`, 1)
		}, false, "spec.rules[0].matches"},
		{"route to another service", kinds.HTTPRoute, func(v string) string {
			return strings.Replace(v, `name: {{ printf "zot-%s" .ID | toJson }}`, `name: kube-dns`, 1)
		}, false, "spec.rules[0].backendRefs[0]"},
		{"route to another namespace", kinds.HTTPRoute, func(v string) string {
			return strings.Replace(v, `namespace: {{ .Login | toJson }}`, `namespace: kube-system`, 1)
		}, false, "spec.rules[0].backendRefs[0].namespace"},
		{"order field printed raw", kinds.S3BucketClaim, func(v string) string {
			return strings.Replace(v, `customerLogin: {{ .Login | toJson }}`, `customerLogin: {{ .Login }}`, 1)
		}, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			tpl := f.set.ForKind(c.kind)
			if _, err := Build(c.kind, tpl, f.in); err != nil {
				t.Fatalf("lawful twin refused: %v", err)
			}
			bad := withValue(t, tpl, c.edit(tpl.Body().Value))
			_, err := Build(c.kind, bad, f.in)
			switch {
			case c.render && !IsRender(err):
				t.Fatalf("want a render refusal, got %v", err)
			case !c.render && !IsPolicy(err):
				t.Fatalf("want a policy refusal, got %v", err)
			case !c.render && !strings.Contains(err.Error(), c.field+":"):
				t.Fatalf("refusal does not name %s: %v", c.field, err)
			}
		})
	}
}

// withValue — копия шаблона с другим Go-шаблоном.
func withValue(t *testing.T, tpl observedv1alpha1.Template, value string) observedv1alpha1.Template {
	t.Helper()
	switch x := tpl.(type) {
	case *observedv1alpha1.ObservedS3BucketClaimTemplate:
		c := x.DeepCopy()
		c.Spec.Value = value
		return c
	case *observedv1alpha1.ObservedVaultClaimTemplate:
		c := x.DeepCopy()
		c.Spec.Value = value
		return c
	case *observedv1alpha1.ObservedIamServiceAccountTemplate:
		c := x.DeepCopy()
		c.Spec.Value = value
		return c
	case *observedv1alpha1.ObservedIamAccessBindingTemplate:
		c := x.DeepCopy()
		c.Spec.Value = value
		return c
	case *observedv1alpha1.ObservedAddonTemplate:
		c := x.DeepCopy()
		c.Spec.Value = value
		return c
	case *observedv1alpha1.ObservedHTTPRouteTemplate:
		c := x.DeepCopy()
		c.Spec.Value = value
		return c
	}
	t.Fatalf("unknown template %T", tpl)
	return nil
}

// withBucketEndpoint — вход сборки, где контроллер бакета вывел endpoint ep.
func withBucketEndpoint(f fixture, ep string) Input {
	in := f.in
	in.Status = readyStatus()
	s3 := in.Status[kinds.S3BucketClaim.StatusKey]
	st := map[string]any{}
	for k, v := range s3.Status {
		st[k] = v
	}
	st["endpoint"] = ep
	s3.Status = st
	in.Status[kinds.S3BucketClaim.StatusKey] = s3
	return in
}

// Endpoint бакета в переменных заказа — всегда https-origin: оператор
// приводит хост из S3BucketClaim.status.endpoint к https://<host>[:port].
// Законный близнец — уже https-origin — проходит без изменений; форма,
// которую чарт registry-instance отверг бы, — отказ политики, а не Addon.
func TestBuild_AddonEndpointIsTheHTTPSOriginOfTheBucket(t *testing.T) {
	for _, c := range []struct{ name, status, want string }{
		{"https origin passes unchanged", "https://s3.example.test", "https://s3.example.test"},
		{"https origin with a port passes unchanged", "https://s3.example.test:9000", "https://s3.example.test:9000"},
		{"bare host gets https", "s3.example.test", "https://s3.example.test"},
		{"bare host with a port gets https", "s3.example.test:9000", "https://s3.example.test:9000"},
		{"trailing slash of the origin is dropped", "https://s3.example.test/", "https://s3.example.test"},
		{"scheme and host case is folded", "HTTPS://S3.Example.Test", "https://s3.example.test"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			u, err := Build(kinds.Addon, f.set.Addon, withBucketEndpoint(f, c.status))
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if got, _, _ := unstructured.NestedString(u.Object, "spec", "variables", "endpoint"); got != c.want {
				t.Fatalf("variables.endpoint %q from status %q, want %q", got, c.status, c.want)
			}
		})
	}
	for _, c := range []struct{ name, status string }{
		{"plain http", "http://s3.example.test"},
		{"another scheme", "ftp://s3.example.test"},
		{"a path", "https://s3.example.test/bucket"},
		{"credentials", "https://key:supersecret@s3.example.test"},
		{"a query", "https://s3.example.test?x=1"},
		{"a fragment", "https://s3.example.test#x"},
		{"empty", ""},
		{"port out of range", "s3.example.test:70000"},
		{"port zero", "s3.example.test:0"},
		{"not a dns name", "s3_example.test"},
	} {
		t.Run("refused: "+c.name, func(t *testing.T) {
			f := newFixture(t)
			_, err := Build(kinds.Addon, f.set.Addon, withBucketEndpoint(f, c.status))
			switch {
			case !IsPolicy(err):
				t.Fatalf("endpoint %q: want a policy refusal, got %v", c.status, err)
			case !strings.Contains(err.Error(), "spec.variables.endpoint:"):
				t.Fatalf("refusal does not name spec.variables.endpoint: %v", err)
			case strings.Contains(err.Error(), "supersecret"):
				t.Fatalf("refusal echoes the credentials of the endpoint: %v", err)
			}
		})
	}
}

// region в переменных заказа — непустое имя региона (форма схемы чарта
// registry-instance, storage.region): Addon с пустым регионом чарт отверг
// бы, и оператор его не пишет. Законный близнец — ru-1.
func TestBuild_AddonRegionIsANonEmptyRegionName(t *testing.T) {
	build := func(t *testing.T, region string) (*unstructured.Unstructured, error) {
		t.Helper()
		f := newFixture(t)
		params := map[string]any{}
		for k, v := range f.in.Params {
			params[k] = v
		}
		storage := map[string]any{}
		for k, v := range params["storage"].(map[string]any) {
			storage[k] = v
		}
		storage["region"] = region
		params["storage"] = storage
		f.in.Params = params
		return Build(kinds.Addon, f.set.Addon, f.in)
	}
	u, err := build(t, "ru-1")
	if err != nil {
		t.Fatalf("lawful twin refused: %v", err)
	}
	if got, _, _ := unstructured.NestedString(u.Object, "spec", "variables", "region"); got != "ru-1" {
		t.Fatalf("variables.region %q, want ru-1", got)
	}
	for _, region := range []string{"", "RU_1", "-ru", strings.Repeat("r", 65)} {
		_, err := build(t, region)
		if !IsPolicy(err) || !strings.Contains(err.Error(), "spec.variables.region:") {
			t.Fatalf("region %q: want a policy refusal naming spec.variables.region, got %v", region, err)
		}
	}
}
