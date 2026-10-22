// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package render

import (
	"errors"
	"strings"
	"testing"
)

// Каждый отказ гейта — пара: запрещённая форма и законный близнец, который
// отличается ровно одним фактом и проходит.
func TestGate_RefusesEachForbiddenFormAndPassesItsTwin(t *testing.T) {
	cases := []struct {
		name, bad, twin, reason string
	}{
		{
			name:   "order field printed without toJson",
			bad:    `id: {{ .ID }}`,
			twin:   `id: {{ .ID | toJson }}`,
			reason: "must end with toJson",
		},
		{
			name:   "order field through toYaml",
			bad:    `login: {{ .Login | toYaml | nindent 2 }}`,
			twin:   `site: {{ .Params.site | toYaml | nindent 2 }}`,
			reason: "printed only through toJson",
		},
		{
			name:   "status field through toYaml",
			bad:    `b: {{ .Status.S3BucketClaim.Status | toYaml | nindent 2 }}`,
			twin:   `b: {{ .Status.S3BucketClaim.Status.bucketName | toJson }}`,
			reason: "printed only through toJson",
		},
		{
			name:   "root dot through toYaml",
			bad:    `all: {{ . | toYaml | nindent 2 }}`,
			twin:   `all: {{ .Params | toYaml | nindent 2 }}`,
			reason: "printed only through toJson",
		},
		{
			name:   "root variable to an order field through toYaml",
			bad:    `{{ range .Params.list }}x: {{ $.Registry.UID | toYaml | nindent 2 }}{{ end }}`,
			twin:   `{{ range .Params.list }}x: {{ $.Registry.UID | toJson }}{{ end }}`,
			reason: "printed only through toJson",
		},
		{
			name:   "env function",
			bad:    `h: {{ env "HOME" | toJson }}`,
			twin:   `h: {{ printf "%s" "HOME" | toJson }}`,
			reason: `"env"`,
		},
		{
			name:   "expandenv function",
			bad:    `h: {{ expandenv "$HOME" | toJson }}`,
			twin:   `h: {{ printf "%s" "$HOME" | toJson }}`,
			reason: `"expandenv"`,
		},
		{
			name:   "getHostByName function",
			bad:    `h: {{ getHostByName "example.test" | toJson }}`,
			twin:   `h: {{ printf "%s" "example.test" | toJson }}`,
			reason: `"getHostByName"`,
		},
		{
			name:   "include function",
			bad:    `h: {{ include "x" . | toJson }}`,
			twin:   `h: {{ index .Params "x" | toJson }}`,
			reason: `"include"`,
		},
		{
			name:   "template call",
			bad:    `{{ template "x" }}`,
			twin:   `{{ "x" | toJson }}`,
			reason: "template/block calls are forbidden",
		},
		{
			name:   "variable declaration",
			bad:    `{{ $x := .ID }}id: {{ $x | toJson }}`,
			twin:   `id: {{ .ID | toJson }}`,
			reason: "variable declarations",
		},
		{
			name:   "range over an order field",
			bad:    `{{ range .Status.S3BucketClaim.Status.conditions }}c: {{ .type | toJson }}{{ end }}`,
			twin:   `{{ range .Params.list }}c: {{ .type | toJson }}{{ end }}`,
			reason: "range over order or status data is forbidden",
		},
		{
			name:   "with over an order field",
			bad:    `{{ with .Registry }}u: {{ .UID | toJson }}{{ end }}`,
			twin:   `{{ with .Params.site }}u: {{ .name | toJson }}{{ end }}`,
			reason: "with over order or status data is forbidden",
		},
		{
			name:   "range over a function result",
			bad:    `{{ range len .Params.list }}x{{ end }}`,
			twin:   `{{ range .Params.list }}x{{ end }}`,
			reason: "range must take a single platform field",
		},
		{
			name:   "chained access on an expression",
			bad:    `x: {{ (index .Params "a").b | toJson }}`,
			twin:   `x: {{ index .Params "a" "b" | toJson }}`,
			reason: "chained field access",
		},
	}
	// Функция вне FuncMap отвергается ещё разбором text/template, функция из
	// FuncMap вне перечня — гейтом; в обоих случаях текст называет её имя.
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Compile("bad", c.bad)
			if err == nil {
				t.Fatalf("gate accepted the forbidden form %q", c.bad)
			}
			if !strings.Contains(err.Error(), c.reason) {
				t.Fatalf("gate refused %q for another reason: %v (want %q)", c.bad, err, c.reason)
			}
			if _, err := Compile("twin", c.twin); err != nil {
				t.Fatalf("gate refused the lawful twin %q: %v", c.twin, err)
			}
		})
	}
}

func TestGate_DefineIsRefused(t *testing.T) {
	_, err := Compile("t", `{{ define "x" }}y{{ end }}a: {{ "b" | toJson }}`)
	var ge *GateError
	if !errors.As(err, &ge) || !strings.Contains(ge.Reason, "define is forbidden") {
		t.Fatalf("define passed the gate: %v", err)
	}
	if _, err := Compile("t", `a: {{ "b" | toJson }}`); err != nil {
		t.Fatalf("twin without define refused: %v", err)
	}
}

func TestGate_ElseBranchKeepsTheOuterScope(t *testing.T) {
	// В else точка не переопределена: .ID там — поле заказа.
	bad := `{{ with .Params.site }}a: {{ .x | toJson }}{{ else }}b: {{ .ID | toYaml | nindent 2 }}{{ end }}`
	if _, err := Compile("t", bad); err == nil || !strings.Contains(err.Error(), "only through toJson") {
		t.Fatalf("else branch printed an order field through toYaml: %v", err)
	}
	twin := `{{ with .Params.site }}a: {{ .x | toJson }}{{ else }}b: {{ .ID | toJson }}{{ end }}`
	if _, err := Compile("t", twin); err != nil {
		t.Fatalf("lawful else branch refused: %v", err)
	}
}
