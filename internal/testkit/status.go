// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package testkit — статусы ведомых видов в той форме, в какой их выводят
// их контроллеры (vault-operator, kaname-iam-operator, addons-operator,
// Envoy Gateway). Общий для проб envtest и kind: второго описания формы
// статуса нет.
package testkit

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Cond — условие metav1.Condition.
func Cond(typ, status string, gen int64) map[string]any {
	return map[string]any{
		"type": typ, "status": status, "reason": "Test", "message": "",
		"lastTransitionTime": time.Now().UTC().Format(time.RFC3339), "observedGeneration": gen,
	}
}

var (
	s3BucketClaim  = schema.GroupKind{Group: "vault.in-cloud.io", Kind: "S3BucketClaim"}
	vaultClaim     = schema.GroupKind{Group: "vault.in-cloud.io", Kind: "VaultClaim"}
	serviceAccount = schema.GroupKind{Group: "iam.kaname.in-cloud.io", Kind: "ServiceAccount"}
	accessBinding  = schema.GroupKind{Group: "iam.kaname.in-cloud.io", Kind: "AccessBinding"}
	addon          = schema.GroupKind{Group: "addons.in-cloud.io", Kind: "Addon"}
	httpRoute      = schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"}
)

// ReadyStatus — статус готового объекта; false — вид не ведомый.
func ReadyStatus(u *unstructured.Unstructured) (map[string]any, bool) {
	g := u.GetGeneration()
	name := u.GetName()
	switch u.GroupVersionKind().GroupKind() {
	case s3BucketClaim:
		return map[string]any{"phase": "Ready", "observedGeneration": g, "bucketName": "b-" + name, "endpoint": "https://s3.example.test",
			"conditions": []any{Cond("KeysWritten", "True", g), Cond("Ready", "True", g)}}, true
	case vaultClaim:
		return map[string]any{"phase": "Ready", "observedGeneration": g, "vault": map[string]any{"authMountPath": "kubernetes-site-a"},
			"conditions": []any{Cond("RolesApplied", "True", g), Cond("Ready", "True", g)}}, true
	case serviceAccount:
		return map[string]any{"observedGeneration": g, "id": "sva" + digits(name),
			"conditions": []any{Cond("Ready", "True", g), Cond("KeysReady", "True", g)}}, true
	case accessBinding:
		return map[string]any{"observedGeneration": g, "id": "acb" + digits(name), "conditions": []any{Cond("Ready", "True", g)}}, true
	case addon:
		return map[string]any{"observedGeneration": g,
			"conditions": []any{Cond("Synced", "True", g), Cond("Healthy", "True", g), Cond("Ready", "True", g)}}, true
	case httpRoute:
		return map[string]any{"parents": []any{map[string]any{
			"parentRef":      map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "namespace": "registry-edge", "name": "registry-gw"},
			"controllerName": "example.test/gateway",
			"conditions":     []any{Cond("Accepted", "True", g), Cond("ResolvedRefs", "True", g)},
		}}}, true
	}
	return nil, false
}

// digits — буквы и цифры имени: идентификатор статуса по имени объекта.
func digits(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		if c := name[i]; (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			out = append(out, c)
		}
	}
	return string(out)
}
