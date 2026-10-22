// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package kinds

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// crdDoc — та часть CustomResourceDefinition, по которой API-сервер
// разрешает вид и ресурс записи.
type crdDoc struct {
	Kind string `json:"kind"`
	Spec struct {
		Group string `json:"group"`
		Names struct {
			Kind   string `json:"kind"`
			Plural string `json:"plural"`
		} `json:"names"`
		Scope    string `json:"scope"`
		Versions []struct {
			Name   string `json:"name"`
			Served bool   `json:"served"`
		} `json:"versions"`
	} `json:"spec"`
}

var docSeparator = regexp.MustCompile(`(?m)^---\s*$`)

// parseCRDs — все документы CustomResourceDefinition файла (в том числе
// многодокументного, как standard-install Gateway API).
func parseCRDs(b []byte) ([]crdDoc, error) {
	var out []crdDoc
	for _, part := range docSeparator.Split(string(b), -1) {
		if strings.TrimSpace(part) == "" {
			continue
		}
		var d crdDoc
		if err := yaml.Unmarshal([]byte(part), &d); err != nil {
			return nil, err
		}
		if d.Kind == "CustomResourceDefinition" {
			out = append(out, d)
		}
	}
	return out, nil
}

// servedBy — отказ, если ни одна CRD не отдаёт вид k под его группой,
// версией, именем ресурса и областью: такой объект API-сервер не примет.
func servedBy(docs []crdDoc, k Kind) error {
	for _, d := range docs {
		if d.Spec.Group != k.GVK.Group || d.Spec.Names.Kind != k.GVK.Kind {
			continue
		}
		if d.Spec.Names.Plural != k.Resource {
			return fmt.Errorf("%s: CRD plural %q, kinds says %q", k.GVK, d.Spec.Names.Plural, k.Resource)
		}
		if (d.Spec.Scope == "Namespaced") != k.Namespaced {
			return fmt.Errorf("%s: CRD scope %q, kinds says namespaced=%v", k.GVK, d.Spec.Scope, k.Namespaced)
		}
		for _, v := range d.Spec.Versions {
			if v.Name == k.GVK.Version && v.Served {
				return nil
			}
		}
		return fmt.Errorf("%s: CRD does not serve version %q", k.GVK, k.GVK.Version)
	}
	return fmt.Errorf("%s: no pinned CRD serves kind %q in group %q", k.GVK, k.GVK.Kind, k.GVK.Group)
}

// Каждый из шести ведомых видов отдаёт закреплённая hack/crds.lock CRD
// источника: вид, ресурс, версия и область в kinds — ровно те, что знает
// API-сервер. Переименование вида у источника без подъёма пина (и наоборот)
// краснит здесь, а не на стенде.
func TestWrittenKinds_AreServedByThePinnedCRDs(t *testing.T) {
	dir := os.Getenv("REGOP_TEST_CRDS")
	if dir == "" {
		dir = filepath.Join("..", "..", "bin", "crds")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var docs []crdDoc
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		d, err := parseCRDs(b)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		docs = append(docs, d...)
	}
	t.Logf("pinned CRD files read %d, CRD documents %d, kinds judged %d", len(files), len(docs), len(Written))
	if len(docs) == 0 {
		// Не пропуск: без закреплённых CRD проба не выполнилась.
		t.Fatalf("not executed: no pinned CRD under %s (files %d); run `make crds`", dir, len(files))
	}
	for _, k := range Written {
		if err := servedBy(docs, k); err != nil {
			t.Error(err)
		}
	}
}

// Сверка с CRD сама способна упасть: законный близнец принят, а прежнее
// имя вида, чужое имя ресурса и неотдаваемая версия — отказ с названным видом.
func TestServedBy_FindsAKindTheCRDDoesNotServe(t *testing.T) {
	const twin = `---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
spec:
  group: iam.kaname.in-cloud.io
  names: {kind: IAMServiceAccount, plural: iamserviceaccounts}
  scope: Namespaced
  versions: [{name: v1alpha1, served: true}]
---
apiVersion: v1
kind: Namespace
metadata: {name: not-a-crd}
`
	docs, err := parseCRDs([]byte(twin))
	if err != nil || len(docs) != 1 {
		t.Fatalf("twin parsed into %d CRDs: %v", len(docs), err)
	}
	k := Kind{GVK: schema.GroupVersionKind{Group: "iam.kaname.in-cloud.io", Version: "v1alpha1", Kind: "IAMServiceAccount"},
		Resource: "iamserviceaccounts", Namespaced: true}
	if err := servedBy(docs, k); err != nil {
		t.Fatalf("lawful twin refused: %v", err)
	}
	cases := []struct {
		name string
		mut  func(Kind) Kind
		want string
	}{
		{"old kind name", func(k Kind) Kind { k.GVK.Kind = "ServiceAccount"; return k }, `no pinned CRD serves kind "ServiceAccount"`},
		{"old plural", func(k Kind) Kind { k.Resource = "serviceaccounts"; return k }, `CRD plural "iamserviceaccounts", kinds says "serviceaccounts"`},
		{"unserved version", func(k Kind) Kind { k.GVK.Version = "v1"; return k }, `does not serve version "v1"`},
		{"cluster scope", func(k Kind) Kind { k.Namespaced = false; return k }, `scope "Namespaced"`},
	}
	for _, c := range cases {
		err := servedBy(docs, c.mut(k))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
}

// Виды kaname — с префиксом IAM (указание владельца 2026-10-22: «не имя а
// kind», «IAMServiceAccount ок другим тоже префиксы добавь»); прежних имён
// ServiceAccount и AccessBinding в группе iam.kaname.in-cloud.io нет.
func TestKanameKinds_CarryTheIAMPrefix(t *testing.T) {
	want := []struct {
		got      Kind
		kind     string
		resource string
	}{
		{IamServiceAccount, "IAMServiceAccount", "iamserviceaccounts"},
		{IamAccessBinding, "IAMAccessBinding", "iamaccessbindings"},
	}
	for _, w := range want {
		g := w.got.GVK
		if g.Group != "iam.kaname.in-cloud.io" || g.Version != "v1alpha1" || g.Kind != w.kind || w.got.Resource != w.resource {
			t.Errorf("step %s writes %s (resource %q), want iam.kaname.in-cloud.io/v1alpha1, Kind=%s (resource %q)",
				w.got.Step, g, w.got.Resource, w.kind, w.resource)
		}
	}
	judged := 0
	for _, k := range Written {
		if k.GVK.Group != "iam.kaname.in-cloud.io" {
			continue
		}
		judged++
		if !strings.HasPrefix(k.GVK.Kind, "IAM") || !strings.HasPrefix(k.Resource, "iam") {
			t.Errorf("kaname kind without the IAM prefix: %s (resource %q)", k.GVK, k.Resource)
		}
	}
	if judged != len(want) {
		t.Fatalf("kaname kinds among Written: %d, want %d", judged, len(want))
	}
}
