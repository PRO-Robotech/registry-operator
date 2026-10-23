// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/api/meta"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/kinds"
)

// SetupWithManager — контроллер заказов. Будят заказ: его собственные
// изменения; изменения шести ведомых видов (по меткам владения — объект
// без меток заказа ничего не будит); метки namespace (проект IAM); ревизии
// шаблона заказа.
func (r *RegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		Named("registry").
		For(&registryv1alpha1.Registry{}, builder.WithPredicates(predicate.Or(
			predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{}, deletionPredicate(),
		)))
	byOwnerLabels := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		l := o.GetLabels()
		if l[registryv1alpha1.LabelManagedBy] != registryv1alpha1.ManagedByValue {
			return nil
		}
		id, ns := l[registryv1alpha1.LabelID], l[registryv1alpha1.LabelNamespace]
		if id == "" || ns == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: ns, Name: id}}}
	})
	available, err := AvailableWritten(mgr.GetRESTMapper())
	if err != nil {
		return err
	}
	for _, k := range available {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(k.GVK)
		b = b.Watches(u, byOwnerLabels)
	}
	b = b.Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		list := &registryv1alpha1.RegistryList{}
		if err := mgr.GetClient().List(ctx, list, client.InNamespace(o.GetName())); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0, len(list.Items))
		for _, reg := range list.Items {
			out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: reg.Namespace, Name: reg.Name}})
		}
		return out
	}), builder.WithPredicates(predicate.LabelChangedPredicate{}))
	b = b.Watches(&registryv1alpha1.RegistryTemplate{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		list := &registryv1alpha1.RegistryList{}
		if err := mgr.GetClient().List(ctx, list, client.MatchingFields{IndexRegistryTemplate: o.GetName()}); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0, len(list.Items))
		for _, reg := range list.Items {
			out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: reg.Namespace, Name: reg.Name}})
		}
		return out
	}))
	concurrency := r.MaxConcurrentReconciles
	if concurrency < 1 {
		concurrency = 1
	}
	return b.WithOptions(controller.Options{MaxConcurrentReconciles: concurrency}).Complete(r)
}

// Setup регистрирует индексы и все контроллеры оператора в менеджере.
func Setup(ctx context.Context, mgr ctrl.Manager, deps *Deps) error {
	if err := SetupIndexes(ctx, mgr); err != nil {
		return err
	}
	if err := (&RegistryReconciler{Client: mgr.GetClient(), Deps: deps}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&RegistryTemplateReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		return err
	}
	for _, k := range observedKinds {
		r := &ObservedTemplateReconciler{Client: mgr.GetClient(), Kind: k.kind, New: k.new, Policy: &deps.Config.Policy}
		if err := r.SetupWithManager(mgr); err != nil {
			return err
		}
	}
	return nil
}

// CacheByObject — кэш ведомых видов только с метками владения оператора:
// чужие объекты тех же видов в памяти оператора не держатся.
func CacheByObject(available ...kinds.Kind) map[client.Object]cache.ByObject {
	if len(available) == 0 {
		available = kinds.Written
	}
	sel := labels.SelectorFromSet(labels.Set{registryv1alpha1.LabelManagedBy: registryv1alpha1.ManagedByValue})
	out := map[client.Object]cache.ByObject{}
	for _, k := range available {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(k.GVK)
		out[u] = cache.ByObject{Label: sel}
	}
	return out
}

// AvailableWritten исключает только отсутствующий необязательный IAM API.
// Ошибки discovery и отсутствие любого обязательного вида не скрываются.
func AvailableWritten(mapper meta.RESTMapper) ([]kinds.Kind, error) {
	out := make([]kinds.Kind, 0, len(kinds.Written))
	for _, k := range kinds.Written {
		if _, err := mapper.RESTMapping(k.GVK.GroupKind(), k.GVK.Version); err != nil {
			if kinds.OptionalClientKind(k) && meta.IsNoMatchError(err) {
				continue
			}
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}
