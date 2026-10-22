// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package controller_test

import (
	"context"
	"slices"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	observedv1alpha1 "github.com/PRO-Robotech/registry-operator/api/observed/v1alpha1"
	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/controller"
)

// Финализатор «в работе» у ревизий шаблонов ставится и снимается под
// версией объекта — тот же класс, что финализатор заказа (finalizer_test.go):
// перечень финализаторов в merge-патче заменяется целиком, и патч без
// версии стёр бы финализатор другого писателя либо вернул снятый.

// templateRacingClient — клиент, у которого между чтением ревизии и патчем
// её финализаторов другой писатель меняет их перечень. Списки ссылок пусты:
// ревизия ни на что не ссылается и никем не используется.
func templateRacingClient(t *testing.T, obj client.Object, race func(client.Object)) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, registryv1alpha1.AddToScheme, observedv1alpha1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	raced := false
	none := func(client.Object) []string { return nil }
	return fake.NewClientBuilder().WithScheme(s).WithObjects(obj).WithStatusSubresource(obj).
		WithIndex(&registryv1alpha1.RegistryTemplate{}, controller.IndexObservedTemplate, none).
		WithIndex(&registryv1alpha1.Registry{}, controller.IndexRegistryTemplate, none).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, o client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if !raced && o.GetName() == obj.GetName() {
					raced = true
					cur := o.DeepCopyObject().(client.Object)
					if err := c.Get(ctx, client.ObjectKeyFromObject(o), cur); err != nil {
						return err
					}
					race(cur)
					if err := c.Update(ctx, cur); err != nil {
						return err
					}
				}
				return c.Patch(ctx, o, patch, opts...)
			},
		}).Build()
}

func templateFinalizers(t *testing.T, c client.Client, obj client.Object) []string {
	t.Helper()
	cur := obj.DeepCopyObject().(client.Object)
	if err := c.Get(context.Background(), types.NamespacedName{Name: obj.GetName()}, cur); err != nil {
		t.Fatal(err)
	}
	return cur.GetFinalizers()
}

// templateCase — ревизия одного из двух контроллеров шаблонов и её проход.
type templateCase struct {
	name      string
	object    func(fins ...string) client.Object
	reconcile func(client.Client, string) error
}

func templateCases() []templateCase {
	return []templateCase{
		{
			name: "observed template",
			object: func(fins ...string) client.Object {
				return &observedv1alpha1.ObservedIamAccessBindingTemplate{ObjectMeta: metav1.ObjectMeta{Name: "access-v1", Finalizers: fins}}
			},
			reconcile: func(c client.Client, name string) error {
				r := &controller.ObservedTemplateReconciler{Client: c, Kind: "ObservedIamAccessBindingTemplate",
					New: func() observedv1alpha1.Template { return &observedv1alpha1.ObservedIamAccessBindingTemplate{} }}
				_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
				return err
			},
		},
		{
			name: "registry template",
			object: func(fins ...string) client.Object {
				return &registryv1alpha1.RegistryTemplate{ObjectMeta: metav1.ObjectMeta{Name: "zot-v1", Finalizers: fins}}
			},
			reconcile: func(c client.Client, name string) error {
				r := &controller.RegistryTemplateReconciler{Client: c}
				_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
				return err
			},
		},
	}
}

// Постановка: финализатор другого писателя, поставленный между чтением и
// патчем, не стирается — патч отвергается конфликтом версии.
func TestTemplateFinalizer_AddingKeepsAForeignFinalizer(t *testing.T) {
	for _, tc := range templateCases() {
		t.Run(tc.name, func(t *testing.T) {
			obj := tc.object()
			c := templateRacingClient(t, obj, func(cur client.Object) {
				cur.SetFinalizers(append(cur.GetFinalizers(), foreignFinalizer))
			})
			if err := tc.reconcile(c, obj.GetName()); !apierrors.IsConflict(err) {
				t.Fatalf("a finalizer patch over a newer revision was not a conflict: %v", err)
			}
			if got := templateFinalizers(t, c, obj); !slices.Contains(got, foreignFinalizer) {
				t.Fatalf("the foreign finalizer was erased: %v", got)
			}
		})
	}
}

// Снятие: финализатор, снятый другим писателем между чтением и патчем, не
// возвращается.
func TestTemplateFinalizer_DroppingDoesNotResurrectARemovedFinalizer(t *testing.T) {
	for _, tc := range templateCases() {
		t.Run(tc.name, func(t *testing.T) {
			obj := tc.object(registryv1alpha1.InUseFinalizer, foreignFinalizer)
			now := metav1.Now()
			obj.SetDeletionTimestamp(&now)
			c := templateRacingClient(t, obj, func(cur client.Object) {
				cur.SetFinalizers(slices.DeleteFunc(cur.GetFinalizers(), func(f string) bool { return f == foreignFinalizer }))
			})
			if err := tc.reconcile(c, obj.GetName()); !apierrors.IsConflict(err) {
				t.Fatalf("a finalizer patch over a newer revision was not a conflict: %v", err)
			}
			if got := templateFinalizers(t, c, obj); slices.Contains(got, foreignFinalizer) {
				t.Fatalf("a finalizer removed by another writer came back: %v", got)
			}
		})
	}
}
