// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package serviceonly_test

import (
	"context"
	"fmt"
	"os"
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
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"

	observed "github.com/PRO-Robotech/registry-operator/api/observed/v1alpha1"
	api "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/config"
	"github.com/PRO-Robotech/registry-operator/internal/controller"
	"github.com/PRO-Robotech/registry-operator/internal/kaname"
	"github.com/PRO-Robotech/registry-operator/internal/kaname/kanametest"
	"github.com/PRO-Robotech/registry-operator/internal/kinds"
	"github.com/PRO-Robotech/registry-operator/internal/ocidigest"
	"github.com/PRO-Robotech/registry-operator/internal/samples"
	"github.com/PRO-Robotech/registry-operator/internal/testkit"
)

type acceptPin struct{}

func (acceptPin) Verify(context.Context, ocidigest.Chart) error { return nil }

// Отдельный настоящий API server: IAM child CRD намеренно НЕ установлены.
// Статусы внешних операторов моделируются; lifecycle самого Registry выполняет production controller.
func TestServiceOnlyWithoutIAMAPICreateAndDelete(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("NOT EXECUTED: KUBEBUILDER_ASSETS is required")
	}
	root := filepath.Join("..", "..")
	crds := os.Getenv("REGOP_TEST_CRDS")
	if crds == "" {
		crds = filepath.Join(root, "bin", "crds")
	}
	filtered := t.TempDir()
	files, err := os.ReadDir(crds)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.IsDir() || strings.HasPrefix(f.Name(), "iam.kaname.in-cloud.io_") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(crds, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(filtered, f.Name()), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(root, "config/crd/bases"), filtered}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, api.AddToScheme, observed.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	available, err := controller.AvailableWritten(c.RESTMapper())
	if err != nil || len(available) != 4 {
		t.Fatalf("API census %d: %v", len(available), err)
	}
	for _, k := range []kinds.Kind{kinds.IamServiceAccount, kinds.IamAccessBinding} {
		if _, err := c.RESTMapper().RESTMapping(k.GVK.GroupKind(), k.GVK.Version); !meta.IsNoMatchError(err) {
			t.Fatal("IAM child API unexpectedly installed")
		}
	}
	conf, err := config.Load(samples.ConfigPath(filepath.Join(root, "config/samples")))
	if err != nil {
		t.Fatal(err)
	}
	conf.Intervals.NotReady.Duration = 100 * time.Millisecond
	conf.Intervals.Refused.Duration = 100 * time.Millisecond
	conf.Enrollment.RequestTimeout.Duration = 0
	conf.Kaname.Timeout.Duration = 0
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0", Cache: cache.Options{ByObject: controller.CacheByObject(available...)}})
	if err != nil {
		t.Fatal(err)
	}
	kn := kanametest.New("unused")
	ctx, cancel := context.WithCancel(context.Background())
	deps := &controller.Deps{Config: conf, Verifier: acceptPin{}, Kaname: kn.Direct(), Recorder: mgr.GetEventRecorder("service-only"), Clock: clock.RealClock{}, APIReader: mgr.GetAPIReader()}
	if err := controller.Setup(ctx, mgr, deps); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("manager did not stop")
		}
	})
	wait := func(what string, f func() error) {
		t.Helper()
		until := time.Now().Add(45 * time.Second)
		for {
			err := f()
			if err == nil {
				return
			}
			if time.Now().After(until) {
				t.Fatalf("%s: %v", what, err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	for _, ns := range []string{"registry-edge", "tenant01"} {
		labels := map[string]string{"registry.in-cloud.io/tenant": "true", api.LabelNamespaceProject: "prj01", api.LabelNamespaceAccount: "acc01"}
		if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: labels}}); err != nil {
			t.Fatal(err)
		}
	}
	set, err := samples.Load(filepath.Join(root, "config/samples"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tpl := range set.Templates() {
		if err := c.Create(ctx, tpl); err != nil {
			t.Fatal(err)
		}
	}
	legacy := set.RegistryTemplate.DeepCopy()
	if err := c.Create(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	service := &api.RegistryTemplate{}
	serviceYAML, err := os.ReadFile(filepath.Join(root, "config/samples/templates/registrytemplate-service.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.UnmarshalStrict(serviceYAML, service); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, service); err != nil {
		t.Fatal(err)
	}
	// Admission принимает только целую optional-пару, обязательные4 refs остаются.
	for i, mutate := range []func(*api.RegistryTemplate){
		func(r *api.RegistryTemplate) { r.Spec.Templates.IamServiceAccount = nil },
		func(r *api.RegistryTemplate) { r.Spec.Templates.IamAccessBinding = nil },
		func(r *api.RegistryTemplate) { r.Spec.Templates.S3BucketClaim = api.TemplateRef{} },
	} {
		bad := set.RegistryTemplate.DeepCopy()
		bad.Name = fmt.Sprintf("bad%d-v1", i)
		bad.Spec.TemplateName = fmt.Sprintf("bad%d", i)
		mutate(bad)
		if err := c.Create(ctx, bad); !apierrors.IsInvalid(err) {
			t.Fatalf("invalid optional/required refs accepted: %v", err)
		}
	}
	newOrder := func(id, template string) *api.Registry {
		r := &api.Registry{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: "tenant01"}, Spec: api.RegistrySpec{ID: id, TemplateRef: api.TemplateRef{Name: template, Version: "v1"}}}
		if err := c.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	legacyOrder := newOrder("legacy01", "zot")
	wait("explicit IAM dependency refusal", func() error {
		r := &api.Registry{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(legacyOrder), r); err != nil {
			return err
		}
		cond := meta.FindStatusCondition(r.Status.Conditions, api.ConditionReady)
		if cond == nil || cond.Reason != api.ReasonDependencyUnavailable || cond.Status != metav1.ConditionFalse {
			return fmt.Errorf("condition %v", cond)
		}
		return nil
	})
	r := newOrder("svc001", "zot-service")
	objects := map[string]*unstructured.Unstructured{}
	mark := func(k kinds.Kind, name, namespace string) {
		wait("child "+k.GVK.Kind, func() error {
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(k.GVK)
			if err := c.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, u); err != nil {
				return err
			}
			st, ok := testkit.ReadyStatus(u)
			if !ok {
				return fmt.Errorf("no status")
			}
			u.Object["status"] = st
			if err := c.Status().Update(ctx, u); err != nil {
				return err
			}
			objects[k.GVK.Kind] = u
			return nil
		})
	}
	mark(kinds.S3BucketClaim, "zot-svc001", "tenant01")
	mark(kinds.VaultClaim, "zot-svc001", "tenant01")
	mark(kinds.Addon, conf.Policy.AddonName("svc001"), "")
	one := int32(1)
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "zot-svc001", Namespace: "tenant01"}, Spec: appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "zot"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "zot"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "zot", Image: "zot"}}}}}}
	if err := c.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1, ReadyReplicas: 1, Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue}}}
	if err := c.Status().Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	mark(kinds.HTTPRoute, "zot-svc001", "registry-edge")
	wait("service ready without IAM", func() error {
		current := &api.Registry{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(r), current); err != nil {
			return err
		}
		cond := meta.FindStatusCondition(current.Status.Conditions, api.ConditionReady)
		if cond == nil || cond.Status != metav1.ConditionTrue {
			return fmt.Errorf("condition %v", cond)
		}
		if len(current.Status.Resources) != 4 {
			return fmt.Errorf("resource census %d", len(current.Status.Resources))
		}
		if meta.FindStatusCondition(current.Status.Conditions, api.ConditionIdentityReady) != nil || meta.FindStatusCondition(current.Status.Conditions, api.ConditionAccessReady) != nil {
			return fmt.Errorf("fake IAM readiness")
		}
		return nil
	})
	if _, ok := kn.Objects()[kaname.RegistryObject("svc001")]; !ok {
		t.Fatal("service skipped resource registration")
	}
	if err := c.Delete(ctx, r); err != nil {
		t.Fatal(err)
	}
	// Addon-controller в этом API fixture отсутствует: моделируем его teardown,
	// но только после того, как Registry действительно удалил Addon.
	wait("addon removed before workload", func() error {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(kinds.Addon.GVK)
		if err := c.Get(ctx, client.ObjectKey{Name: conf.Policy.AddonName("svc001")}, u); !apierrors.IsNotFound(err) {
			return fmt.Errorf("addon remains: %v", err)
		}
		return nil
	})
	if err := c.Delete(ctx, d); err != nil {
		t.Fatal(err)
	}
	wait("service deletion", func() error {
		cur := &api.Registry{}
		err := c.Get(ctx, client.ObjectKeyFromObject(r), cur)
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("order still present: %v", err)
	})
	for _, u := range objects {
		wait("owned child gone", func() error {
			cur := &unstructured.Unstructured{}
			cur.SetGroupVersionKind(u.GroupVersionKind())
			err := c.Get(ctx, client.ObjectKeyFromObject(u), cur)
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("child remains: %v", err)
		})
	}
	if _, ok := kn.Objects()[kaname.RegistryObject("svc001")]; ok {
		t.Fatal("registration leaked")
	}
	res := &api.RegistryIDReservation{}
	if err := c.Get(ctx, client.ObjectKey{Name: "svc001"}, res); err != nil {
		t.Fatal(err)
	}
	if res.Status.Phase != api.ReservationReleased {
		t.Fatal("tombstone not released")
	}
}
