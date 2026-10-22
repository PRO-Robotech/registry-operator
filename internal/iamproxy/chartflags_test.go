// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package iamproxy_test

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/PRO-Robotech/registry-operator/internal/iamproxy"
)

// goldenInstanceGlob — эталонные рендеры чарта registry-instance (их пишет
// и сверяет с шаблоном проба charts/tests): флаги контейнера iam-proxy в них —
// то, с чем прокси запускается на самом деле.
const goldenInstanceGlob = "../../charts/tests/golden/instance-*.yaml"

// chartFlagCensus — перепись сверки: сколько эталонов прочитано и сколько
// флагов контейнера iam-proxy в них разобрано.
type chartFlagCensus struct {
	files, flags int
}

// judgeChartFlags — сверка флагов iam-proxy из эталонов чарта с флагами
// cmd/iam-proxy в обе стороны: флаг эталона, которого прокси не знает, и
// флаг прокси, которого эталон не передаёт, — расхождение; конфигурация
// из флагов эталона обязана пройти проверку старта.
func judgeChartFlags(paths []string) (chartFlagCensus, []string) {
	var census chartFlagCensus
	var findings []string
	declared := map[string]bool{}
	probe := flag.NewFlagSet("iam-proxy", flag.ContinueOnError)
	(&iamproxy.Config{}).BindFlags(probe)
	probe.VisitAll(func(f *flag.Flag) { declared[f.Name] = true })
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		census.files++
		args, err := iamProxyArgs(raw)
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		census.flags += len(args)
		var c iamproxy.Config
		fs := flag.NewFlagSet("iam-proxy", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		c.BindFlags(fs)
		passed := map[string]bool{}
		for _, a := range args {
			name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
			if !declared[name] {
				findings = append(findings, fmt.Sprintf("%s: the chart passes --%s, iam-proxy does not know it", path, name))
				continue
			}
			passed[name] = true
			if err := fs.Parse([]string{a}); err != nil {
				findings = append(findings, fmt.Sprintf("%s: %s is not understood: %v", path, a, err))
			}
		}
		var missing []string
		for name := range declared {
			if !passed[name] {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		for _, name := range missing {
			findings = append(findings, fmt.Sprintf("%s: iam-proxy declares --%s, the chart does not pass it", path, name))
		}
		if err := c.Validate(); err != nil {
			findings = append(findings, fmt.Sprintf("%s: the configuration of the chart is refused at start: %v", path, err))
		}
	}
	return census, findings
}

// iamProxyArgs — аргументы единственного контейнера iam-proxy в
// Deployment эталона (многодокументный YAML).
func iamProxyArgs(raw []byte) ([]string, error) {
	type container struct {
		Name string   `json:"name"`
		Args []string `json:"args"`
	}
	type object struct {
		Kind string `json:"kind"`
		Spec struct {
			Template struct {
				Spec struct {
					Containers []container `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	var found [][]string
	for _, doc := range bytes.Split(raw, []byte("\n---")) {
		var o object
		if err := yaml.Unmarshal(doc, &o); err != nil {
			return nil, err
		}
		if o.Kind != "Deployment" {
			continue
		}
		for _, c := range o.Spec.Template.Spec.Containers {
			if c.Name == "iam-proxy" {
				found = append(found, c.Args)
			}
		}
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("%d iam-proxy containers in Deployments, want exactly 1", len(found))
	}
	if len(found[0]) == 0 {
		return nil, fmt.Errorf("the iam-proxy container has no args")
	}
	return found[0], nil
}

// Интерфейс флагов, с которым чарт registry-instance запускает прокси:
// флаги берутся из эталонных рендеров чарта, а не выписываются здесь.
// Эталонов ноль — сверка не выполнилась, а не прошла.
func TestConfig_TheFlagsOfTheInstanceChartAreUnderstood(t *testing.T) {
	paths, err := filepath.Glob(goldenInstanceGlob)
	if err != nil {
		t.Fatal(err)
	}
	census, findings := judgeChartFlags(paths)
	t.Logf("golden renders read %d, iam-proxy flags parsed %d", census.files, census.flags)
	if census.files == 0 || census.flags == 0 {
		t.Fatalf("not executed: no golden render of the instance chart under %s (read %d, flags %d)", goldenInstanceGlob, census.files, census.flags)
	}
	for _, f := range findings {
		t.Error(f)
	}
}

// Сверка способна упасть: каждая порча одного флага синтетического
// эталона — своё расхождение с названным флагом; законный близнец
// молчит; эталон без контейнера iam-proxy — находка, а не молчание.
func TestConfig_ChartFlagJudgeFindsEachDivergence(t *testing.T) {
	lawful := []string{
		"--listen=:8080", "--health-listen=:8081", "--upstream=http://127.0.0.1:5000", "--upstream-ready-path=/readyz",
		"--registry-id=abc123", "--public=false",
		"--jwks-url=https://iam.example.test/jwks", "--jwks-ca=/ca", "--issuer=https://iam.example.test", "--audience=registry",
		"--token-types=at+jwt",
		"--kaname-url=https://kaname.iam-system.svc:9098", "--kaname-ca=/ca",
		"--check-timeout=2s", "--cache-ttl=10s",
		"--enroll-url=https://registry-operator.registry-system.svc:8443", "--enroll-ca=/ca", "--enroll-token-file=/tok", "--enroll-timeout=15s",
	}
	render := func(container string, args []string) string {
		var b strings.Builder
		b.WriteString("apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: zot-abc123\n---\n")
		b.WriteString("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: zot-abc123\nspec:\n  template:\n    spec:\n      containers:\n")
		b.WriteString("        - name: zot\n          args: [serve]\n")
		b.WriteString("        - name: " + container + "\n          args:\n")
		for _, a := range args {
			b.WriteString("            - \"" + a + "\"\n")
		}
		return b.String()
	}
	judge := func(t *testing.T, body string) (chartFlagCensus, []string) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "instance-abc123.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return judgeChartFlags([]string{path})
	}

	census, findings := judge(t, render("iam-proxy", lawful))
	if len(findings) != 0 || census.files != 1 || census.flags != len(lawful) {
		t.Fatalf("lawful twin: census %+v, findings %q", census, findings)
	}

	without := func(name string) []string {
		return slices.DeleteFunc(slices.Clone(lawful), func(a string) bool { return strings.HasPrefix(a, "--"+name+"=") })
	}
	for name, c := range map[string]struct {
		body string
		want string
	}{
		"flag the proxy does not know": {render("iam-proxy", append(slices.Clone(lawful), "--kaname-address=kaname:9090")), "passes --kaname-address, iam-proxy does not know it"},
		"flag the chart drops":         {render("iam-proxy", without("public")), "declares --public, the chart does not pass it"},
		"value refused at start":       {render("iam-proxy", append(without("kaname-url"), "--kaname-url=http://kaname:9098")), "refused at start: kaname-url"},
		"value that does not parse":    {render("iam-proxy", append(without("check-timeout"), "--check-timeout=soon")), "--check-timeout=soon is not understood"},
		"no iam-proxy container":       {render("proxy", lawful), "0 iam-proxy containers"},
	} {
		t.Run(name, func(t *testing.T) {
			_, findings := judge(t, c.body)
			if !slices.ContainsFunc(findings, func(f string) bool { return strings.Contains(f, c.want) }) {
				t.Fatalf("want a finding with %q, got %q", c.want, findings)
			}
		})
	}

	if census, findings := judgeChartFlags(nil); census.files != 0 || len(findings) != 0 {
		t.Fatalf("an empty walk is a census of zero, not a finding: %+v %q", census, findings)
	}
}
