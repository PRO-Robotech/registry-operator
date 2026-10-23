// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package controller_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/controller"
)

const foreignFinalizer = "platform.example.test/audit"

// errPassStopped — проход остановлен сразу после патча финализатора:
// предмет проб — только патч, дальше пайплайн не идёт.
var errPassStopped = errors.New("pass stopped after the finalizer patch")

// racingClient — клиент, у которого между чтением заказа и патчем его
// финализаторов другой писатель меняет их перечень.
func racingClient(t *testing.T, reg *registryv1alpha1.Registry, race func(*registryv1alpha1.Registry)) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, registryv1alpha1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	raced := false
	return fake.NewClientBuilder().WithScheme(s).WithObjects(reg).WithStatusSubresource(reg).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*registryv1alpha1.Registry); ok && !raced {
					raced = true
					cur := &registryv1alpha1.Registry{}
					if err := c.Get(ctx, client.ObjectKeyFromObject(obj), cur); err != nil {
						return err
					}
					race(cur)
					if err := c.Update(ctx, cur); err != nil {
						return err
					}
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
			Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
				return errPassStopped
			},
		}).Build()
}

func finalizerReconciler(c client.Client) *controller.RegistryReconciler {
	return &controller.RegistryReconciler{Client: c, Deps: &controller.Deps{
		Config: conf, APIReader: c, Kaname: kn.Direct(), Clock: clocktesting.NewFakePassiveClock(time.Now()),
		Recorder: events.NewFakeRecorder(16),
	}}
}

func finalizers(t *testing.T, c client.Client) []string {
	t.Helper()
	cur := &registryv1alpha1.Registry{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "cust01", Name: "fin01"}, cur); err != nil {
		t.Fatal(err)
	}
	return cur.Finalizers
}

func finalizerOrder(fins ...string) *registryv1alpha1.Registry {
	return &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: "fin01", Namespace: "cust01", Finalizers: fins},
		Spec:       registryv1alpha1.RegistrySpec{ID: "fin01a", TemplateRef: registryv1alpha1.TemplateRef{Name: "zot", Version: "v1"}},
	}
}

// Финализатор заказа ставится под версией объекта: финализатор другого
// писателя, поставленный между чтением и патчем, не стирается.
func TestFinalizer_AddingKeepsAForeignFinalizer(t *testing.T) {
	c := racingClient(t, finalizerOrder(), func(r *registryv1alpha1.Registry) {
		r.Finalizers = append(r.Finalizers, foreignFinalizer)
	})
	_, err := finalizerReconciler(c).Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "cust01", Name: "fin01"}})
	if !apierrors.IsConflict(err) {
		t.Fatalf("a finalizer patch over a newer object was not a conflict: %v", err)
	}
	if got := finalizers(t, c); !slices.Contains(got, foreignFinalizer) {
		t.Fatalf("the foreign finalizer was erased: %v", got)
	}
}

// Снятие финализатора — тоже под версией: финализатор, снятый другим
// писателем, не возвращается.
func TestFinalizer_DroppingDoesNotResurrectARemovedFinalizer(t *testing.T) {
	reg := finalizerOrder(registryv1alpha1.RegistryFinalizer, foreignFinalizer)
	now := metav1.Now()
	reg.DeletionTimestamp = &now
	c := racingClient(t, reg, func(r *registryv1alpha1.Registry) {
		r.Finalizers = slices.DeleteFunc(r.Finalizers, func(f string) bool { return f == foreignFinalizer })
	})
	_, err := finalizerReconciler(c).Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "cust01", Name: "fin01"}})
	if !apierrors.IsConflict(err) {
		t.Fatalf("a finalizer patch over a newer object was not a conflict: %v", err)
	}
	if got := finalizers(t, c); slices.Contains(got, foreignFinalizer) {
		t.Fatalf("a finalizer removed by another writer came back: %v", got)
	}
}
