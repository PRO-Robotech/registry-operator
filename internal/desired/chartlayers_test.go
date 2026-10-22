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

// goldenRender — один эталонный рендер чарта, то есть одна установка площадки:
// её слои AddonValue и селекторы каждой её формы AddonTemplate. Слои разных
// рендеров не смешиваются — одноимённый слой двух установок не делает
// выбор Addon заказа двойным.
type goldenRender struct {
	path   string
	layers []layer
	forms  [][]map[string]string
}

// templateAction — действие Go-шаблона в тексте AddonTemplate: все они стоят
// на месте скаляра, и замена скаляром оставляет разбираемый YAML.
var templateAction = regexp.MustCompile(`\{\{.*?\}\}`)

// platformLayers читает эталоны порознь: у каждого рендера — его слои
// AddonValue и селекторы каждой его формы AddonTemplate (собственный ответ
// установки на вопрос «какие слои выбирает Addon заказа»).
func platformLayers(paths []string) (layerCensus, []goldenRender, []string) {
	var census layerCensus
	var renders []goldenRender
	var findings []string
	for _, path := range paths {
		raw, err := os.ReadFile(path) //nolint:gosec // путь — эталоны чарта в дереве.
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		census.files++
		r := goldenRender{path: path}
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
				r.layers = append(r.layers, layer{name: obj.Metadata.Name, labels: obj.Metadata.Labels})
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
				r.forms = append(r.forms, sels)
			}
		}
		renders = append(renders, r)
	}
	return census, renders, findings
}

// judgeRenders — сверка Addon заказа с каждым рендером по отдельности:
// слои и формы одной установки. Рендер без формы — находка, а не молчание.
func judgeRenders(ours []map[string]string, renders []goldenRender) []string {
	var findings []string
	for _, r := range renders {
		if len(r.forms) == 0 {
			findings = append(findings, fmt.Sprintf("%s: no AddonTemplate form to judge the order Addon against", r.path))
		}
		for _, form := range r.forms {
			for _, f := range judgeLayers(ours, r.layers, form) {
				findings = append(findings, fmt.Sprintf("%s: %s", r.path, f))
			}
		}
	}
	return findings
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
	census, renders, findings := platformLayers(paths)
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
	findings = append(findings, judgeRenders(addonSelectors(t, u), renders)...)
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

// Рендеры судятся порознь: две установки с одноимёнными слоями — законный
// близнец, молчат; двойной слой внутри одной установки и установка без
// формы — находки с названным рендером.
func TestAddon_RendersAreJudgedOneByOne(t *testing.T) {
	sel := func(addon, values string) map[string]string {
		return map[string]string{"addons.in-cloud.io/addon": addon, "addons.in-cloud.io/values": values}
	}
	layers := []layer{
		{"registry-platform-edge", sel("registry-platform", "edge")},
		{"registry-instance-order", sel("registry-instance", "order")},
		{"registry-instance-site", sel("registry-instance", "site")},
	}
	form := []map[string]string{sel("registry-instance", "order"), sel("registry-instance", "site")}
	ours := []map[string]string{sel("registry-instance", "site"), sel("registry-instance", "order")}
	two := []goldenRender{
		{path: "platform-default.yaml", layers: layers, forms: [][]map[string]string{form}},
		{path: "platform-all.yaml", layers: layers, forms: [][]map[string]string{form}},
	}
	if f := judgeRenders(ours, two); len(f) != 0 {
		t.Fatalf("lawful twin: two installations with the same layers: %v", f)
	}
	doubled := []goldenRender{{path: "platform-doubled.yaml", layers: append(slices.Clone(layers), layers[2]), forms: [][]map[string]string{form}}}
	if f := judgeRenders(ours, doubled); !slices.ContainsFunc(f, func(s string) bool {
		return strings.HasPrefix(s, "platform-doubled.yaml: ") && strings.Contains(s, "selects 2 platform layers [registry-instance-site registry-instance-site]")
	}) {
		t.Fatalf("a layer rendered twice in one installation is silent: %v", f)
	}
	formless := []goldenRender{{path: "platform-formless.yaml", layers: layers}}
	if f := judgeRenders(ours, formless); !slices.Contains(f, "platform-formless.yaml: no AddonTemplate form to judge the order Addon against") {
		t.Fatalf("a render without a form is silent: %v", f)
	}
}
