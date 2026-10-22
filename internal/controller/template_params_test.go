// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package controller_test

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	observedv1alpha1 "github.com/PRO-Robotech/registry-operator/api/observed/v1alpha1"
	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/controller"
	"github.com/PRO-Robotech/registry-operator/internal/samples"
)

// acceptedCondition — условие Accepted ревизии после одного прохода её
// контроллера на клиенте, где есть только она (шесть шаблонов отсутствуют).
func acceptedCondition(t *testing.T, params string) *metav1.Condition {
	t.Helper()
	set, err := samples.Load(samplesDir)
	if err != nil {
		t.Fatal(err)
	}
	rt := set.RegistryTemplate.DeepCopy()
	rt.Spec.Params = params
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, registryv1alpha1.AddToScheme, observedv1alpha1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	none := func(client.Object) []string { return nil }
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rt).WithStatusSubresource(rt).
		WithIndex(&registryv1alpha1.Registry{}, controller.IndexRegistryTemplate, none).Build()
	r := &controller.RegistryTemplateReconciler{Client: c}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: rt.Name}}); err != nil {
		t.Fatal(err)
	}
	cur := &registryv1alpha1.RegistryTemplate{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: rt.Name}, cur); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(cur.Status.Conditions, registryv1alpha1.ConditionAccepted)
	if cond == nil {
		t.Fatal("no Accepted condition")
	}
	return cond
}

// Регион площадки — обязательный параметр ревизии: без непустого
// params.storage.region ревизия не принимается, и отказ называет параметр.
// Законный близнец — ru-1 — проходит проверку параметров (дальше ревизия
// упирается в отсутствующие шаблоны, а не в параметры).
func TestRegistryTemplate_ParamsWithoutARegionAreNotAccepted(t *testing.T) {
	params := func(region string) string {
		return "storage:\n  vaultConfig: s3-bucket\n  region: " + region + "\n"
	}
	twin := acceptedCondition(t, params("ru-1"))
	if strings.Contains(twin.Message, "params") {
		t.Fatalf("lawful twin refused by its params: %s", twin.Message)
	}
	for _, c := range []struct{ name, params string }{
		{"empty region", params(`""`)},
		{"region not a name", params("RU_1")},
		{"no region", "storage:\n  vaultConfig: s3-bucket\n"},
		{"no storage", "addon:\n  valuesAddon: registry-instance\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cond := acceptedCondition(t, c.params)
			if cond.Status != metav1.ConditionFalse || cond.Reason != registryv1alpha1.ReasonTemplateInvalid ||
				!strings.Contains(cond.Message, "params.storage.region:") {
				t.Fatalf("Accepted %s %s %q, want False TemplateInvalid naming params.storage.region", cond.Status, cond.Reason, cond.Message)
			}
		})
	}
}
