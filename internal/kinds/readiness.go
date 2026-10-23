// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package kinds

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
)

// Result — готовность одного ресурса.
type Result struct {
	Ready bool
	// Reason — код из каталога причин Registry.
	Reason string
	// Message — сырое описание для внутренней проекции; наружу не выходит.
	Message string
}

func ready() Result {
	return Result{Ready: true, Reason: registryv1alpha1.ReasonResourceReady}
}

func notReady(reason, format string, args ...any) Result {
	return Result{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// Missing — результат для отсутствующего ресурса.
func Missing() Result {
	return notReady(registryv1alpha1.ReasonResourceMissing, "resource does not exist")
}

type condition struct {
	status             string
	reason             string
	message            string
	observedGeneration int64
	found              bool
}

func findCondition(conds []any, typ string) condition {
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok || m["type"] != typ {
			continue
		}
		out := condition{found: true}
		out.status, _ = m["status"].(string)
		out.reason, _ = m["reason"].(string)
		out.message, _ = m["message"].(string)
		switch og := m["observedGeneration"].(type) {
		case int64:
			out.observedGeneration = og
		case float64:
			out.observedGeneration = int64(og)
		}
		return out
	}
	return condition{}
}

func statusConditions(u *unstructured.Unstructured) []any {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	return conds
}

// statusGenerationCurrent — status.observedGeneration равно generation.
func statusGenerationCurrent(u *unstructured.Unstructured) (bool, int64) {
	og, found, err := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	if err != nil || !found {
		return false, 0
	}
	return og == u.GetGeneration(), og
}

// conditionTrue — условие есть, True и (если несёт своё поколение) снято с
// текущего поколения.
func conditionTrue(u *unstructured.Unstructured, typ string) (bool, condition) {
	c := findCondition(statusConditions(u), typ)
	if !c.found || c.status != "True" {
		return false, c
	}
	if c.observedGeneration != 0 && c.observedGeneration != u.GetGeneration() {
		return false, c
	}
	return true, c
}

func deleting(u *unstructured.Unstructured) bool { return u.GetDeletionTimestamp() != nil }

// vaultPhaseReady — общее правило ресурсов vault-operator: Failed — отказ;
// status.observedGeneration текущее; phase Ready; условие вида True.
func vaultPhaseReady(u *unstructured.Unstructured, cond string) Result {
	if deleting(u) {
		return notReady(registryv1alpha1.ReasonResourceProgressing, "resource is being deleted")
	}
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	current, og := statusGenerationCurrent(u)
	switch {
	case phase == "Failed":
		return notReady(registryv1alpha1.ReasonResourceDegraded, "phase Failed: %s", findCondition(statusConditions(u), "Ready").message)
	case !current:
		return notReady(registryv1alpha1.ReasonResourceStale, "observedGeneration %d, generation %d", og, u.GetGeneration())
	case phase != "Ready":
		return notReady(registryv1alpha1.ReasonResourceProgressing, "phase %q", phase)
	}
	if ok, c := conditionTrue(u, cond); !ok {
		return notReady(registryv1alpha1.ReasonResourceProgressing, "%s is not True on the current generation: %s", cond, c.message)
	}
	return ready()
}

// s3BucketClaimReady — бакет создан и ключи записаны в Vault.
func s3BucketClaimReady(u *unstructured.Unstructured) Result {
	return vaultPhaseReady(u, "KeysWritten")
}

// vaultClaimReady — роль Vault записана.
func vaultClaimReady(u *unstructured.Unstructured) Result {
	return vaultPhaseReady(u, "RolesApplied")
}

// iamTerminalReasons — причины Ready=False у ресурсов kaname-iam-operator,
// которые повтор не снимет.
var iamTerminalReasons = map[string]bool{"Invalid": true, "Forbidden": true, "Conflict": true, "NotOwned": true}

// iamReady — общее правило ресурсов kaname-iam-operator: Drifted или
// Conflict — отказ; Ready=True на текущем поколении; status.id выдан.
func iamReady(u *unstructured.Unstructured) Result {
	if deleting(u) {
		return notReady(registryv1alpha1.ReasonResourceProgressing, "resource is being deleted")
	}
	conds := statusConditions(u)
	for _, bad := range []string{"Drifted", "Conflict"} {
		if c := findCondition(conds, bad); c.found && c.status == "True" {
			return notReady(registryv1alpha1.ReasonResourceDegraded, "%s: %s", bad, c.message)
		}
	}
	c := findCondition(conds, "Ready")
	if !c.found {
		return notReady(registryv1alpha1.ReasonResourceProgressing, "no Ready condition")
	}
	og := c.observedGeneration
	if og == 0 {
		_, og = statusGenerationCurrent(u)
	}
	if og != u.GetGeneration() {
		return notReady(registryv1alpha1.ReasonResourceStale, "Ready observedGeneration %d, generation %d", og, u.GetGeneration())
	}
	if c.status != "True" {
		if iamTerminalReasons[c.reason] {
			return notReady(registryv1alpha1.ReasonResourceDegraded, "Ready %s: %s", c.reason, c.message)
		}
		return notReady(registryv1alpha1.ReasonResourceProgressing, "Ready %s: %s", c.reason, c.message)
	}
	if id, _, _ := unstructured.NestedString(u.Object, "status", "id"); id == "" {
		return notReady(registryv1alpha1.ReasonResourceProgressing, "status.id is not assigned")
	}
	return ready()
}

// iamServiceAccountReady — учётка выдана, а объявленные ключи выпущены.
func iamServiceAccountReady(u *unstructured.Unstructured) Result {
	r := iamReady(u)
	if !r.Ready {
		return r
	}
	if keys, _, _ := unstructured.NestedSlice(u.Object, "spec", "keys"); len(keys) > 0 {
		if ok, c := conditionTrue(u, "KeysReady"); !ok {
			return notReady(registryv1alpha1.ReasonResourceProgressing, "KeysReady is not True on the current generation: %s", c.message)
		}
	}
	return r
}

// iamAccessBindingReady — выдача создана в kaname.
func iamAccessBindingReady(u *unstructured.Unstructured) Result { return iamReady(u) }

// addonReady — addons-operator видел текущее поколение; Ready, Synced и
// Healthy (Application Argo CD) — True. Готовность рабочей нагрузки
// (Deployment Available) оператор проверяет отдельно.
func addonReady(u *unstructured.Unstructured) Result {
	if deleting(u) {
		return notReady(registryv1alpha1.ReasonResourceProgressing, "resource is being deleted")
	}
	if current, og := statusGenerationCurrent(u); !current {
		return notReady(registryv1alpha1.ReasonResourceStale, "observedGeneration %d, generation %d", og, u.GetGeneration())
	}
	if c := findCondition(statusConditions(u), "Degraded"); c.found && c.status == "True" {
		return notReady(registryv1alpha1.ReasonResourceDegraded, "Degraded %s: %s", c.reason, c.message)
	}
	for _, typ := range []string{"Synced", "Healthy", "Ready"} {
		if ok, c := conditionTrue(u, typ); !ok {
			return notReady(registryv1alpha1.ReasonResourceProgressing, "%s is not True on the current generation: %s %s", typ, c.reason, c.message)
		}
	}
	return ready()
}

// httpRouteReady — у каждого родителя маршрута Accepted=True и
// ResolvedRefs=True текущего поколения; родителей нет — не готов.
func httpRouteReady(u *unstructured.Unstructured) Result {
	if deleting(u) {
		return notReady(registryv1alpha1.ReasonResourceProgressing, "resource is being deleted")
	}
	parents, _, _ := unstructured.NestedSlice(u.Object, "status", "parents")
	if len(parents) == 0 {
		return notReady(registryv1alpha1.ReasonResourceProgressing, "no parent has reported status")
	}
	for i, p := range parents {
		pm, ok := p.(map[string]any)
		if !ok {
			return notReady(registryv1alpha1.ReasonResourceProgressing, "parent %d has no status", i)
		}
		conds, _ := pm["conditions"].([]any)
		for _, typ := range []string{"Accepted", "ResolvedRefs"} {
			c := findCondition(conds, typ)
			switch {
			case !c.found:
				return notReady(registryv1alpha1.ReasonResourceProgressing, "parent %d: no %s condition", i, typ)
			case c.observedGeneration != u.GetGeneration():
				return notReady(registryv1alpha1.ReasonResourceStale, "parent %d: %s observedGeneration %d, generation %d", i, typ, c.observedGeneration, u.GetGeneration())
			case c.status != "True":
				return notReady(registryv1alpha1.ReasonResourceDegraded, "parent %d: %s %s: %s", i, typ, c.reason, c.message)
			}
		}
	}
	return ready()
}

// deploymentAvailable — status.observedGeneration ≥ generation, Available=True,
// все реплики обновлены и доступны; ProgressDeadlineExceeded — отказ.
func deploymentAvailable(u *unstructured.Unstructured) Result {
	if deleting(u) {
		return notReady(registryv1alpha1.ReasonResourceProgressing, "workload is being deleted")
	}
	og, _, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	if og < u.GetGeneration() {
		return notReady(registryv1alpha1.ReasonResourceStale, "workload observedGeneration %d, generation %d", og, u.GetGeneration())
	}
	conds := statusConditions(u)
	if p := findCondition(conds, "Progressing"); p.found && p.status == "False" && p.reason == "ProgressDeadlineExceeded" {
		return notReady(registryv1alpha1.ReasonResourceDegraded, "workload ProgressDeadlineExceeded: %s", p.message)
	}
	if a := findCondition(conds, "Available"); !a.found || a.status != "True" {
		return notReady(registryv1alpha1.ReasonResourceProgressing, "workload is not Available: %s", a.message)
	}
	want, found, _ := unstructured.NestedInt64(u.Object, "spec", "replicas")
	if !found {
		want = 1
	}
	updated, _, _ := unstructured.NestedInt64(u.Object, "status", "updatedReplicas")
	available, _, _ := unstructured.NestedInt64(u.Object, "status", "availableReplicas")
	if updated < want || available < want {
		return notReady(registryv1alpha1.ReasonResourceProgressing, "workload updated %d, available %d of %d replicas", updated, available, want)
	}
	return ready()
}

// DeploymentReady — готовность рабочей нагрузки Addon.
func DeploymentReady(u *unstructured.Unstructured) Result { return deploymentAvailable(u) }
