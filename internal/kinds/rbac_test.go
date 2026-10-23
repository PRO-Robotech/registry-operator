// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package kinds

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

const rolePath = "../../charts/registry-operator/files/role.yaml"

// ownWrites — записи оператора в собственные виды: заказ, шаблоны, резерв,
// события и проверка токенов эндпоинта заявок. Перечень закрыт.
var ownWrites = map[string][]string{
	"registry.in-cloud.io/registries":                    {"patch", "update"},
	"registry.in-cloud.io/registries/status":             {"patch", "update"},
	"registry.in-cloud.io/registries/finalizers":         {"update"},
	"registry.in-cloud.io/registrytemplates":             {"patch", "update"},
	"registry.in-cloud.io/registrytemplates/status":      {"patch", "update"},
	"registry.in-cloud.io/registrytemplates/finalizers":  {"update"},
	"registry.in-cloud.io/registryidreservations":        {"create"},
	"registry.in-cloud.io/registryidreservations/status": {"patch", "update"},
	"events.k8s.io/events":                               {"create", "patch"},
	"authentication.k8s.io/tokenreviews":                 {"create"},
}

var readVerbs = []string{"get", "list", "watch"}

// writeViolations — правила роли, пишущие сверх шести ведомых видов и
// собственных объектов; чтение — только get/list/watch.
func writeViolations(role *rbacv1.ClusterRole) []string {
	written := map[string]bool{}
	for _, k := range Written {
		written[k.GVK.Group+"/"+k.Resource] = true
	}
	var out []string
	for _, rule := range role.Rules {
		for _, g := range rule.APIGroups {
			for _, res := range rule.Resources {
				key := g + "/" + res
				for _, v := range rule.Verbs {
					switch {
					case v == "*" || g == "*" || res == "*":
						out = append(out, fmt.Sprintf("%s: wildcard %q", key, v))
					case slices.Contains(readVerbs, v):
					case written[key] && slices.Contains([]string{"create", "patch", "delete"}, v):
					case strings.HasPrefix(key, "observed.registry.in-cloud.io/") && slices.Contains([]string{"patch", "update"}, v):
					case slices.Contains(ownWrites[key], v):
					default:
						out = append(out, fmt.Sprintf("%s: %s is not allowed", key, v))
					}
				}
			}
		}
	}
	return out
}

func loadRole(t *testing.T, b []byte) *rbacv1.ClusterRole {
	t.Helper()
	role := &rbacv1.ClusterRole{}
	if err := yaml.UnmarshalStrict(b, role); err != nil {
		t.Fatal(err)
	}
	return role
}

// Роль оператора пишет ровно шесть ведомых видов (и свои объекты); всё
// прочее — только чтение. Каждый из шести видов роль действительно пишет.
func TestClusterRole_WritesOnlyTheSixKinds(t *testing.T) {
	b, err := os.ReadFile(rolePath)
	if err != nil {
		t.Fatal(err)
	}
	role := loadRole(t, b)
	if v := writeViolations(role); len(v) != 0 {
		t.Fatalf("role writes beyond the six kinds: %v", v)
	}
	for _, k := range Written {
		found := false
		for _, rule := range role.Rules {
			if slices.Contains(rule.APIGroups, k.GVK.Group) && slices.Contains(rule.Resources, k.Resource) && slices.Contains(rule.Verbs, "create") {
				found = true
			}
		}
		if !found {
			t.Fatalf("role does not write %s/%s", k.GVK.Group, k.Resource)
		}
	}
	if n := len(role.Rules); n == 0 {
		t.Fatal("role has no rules: the census read nothing")
	}
}

// Проверка роли сама способна упасть: синтетическая роль с записью в
// секреты отвергается, её близнец без этого правила — нет.
func TestClusterRole_CheckFindsAForbiddenWrite(t *testing.T) {
	twin := &rbacv1.ClusterRole{Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{"vault.in-cloud.io"}, Resources: []string{"vaultclaims"}, Verbs: []string{"create", "get"}},
		{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}},
	}}
	if v := writeViolations(twin); len(v) != 0 {
		t.Fatalf("lawful twin refused: %v", v)
	}
	bad := twin.DeepCopy()
	bad.Rules[1].Verbs = append(bad.Rules[1].Verbs, "create")
	if v := writeViolations(bad); len(v) != 1 || !strings.Contains(v[0], "/secrets: create") {
		t.Fatalf("write to secrets not found: %v", v)
	}
	star := twin.DeepCopy()
	star.Rules[0].Resources = []string{"*"}
	if v := writeViolations(star); len(v) == 0 {
		t.Fatal("wildcard not found")
	}
}
