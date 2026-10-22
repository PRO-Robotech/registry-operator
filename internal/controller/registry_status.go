// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/desired"
	"github.com/PRO-Robotech/registry-operator/internal/kinds"
	"github.com/PRO-Robotech/registry-operator/internal/metrics"
)

// defaultStallTimeout — StallTimeout ревизии по умолчанию (как в CRD).
const defaultStallTimeout = 15 * time.Minute

// pass — состояние одного прохода заказа: из него собирается публичная
// проекция (коды) и внутренняя (сырые сообщения).
type pass struct {
	reg  *registryv1alpha1.Registry
	orig *registryv1alpha1.Registry
	now  time.Time

	reservation      *registryv1alpha1.RegistryIDReservation
	names            desired.Names
	templateResolved bool
	allReady         bool
	stallTimeout     *metav1.Duration
	endpoint         string

	refused       bool
	reason        string
	message       string
	resources     []registryv1alpha1.ResourceStatus
	registration  *registryv1alpha1.RegistrationStatus
	diagnostics   []registryv1alpha1.ResourceDiagnostic
	syncSet       bool
	syncActive    bool
	syncReason    string
	deleting      bool
	deleteStep    registryv1alpha1.Step
	deleteReason  string
	deleteStalled bool
}

func newPass(reg *registryv1alpha1.Registry, now time.Time) *pass {
	return &pass{reg: reg, orig: reg.DeepCopy(), now: now}
}

func (p *pass) refuse(reason, msg string) {
	p.refused, p.reason, p.message = true, reason, msg
}

func (p *pass) wait(reason, msg string) {
	p.reason, p.message = reason, msg
}

// record — состояние ресурса шага.
func (p *pass) record(k kinds.Kind, live *unstructured.Unstructured, res kinds.Result) {
	p.resources = append(p.resources, registryv1alpha1.ResourceStatus{
		Step: k.Step, APIVersion: k.GVK.GroupVersion().String(), Kind: k.GVK.Kind,
		Namespace: live.GetNamespace(), Name: live.GetName(), Ready: res.Ready, Reason: res.Reason,
	})
	if res.Message != "" {
		p.diagnostics = append(p.diagnostics, registryv1alpha1.ResourceDiagnostic{
			Step: k.Step, Kind: k.GVK.Kind, Namespace: live.GetNamespace(), Name: live.GetName(), Message: truncate(res.Message, 1024),
		})
	}
	outcome := "waiting"
	if res.Ready {
		outcome = "ready"
	}
	metrics.StepOutcomes.WithLabelValues(string(k.Step), outcome).Inc()
	if res.Ready && k.GVK == kinds.HTTPRoute.GVK {
		if hosts, _, _ := unstructured.NestedStringSlice(live.Object, "spec", "hostnames"); len(hosts) > 0 {
			p.endpoint = hosts[0] + "/" + p.reg.Spec.ID
		}
	}
}

// recordWaiting — шаг за закрытой стадией, объекта нет.
func (p *pass) recordWaiting(k kinds.Kind, name, namespace string) {
	p.resources = append(p.resources, registryv1alpha1.ResourceStatus{
		Step: k.Step, APIVersion: k.GVK.GroupVersion().String(), Kind: k.GVK.Kind,
		Namespace: namespace, Name: name, Ready: false, Reason: registryv1alpha1.ReasonWaitingForDependency,
	})
	metrics.StepOutcomes.WithLabelValues(string(k.Step), "waiting").Inc()
}

// compose собирает публичную проекцию статуса.
func (p *pass) compose() {
	reg := p.reg
	st := &reg.Status
	gen := reg.Generation
	st.ObservedGeneration = gen

	if p.deleting {
		st.Phase = registryv1alpha1.PhaseDeleting
		st.Endpoint = ""
		st.Resources = nil
		st.Registration = nil
		setCondition(&st.Conditions, gen, registryv1alpha1.ConditionReady, false, registryv1alpha1.ReasonDeleting)
		reason := registryv1alpha1.ReasonDeleting
		if p.deleteReason != "" {
			reason = p.deleteReason
		}
		if p.deleteStalled {
			setCondition(&st.Conditions, gen, registryv1alpha1.ConditionDeletionStalled, true, registryv1alpha1.ReasonDeletionTimeout)
		} else {
			setCondition(&st.Conditions, gen, registryv1alpha1.ConditionDeletionStalled, false, reason)
		}
		return
	}

	switch {
	case p.reservation != nil:
		setCondition(&st.Conditions, gen, registryv1alpha1.ConditionIDReserved, true, registryv1alpha1.ReasonIDReserved)
	case p.reason == registryv1alpha1.ReasonIDTaken:
		setCondition(&st.Conditions, gen, registryv1alpha1.ConditionIDReserved, false, registryv1alpha1.ReasonIDTaken)
	}
	if p.templateResolved {
		setCondition(&st.Conditions, gen, registryv1alpha1.ConditionTemplateResolved, true, registryv1alpha1.ReasonTemplateResolved)
	} else if p.reason == registryv1alpha1.ReasonTemplateNotFound || p.reason == registryv1alpha1.ReasonTemplateNotAccepted {
		setCondition(&st.Conditions, gen, registryv1alpha1.ConditionTemplateResolved, false, p.reason)
	}

	if len(p.resources) > 0 {
		st.Resources = p.resources
		for _, rs := range p.resources {
			setCondition(&st.Conditions, gen, registryv1alpha1.StepCondition(rs.Step), rs.Ready, rs.Reason)
		}
	}
	if p.registration != nil {
		st.Registration = p.registration
		setCondition(&st.Conditions, gen, registryv1alpha1.ConditionRegistrationReady, p.registration.Ready, p.registration.Reason)
	}
	if p.syncSet {
		setCondition(&st.Conditions, gen, registryv1alpha1.ConditionSyncRecovering, p.syncActive, p.syncReason)
	}

	switch {
	case p.refused:
		st.Phase = registryv1alpha1.PhaseFailed
		setCondition(&st.Conditions, gen, registryv1alpha1.ConditionReady, false, p.reason)
	case !p.templateResolved:
		st.Phase = registryv1alpha1.PhasePending
		setCondition(&st.Conditions, gen, registryv1alpha1.ConditionReady, false, orReason(p.reason, registryv1alpha1.ReasonTemplateNotFound))
	case p.allReady:
		st.Phase = registryv1alpha1.PhaseReady
		st.Provisioned = true
		st.ProgressingSince = nil
		st.Endpoint = p.endpoint
		setCondition(&st.Conditions, gen, registryv1alpha1.ConditionReady, true, registryv1alpha1.ReasonReconciled)
	default:
		switch {
		case st.Template != nil && *st.Template != reg.Spec.TemplateRef:
			st.Phase = registryv1alpha1.PhaseUpgrading
		case st.Provisioned:
			st.Phase = registryv1alpha1.PhaseDegraded
		default:
			st.Phase = registryv1alpha1.PhaseProvisioning
		}
		setCondition(&st.Conditions, gen, registryv1alpha1.ConditionReady, false, orReason(p.reason, p.firstNotReady()))
	}

	if st.Phase == registryv1alpha1.PhaseReady {
		setCondition(&st.Conditions, gen, registryv1alpha1.ConditionStalled, false, registryv1alpha1.ReasonReconciled)
		return
	}
	if st.ProgressingSince == nil {
		t := metav1.NewTime(p.now)
		st.ProgressingSince = &t
	}
	timeout := defaultStallTimeout
	if p.stallTimeout != nil && p.stallTimeout.Duration > 0 {
		timeout = p.stallTimeout.Duration
	}
	stalled := p.now.Sub(st.ProgressingSince.Time) > timeout
	if stalled {
		setCondition(&st.Conditions, gen, registryv1alpha1.ConditionStalled, true, registryv1alpha1.ReasonStallTimeout)
	} else {
		setCondition(&st.Conditions, gen, registryv1alpha1.ConditionStalled, false, registryv1alpha1.ReasonProgressing)
	}
}

// firstNotReady — код причины первого неготового шага пайплайна.
func (p *pass) firstNotReady() string {
	registrationBlocks := p.registration != nil && !p.registration.Ready
	for _, rs := range p.resources {
		if rs.Ready {
			continue
		}
		// Шаг поздней стадии ждёт и регистрации первой: причина — она.
		if rs.Reason == registryv1alpha1.ReasonWaitingForDependency && registrationBlocks {
			return p.registration.Reason
		}
		return rs.Reason
	}
	if registrationBlocks {
		return p.registration.Reason
	}
	return registryv1alpha1.ReasonProgressing
}

func orReason(r, def string) string {
	if r == "" {
		return def
	}
	return r
}

// writeStatus пишет публичную проекцию, если она изменилась (с проверкой
// resourceVersion: проход, читавший старую версию, не перезапишет новую).
func (r *RegistryReconciler) writeStatus(ctx context.Context, p *pass) error {
	p.compose()
	if equality.Semantic.DeepEqual(p.orig.Status, p.reg.Status) {
		return nil
	}
	if p.orig.Status.Phase != p.reg.Status.Phase {
		metrics.PhaseTransitions.WithLabelValues(string(p.reg.Status.Phase)).Inc()
		evType := corev1.EventTypeNormal
		if p.reg.Status.Phase == registryv1alpha1.PhaseFailed || p.reg.Status.Phase == registryv1alpha1.PhaseDegraded {
			evType = corev1.EventTypeWarning
		}
		r.Recorder.Eventf(p.reg, nil, evType, string(p.reg.Status.Phase), "Phase", "registry phase %s", p.reg.Status.Phase)
	}
	return r.Status().Patch(ctx, p.reg, client.MergeFromWithOptions(p.orig, client.MergeFromWithOptimisticLock{}))
}

// writeDiagnostics пишет внутреннюю проекцию (сырые сообщения) в резерв,
// если она изменилась.
func (r *RegistryReconciler) writeDiagnostics(ctx context.Context, p *pass) error {
	res := p.reservation
	if res == nil {
		return nil
	}
	msg := p.message
	if msg == "" && p.deleting && p.deleteStep != "" {
		msg = "deletion waits at step " + string(p.deleteStep)
	}
	d := &registryv1alpha1.Diagnostics{Message: truncate(msg, 2048)}
	if len(p.diagnostics) > 0 {
		d.Resources = p.diagnostics
		if len(d.Resources) > 16 {
			d.Resources = d.Resources[:16]
		}
	}
	if d.Message == "" && len(d.Resources) == 0 {
		d = nil
	}
	if equality.Semantic.DeepEqual(res.Status.Diagnostics, d) {
		return nil
	}
	patch := client.MergeFrom(res.DeepCopy())
	res.Status.Diagnostics = d
	return r.Status().Patch(ctx, res, patch)
}
