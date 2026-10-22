// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/kinds"
	"github.com/PRO-Robotech/registry-operator/internal/metrics"
)

// finalize снимает ресурсы заказа строго в обратном порядке пайплайна и
// освобождает резерв. Заказ, которому резерв не принадлежит (id занят
// другим заказом), не трогает ничего: снимается только его финализатор.
func (r *RegistryReconciler) finalize(ctx context.Context, reg *registryv1alpha1.Registry) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(reg, registryv1alpha1.RegistryFinalizer) {
		return ctrl.Result{}, nil
	}
	res := &registryv1alpha1.RegistryIDReservation{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Name: reg.Spec.ID}, res)
	switch {
	case apierrors.IsNotFound(err):
		return ctrl.Result{}, r.dropFinalizer(ctx, reg)
	case err != nil:
		return ctrl.Result{}, err
	}
	ref := res.Spec.RegistryRef
	if ref.Namespace != reg.Namespace || ref.Name != reg.Name || ref.UID != string(reg.UID) || res.Status.Phase == registryv1alpha1.ReservationReleased {
		return ctrl.Result{}, r.dropFinalizer(ctx, reg)
	}

	p := newPass(reg, r.now())
	p.deleting = true
	p.reservation = res
	if reg.Status.DeletingSince == nil {
		t := metav1.NewTime(p.now)
		reg.Status.DeletingSince = &t
		r.Recorder.Eventf(reg, nil, corev1.EventTypeNormal, registryv1alpha1.ReasonDeleting, "Delete", "%s", registryv1alpha1.ReasonMessages[registryv1alpha1.ReasonDeleting])
	}
	p.deleteStalled = p.now.Sub(reg.Status.DeletingSince.Time) > r.Config.Intervals.DeletionTimeout.Duration
	done, err := r.teardown(ctx, p)
	if werr := r.writeStatus(ctx, p); werr != nil {
		return ctrl.Result{}, werr
	}
	if werr := r.writeDiagnostics(ctx, p); werr != nil && !apierrors.IsNotFound(werr) {
		return ctrl.Result{}, werr
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if !done {
		return ctrl.Result{RequeueAfter: requeueSoon}, nil
	}

	cur := p.reservation
	patch := client.MergeFrom(cur.DeepCopy())
	now := metav1.NewTime(r.now())
	cur.Status.Phase = registryv1alpha1.ReservationReleased
	cur.Status.ReleasedAt = &now
	cur.Status.Diagnostics = nil
	if err := r.Status().Patch(ctx, cur, patch); err != nil {
		return ctrl.Result{}, err
	}
	metrics.DeletionSteps.WithLabelValues("reservation", "released").Inc()
	return ctrl.Result{}, r.dropFinalizer(ctx, reg)
}

// dropFinalizer снимает финализатор заказа под версией объекта: без неё
// merge patch вернул бы финализатор, снятый другим писателем после чтения.
func (r *RegistryReconciler) dropFinalizer(ctx context.Context, reg *registryv1alpha1.Registry) error {
	patch := client.MergeFromWithOptions(reg.DeepCopy(), client.MergeFromWithOptimisticLock{})
	controllerutil.RemoveFinalizer(reg, registryv1alpha1.RegistryFinalizer)
	return client.IgnoreNotFound(r.Patch(ctx, reg, patch))
}

// teardown — один шаг удаления за раз: следующий шаг снимается, только когда
// предыдущий снят целиком (объекты исчезли, регистрация в IAM снята). true —
// снято всё.
func (r *RegistryReconciler) teardown(ctx context.Context, p *pass) (bool, error) {
	reg := p.reg
	for _, ts := range kinds.DeletionOrder {
		if ts.Registration {
			done, err := r.unregisterAll(ctx, p)
			if err != nil || !done {
				p.deleteStep = registryv1alpha1.StepRegistration
				return false, err
			}
			continue
		}
		k := ts.Kind
		objs, err := r.listOwned(ctx, k, reg)
		if err != nil {
			return false, err
		}
		if k.Stateful {
			pending, err := r.settleDeletionPolicy(ctx, k, reg, objs)
			if err != nil {
				return false, err
			}
			if pending {
				p.deleteReason = registryv1alpha1.ReasonDeletionPolicyPending
				p.deleteStep = k.Step
				metrics.DeletionSteps.WithLabelValues(string(k.Step), "waiting").Inc()
				return false, nil
			}
		}
		for i := range objs {
			o := &objs[i]
			if o.GetDeletionTimestamp() != nil {
				continue
			}
			uid := o.GetUID()
			if err := r.Delete(ctx, o, client.Preconditions{UID: &uid}); client.IgnoreNotFound(err) != nil {
				return false, err
			}
			metrics.DeletionSteps.WithLabelValues(string(k.Step), "deleted").Inc()
			p.diagnostics = append(p.diagnostics, registryv1alpha1.ResourceDiagnostic{
				Step: k.Step, Kind: k.GVK.Kind, Namespace: o.GetNamespace(), Name: o.GetName(), Message: "deletion requested",
			})
		}
		if len(objs) > 0 {
			p.deleteStep = k.Step
			metrics.DeletionSteps.WithLabelValues(string(k.Step), "waiting").Inc()
			return false, nil
		}
		if k.GVK == kinds.Addon.GVK {
			gone, err := r.workloadGone(ctx, reg)
			if err != nil {
				return false, err
			}
			if !gone {
				p.deleteStep = k.Step
				p.message = "workload of the removed addon still exists"
				return false, nil
			}
		}
	}
	return true, nil
}

// settleDeletionPolicy приводит spec.deletionPolicy stateful-ресурса к
// политике удаления заказа и ждёт, пока контроллер ресурса увидит её. Purge
// бакета — только здесь, в фазе Deleting, и только по политике заказа;
// VaultClaim всегда Retain: гость не снимает общий mount.
func (r *RegistryReconciler) settleDeletionPolicy(ctx context.Context, k kinds.Kind, reg *registryv1alpha1.Registry, objs []unstructured.Unstructured) (bool, error) {
	want := string(registryv1alpha1.DeletionPolicyRetain)
	if k.GVK == kinds.S3BucketClaim.GVK && reg.Spec.DeletionPolicy == registryv1alpha1.DeletionPolicyPurge {
		want = string(registryv1alpha1.DeletionPolicyPurge)
	}
	pending := false
	for i := range objs {
		o := &objs[i]
		if o.GetDeletionTimestamp() != nil {
			continue
		}
		cur, _, _ := unstructured.NestedString(o.Object, "spec", "deletionPolicy")
		if cur != want {
			body, err := json.Marshal(map[string]any{"spec": map[string]any{"deletionPolicy": want}})
			if err != nil {
				return false, err
			}
			if err := r.Patch(ctx, o, client.RawPatch(types.MergePatchType, body), client.FieldOwner(FieldManager)); err != nil {
				return false, fmt.Errorf("set deletionPolicy %s on %s %s/%s: %w", want, k.GVK.Kind, o.GetNamespace(), o.GetName(), err)
			}
			pending = true
			continue
		}
		og, _, _ := unstructured.NestedInt64(o.Object, "status", "observedGeneration")
		if og < o.GetGeneration() {
			pending = true
		}
	}
	return pending, nil
}

// workloadGone — рабочая нагрузка снятого Addon исчезла.
func (r *RegistryReconciler) workloadGone(ctx context.Context, reg *registryv1alpha1.Registry) (bool, error) {
	d := &unstructured.Unstructured{}
	d.SetGroupVersionKind(kinds.Deployment.GVK)
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: reg.Namespace, Name: r.Config.Policy.ResourceName(reg.Spec.ID, "")}, d)
	switch {
	case apierrors.IsNotFound(err):
		return true, nil
	case err != nil:
		return false, err
	}
	return false, nil
}
