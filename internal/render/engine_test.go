// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package render

import (
	"errors"
	"strings"
	"testing"
)

func testContext() Context {
	return Context{
		Registry: Registry{Name: "abc123", Namespace: "cust01", UID: "6f1c2a3b-4d5e-4f60-8a71-92b3c4d5e6f7", DeletionPolicy: "Retain"},
		ID:       "abc123",
		Login:    "cust01",
		Params:   map[string]any{"site": map[string]any{"name": "s1"}, "list": []any{map[string]any{"type": "a"}}},
		Template: TemplateInfo{Name: "zot", Version: "v1"},
		Status: map[string]Child{
			"S3BucketClaim": {Name: "zot-abc123", Namespace: "cust01", Status: map[string]any{"bucketName": "b-1"}},
		},
	}
}

func TestExecute_OrderDataIsEscapedNotInterpreted(t *testing.T) {
	ctx := testContext()
	ctx.Login = "x\ny: {{ injected }}"
	tpl, err := Compile("t", `login: {{ .Login | toJson }}`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tpl.Execute(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if out["login"] != ctx.Login || len(out) != 1 {
		t.Fatalf("order data changed the document structure: %#v", out)
	}
}

func TestExecute_MissingStatusOfANotReadyStepFails(t *testing.T) {
	tpl, err := Compile("t", `p: {{ .Status.VaultClaim.Name | toJson }}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tpl.Execute(testContext()); err == nil {
		t.Fatal("a template reading the status of a step that is not ready rendered")
	}
	twin, err := Compile("t", `p: {{ .Status.S3BucketClaim.Status.bucketName | toJson }}`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := twin.Execute(testContext())
	if err != nil {
		t.Fatalf("status of a ready step did not render: %v", err)
	}
	if out["p"] != "b-1" {
		t.Fatalf("p = %#v", out["p"])
	}
}

func TestExecute_IntegersStayIntegers(t *testing.T) {
	// Ключ не «n»: в YAML 1.1 разбора sigs.k8s.io/yaml это булево false.
	tpl, err := Compile("t", `epoch: {{ .Registry.SyncEpoch | toJson }}`)
	if err != nil {
		t.Fatal(err)
	}
	ctx := testContext()
	ctx.Registry.SyncEpoch = 7
	out, err := tpl.Execute(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := out["epoch"].(int64); !ok || v != 7 {
		t.Fatalf("epoch = %#v (%T), want int64 7", out["epoch"], out["epoch"])
	}
}

func TestExecute_DuplicateKeysAndNonMappingsAreRefused(t *testing.T) {
	for _, src := range []string{"a: 1\na: 2\n", "- a\n- b\n", "just text"} {
		tpl, err := Compile("t", src)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tpl.Execute(testContext()); err == nil {
			t.Fatalf("%q rendered into a spec", src)
		}
	}
	tpl, err := Compile("t", "a: 1\nb: 2\n")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := tpl.Execute(testContext()); err != nil || len(out) != 2 {
		t.Fatalf("lawful mapping refused: %v %#v", err, out)
	}
}

func TestExecute_OutputLimit(t *testing.T) {
	ctx := testContext()
	ctx.Params = map[string]any{"big": strings.Repeat("x", MaxOutputBytes)}
	tpl, err := Compile("t", `a: {{ .Params.big | toJson }}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tpl.Execute(ctx); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("oversized output: %v", err)
	}
	ctx.Params = map[string]any{"big": strings.Repeat("x", 1024)}
	if _, err := tpl.Execute(ctx); err != nil {
		t.Fatalf("small output refused: %v", err)
	}
}
