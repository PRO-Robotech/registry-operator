//go:build e2e

// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Пробы на kind: оператор поставлен своим чартом и работает под своей
// ClusterRole (envtest этого не проверяет — там всё от имени администратора),
// kaname — двойник с mTLS внутреннего листенера. Проба проводит заказ через
// весь пайплайн, снимает его и сверяет перепись: после снятия объектов
// оператора не осталось, резерв — надгробие, регистрация в IAM снята.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/yaml"

	observedv1alpha1 "github.com/PRO-Robotech/registry-operator/api/observed/v1alpha1"
	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/kaname"
	"github.com/PRO-Robotech/registry-operator/internal/kaname/kanametest"
	"github.com/PRO-Robotech/registry-operator/internal/kinds"
	"github.com/PRO-Robotech/registry-operator/internal/samples"
	"github.com/PRO-Robotech/registry-operator/internal/testkit"
)

const (
	root       = "../.."
	opNS       = "registry-system"
	fakeNS     = "kaname-fake"
	tenantNS   = "e2e-tenant"
	edgeNS     = "registry-edge"
	registryID = "e2e01a"
	wait       = 3 * time.Minute
)

var k8s client.Client

func sh(t *testing.T, stdin []byte, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = root
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func eventually(t *testing.T, what string, f func() error) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		err := f()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not reached within %s: %v", what, wait, err)
		}
		time.Sleep(time.Second)
	}
}

func env(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is required", name)
	}
	return v
}

// secret — TLS-секрет из листа и центра.
func secret(t *testing.T, ns, name string, leaf *kanametest.Leaf, ca *kanametest.CA) {
	t.Helper()
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Data:       map[string][]byte{"tls.crt": leaf.CertPEM, "tls.key": leaf.KeyPEM, "ca.crt": ca.PEM},
	}
	if err := k8s.Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func namespace(t *testing.T, name string, labels map[string]string) {
	t.Helper()
	if err := k8s.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
}

// fakeObjects — зеркало двойника kaname через прокси API-сервера.
func fakeObjects(t *testing.T, cs *kubernetes.Clientset) map[string]kanametest.Object {
	t.Helper()
	b, err := cs.CoreV1().Services(fakeNS).ProxyGet("http", "fake", "8080", "/objects", nil).DoRaw(context.Background())
	if err != nil {
		t.Logf("fake kaname objects: %v", err)
		return nil
	}
	out := map[string]kanametest.Object{}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestE2E_OrderThroughTheWholePipelineAndBack(t *testing.T) {
	opImage := env(t, "E2E_OPERATOR_IMAGE")
	fakeImage := env(t, "E2E_FAKE_IMAGE")
	crds := env(t, "REGOP_TEST_CRDS")
	// Чарт экземпляра опубликован в реестр стенда: пин версии и digest
	// сверяется оператором по-настоящему (протокол Distribution).
	chartHost := env(t, "E2E_CHART_HOST")
	chartDigest := env(t, "E2E_CHART_DIGEST")
	pin := map[string]any{"repoURL": "oci://" + chartHost + "/charts", "name": "registry-instance", "version": "1.0.0", "digest": chartDigest}

	cfg, err := config.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, registryv1alpha1.AddToScheme, observedv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	if k8s, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		t.Fatal(err)
	}
	cs := kubernetes.NewForConfigOrDie(cfg)
	ctx := context.Background()

	// CRD: свои и целевые (закреплённые hack/crds.lock).
	sh(t, nil, "kubectl", "apply", "--server-side", "-f", "config/crd/bases")
	for _, f := range []string{"vault.in-cloud.io_s3bucketclaims.yaml", "vault.in-cloud.io_vaultclaims.yaml", "iam.kaname.in-cloud.io_serviceaccounts.yaml",
		"iam.kaname.in-cloud.io_accessbindings.yaml", "addons.in-cloud.io_addons.yaml", "gateway-api-standard-install.yaml"} {
		sh(t, nil, "kubectl", "apply", "--server-side", "-f", filepath.Join(crds, f))
	}
	sh(t, nil, "kubectl", "wait", "--for=condition=Established", "crd", "--all", "--timeout=120s")

	for _, ns := range []string{opNS, fakeNS, edgeNS} {
		namespace(t, ns, nil)
	}
	namespace(t, tenantNS, map[string]string{
		"registry.in-cloud.io/tenant": "true", registryv1alpha1.LabelNamespaceProject: "prje2e", registryv1alpha1.LabelNamespaceAccount: "acce2e",
	})

	// Центр и листы: сервер двойника, лист службы registry, сервер заявок.
	ca, err := kanametest.NewCA("e2e-ca")
	if err != nil {
		t.Fatal(err)
	}
	fakeLeaf, _ := ca.Issue("fake", []string{"fake." + fakeNS + ".svc"}, nil)
	san := "spiffe://cluster.local/ns/" + opNS + "/sa/kacho-registry"
	opLeaf, _ := ca.Issue("registry-operator", nil, []string{san})
	enrollLeaf, _ := ca.Issue("registry-operator", []string{"registry-operator." + opNS + ".svc"}, nil)
	secret(t, fakeNS, "fake-certs", fakeLeaf, ca)
	secret(t, opNS, "registry-operator-kaname-client", opLeaf, ca)
	secret(t, opNS, "registry-operator-enrollment-tls", enrollLeaf, ca)

	sh(t, []byte(fmt.Sprintf(fakeManifest, fakeNS, fakeImage, san, fakeNS)), "kubectl", "apply", "-f", "-")
	sh(t, nil, "kubectl", "-n", fakeNS, "rollout", "status", "deploy/fake", "--timeout=180s")

	// Оператор — своим чартом, конфигурация — пример площадки.
	conf, err := os.ReadFile(filepath.Join(root, samples.ConfigPath("config/samples")))
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if err := yaml.Unmarshal(conf, &c); err != nil {
		t.Fatal(err)
	}
	c["kaname"] = map[string]any{"address": "fake." + fakeNS + ".svc:9091", "serverName": "fake." + fakeNS + ".svc", "timeout": "5s"}
	c["enrollment"] = map[string]any{"audience": "registry-operator", "maxRepositories": 16, "ratePerMinute": 60, "burst": 10, "enrollerGrace": "10m", "requestTimeout": "5s"}
	c["intervals"] = map[string]any{"notReady": "2s", "ready": "10m", "refused": "10s", "deletionTimeout": "30m"}
	c["chartVerification"] = map[string]any{"timeout": "10s", "cacheTTL": "10m", "plainHTTPHosts": []any{chartHost}}
	addonPolicy := c["policy"].(map[string]any)["addon"].(map[string]any)
	addonPolicy["charts"] = []any{pin}
	repo, digest, ok := strings.Cut(opImage, "@")
	if !ok {
		t.Fatalf("E2E_OPERATOR_IMAGE must be repository@sha256:…, got %s", opImage)
	}
	values := map[string]any{
		"image":      map[string]any{"repository": repo, "tag": "e2e", "digest": digest},
		"replicas":   1,
		"config":     c,
		"kaname":     map[string]any{"certificate": map[string]any{"create": false}},
		"enrollment": map[string]any{"certificate": map[string]any{"create": false}},
	}
	vb, _ := yaml.Marshal(values)
	vf := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(vf, vb, 0o600); err != nil {
		t.Fatal(err)
	}
	sh(t, nil, "helm", "install", "registry-operator", "charts/registry-operator", "-n", opNS, "-f", vf, "--wait", "--timeout", "3m")

	set, err := samples.Load(filepath.Join(root, "config/samples"))
	if err != nil {
		t.Fatal(err)
	}
	set.Addon.Spec.Chart = observedv1alpha1.ChartPin{RepoURL: pin["repoURL"].(string), Name: "registry-instance", Version: "1.0.0", Digest: chartDigest}
	for _, tpl := range set.Templates() {
		if err := k8s.Create(ctx, tpl); err != nil {
			t.Fatal(err)
		}
	}
	if err := k8s.Create(ctx, set.RegistryTemplate); err != nil {
		t.Fatal(err)
	}
	reg := &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: registryID, Namespace: tenantNS},
		Spec:       registryv1alpha1.RegistrySpec{ID: registryID, TemplateRef: registryv1alpha1.TemplateRef{Name: "zot", Version: "v1"}},
	}
	if err := k8s.Create(ctx, reg); err != nil {
		t.Fatal(err)
	}

	// Пайплайн под ролью оператора: каждый шаг создан, готовность — от
	// контроллеров вида (здесь — проба вместо них).
	for _, stage := range kinds.Stages {
		for _, k := range stage {
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(k.GVK)
			eventually(t, "created "+k.GVK.Kind, func() error {
				list := &unstructured.UnstructuredList{}
				list.SetGroupVersionKind(k.GVK.GroupVersion().WithKind(k.GVK.Kind + "List"))
				if err := k8s.List(ctx, list, client.MatchingLabels{registryv1alpha1.LabelID: registryID}); err != nil {
					return err
				}
				if len(list.Items) != 1 {
					return fmt.Errorf("%d objects", len(list.Items))
				}
				*u = list.Items[0]
				return nil
			})
			if k.GVK == kinds.Addon.GVK {
				workload(t, ctx)
			}
			eventually(t, "ready "+k.GVK.Kind, func() error {
				cur := &unstructured.Unstructured{}
				cur.SetGroupVersionKind(k.GVK)
				if err := k8s.Get(ctx, client.ObjectKeyFromObject(u), cur); err != nil {
					return err
				}
				st, _ := testkit.ReadyStatus(cur)
				cur.Object["status"] = st
				return k8s.Status().Update(ctx, cur)
			})
		}
	}
	eventually(t, "registry ready", func() error {
		cur := &registryv1alpha1.Registry{}
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(reg), cur); err != nil {
			return err
		}
		if c := meta.FindStatusCondition(cur.Status.Conditions, registryv1alpha1.ConditionReady); c == nil || c.Status != metav1.ConditionTrue {
			return fmt.Errorf("phase %s conditions %v", cur.Status.Phase, cur.Status.Conditions)
		}
		return nil
	})
	if o, ok := fakeObjects(t, cs)[kaname.RegistryObject(registryID)]; !ok || len(o.ParentChain) != 1 || o.ParentChain[0] != "project:prje2e" {
		t.Fatalf("registry is not registered under its project: %v %s", ok, o)
	}

	// Снятие и перепись.
	if err := k8s.Delete(ctx, reg); err != nil {
		t.Fatal(err)
	}
	eventually(t, "workload removed by the addon cascade", func() error {
		a := &unstructured.Unstructured{}
		a.SetGroupVersionKind(kinds.Addon.GVK)
		err := k8s.Get(ctx, types.NamespacedName{Name: "reg-" + registryID}, a)
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("addon still exists: %v", err)
		}
		d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "zot-" + registryID, Namespace: tenantNS}}
		return client.IgnoreNotFound(k8s.Delete(ctx, d))
	})
	eventually(t, "order gone", func() error {
		err := k8s.Get(ctx, client.ObjectKeyFromObject(reg), &registryv1alpha1.Registry{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("order still exists: %v", err)
	})
	for _, k := range kinds.Written {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(k.GVK.GroupVersion().WithKind(k.GVK.Kind + "List"))
		if err := k8s.List(ctx, list, client.MatchingLabels{registryv1alpha1.LabelManagedBy: registryv1alpha1.ManagedByValue}); err != nil {
			t.Fatal(err)
		}
		if n := len(list.Items); n != 0 {
			t.Fatalf("census: %d %s left after the deletion", n, k.GVK.Kind)
		}
	}
	res := &registryv1alpha1.RegistryIDReservation{}
	if err := k8s.Get(ctx, types.NamespacedName{Name: registryID}, res); err != nil {
		t.Fatal(err)
	}
	if res.Status.Phase != registryv1alpha1.ReservationReleased {
		t.Fatalf("reservation phase %s", res.Status.Phase)
	}
	if _, ok := fakeObjects(t, cs)[kaname.RegistryObject(registryID)]; ok {
		t.Fatal("registry registration survived the deletion")
	}
	t.Logf("census: 0 objects of %d kinds left; reservation %s; IAM registration removed", len(kinds.Written), res.Status.Phase)
}

// workload — Deployment zot-<id>, как его поставил бы Argo CD.
func workload(t *testing.T, ctx context.Context) {
	t.Helper()
	one := int32(1)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "zot-" + registryID, Namespace: tenantNS},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "zot-e2e"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "zot-e2e"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "pause", Image: "registry.k8s.io/pause:3.10"}}},
			},
		},
	}
	if err := k8s.Create(ctx, d); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	eventually(t, "workload available", func() error {
		cur := &appsv1.Deployment{}
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(d), cur); err != nil {
			return err
		}
		if cur.Status.AvailableReplicas < 1 {
			return fmt.Errorf("available %d", cur.Status.AvailableReplicas)
		}
		return nil
	})
}

const fakeManifest = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: fake
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels: {app: fake}
  template:
    metadata:
      labels: {app: fake}
    spec:
      containers:
      - name: fake
        image: %s
        args: [--allowed-san=%s]
        ports:
        - {name: internal, containerPort: 9091}
        - {name: public, containerPort: 9090}
        - {name: debug, containerPort: 8080}
        volumeMounts:
        - {name: certs, mountPath: /certs, readOnly: true}
      volumes:
      - name: certs
        secret: {secretName: fake-certs}
---
apiVersion: v1
kind: Service
metadata:
  name: fake
  namespace: %s
spec:
  selector: {app: fake}
  ports:
  - {name: internal, port: 9091, targetPort: internal}
  - {name: public, port: 9090, targetPort: public}
  - {name: http, port: 8080, targetPort: debug}
`
