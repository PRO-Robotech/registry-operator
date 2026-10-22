// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package controller_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	observedv1alpha1 "github.com/PRO-Robotech/registry-operator/api/observed/v1alpha1"
	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/config"
	"github.com/PRO-Robotech/registry-operator/internal/controller"
	"github.com/PRO-Robotech/registry-operator/internal/kaname/kanametest"
	"github.com/PRO-Robotech/registry-operator/internal/ocidigest"
	"github.com/PRO-Robotech/registry-operator/internal/samples"
)

const samplesDir = "../../config/samples"

// Общее окружение пакета: API-сервер envtest с CRD оператора и целевыми
// CRD (vault-operator, kaname-iam-operator, addons-operator, Gateway API),
// менеджер с контроллерами оператора и двойник kaname.
var (
	k8s  client.Client
	kn   *kanametest.Fake
	conf *config.Config
)

// pinAccepted — сверка пина чарта проб: реестр чартов не нужен.
type pinAccepted struct{}

func (pinAccepted) Verify(context.Context, ocidigest.Chart) error { return nil }

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	crds := os.Getenv("REGOP_TEST_CRDS")
	if crds == "" {
		crds = filepath.Join("..", "..", "bin", "crds")
	}
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		// Не пропуск: без API-сервера проба не выполнилась, и это отказ.
		fmt.Fprintln(os.Stderr, "envtest: KUBEBUILDER_ASSETS is not set; run `make test`")
		return 3
	}
	if _, err := os.Stat(filepath.Join(crds, "vault.in-cloud.io_vaultclaims.yaml")); err != nil {
		fmt.Fprintln(os.Stderr, "envtest: target CRDs are missing; run `make crds`:", err)
		return 3
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases"), crds},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "envtest start:", err)
		return 3
	}
	defer func() { _ = env.Stop() }()

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, registryv1alpha1.AddToScheme, observedv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
	}
	conf, err = config.Load(samples.ConfigPath(samplesDir))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	// Темп проб: события будят проход сразу, повторы — короткие. Срок
	// заявки и вызова IAM — ноль: снятие не ждёт идущих заявок.
	conf.Intervals.NotReady = metav1.Duration{Duration: time.Second}
	conf.Intervals.Refused = metav1.Duration{Duration: 2 * time.Second}
	conf.Enrollment.RequestTimeout = metav1.Duration{}
	conf.Kaname.Timeout = metav1.Duration{}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Cache:                  cache.Options{ByObject: controller.CacheByObject()},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	kn = kanametest.New("unused")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps := &controller.Deps{
		Config: conf, Verifier: pinAccepted{}, Kaname: kn.Direct(), Recorder: mgr.GetEventRecorder("registry-operator"),
		Clock: clock.RealClock{}, APIReader: mgr.GetAPIReader(), MaxConcurrentReconciles: 4,
	}
	if err := controller.Setup(ctx, mgr, deps); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	go func() {
		if err := mgr.Start(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "manager:", err)
		}
	}()
	k8s, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	if err := installTemplates(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "templates:", err)
		return 3
	}
	return m.Run()
}

// installTemplates ставит ревизию zot-v1 и её шесть шаблонов из примеров и
// ждёт, пока все примут.
func installTemplates(ctx context.Context) error {
	// Namespace края, где живут маршруты заказов (политика примера).
	if err := k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "registry-edge"}}); err != nil {
		return err
	}
	set, err := samples.Load(samplesDir)
	if err != nil {
		return err
	}
	for _, t := range set.Templates() {
		if err := k8s.Create(ctx, t); err != nil {
			return err
		}
	}
	if err := k8s.Create(ctx, set.RegistryTemplate); err != nil {
		return err
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		rt := &registryv1alpha1.RegistryTemplate{}
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(set.RegistryTemplate), rt); err == nil {
			for _, c := range rt.Status.Conditions {
				if c.Type == registryv1alpha1.ConditionAccepted && c.Status == metav1.ConditionTrue {
					return nil
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("template revision zot-v1 was not accepted within 60s")
}

// tenant — namespace арендатора с метками платформы.
func tenant(t *testing.T, name, project, account string) {
	t.Helper()
	labels := map[string]string{"registry.in-cloud.io/tenant": "true"}
	if project != "" {
		labels[registryv1alpha1.LabelNamespaceProject] = project
	}
	if account != "" {
		labels[registryv1alpha1.LabelNamespaceAccount] = account
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	if err := k8s.Create(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
}
