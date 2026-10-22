// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/kinds"
	"github.com/PRO-Robotech/registry-operator/internal/metrics"
)

// addonSynced — у Addon Synced=True на текущем поколении.
func addonSynced(u *unstructured.Unstructured) bool {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok || m["type"] != "Synced" {
			continue
		}
		og, _ := m["observedGeneration"].(int64)
		return m["status"] == "True" && (og == 0 || og == u.GetGeneration())
	}
	return false
}

// recoveryDelay — пауза до следующего продвижения эпохи: начальная пауза,
// удвоенная по числу уже сделанных продвижений, не выше потолка.
func recoveryDelay(initial, ceiling time.Duration, attempts int32) time.Duration {
	d := initial
	for i := int32(0); i < attempts && d < ceiling; i++ {
		d *= 2
	}
	return min(d, ceiling)
}

// recoverSync — восстановление синхронизации рабочей нагрузки. Когда Argo CD
// исчерпал повторы синхронизации, автосинхронизация с тем же источником
// больше не запускается. Оператор продвигает эпоху в переменных Addon:
// меняются values, а с ними источник Application, и Argo CD начинает новую
// синхронизацию с полным повтором. Операция без результата синхронизации
// эпохой не перезапускается — это видно в условии, эпоха не трогается.
func (r *RegistryReconciler) recoverSync(ctx context.Context, p *pass, addon *unstructured.Unstructured) {
	st := &p.reg.Status.Sync
	healthy := func() {
		st.StuckSince = nil
		st.Attempts = 0
		p.syncSet, p.syncActive, p.syncReason = true, false, registryv1alpha1.ReasonSyncHealthy
	}
	if addonSynced(addon) {
		healthy()
		return
	}
	name, _, _ := unstructured.NestedString(addon.Object, "status", "applicationRef", "name")
	ns, _, _ := unstructured.NestedString(addon.Object, "status", "applicationRef", "namespace")
	if name == "" || ns != r.Config.Policy.Addon.ArgoCDNamespace {
		return
	}
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(kinds.Application)
	if err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, app); err != nil {
		return
	}
	phase, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")
	if phase != "Failed" && phase != "Error" {
		healthy()
		return
	}
	if result, _, _ := unstructured.NestedMap(app.Object, "status", "operationState", "syncResult"); result == nil {
		p.syncSet, p.syncActive, p.syncReason = true, false, registryv1alpha1.ReasonSyncRecoveryNoResult
		return
	}
	p.syncSet, p.syncActive, p.syncReason = true, true, registryv1alpha1.ReasonSyncRecovering
	now := metav1.NewTime(p.now)
	if st.StuckSince == nil {
		st.StuckSince = &now
		return
	}
	last := st.StuckSince
	if st.LastAdvanceTime != nil {
		last = st.LastAdvanceTime
	}
	sr := r.Config.SyncRecovery
	if p.now.Sub(last.Time) < recoveryDelay(sr.InitialDelay.Duration, sr.MaxDelay.Duration, st.Attempts) {
		return
	}
	st.Epoch++
	st.Attempts++
	st.LastAdvanceTime = &now
	metrics.SyncEpochAdvances.Inc()
	r.Recorder.Eventf(p.reg, nil, corev1.EventTypeWarning, registryv1alpha1.ReasonSyncRecovering, "AdvanceSyncEpoch",
		"%s", registryv1alpha1.ReasonMessages[registryv1alpha1.ReasonSyncRecovering])
}
