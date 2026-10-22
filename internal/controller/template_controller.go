// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	observedv1alpha1 "github.com/PRO-Robotech/registry-operator/api/observed/v1alpha1"
	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/policy"
	"github.com/PRO-Robotech/registry-operator/internal/render"
)

// +kubebuilder:rbac:groups=registry.in-cloud.io,resources=registrytemplates,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=registry.in-cloud.io,resources=registrytemplates/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=registry.in-cloud.io,resources=registrytemplates/finalizers,verbs=update
// +kubebuilder:rbac:groups=observed.registry.in-cloud.io,resources=observeds3bucketclaimtemplates;observedvaultclaimtemplates;observediamserviceaccounttemplates;observediamaccessbindingtemplates;observedaddontemplates;observedhttproutetemplates,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=observed.registry.in-cloud.io,resources=observeds3bucketclaimtemplates/status;observedvaultclaimtemplates/status;observediamserviceaccounttemplates/status;observediamaccessbindingtemplates/status;observedaddontemplates/status;observedhttproutetemplates/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=observed.registry.in-cloud.io,resources=observeds3bucketclaimtemplates/finalizers;observedvaultclaimtemplates/finalizers;observediamserviceaccounttemplates/finalizers;observediamaccessbindingtemplates/finalizers;observedaddontemplates/finalizers;observedhttproutetemplates/finalizers,verbs=update

// Ключи индексов «кто на что ссылается».
const (
	// IndexRegistryTemplate — заказ по ревизии шаблона: и заказанной, и той,
	// по которой отрисованы его ресурсы.
	IndexRegistryTemplate = "registry.in-cloud.io/template"
	// IndexObservedTemplate — ревизия шаблона заказа по ссылкам на
	// observed-шаблоны: "<Kind>/<name>".
	IndexObservedTemplate = "registry.in-cloud.io/observed"
)

// ReasonTemplateInUse — ревизия удаляется, но на неё ссылаются.
const ReasonTemplateInUse = "InUse"

// observedKinds — шесть видов observed-шаблонов: фабрика объекта и ключ
// ссылки из RegistryTemplate.
var observedKinds = []struct {
	kind string
	new  func() observedv1alpha1.Template
	ref  func(registryv1alpha1.ResourceTemplates) registryv1alpha1.TemplateRef
}{
	{"ObservedS3BucketClaimTemplate", func() observedv1alpha1.Template { return &observedv1alpha1.ObservedS3BucketClaimTemplate{} },
		func(r registryv1alpha1.ResourceTemplates) registryv1alpha1.TemplateRef { return r.S3BucketClaim }},
	{"ObservedVaultClaimTemplate", func() observedv1alpha1.Template { return &observedv1alpha1.ObservedVaultClaimTemplate{} },
		func(r registryv1alpha1.ResourceTemplates) registryv1alpha1.TemplateRef { return r.VaultClaim }},
	{"ObservedIamServiceAccountTemplate", func() observedv1alpha1.Template { return &observedv1alpha1.ObservedIamServiceAccountTemplate{} },
		func(r registryv1alpha1.ResourceTemplates) registryv1alpha1.TemplateRef { return r.IamServiceAccount }},
	{"ObservedIamAccessBindingTemplate", func() observedv1alpha1.Template { return &observedv1alpha1.ObservedIamAccessBindingTemplate{} },
		func(r registryv1alpha1.ResourceTemplates) registryv1alpha1.TemplateRef { return r.IamAccessBinding }},
	{"ObservedAddonTemplate", func() observedv1alpha1.Template { return &observedv1alpha1.ObservedAddonTemplate{} },
		func(r registryv1alpha1.ResourceTemplates) registryv1alpha1.TemplateRef { return r.Addon }},
	{"ObservedHTTPRouteTemplate", func() observedv1alpha1.Template { return &observedv1alpha1.ObservedHTTPRouteTemplate{} },
		func(r registryv1alpha1.ResourceTemplates) registryv1alpha1.TemplateRef { return r.HTTPRoute }},
}

// SetupIndexes регистрирует индексы ссылок.
func SetupIndexes(ctx context.Context, mgr ctrl.Manager) error {
	fi := mgr.GetFieldIndexer()
	if err := fi.IndexField(ctx, &registryv1alpha1.Registry{}, IndexRegistryTemplate, func(o client.Object) []string {
		reg := o.(*registryv1alpha1.Registry)
		out := []string{reg.Spec.TemplateRef.ObjectName()}
		if t := reg.Status.Template; t != nil && t.ObjectName() != out[0] {
			out = append(out, t.ObjectName())
		}
		return out
	}); err != nil {
		return err
	}
	return fi.IndexField(ctx, &registryv1alpha1.RegistryTemplate{}, IndexObservedTemplate, func(o client.Object) []string {
		rt := o.(*registryv1alpha1.RegistryTemplate)
		out := make([]string, 0, len(observedKinds))
		for _, k := range observedKinds {
			out = append(out, k.kind+"/"+k.ref(rt.Spec.Templates).ObjectName())
		}
		return out
	})
}

// ---------------------------------------------------------------- observed

// ObservedTemplateReconciler — принятие ревизии observed-шаблона одного
// вида: разбор, AST-гейт и проверки вида; удаление держится, пока на
// ревизию ссылается RegistryTemplate.
type ObservedTemplateReconciler struct {
	client.Client
	Kind   string
	New    func() observedv1alpha1.Template
	Policy *policy.Config
}

// Reconcile — один проход ревизии.
func (r *ObservedTemplateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := r.New()
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	refs := &registryv1alpha1.RegistryTemplateList{}
	if err := r.List(ctx, refs, client.MatchingFields{IndexObservedTemplate: r.Kind + "/" + obj.GetName()}); err != nil {
		return ctrl.Result{}, err
	}
	if !obj.GetDeletionTimestamp().IsZero() {
		if len(refs.Items) > 0 {
			return ctrl.Result{}, r.setAccepted(ctx, obj, false, ReasonTemplateInUse,
				fmt.Sprintf("referenced by RegistryTemplate %s", refs.Items[0].Name))
		}
		return ctrl.Result{}, r.dropFinalizer(ctx, obj)
	}
	if !controllerutil.ContainsFinalizer(obj, registryv1alpha1.InUseFinalizer) {
		// Финализаторы — массив, merge patch заменяет его целиком: патч
		// ставится и снимается под версией объекта (как у заказа), иначе он
		// стёр бы чужой финализатор либо вернул снятый. Конфликт — повтор.
		patch := client.MergeFromWithOptions(obj.DeepCopyObject().(client.Object), client.MergeFromWithOptimisticLock{})
		controllerutil.AddFinalizer(obj, registryv1alpha1.InUseFinalizer)
		if err := r.Patch(ctx, obj, patch); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := r.check(obj); err != nil {
		return ctrl.Result{}, r.setAccepted(ctx, obj, false, registryv1alpha1.ReasonTemplateInvalid, err.Error())
	}
	return ctrl.Result{}, r.setAccepted(ctx, obj, true, registryv1alpha1.ReasonAccepted, "template revision is accepted")
}

// check — разбор и гейт Go-шаблона и проверки вида, известные без заказа.
func (r *ObservedTemplateReconciler) check(obj observedv1alpha1.Template) error {
	if _, err := render.Compile(obj.GetName(), obj.Body().Value); err != nil {
		return err
	}
	switch t := obj.(type) {
	case *observedv1alpha1.ObservedAddonTemplate:
		pin := t.Spec.Chart
		if !r.Policy.Addon.ChartAllowed(policy.Chart{RepoURL: pin.RepoURL, Name: pin.Name, Version: pin.Version, Digest: pin.Digest}) {
			return fmt.Errorf("chart pin %s/%s %s %s is not an allowed platform pin", pin.RepoURL, pin.Name, pin.Version, pin.Digest)
		}
	case *observedv1alpha1.ObservedHTTPRouteTemplate:
		ns := t.Spec.TargetNamespace
		if ns == "" && !r.Policy.Route.AllowOrderNamespace {
			return fmt.Errorf("routes in the order namespace are not allowed by the platform")
		}
		if ns != "" && !slices.Contains(r.Policy.Route.Namespaces, ns) {
			return fmt.Errorf("namespace %q is not a platform route namespace", ns)
		}
	}
	return nil
}

func (r *ObservedTemplateReconciler) setAccepted(ctx context.Context, obj observedv1alpha1.Template, ok bool, reason, msg string) error {
	st := obj.TemplateStatus()
	orig := st.DeepCopy()
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&st.Conditions, metav1.Condition{
		Type: registryv1alpha1.ConditionAccepted, Status: status, Reason: reason,
		Message: truncate(msg, 1024), ObservedGeneration: obj.GetGeneration(),
	})
	st.ObservedGeneration = obj.GetGeneration()
	if equality.Semantic.DeepEqual(orig, st) {
		return nil
	}
	return r.Status().Update(ctx, obj)
}

func (r *ObservedTemplateReconciler) dropFinalizer(ctx context.Context, obj observedv1alpha1.Template) error {
	if !controllerutil.ContainsFinalizer(obj, registryv1alpha1.InUseFinalizer) {
		return nil
	}
	patch := client.MergeFromWithOptions(obj.DeepCopyObject().(client.Object), client.MergeFromWithOptimisticLock{})
	controllerutil.RemoveFinalizer(obj, registryv1alpha1.InUseFinalizer)
	return client.IgnoreNotFound(r.Patch(ctx, obj, patch))
}

// SetupWithManager — контроллер ревизий вида; ссылки RegistryTemplate
// будят ревизии, на которые указывают.
func (r *ObservedTemplateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("template-"+r.Kind).
		For(r.New(), builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.AnnotationChangedPredicate{}, deletionPredicate()))).
		Watches(&registryv1alpha1.RegistryTemplate{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			rt := o.(*registryv1alpha1.RegistryTemplate)
			for _, k := range observedKinds {
				if k.kind == r.Kind {
					return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: k.ref(rt.Spec.Templates).ObjectName()}}}
				}
			}
			return nil
		})).
		WithOptions(controller.Options{MaxConcurrentReconciles: 2}).
		Complete(r)
}

// ---------------------------------------------------------------- registry template

// RegistryTemplateReconciler — принятие ревизии шаблона заказа: параметры
// разбираются, каждая из шести ссылок существует и принята; удаление
// держится, пока на ревизию ссылается заказ.
type RegistryTemplateReconciler struct {
	client.Client
}

// Reconcile — один проход ревизии.
func (r *RegistryTemplateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	rt := &registryv1alpha1.RegistryTemplate{}
	if err := r.Get(ctx, req.NamespacedName, rt); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	users := &registryv1alpha1.RegistryList{}
	if err := r.List(ctx, users, client.MatchingFields{IndexRegistryTemplate: rt.Name}); err != nil {
		return ctrl.Result{}, err
	}
	if !rt.DeletionTimestamp.IsZero() {
		if len(users.Items) > 0 {
			u := users.Items[0]
			return ctrl.Result{}, r.setAccepted(ctx, rt, false, ReasonTemplateInUse, fmt.Sprintf("referenced by Registry %s/%s", u.Namespace, u.Name))
		}
		if controllerutil.ContainsFinalizer(rt, registryv1alpha1.InUseFinalizer) {
			patch := client.MergeFromWithOptions(rt.DeepCopy(), client.MergeFromWithOptimisticLock{})
			controllerutil.RemoveFinalizer(rt, registryv1alpha1.InUseFinalizer)
			return ctrl.Result{}, client.IgnoreNotFound(r.Patch(ctx, rt, patch))
		}
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(rt, registryv1alpha1.InUseFinalizer) {
		patch := client.MergeFromWithOptions(rt.DeepCopy(), client.MergeFromWithOptimisticLock{})
		controllerutil.AddFinalizer(rt, registryv1alpha1.InUseFinalizer)
		if err := r.Patch(ctx, rt, patch); err != nil {
			return ctrl.Result{}, err
		}
	}
	params, err := render.StrictYAMLMap([]byte(rt.Spec.Params))
	if err != nil {
		return ctrl.Result{}, r.setAccepted(ctx, rt, false, registryv1alpha1.ReasonTemplateInvalid, "params: "+err.Error())
	}
	if err := policy.CheckTemplateParams(params); err != nil {
		return ctrl.Result{}, r.setAccepted(ctx, rt, false, registryv1alpha1.ReasonTemplateInvalid, err.Error())
	}
	for _, k := range observedKinds {
		ref := k.ref(rt.Spec.Templates)
		obj := k.new()
		if err := r.Get(ctx, types.NamespacedName{Name: ref.ObjectName()}, obj); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.setAccepted(ctx, rt, false, registryv1alpha1.ReasonTemplateInvalid,
				fmt.Sprintf("%s %s does not exist", k.kind, ref.ObjectName()))
		}
		if !obj.GetDeletionTimestamp().IsZero() || !accepted(obj, obj.TemplateStatus().Conditions) {
			return ctrl.Result{}, r.setAccepted(ctx, rt, false, registryv1alpha1.ReasonTemplateInvalid,
				fmt.Sprintf("%s %s is not accepted", k.kind, ref.ObjectName()))
		}
	}
	return ctrl.Result{}, r.setAccepted(ctx, rt, true, registryv1alpha1.ReasonAccepted, "template revision is accepted")
}

func (r *RegistryTemplateReconciler) setAccepted(ctx context.Context, rt *registryv1alpha1.RegistryTemplate, ok bool, reason, msg string) error {
	orig := rt.Status.DeepCopy()
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
		Type: registryv1alpha1.ConditionAccepted, Status: status, Reason: reason,
		Message: truncate(msg, 1024), ObservedGeneration: rt.Generation,
	})
	rt.Status.ObservedGeneration = rt.Generation
	if equality.Semantic.DeepEqual(orig, &rt.Status) {
		return nil
	}
	return r.Status().Update(ctx, rt)
}

// SetupWithManager — контроллер ревизий шаблона заказа: будят его смена
// любого из шести observed-шаблонов (принятие) и заказы (ссылки).
func (r *RegistryTemplateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		Named("registrytemplate").
		For(&registryv1alpha1.RegistryTemplate{}, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, deletionPredicate()))).
		Watches(&registryv1alpha1.Registry{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			reg := o.(*registryv1alpha1.Registry)
			out := []reconcile.Request{{NamespacedName: types.NamespacedName{Name: reg.Spec.TemplateRef.ObjectName()}}}
			if t := reg.Status.Template; t != nil {
				out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Name: t.ObjectName()}})
			}
			return out
		}))
	for _, k := range observedKinds {
		kind := k.kind
		b = b.Watches(k.new(), handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
			list := &registryv1alpha1.RegistryTemplateList{}
			if err := mgr.GetClient().List(ctx, list, client.MatchingFields{IndexObservedTemplate: kind + "/" + o.GetName()}); err != nil {
				return nil
			}
			out := make([]reconcile.Request, 0, len(list.Items))
			for _, rt := range list.Items {
				out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Name: rt.Name}})
			}
			return out
		}))
	}
	return b.WithOptions(controller.Options{MaxConcurrentReconciles: 2}).Complete(r)
}

// deletionPredicate — событие, в котором объекту поставлена отметка
// удаления (поколение у CRD без изменения spec тоже растёт, но ждать этого
// незачем).
func deletionPredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			return !e.ObjectNew.GetDeletionTimestamp().IsZero()
		},
	}
}
