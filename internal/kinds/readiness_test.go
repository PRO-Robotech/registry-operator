// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package kinds

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/yaml"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
)

func obj(t *testing.T, src string) *unstructured.Unstructured {
	t.Helper()
	j, err := yaml.YAMLToJSON([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := utiljson.Unmarshal(j, &m); err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: m}
}

type readyCase struct {
	name   string
	kind   Kind
	obj    string
	ready  bool
	reason string
}

// Каждый отрицательный случай меняет ровно один факт против готового
// близнеца того же вида.
func TestReadiness_EachKindNeedsItsOwnStatusOnTheCurrentGeneration(t *testing.T) {
	s3Ready := `
metadata: {name: zot-abc123, generation: 2}
status:
  observedGeneration: 2
  phase: Ready
  conditions: [{type: KeysWritten, status: "True", observedGeneration: 2}]`
	vcReady := `
metadata: {name: zot-abc123, generation: 3}
status:
  observedGeneration: 3
  phase: Ready
  conditions: [{type: RolesApplied, status: "True", observedGeneration: 3}]`
	saReady := `
metadata: {name: zot-abc123-ci, generation: 1}
spec: {keys: [{name: push}]}
status:
  id: sva1
  observedGeneration: 1
  conditions:
  - {type: Ready, status: "True", observedGeneration: 1}
  - {type: KeysReady, status: "True", observedGeneration: 1}`
	abReady := `
metadata: {name: zot-abc123-ci-pusher, generation: 1}
status:
  id: acb1
  conditions:
  - {type: Ready, status: "True", observedGeneration: 1}`
	addonReadyObj := `
metadata: {name: reg-abc123, generation: 4}
status:
  observedGeneration: 4
  conditions:
  - {type: Ready, status: "True", observedGeneration: 4}
  - {type: Synced, status: "True", observedGeneration: 4}
  - {type: Healthy, status: "True", observedGeneration: 4}`
	routeReady := `
metadata: {name: zot-abc123, generation: 2}
status:
  parents:
  - conditions:
    - {type: Accepted, status: "True", observedGeneration: 2}
    - {type: ResolvedRefs, status: "True", observedGeneration: 2}`
	deployReady := `
metadata: {name: zot-abc123, generation: 5}
spec: {replicas: 2}
status:
  observedGeneration: 5
  updatedReplicas: 2
  availableReplicas: 2
  conditions:
  - {type: Available, status: "True"}`

	cases := []readyCase{
		{"s3 ready", S3BucketClaim, s3Ready, true, registryv1alpha1.ReasonResourceReady},
		{"s3 stale generation", S3BucketClaim, replace(s3Ready, "observedGeneration: 2\n  phase", "observedGeneration: 1\n  phase"), false, registryv1alpha1.ReasonResourceStale},
		{"s3 keys not written", S3BucketClaim, replace(s3Ready, `status: "True"`, `status: "False"`), false, registryv1alpha1.ReasonResourceProgressing},
		{"s3 keys written on an old generation", S3BucketClaim, replace(s3Ready, `"True", observedGeneration: 2`, `"True", observedGeneration: 1`), false, registryv1alpha1.ReasonResourceProgressing},
		{"s3 failed", S3BucketClaim, replace(s3Ready, "phase: Ready", "phase: Failed"), false, registryv1alpha1.ReasonResourceDegraded},
		{"s3 pending", S3BucketClaim, replace(s3Ready, "phase: Ready", "phase: Pending"), false, registryv1alpha1.ReasonResourceProgressing},

		{"vaultclaim ready", VaultClaim, vcReady, true, registryv1alpha1.ReasonResourceReady},
		{"vaultclaim roles not applied", VaultClaim, replace(vcReady, "RolesApplied", "PoliciesApplied"), false, registryv1alpha1.ReasonResourceProgressing},
		{"vaultclaim failed", VaultClaim, replace(vcReady, "phase: Ready", "phase: Failed"), false, registryv1alpha1.ReasonResourceDegraded},

		{"iam sa ready", IamServiceAccount, saReady, true, registryv1alpha1.ReasonResourceReady},
		{"iam sa without id", IamServiceAccount, replace(saReady, "id: sva1", "id: \"\""), false, registryv1alpha1.ReasonResourceProgressing},
		{"iam sa keys not ready", IamServiceAccount, replace(saReady, "{type: KeysReady, status: \"True\"", "{type: KeysReady, status: \"False\""), false, registryv1alpha1.ReasonResourceProgressing},
		{"iam sa ready on an old generation", IamServiceAccount, replace(saReady, "generation: 1}", "generation: 2}"), false, registryv1alpha1.ReasonResourceStale},
		{"iam sa drifted", IamServiceAccount, saReady + "\n  - {type: Drifted, status: \"True\"}", false, registryv1alpha1.ReasonResourceDegraded},
		{"iam sa forbidden", IamServiceAccount, replace(saReady, "{type: Ready, status: \"True\"", "{type: Ready, status: \"False\", reason: Forbidden"), false, registryv1alpha1.ReasonResourceDegraded},
		{"iam sa retrying", IamServiceAccount, replace(saReady, "{type: Ready, status: \"True\"", "{type: Ready, status: \"False\", reason: Retrying"), false, registryv1alpha1.ReasonResourceProgressing},

		{"iam ab ready", IamAccessBinding, abReady, true, registryv1alpha1.ReasonResourceReady},
		{"iam ab conflict", IamAccessBinding, abReady + "\n  - {type: Conflict, status: \"True\"}", false, registryv1alpha1.ReasonResourceDegraded},
		{"iam ab without id", IamAccessBinding, replace(abReady, "id: acb1", "id: \"\""), false, registryv1alpha1.ReasonResourceProgressing},

		{"addon ready", Addon, addonReadyObj, true, registryv1alpha1.ReasonResourceReady},
		{"addon not observed", Addon, replace(addonReadyObj, "observedGeneration: 4\n  conditions", "observedGeneration: 3\n  conditions"), false, registryv1alpha1.ReasonResourceStale},
		{"addon not synced", Addon, replace(addonReadyObj, "{type: Synced, status: \"True\"", "{type: Synced, status: \"False\""), false, registryv1alpha1.ReasonResourceProgressing},
		{"addon not healthy", Addon, replace(addonReadyObj, "{type: Healthy, status: \"True\"", "{type: Healthy, status: \"False\""), false, registryv1alpha1.ReasonResourceProgressing},
		{"addon degraded", Addon, addonReadyObj + "\n  - {type: Degraded, status: \"True\"}", false, registryv1alpha1.ReasonResourceDegraded},

		{"route ready", HTTPRoute, routeReady, true, registryv1alpha1.ReasonResourceReady},
		{"route without parents", HTTPRoute, "metadata: {name: r, generation: 1}\nstatus: {parents: []}", false, registryv1alpha1.ReasonResourceProgressing},
		{"route refs not resolved", HTTPRoute, replace(routeReady, "{type: ResolvedRefs, status: \"True\"", "{type: ResolvedRefs, status: \"False\""), false, registryv1alpha1.ReasonResourceDegraded},
		{"route accepted on an old generation", HTTPRoute, replace(routeReady, "{type: Accepted, status: \"True\", observedGeneration: 2}", "{type: Accepted, status: \"True\", observedGeneration: 1}"), false, registryv1alpha1.ReasonResourceStale},

		{"deployment available", Deployment, deployReady, true, registryv1alpha1.ReasonResourceReady},
		{"deployment one replica short", Deployment, replace(deployReady, "availableReplicas: 2", "availableReplicas: 1"), false, registryv1alpha1.ReasonResourceProgressing},
		{"deployment not observed", Deployment, replace(deployReady, "observedGeneration: 5", "observedGeneration: 4"), false, registryv1alpha1.ReasonResourceStale},
		{"deployment past deadline", Deployment, deployReady + "\n  - {type: Progressing, status: \"False\", reason: ProgressDeadlineExceeded}", false, registryv1alpha1.ReasonResourceDegraded},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.kind.Ready(obj(t, c.obj))
			if got.Ready != c.ready || got.Reason != c.reason {
				t.Fatalf("ready=%v reason=%s (%s), want ready=%v reason=%s", got.Ready, got.Reason, got.Message, c.ready, c.reason)
			}
		})
	}
}

func replace(s, old, repl string) string {
	if !strings.Contains(s, old) {
		panic("fixture edit does not apply: " + old)
	}
	return strings.Replace(s, old, repl, 1)
}

func TestStages_NextStageWaitsForEveryStepOfThePreviousOne(t *testing.T) {
	r := map[registryv1alpha1.Step]bool{}
	if got := FirstNotReadyStage(r); got != 0 {
		t.Fatalf("empty: stage %d", got)
	}
	// Первая стадия — бакет, роль Vault, учётка и регистрация в IAM разом.
	for _, s := range []registryv1alpha1.Step{registryv1alpha1.StepStorage, registryv1alpha1.StepSecrets, registryv1alpha1.StepIdentity} {
		r[s] = true
	}
	if got := FirstNotReadyStage(r); got != 0 {
		t.Fatalf("stage one without the IAM registration opened stage %d", got)
	}
	r[registryv1alpha1.StepRegistration] = true
	if got := FirstNotReadyStage(r); got != 1 {
		t.Fatalf("stage one ready: stage %d, want 1", got)
	}
	r[registryv1alpha1.StepAccess] = true
	if got := FirstNotReadyStage(r); got != 1 {
		t.Fatalf("access without workload opened stage %d", got)
	}
	r[registryv1alpha1.StepWorkload] = true
	if got := FirstNotReadyStage(r); got != 2 {
		t.Fatalf("all but route: stage %d, want 2", got)
	}
	r[registryv1alpha1.StepRoute] = true
	if got := FirstNotReadyStage(r); got != len(Stages) {
		t.Fatalf("all: stage %d, want %d", got, len(Stages))
	}
	// Готовый поздний шаг не открывает стадию, пока ранний не готов.
	r[registryv1alpha1.StepRegistration] = false
	if got := FirstNotReadyStage(r); got != 0 {
		t.Fatalf("registration regressed: stage %d, want 0", got)
	}
}

func TestStages_FirstStageIsTheParallelSetOfTheFinalForm(t *testing.T) {
	want := [][]registryv1alpha1.Step{
		{registryv1alpha1.StepStorage, registryv1alpha1.StepSecrets, registryv1alpha1.StepIdentity},
		{registryv1alpha1.StepAccess, registryv1alpha1.StepWorkload},
		{registryv1alpha1.StepRoute},
	}
	if len(Stages) != len(want) {
		t.Fatalf("%d stages, want %d", len(Stages), len(want))
	}
	for i, st := range Stages {
		if len(st) != len(want[i]) {
			t.Fatalf("stage %d has %d steps, want %d", i, len(st), len(want[i]))
		}
		for j, k := range st {
			if k.Step != want[i][j] {
				t.Fatalf("stage %d step %d = %s, want %s", i, j, k.Step, want[i][j])
			}
		}
	}
	if RegistrationStage != 0 {
		t.Fatalf("IAM registration runs in stage %d, want 0", RegistrationStage)
	}
	if len(Written) != 6 {
		t.Fatalf("the operator writes %d kinds, want exactly six", len(Written))
	}
}

func TestDeletionOrder_IsThePipelineReversed(t *testing.T) {
	stageOf := map[registryv1alpha1.Step]int{registryv1alpha1.StepRegistration: RegistrationStage}
	steps := 1
	for i, st := range Stages {
		for _, k := range st {
			stageOf[k.Step] = i
			steps++
		}
	}
	if steps != len(DeletionOrder) {
		t.Fatalf("pipeline has %d steps, deletion order %d", steps, len(DeletionOrder))
	}
	step := func(ts TeardownStep) registryv1alpha1.Step {
		if ts.Registration {
			return registryv1alpha1.StepRegistration
		}
		return ts.Kind.Step
	}
	// Шаг поздней стадии снимается раньше любого шага ранней.
	for i := 1; i < len(DeletionOrder); i++ {
		a, b := step(DeletionOrder[i-1]), step(DeletionOrder[i])
		if stageOf[a] < stageOf[b] {
			t.Fatalf("%s is deleted before %s of a later stage", a, b)
		}
	}
	want := []registryv1alpha1.Step{"route", "workload", "access", "registration", "identity", "secrets", "storage"}
	for i, ts := range DeletionOrder {
		if step(ts) != want[i] {
			t.Fatalf("deletion order[%d] = %s, want %s", i, step(ts), want[i])
		}
	}
}
