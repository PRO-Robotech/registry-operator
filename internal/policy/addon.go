// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/PRO-Robotech/registry-operator/internal/kinds"
)

// ProjectMode — как выводится проект Argo CD Addon заказа.
type ProjectMode string

const (
	// ProjectTenant — проект на арендатора: <prefix><login>.
	ProjectTenant ProjectMode = "Tenant"
	// ProjectFixed — один проект площадки.
	ProjectFixed ProjectMode = "Fixed"
)

// ProjectPolicy — правило проекта Argo CD.
type ProjectPolicy struct {
	Mode   ProjectMode `json:"mode"`
	Prefix string      `json:"prefix,omitempty"`
	Name   string      `json:"name,omitempty"`
}

// ChartPin — разрешённый пин чарта: источник, версия и digest.
type ChartPin struct {
	RepoURL string `json:"repoURL"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// SyncOptionsPolicy — разрешённые и обязательные опции синхронизации.
type SyncOptionsPolicy struct {
	Allowed  []string `json:"allowed"`
	Required []string `json:"required,omitempty"`
}

// IgnoreDifference — правило игнорирования расхождений.
type IgnoreDifference struct {
	Group                 string   `json:"group,omitempty"`
	Kind                  string   `json:"kind"`
	Name                  string   `json:"name,omitempty"`
	Namespace             string   `json:"namespace,omitempty"`
	JSONPointers          []string `json:"jsonPointers,omitempty"`
	JQPathExpressions     []string `json:"jqPathExpressions,omitempty"`
	ManagedFieldsManagers []string `json:"managedFieldsManagers,omitempty"`
}

// AddonPolicy — форма Addon заказа (VAP V10).
type AddonPolicy struct {
	ArgoCDNamespace   string            `json:"argocdNamespace"`
	TargetCluster     string            `json:"targetCluster"`
	Project           ProjectPolicy     `json:"project"`
	Charts            []ChartPin        `json:"charts"`
	PluginNames       []string          `json:"pluginNames,omitempty"`
	SyncOptions       SyncOptionsPolicy `json:"syncOptions"`
	MaxRetryLimit     int64             `json:"maxRetryLimit"`
	ValuesAddonPrefix string            `json:"valuesAddonPrefix"`
	// TemplateVariables — переменные, которые шаблон Addon обязан
	// отрисовать сверх переменных заказа (пути бакета и Vault из статусов
	// готовых шагов). Перечень закрыт: Addon несёт ровно переменные заказа
	// и эти — контракт со слоем заказа платформы.
	TemplateVariables        []string           `json:"templateVariables"`
	MaxValuesSelectors       int                `json:"maxValuesSelectors,omitempty"`
	AllowedIgnoreDifferences []IgnoreDifference `json:"allowedIgnoreDifferences,omitempty"`
}

var (
	extraVarKey  = regexp.MustCompile(`^[a-z][A-Za-z0-9]{0,62}$`)
	extraVarVal  = regexp.MustCompile(`^[A-Za-z0-9._:/@=+,-]{0,1024}$`)
	selectorName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	chartVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`)
)

const (
	// LabelAddonValueAddon — метка AddonValue, по которой Addon выбирает слои.
	LabelAddonValueAddon = "addons.in-cloud.io/addon"
	// LabelAddonValueValues — метка вида слоя AddonValue.
	LabelAddonValueValues = "addons.in-cloud.io/values"
	// MaxVariables — предел числа переменных Addon заказа.
	MaxVariables = 32
	// defaultMaxValuesSelectors — предел числа селекторов по умолчанию.
	defaultMaxValuesSelectors = 16
)

// Validate проверяет параметры политики Addon; addonPrefix — префикс имён
// Addon заказов, с которым слои платформы не пересекаются.
func (c *AddonPolicy) Validate(addonPrefix string) error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if !dnsLabel.MatchString(c.ArgoCDNamespace) {
		add("argocdNamespace %q is not a DNS label", c.ArgoCDNamespace)
	}
	if c.TargetCluster == "" {
		add("targetCluster is required")
	}
	switch c.Project.Mode {
	case ProjectTenant:
		if c.Project.Prefix == "" || !dnsLabel.MatchString(strings.TrimSuffix(c.Project.Prefix, "-")) || c.Project.Name != "" {
			add("project mode Tenant needs a DNS-label prefix and no name")
		}
	case ProjectFixed:
		if !dnsLabel.MatchString(c.Project.Name) || c.Project.Prefix != "" {
			add("project mode Fixed needs a DNS-label name and no prefix")
		}
	default:
		add("project.mode %q is neither Tenant nor Fixed", c.Project.Mode)
	}
	if len(c.Charts) == 0 {
		add("charts must allow at least one chart pin")
	}
	for i, ch := range c.Charts {
		if !ociRepo.MatchString(ch.RepoURL) || !dnsLabel.MatchString(ch.Name) ||
			!chartVersion.MatchString(ch.Version) || !digestRe.MatchString(ch.Digest) {
			add("charts[%d]: repoURL oci://host/path, name, version and sha256 digest are required", i)
		}
	}
	if len(c.SyncOptions.Allowed) == 0 {
		add("syncOptions.allowed must not be empty")
	}
	for _, r := range c.SyncOptions.Required {
		if !slices.Contains(c.SyncOptions.Allowed, r) {
			add("syncOptions.required %q is not in syncOptions.allowed", r)
		}
	}
	for _, o := range c.SyncOptions.Allowed {
		if strings.HasPrefix(o, "CreateNamespace") {
			add("syncOptions.allowed must not allow CreateNamespace")
		}
	}
	if c.MaxRetryLimit < 1 || c.MaxRetryLimit > 100 {
		add("maxRetryLimit %d is out of 1..100", c.MaxRetryLimit)
	}
	if c.ValuesAddonPrefix == "" || !dnsLabel.MatchString(strings.TrimSuffix(c.ValuesAddonPrefix, "-")) {
		add("valuesAddonPrefix must be a DNS-label prefix")
	}
	if strings.HasPrefix(c.ValuesAddonPrefix, addonPrefix) || strings.HasPrefix(addonPrefix, c.ValuesAddonPrefix) {
		add("valuesAddonPrefix %q must not overlap the order Addon names %s*", c.ValuesAddonPrefix, addonPrefix)
	}
	if c.MaxValuesSelectors < 0 || c.MaxValuesSelectors > 64 {
		add("maxValuesSelectors %d is out of 0..64", c.MaxValuesSelectors)
	}
	if len(c.TemplateVariables)+len(OrderVariables) > MaxVariables {
		add("templateVariables: at most %d variables together with the order variables", MaxVariables)
	}
	seen := map[string]bool{}
	for _, k := range c.TemplateVariables {
		switch {
		case !extraVarKey.MatchString(k):
			add("templateVariables: %q is not a variable name", k)
		case slices.Contains(OrderVariables, k):
			add("templateVariables: %q is an order variable set by the operator", k)
		case seen[k]:
			add("templateVariables: %q is listed twice", k)
		}
		seen[k] = true
	}
	return errors.Join(errs...)
}

// ExpectedProject — проект Argo CD Addon заказа в данном namespace.
func (c *AddonPolicy) ExpectedProject(login string) string {
	if c.Project.Mode == ProjectTenant {
		return c.Project.Prefix + login
	}
	return c.Project.Name
}

// ChartAllowed — пин чарта входит в перечень площадки.
func (c *AddonPolicy) ChartAllowed(ch Chart) bool {
	return slices.Contains(c.Charts, ChartPin(ch))
}

// addonSpec — зеркало spec Addon addons-operator в объёме разрешённых полей.
type addonSpec struct {
	Chart           string            `json:"chart"`
	RepoURL         string            `json:"repoURL"`
	Version         string            `json:"version"`
	TargetCluster   string            `json:"targetCluster"`
	TargetNamespace string            `json:"targetNamespace"`
	Backend         addonBackend      `json:"backend"`
	ValuesSelectors []valuesSelector  `json:"valuesSelectors,omitempty"`
	Variables       map[string]string `json:"variables,omitempty"`
	PluginName      string            `json:"pluginName,omitempty"`
	ReleaseName     string            `json:"releaseName"`
}

type addonBackend struct {
	Type              string             `json:"type"`
	Namespace         string             `json:"namespace"`
	Project           string             `json:"project,omitempty"`
	Finalizer         *bool              `json:"finalizer,omitempty"`
	SyncPolicy        *syncPolicy        `json:"syncPolicy,omitempty"`
	IgnoreDifferences []IgnoreDifference `json:"ignoreDifferences,omitempty"`
}

type syncPolicy struct {
	Automated   *automated `json:"automated,omitempty"`
	SyncOptions []string   `json:"syncOptions,omitempty"`
	Retry       *retry     `json:"retry,omitempty"`
}

type automated struct {
	Prune      *bool `json:"prune,omitempty"`
	SelfHeal   *bool `json:"selfHeal,omitempty"`
	AllowEmpty *bool `json:"allowEmpty,omitempty"`
}

type retry struct {
	Limit   *int64   `json:"limit,omitempty"`
	Backoff *backoff `json:"backoff,omitempty"`
}

type backoff struct {
	Duration    string `json:"duration,omitempty"`
	Factor      *int64 `json:"factor,omitempty"`
	MaxDuration string `json:"maxDuration,omitempty"`
}

type valuesSelector struct {
	Name        string            `json:"name"`
	Priority    int               `json:"priority"`
	MatchLabels map[string]string `json:"matchLabels"`
}

func (c *Config) checkAddon(v *Violations, o Order, u *unstructured.Unstructured, spec map[string]any) {
	if want := c.AddonName(o.ID); u.GetName() != want || u.GetNamespace() != "" {
		v.add("metadata.name", "must be the cluster-scoped %q", want)
	}
	var s addonSpec
	if absorb(v, decodeStrict(kinds.Addon.GVK.Kind, spec, &s)) {
		return
	}
	p := &c.Addon
	if s.RepoURL != o.Chart.RepoURL || s.Chart != o.Chart.Name || s.Version != o.Chart.Version {
		v.add("spec.chart", "chart, repoURL and version must come from the chart pin")
	}
	if !p.ChartAllowed(o.Chart) {
		v.add("spec.chart", "chart pin %s/%s %s %s is not an allowed platform pin", o.Chart.RepoURL, o.Chart.Name, o.Chart.Version, o.Chart.Digest)
	}
	if s.TargetCluster != p.TargetCluster {
		v.add("spec.targetCluster", "must be %q", p.TargetCluster)
	}
	if s.TargetNamespace != o.Login {
		v.add("spec.targetNamespace", "must be the order namespace %q", o.Login)
	}
	if want := c.ResourceName(o.ID, ""); s.ReleaseName != want {
		v.add("spec.releaseName", "must be %q", want)
	}
	if len(p.PluginNames) == 0 {
		if s.PluginName != "" {
			v.add("spec.pluginName", "plugins are not allowed")
		}
	} else if !slices.Contains(p.PluginNames, s.PluginName) {
		v.add("spec.pluginName", "must be one of %v", p.PluginNames)
	}
	p.checkBackend(v, &s.Backend, o)
	p.checkSelectors(v, s.ValuesSelectors)
	checkVariables(v, s.Variables, o, p.TemplateVariables)
}

func (c *AddonPolicy) checkBackend(v *Violations, b *addonBackend, o Order) {
	if b.Type != "argocd" {
		v.add("spec.backend.type", "must be argocd")
	}
	if b.Namespace != c.ArgoCDNamespace {
		v.add("spec.backend.namespace", "must be %q", c.ArgoCDNamespace)
	}
	if want := c.ExpectedProject(o.Login); b.Project != want {
		v.add("spec.backend.project", "must be %q", want)
	}
	if b.Finalizer == nil || !*b.Finalizer {
		v.add("spec.backend.finalizer", "must be true: workload resources are removed by the Argo CD cascade")
	}
	if !ignoreDiffsAllowed(b.IgnoreDifferences, c.AllowedIgnoreDifferences) {
		v.add("spec.backend.ignoreDifferences", "must be a subset of the allowed rules")
	}
	sp := b.SyncPolicy
	if sp == nil {
		v.add("spec.backend.syncPolicy", "is required")
		return
	}
	if sp.Automated == nil {
		v.add("spec.backend.syncPolicy.automated", "is required")
	} else {
		if sp.Automated.Prune == nil || !*sp.Automated.Prune {
			v.add("spec.backend.syncPolicy.automated.prune", "must be true")
		}
		if sp.Automated.SelfHeal == nil || !*sp.Automated.SelfHeal {
			v.add("spec.backend.syncPolicy.automated.selfHeal", "must be true")
		}
		if sp.Automated.AllowEmpty != nil && *sp.Automated.AllowEmpty {
			v.add("spec.backend.syncPolicy.automated.allowEmpty", "must be false: an empty render must not prune the workload")
		}
	}
	for _, opt := range sp.SyncOptions {
		if !slices.Contains(c.SyncOptions.Allowed, opt) {
			v.add("spec.backend.syncPolicy.syncOptions", "option %q is not allowed", opt)
		}
	}
	for _, req := range c.SyncOptions.Required {
		if !slices.Contains(sp.SyncOptions, req) {
			v.add("spec.backend.syncPolicy.syncOptions", "option %q is required", req)
		}
	}
	if sp.Retry == nil || sp.Retry.Limit == nil {
		v.add("spec.backend.syncPolicy.retry.limit", "is required")
	} else if *sp.Retry.Limit < 1 || *sp.Retry.Limit > c.MaxRetryLimit {
		v.add("spec.backend.syncPolicy.retry.limit", "must be within 1..%d", c.MaxRetryLimit)
	}
}

func ignoreDiffsAllowed(got, allowed []IgnoreDifference) bool {
	for _, g := range got {
		ok := false
		for _, a := range allowed {
			if g.Group == a.Group && g.Kind == a.Kind && g.Name == a.Name && g.Namespace == a.Namespace &&
				slices.Equal(g.JSONPointers, a.JSONPointers) && slices.Equal(g.JQPathExpressions, a.JQPathExpressions) &&
				slices.Equal(g.ManagedFieldsManagers, a.ManagedFieldsManagers) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func (c *AddonPolicy) checkSelectors(v *Violations, sels []valuesSelector) {
	limit := c.MaxValuesSelectors
	if limit == 0 {
		limit = defaultMaxValuesSelectors
	}
	if len(sels) == 0 {
		v.add("spec.valuesSelectors", "must select at least one platform values layer")
	}
	if len(sels) > limit {
		v.add("spec.valuesSelectors", "at most %d selectors are allowed", limit)
	}
	seen := map[string]bool{}
	for i, s := range sels {
		f := fmt.Sprintf("spec.valuesSelectors[%d]", i)
		if !selectorName.MatchString(s.Name) {
			v.add(f+".name", "must be a DNS label")
		}
		if seen[s.Name] {
			v.add(f+".name", "duplicate selector name %q", s.Name)
		}
		seen[s.Name] = true
		if s.Priority < 0 || s.Priority > 100 {
			v.add(f+".priority", "must be within 0..100")
		}
		addon, ok := s.MatchLabels[LabelAddonValueAddon]
		if !ok || !strings.HasPrefix(addon, c.ValuesAddonPrefix) {
			v.add(f+".matchLabels", "must select %s=%s* (platform layers only)", LabelAddonValueAddon, c.ValuesAddonPrefix)
		}
		for k := range s.MatchLabels {
			if k != LabelAddonValueAddon && k != LabelAddonValueValues {
				v.add(f+".matchLabels", "label %q is not allowed", k)
			}
		}
	}
}

func checkVariables(v *Violations, vars map[string]string, o Order, templateVars []string) {
	want := o.OrderVariableValues()
	for _, k := range OrderVariables {
		got, ok := vars[k]
		switch {
		case !ok:
			v.add("spec.variables."+k, "is required")
		case got != want[k]:
			v.add("spec.variables."+k, "must equal the order value %q", want[k])
		}
	}
	for _, k := range templateVars {
		if _, ok := vars[k]; !ok {
			v.add("spec.variables."+k, "is required by the platform order layer")
		}
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if _, order := want[k]; order {
			continue
		}
		if !slices.Contains(templateVars, k) {
			v.add("spec.variables."+k, "is not a variable of the platform order layer")
			continue
		}
		// Значения подставляются в шаблоны AddonValue платформы, которые
		// addons-operator исполняет с полным sprig: ни {{, ни <path: (третий
		// движок подстановки плагина) в значение не попадает по построению.
		if val := vars[k]; !extraVarVal.MatchString(val) {
			v.add("spec.variables."+k, "value must match %s", extraVarVal.String())
		}
	}
}

// RenderedVariablesFree — шаблон не пишет переменные заказа: их ставит
// оператор.
func RenderedVariablesFree(rendered map[string]any) error {
	vars, ok := rendered["variables"].(map[string]any)
	if !ok {
		return nil
	}
	v := &Violations{Kind: kinds.Addon.GVK.Kind}
	for _, k := range OrderVariables {
		if _, set := vars[k]; set {
			v.add("spec.variables."+k, "set by the operator from the order")
		}
	}
	return v.err()
}
