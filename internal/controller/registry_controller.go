// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	observedv1alpha1 "github.com/PRO-Robotech/registry-operator/api/observed/v1alpha1"
	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/desired"
	"github.com/PRO-Robotech/registry-operator/internal/kinds"
	"github.com/PRO-Robotech/registry-operator/internal/metrics"
	"github.com/PRO-Robotech/registry-operator/internal/ocidigest"
	"github.com/PRO-Robotech/registry-operator/internal/render"
)

// +kubebuilder:rbac:groups=registry.in-cloud.io,resources=registries,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=registry.in-cloud.io,resources=registries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=registry.in-cloud.io,resources=registries/finalizers,verbs=update
// +kubebuilder:rbac:groups=registry.in-cloud.io,resources=registryidreservations,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=registry.in-cloud.io,resources=registryidreservations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=vault.in-cloud.io,resources=s3bucketclaims;vaultclaims,verbs=get;list;watch;create;patch;delete
// +kubebuilder:rbac:groups=iam.kaname.in-cloud.io,resources=serviceaccounts;accessbindings,verbs=get;list;watch;create;patch;delete
// +kubebuilder:rbac:groups=addons.in-cloud.io,resources=addons,verbs=get;list;watch;create;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// RegistryReconciler ведёт заказ Registry.
type RegistryReconciler struct {
	client.Client
	*Deps
}

// templateSet — ревизия заказа и её шесть принятых шаблонов.
type templateSet struct {
	rt        *registryv1alpha1.RegistryTemplate
	s3        *observedv1alpha1.ObservedS3BucketClaimTemplate
	sa        *observedv1alpha1.ObservedIamServiceAccountTemplate
	vault     *observedv1alpha1.ObservedVaultClaimTemplate
	ab        *observedv1alpha1.ObservedIamAccessBindingTemplate
	addon     *observedv1alpha1.ObservedAddonTemplate
	route     *observedv1alpha1.ObservedHTTPRouteTemplate
	params    map[string]any
	templates map[registryv1alpha1.Step]observedv1alpha1.Template
}

// refusal — отказ заказа: вход не годится, повтор не поможет, пока вход не
// изменится. reason — код из каталога, msg — сырое описание для внутренней
// проекции.
type refusal struct {
	reason string
	msg    string
}

func (r *refusal) Error() string { return r.reason + ": " + r.msg }

// waiting — заказ ждёт внешнего условия (шаблон не принят, реестр чартов не
// ответил): не отказ, повтор по темпу неготового.
type waiting struct {
	reason string
	msg    string
}

func (w *waiting) Error() string { return w.reason + ": " + w.msg }

// Reconcile — один проход заказа.
func (r *RegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reg := &registryv1alpha1.Registry{}
	if err := r.Get(ctx, req.NamespacedName, reg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !reg.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, reg)
	}
	if !controllerutil.ContainsFinalizer(reg, registryv1alpha1.RegistryFinalizer) {
		// Перечень финализаторов — массив, и merge patch заменяет его
		// целиком: без версии объекта патч стёр бы финализатор, поставленный
		// другим писателем после чтения. Конфликт — повтор прохода.
		patch := client.MergeFromWithOptions(reg.DeepCopy(), client.MergeFromWithOptimisticLock{})
		controllerutil.AddFinalizer(reg, registryv1alpha1.RegistryFinalizer)
		if err := r.Patch(ctx, reg, patch); err != nil {
			return ctrl.Result{}, err
		}
	}
	p := newPass(reg, r.now())
	result, err := r.provision(ctx, p)
	if werr := r.writeStatus(ctx, p); werr != nil {
		return ctrl.Result{}, werr
	}
	if werr := r.writeDiagnostics(ctx, p); werr != nil {
		log.FromContext(ctx).Error(werr, "diagnostics were not written")
	}
	return result, err
}

func (r *RegistryReconciler) provision(ctx context.Context, p *pass) (ctrl.Result, error) {
	iv := r.Config.Intervals
	reg := p.reg

	if err := r.reserve(ctx, p); err != nil {
		return r.outcome(ctx, p, err)
	}
	set, err := r.resolveTemplates(ctx, reg)
	if err != nil {
		return r.outcome(ctx, p, err)
	}
	p.templateResolved = true
	if err := r.checkClaimPolicy(ctx, reg, set.rt); err != nil {
		return r.outcome(ctx, p, err)
	}

	scope, err := r.resolveScope(ctx, p)
	if err != nil {
		return r.outcome(ctx, p, err)
	}

	in := desired.Input{
		Registry:  reg,
		Template:  reg.Spec.TemplateRef,
		Params:    set.params,
		Status:    map[string]render.Child{},
		SyncEpoch: reg.Status.Sync.Epoch,
		Names:     desired.NamesFor(&r.Config.Policy, reg.Spec.ID, reg.Namespace, set.sa, set.ab, set.route),
		Policy:    &r.Config.Policy,
	}
	p.names = in.Names
	var addonLive *unstructured.Unstructured
	open := true
	var waitErr error
	for i, stage := range kinds.Stages {
		stageReady := true
		for _, k := range stage {
			if !open {
				r.observeClosed(ctx, p, k, in)
				stageReady = false
				continue
			}
			live, res, err := r.ensureStep(ctx, p, k, set.templates[k.Step], in)
			if err != nil {
				return r.outcome(ctx, p, err)
			}
			p.record(k, live, res)
			if k.GVK == kinds.Addon.GVK {
				addonLive = live
			}
			if !res.Ready {
				stageReady = false
				continue
			}
			in.Status[k.StatusKey] = render.Child{Name: live.GetName(), Namespace: live.GetNamespace(), Status: statusOf(live)}
		}
		if i == kinds.RegistrationStage {
			ready, err := r.ensureRegistration(ctx, p, scope)
			var w *waiting
			switch {
			case errors.As(err, &w):
				waitErr = err
			case err != nil:
				return r.outcome(ctx, p, err)
			}
			stageReady = stageReady && ready
		}
		open = open && stageReady
	}

	if addonLive != nil {
		r.recoverSync(ctx, p, addonLive)
	}
	p.allReady = open
	p.stallTimeout = set.rt.Spec.StallTimeout
	if waitErr != nil {
		return r.outcome(ctx, p, waitErr)
	}
	if open {
		if err := r.collectStale(ctx, p, in); err != nil {
			return ctrl.Result{}, err
		}
		p.reg.Status.Template = &registryv1alpha1.TemplateRef{Name: reg.Spec.TemplateRef.Name, Version: reg.Spec.TemplateRef.Version}
		return ctrl.Result{RequeueAfter: iv.Ready.Duration}, nil
	}
	return ctrl.Result{RequeueAfter: iv.NotReady.Duration}, nil
}

// outcome переводит ошибку прохода в фазу и темп повтора.
func (r *RegistryReconciler) outcome(ctx context.Context, p *pass, err error) (ctrl.Result, error) {
	iv := r.Config.Intervals
	var ref *refusal
	var w *waiting
	switch {
	case errors.As(err, &ref):
		p.refuse(ref.reason, ref.msg)
		metrics.Refusals.WithLabelValues(ref.reason).Inc()
		r.Recorder.Eventf(p.reg, nil, corev1.EventTypeWarning, ref.reason, "Refuse", "%s", registryv1alpha1.ReasonMessages[ref.reason])
		return ctrl.Result{RequeueAfter: iv.Refused.Duration}, nil
	case errors.As(err, &w):
		p.wait(w.reason, w.msg)
		return ctrl.Result{RequeueAfter: iv.NotReady.Duration}, nil
	}
	p.wait(registryv1alpha1.ReasonApplyFailed, err.Error())
	log.FromContext(ctx).Error(err, "registry pass failed")
	return ctrl.Result{}, err
}

// reserve — резерв id: атомарный CREATE; занятое имя — отказ IDTaken, если
// резерв не этого заказа (по uid).
func (r *RegistryReconciler) reserve(ctx context.Context, p *pass) error {
	reg := p.reg
	res := &registryv1alpha1.RegistryIDReservation{
		ObjectMeta: metav1.ObjectMeta{
			Name:   reg.Spec.ID,
			Labels: map[string]string{registryv1alpha1.LabelManagedBy: registryv1alpha1.ManagedByValue},
		},
		Spec: registryv1alpha1.RegistryIDReservationSpec{RegistryRef: registryv1alpha1.RegistryRef{
			Namespace: reg.Namespace, Name: reg.Name, UID: string(reg.UID),
		}},
	}
	err := r.Create(ctx, res)
	switch {
	case err == nil:
		r.Recorder.Eventf(reg, nil, corev1.EventTypeNormal, registryv1alpha1.ReasonIDReserved, "Reserve", "%s", registryv1alpha1.ReasonMessages[registryv1alpha1.ReasonIDReserved])
	case apierrors.IsAlreadyExists(err):
		if err := r.APIReader.Get(ctx, types.NamespacedName{Name: reg.Spec.ID}, res); err != nil {
			return err
		}
	default:
		return err
	}
	if res.Spec.RegistryRef.UID != string(reg.UID) || res.Status.Phase == registryv1alpha1.ReservationReleased {
		return &refusal{reason: registryv1alpha1.ReasonIDTaken, msg: fmt.Sprintf("reservation %s belongs to %s/%s", res.Name, res.Spec.RegistryRef.Namespace, res.Spec.RegistryRef.Name)}
	}
	p.reservation = res
	if res.Status.Phase != registryv1alpha1.ReservationActive {
		patch := client.MergeFrom(res.DeepCopy())
		res.Status.Phase = registryv1alpha1.ReservationActive
		if err := r.Status().Patch(ctx, res, patch); err != nil {
			return err
		}
	}
	return nil
}

// resolveTemplates — ревизия заказа и шесть её шаблонов, все приняты.
func (r *RegistryReconciler) resolveTemplates(ctx context.Context, reg *registryv1alpha1.Registry) (*templateSet, error) {
	s := &templateSet{
		rt:    &registryv1alpha1.RegistryTemplate{},
		s3:    &observedv1alpha1.ObservedS3BucketClaimTemplate{},
		sa:    &observedv1alpha1.ObservedIamServiceAccountTemplate{},
		vault: &observedv1alpha1.ObservedVaultClaimTemplate{},
		ab:    &observedv1alpha1.ObservedIamAccessBindingTemplate{},
		addon: &observedv1alpha1.ObservedAddonTemplate{},
		route: &observedv1alpha1.ObservedHTTPRouteTemplate{},
	}
	get := func(name string, obj client.Object, conds func() []metav1.Condition) error {
		if err := r.Get(ctx, types.NamespacedName{Name: name}, obj); err != nil {
			if apierrors.IsNotFound(err) {
				return &waiting{reason: registryv1alpha1.ReasonTemplateNotFound, msg: fmt.Sprintf("%T %s does not exist", obj, name)}
			}
			return err
		}
		if !obj.GetDeletionTimestamp().IsZero() || !accepted(obj, conds()) {
			return &waiting{reason: registryv1alpha1.ReasonTemplateNotAccepted, msg: fmt.Sprintf("%T %s is not accepted", obj, name)}
		}
		return nil
	}
	if err := get(reg.Spec.TemplateRef.ObjectName(), s.rt, func() []metav1.Condition { return s.rt.Status.Conditions }); err != nil {
		return nil, err
	}
	refs := s.rt.Spec.Templates
	for _, t := range []struct {
		ref registryv1alpha1.TemplateRef
		obj observedv1alpha1.Template
	}{
		{refs.S3BucketClaim, s.s3}, {refs.IamServiceAccount, s.sa}, {refs.VaultClaim, s.vault},
		{refs.IamAccessBinding, s.ab}, {refs.Addon, s.addon}, {refs.HTTPRoute, s.route},
	} {
		obj := t.obj
		if err := get(t.ref.ObjectName(), obj, func() []metav1.Condition { return obj.TemplateStatus().Conditions }); err != nil {
			return nil, err
		}
	}
	params, err := render.StrictYAMLMap([]byte(s.rt.Spec.Params))
	if err != nil {
		return nil, &refusal{reason: registryv1alpha1.ReasonRenderFailed, msg: "params: " + err.Error()}
	}
	s.params = params
	s.templates = map[registryv1alpha1.Step]observedv1alpha1.Template{
		registryv1alpha1.StepStorage: s.s3, registryv1alpha1.StepIdentity: s.sa, registryv1alpha1.StepSecrets: s.vault,
		registryv1alpha1.StepAccess: s.ab, registryv1alpha1.StepWorkload: s.addon, registryv1alpha1.StepRoute: s.route,
	}
	return s, nil
}

// checkClaimPolicy — namespace заказа подходит под непустой селектор ревизии.
func (r *RegistryReconciler) checkClaimPolicy(ctx context.Context, reg *registryv1alpha1.Registry, rt *registryv1alpha1.RegistryTemplate) error {
	sel := rt.Spec.ClaimPolicy.NamespaceSelector.MatchLabels
	if len(sel) == 0 {
		return &refusal{reason: registryv1alpha1.ReasonNotAllowed, msg: "template claim policy selects nothing"}
	}
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: reg.Namespace}, ns); err != nil {
		return err
	}
	if !labels.SelectorFromSet(sel).Matches(labels.Set(ns.Labels)) {
		return &refusal{reason: registryv1alpha1.ReasonNotAllowed, msg: fmt.Sprintf("namespace %s does not match the claim policy", reg.Namespace)}
	}
	return nil
}

// ensureStep собирает, проверяет и пишет объект шага и читает его готовность.
func (r *RegistryReconciler) ensureStep(ctx context.Context, p *pass, k kinds.Kind, tpl observedv1alpha1.Template, in desired.Input) (*unstructured.Unstructured, kinds.Result, error) {
	want, err := desired.Build(k, tpl, in)
	switch {
	case desired.IsRender(err):
		return nil, kinds.Result{}, &refusal{reason: registryv1alpha1.ReasonRenderFailed, msg: fmt.Sprintf("%s: %v", k.Step, err)}
	case desired.IsPolicy(err):
		return nil, kinds.Result{}, &refusal{reason: registryv1alpha1.ReasonPolicyViolation, msg: fmt.Sprintf("%s: %v", k.Step, err)}
	case err != nil:
		return nil, kinds.Result{}, err
	}
	if k.GVK == kinds.Addon.GVK {
		if err := r.verifyChart(ctx, tpl); err != nil {
			return nil, kinds.Result{}, err
		}
	}
	live, err := r.apply(ctx, p, k, want)
	if err != nil {
		return nil, kinds.Result{}, err
	}
	res := k.Ready(live)
	if res.Ready && k.GVK == kinds.Addon.GVK {
		res = r.workloadReady(ctx, p.reg.Namespace, in.Names.Workload)
	}
	return live, res, nil
}

func (r *RegistryReconciler) apply(ctx context.Context, p *pass, k kinds.Kind, want *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	live, err := applyOwned(ctx, r.Client, r.APIReader, want, p.reg)
	switch {
	case errors.Is(err, errAdoptionRefused):
		return nil, &refusal{reason: registryv1alpha1.ReasonAdoptionRefused, msg: fmt.Sprintf("%s %s/%s", k.GVK.Kind, want.GetNamespace(), want.GetName())}
	case apierrors.IsAlreadyExists(err):
		return nil, &waiting{reason: registryv1alpha1.ReasonApplyFailed, msg: fmt.Sprintf("%s %s/%s appeared concurrently", k.GVK.Kind, want.GetNamespace(), want.GetName())}
	case apierrors.IsInvalid(err) || apierrors.IsBadRequest(err):
		return nil, &refusal{reason: registryv1alpha1.ReasonPolicyViolation, msg: fmt.Sprintf("%s %s rejected by the API: %v", k.GVK.Kind, want.GetName(), err)}
	case err != nil:
		return nil, err
	}
	metrics.StepOutcomes.WithLabelValues(string(k.Step), "applied").Inc()
	return live, nil
}

// verifyChart — версия чарта в реестре разрешается в пин digest.
func (r *RegistryReconciler) verifyChart(ctx context.Context, tpl observedv1alpha1.Template) error {
	at, ok := tpl.(*observedv1alpha1.ObservedAddonTemplate)
	if !ok {
		return &refusal{reason: registryv1alpha1.ReasonPolicyViolation, msg: fmt.Sprintf("workload step needs an ObservedAddonTemplate, got %T", tpl)}
	}
	pin := at.Spec.Chart
	err := r.Verifier.Verify(ctx, ocidigest.Chart{RepoURL: pin.RepoURL, Name: pin.Name, Version: pin.Version, Digest: pin.Digest})
	switch {
	case err == nil:
		metrics.ChartVerifications.WithLabelValues("match").Inc()
		return nil
	case errors.Is(err, ocidigest.ErrMismatch), errors.Is(err, ocidigest.ErrInvalidReference):
		metrics.ChartVerifications.WithLabelValues("mismatch").Inc()
		return &refusal{reason: registryv1alpha1.ReasonChartDigestMismatch, msg: err.Error()}
	default:
		metrics.ChartVerifications.WithLabelValues("unreachable").Inc()
		return &waiting{reason: registryv1alpha1.ReasonChartUnreachable, msg: err.Error()}
	}
}

// workloadReady — рабочая нагрузка Addon (Deployment zot) доступна.
func (r *RegistryReconciler) workloadReady(ctx context.Context, namespace, name string) kinds.Result {
	d := &unstructured.Unstructured{}
	d.SetGroupVersionKind(kinds.Deployment.GVK)
	if err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, d); err != nil {
		if apierrors.IsNotFound(err) {
			return kinds.Result{Reason: registryv1alpha1.ReasonResourceProgressing, Message: "workload does not exist yet"}
		}
		return kinds.Result{Reason: registryv1alpha1.ReasonResourceProgressing, Message: "workload: " + err.Error()}
	}
	return kinds.DeploymentReady(d)
}

// observeClosed — шаг за закрытой стадией: не пишется, но если объект уже
// есть (стадия была открыта раньше), его готовность видна в статусе.
func (r *RegistryReconciler) observeClosed(ctx context.Context, p *pass, k kinds.Kind, in desired.Input) {
	name, ns := in.Names.ObjectKey(k, p.reg.Namespace)
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(k.GVK)
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, live)
	if err != nil || !owned(live, p.reg) {
		p.recordWaiting(k, name, ns)
		return
	}
	res := k.Ready(live)
	if res.Ready {
		res = kinds.Result{Reason: registryv1alpha1.ReasonWaitingForDependency, Message: "previous step is not ready"}
	}
	p.record(k, live, res)
}

// collectStale снимает объекты заказа, которых нет в желаемом наборе (после
// смены ревизии с другим суффиксом имени или namespace маршрута). Только
// после готовности всего пайплайна на новой ревизии и только при её смене.
func (r *RegistryReconciler) collectStale(ctx context.Context, p *pass, in desired.Input) error {
	cur := p.reg.Status.Template
	if cur != nil && *cur == p.reg.Spec.TemplateRef {
		return nil
	}
	want := map[string]bool{}
	for _, st := range kinds.Stages {
		for _, k := range st {
			name, ns := in.Names.ObjectKey(k, p.reg.Namespace)
			want[k.GVK.Kind+"/"+ns+"/"+name] = true
		}
	}
	for _, k := range kinds.Written {
		objs, err := r.listOwned(ctx, k, p.reg)
		if err != nil {
			return err
		}
		for i := range objs {
			o := &objs[i]
			if want[k.GVK.Kind+"/"+o.GetNamespace()+"/"+o.GetName()] || o.GetDeletionTimestamp() != nil || k.Stateful {
				continue
			}
			uid := o.GetUID()
			if err := r.Delete(ctx, o, client.Preconditions{UID: &uid}); client.IgnoreNotFound(err) != nil {
				return err
			}
		}
	}
	return nil
}

// listOwned — объекты вида с метками владения заказа, мимо кэша.
func (r *RegistryReconciler) listOwned(ctx context.Context, k kinds.Kind, reg *registryv1alpha1.Registry) ([]unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(k.GVK.GroupVersion().WithKind(k.GVK.Kind + "List"))
	sel := client.MatchingLabels{
		registryv1alpha1.LabelManagedBy: registryv1alpha1.ManagedByValue,
		registryv1alpha1.LabelOwnerUID:  string(reg.UID),
	}
	if err := r.APIReader.List(ctx, list, sel); err != nil {
		// Вида нет на кластере — объектов вида у заказа нет.
		if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]unstructured.Unstructured, 0, len(list.Items))
	for _, o := range list.Items {
		if owned(&o, reg) {
			out = append(out, o)
		}
	}
	return out, nil
}

func statusOf(u *unstructured.Unstructured) map[string]any {
	st, _, _ := unstructured.NestedMap(u.Object, "status")
	if st == nil {
		return map[string]any{}
	}
	return st
}

// requeueSoon — короткая пауза ожидания исчезновения при удалении.
const requeueSoon = 5 * time.Second
