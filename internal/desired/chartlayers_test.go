// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package desired

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/PRO-Robotech/registry-operator/internal/kinds"
)

// goldenPlatformGlob — эталонные рендеры чарта registry-platform (их пишет и
// сверяет с шаблоном проба charts/tests): слои AddonValue и AddonTemplate в
// них — то, что площадка ставит на самом деле.
const goldenPlatformGlob = "../../charts/tests/golden/platform-*.yaml"

// layer — слой AddonValue площадки: имя и метки.
type layer struct {
	name   string
	labels map[string]string
}

// layerCensus — перепись сверки: эталонов прочитано, слоёв AddonValue и
// форм AddonTemplate в них разобрано.
type layerCensus struct {
	files, layers, templates int
}

// templateAction — действие Go-шаблона в тексте AddonTemplate: все они стоят
// на месте скаляра, и замена скаляром оставляет разбираемый YAML.
var templateAction = regexp.MustCompile(`\{\{.*?\}\}`)

// platformLayers читает эталоны: слои AddonValue и селекторы каждой формы
// AddonTemplate площадки (её собственный ответ на вопрос «какие слои
// выбирает Addon заказа»).
func platformLayers(paths []string) (layerCensus, []layer, [][]map[string]string, []string) {
	var census layerCensus
	var layers []layer
	var forms [][]map[string]string
	var findings []string
	for _, path := range paths {
		raw, err := os.ReadFile(path) //nolint:gosec // путь — эталоны чарта в дереве.
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		census.files++
		for _, doc := range bytes.Split(raw, []byte("\n---")) {
			var obj struct {
				Kind     string `json:"kind"`
				Metadata struct {
					Name   string            `json:"name"`
					Labels map[string]string `json:"labels"`
				} `json:"metadata"`
				Spec struct {
					Template string `json:"template"`
				} `json:"spec"`
			}
			if err := yaml.Unmarshal(doc, &obj); err != nil {
				findings = append(findings, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			switch obj.Kind {
			case "AddonValue":
				census.layers++
				layers = append(layers, layer{name: obj.Metadata.Name, labels: obj.Metadata.Labels})
			case "AddonTemplate":
				census.templates++
				var form struct {
					Spec struct {
						ValuesSelectors []struct {
							MatchLabels map[string]string `json:"matchLabels"`
						} `json:"valuesSelectors"`
					} `json:"spec"`
				}
				text := templateAction.ReplaceAllString(obj.Spec.Template, `"x"`)
				if err := yaml.Unmarshal([]byte(text), &form); err != nil {
					findings = append(findings, fmt.Sprintf("%s: AddonTemplate %s: %v", path, obj.Metadata.Name, err))
					continue
				}
				var sels []map[string]string
				for _, s := range form.Spec.ValuesSelectors {
					sels = append(sels, s.MatchLabels)
				}
				forms = append(forms, sels)
			}
		}
	}
	return census, layers, forms, findings
}

// selected — имена слоёв, чьи метки содержат все метки селектора.
func selected(sel map[string]string, layers []layer) []string {
	var out []string
	for _, l := range layers {
		ok := len(sel) > 0
		for k, v := range sel {
			if l.labels[k] != v {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, l.name)
		}
	}
	sort.Strings(out)
	return out
}

// judgeLayers — Addon заказа выбирает ровно те слои, что выбирает форма
// площадки: каждый его селектор попадает ровно в один слой, объединение
// совпадает с выбором формы. Расхождение — находка с названным слоем.
func judgeLayers(ours []map[string]string, layers []layer, platform []map[string]string) []string {
	var findings []string
	var got, want []string
	for i, sel := range ours {
		m := selected(sel, layers)
		if len(m) != 1 {
			findings = append(findings, fmt.Sprintf("order Addon selector %d %v selects %d platform layers %v, want exactly 1", i, sel, len(m), m))
		}
		got = append(got, m...)
	}
	for _, sel := range platform {
		want = append(want, selected(sel, layers)...)
	}
	sort.Strings(got)
	sort.Strings(want)
	got, want = slices.Compact(got), slices.Compact(want)
	if len(want) == 0 {
		findings = append(findings, "the platform form selects no AddonValue layer")
	}
	for _, n := range want {
		if !slices.Contains(got, n) {
			findings = append(findings, fmt.Sprintf("platform layer %s is not selected by the order Addon", n))
		}
	}
	for _, n := range got {
		if !slices.Contains(want, n) {
			findings = append(findings, fmt.Sprintf("order Addon selects %s, which the platform form does not", n))
		}
	}
	return findings
}

// addonSelectors — matchLabels селекторов собранного Addon заказа.
func addonSelectors(t *testing.T, u *unstructured.Unstructured) []map[string]string {
	t.Helper()
	raw, _, err := unstructured.NestedSlice(u.Object, "spec", "valuesSelectors")
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, s := range raw {
		ml, _, err := unstructured.NestedStringMap(s.(map[string]any), "matchLabels")
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ml)
	}
	return out
}

// Addon заказа из примеров (ревизия zot-v1, политика площадки) выбирает
// ровно те слои AddonValue, что ставит чарт registry-platform, и ровно те,
// что выбирает его собственная форма AddonTemplate. Слои и форма берутся из
// эталонных рендеров чарта, а не выписываются здесь; эталонов ноль — сверка
// не выполнилась, а не прошла.
func TestAddon_SelectsExactlyTheLayersThePlatformChartInstalls(t *testing.T) {
	paths, err := filepath.Glob(goldenPlatformGlob)
	if err != nil {
		t.Fatal(err)
	}
	census, layers, forms, findings := platformLayers(paths)
	t.Logf("golden renders read %d, AddonValue layers %d, AddonTemplate forms %d", census.files, census.layers, census.templates)
	if census.files == 0 || census.layers == 0 || census.templates == 0 {
		t.Fatalf("not executed: no golden render of the platform chart under %s (read %d, layers %d, forms %d)",
			goldenPlatformGlob, census.files, census.layers, census.templates)
	}
	f := newFixture(t)
	u, err := Build(kinds.Addon, f.set.Addon, f.in)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	ours := addonSelectors(t, u)
	for _, form := range forms {
		findings = append(findings, judgeLayers(ours, layers, form)...)
	}
	for _, x := range findings {
		t.Error(x)
	}
}

// Сверка способна упасть: каждая порча селектора Addon заказа — своя
// находка с названным слоем; законный близнец молчит; форма площадки,
// не выбирающая ничего, — находка, а не молчание.
func TestAddon_LayerJudgeFindsEachDivergence(t *testing.T) {
	layers := []layer{
		{"registry-platform-edge", map[string]string{"addons.in-cloud.io/addon": "registry-platform", "addons.in-cloud.io/values": "edge"}},
		{"registry-instance-order", map[string]string{"addons.in-cloud.io/addon": "registry-instance", "addons.in-cloud.io/values": "order"}},
		{"registry-instance-site", map[string]string{"addons.in-cloud.io/addon": "registry-instance", "addons.in-cloud.io/values": "site"}},
	}
	sel := func(addon, values string) map[string]string {
		return map[string]string{"addons.in-cloud.io/addon": addon, "addons.in-cloud.io/values": values}
	}
	platform := []map[string]string{sel("registry-instance", "order"), sel("registry-instance", "site")}
	lawful := []map[string]string{sel("registry-instance", "site"), sel("registry-instance", "order")}
	if f := judgeLayers(lawful, layers, platform); len(f) != 0 {
		t.Fatalf("lawful twin: %v", f)
	}
	for _, c := range []struct {
		name string
		ours []map[string]string
		want string
	}{
		{"the label of the platform Addon itself", []map[string]string{sel("registry-platform", "site"), sel("registry-platform", "order")}, "platform layer registry-instance-order is not selected"},
		{"the site layer is missing", []map[string]string{sel("registry-instance", "order")}, "platform layer registry-instance-site is not selected"},
		{"the edge layer of the platform Addon", []map[string]string{sel("registry-instance", "site"), sel("registry-instance", "order"), sel("registry-platform", "edge")}, "order Addon selects registry-platform-edge"},
		{"a selector on the addon label only", []map[string]string{{"addons.in-cloud.io/addon": "registry-instance"}}, "selects 2 platform layers"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := judgeLayers(c.ours, layers, platform)
			if !slices.ContainsFunc(f, func(s string) bool { return strings.Contains(s, c.want) }) {
				t.Fatalf("want a finding %q, got %v", c.want, f)
			}
		})
	}
	if f := judgeLayers(lawful, layers, []map[string]string{sel("registry-instance", "none")}); !slices.Contains(f, "the platform form selects no AddonValue layer") {
		t.Fatalf("an empty platform selection is silent: %v", f)
	}
}
