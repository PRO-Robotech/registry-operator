// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/PRO-Robotech/registry-operator/internal/kinds"
)

// Chart — закреплённый чарт Addon.
type Chart struct {
	RepoURL string
	Name    string
	Version string
	Digest  string
}

// Order — то, что выведено из заказа и сверяется с объектом точно.
type Order struct {
	ID    string
	Login string
	UID   string
	// SyncEpoch — эпоха синхронизации рабочей нагрузки.
	SyncEpoch int64
	// ServiceAccountName — имя iam ServiceAccount заказа (субъект выдачи).
	ServiceAccountName string
	// RouteNamespace — namespace маршрута заказа.
	RouteNamespace string
	// Public — анонимное чтение регистри.
	Public bool
	// Chart — пин чарта Addon.
	Chart Chart
}

// Переменные Addon, которые оператор выводит из заказа. Это контракт с
// объектами платформы: AddonValue платформы подставляют их как
// {{ .Variables.<имя> }}. Шаблон их не пишет, значения сверяются с заказом
// точно.
const (
	VarID          = "id"
	VarLogin       = "login"
	VarRegistryUID = "registryUID"
	VarSyncEpoch   = "syncEpoch"
	VarPublic      = "public"
)

// OrderVariables — перечень переменных заказа.
var OrderVariables = []string{VarID, VarLogin, VarRegistryUID, VarSyncEpoch, VarPublic}

// OrderVariableValues — значения переменных заказа.
func (o Order) OrderVariableValues() map[string]string {
	return map[string]string{
		VarID:          o.ID,
		VarLogin:       o.Login,
		VarRegistryUID: o.UID,
		VarSyncEpoch:   strconv.FormatInt(o.SyncEpoch, 10),
		VarPublic:      strconv.FormatBool(o.Public),
	}
}

// operatorKeys — ключи spec, которые ставит только оператор, по виду.
var operatorKeys = map[string]map[string]string{
	kinds.S3BucketClaim.GVK.Kind: {
		"deletionPolicy": "set by the operator: Retain, Purge only while the order is deleted",
	},
	kinds.VaultClaim.GVK.Kind: {
		"deletionPolicy": "set by the operator: Retain",
	},
	kinds.IamServiceAccount.GVK.Kind: {
		"name":           "set by the operator: the object name",
		"deletionPolicy": "set by the operator: Delete",
	},
	kinds.IamAccessBinding.GVK.Kind: {
		"deletionPolicy": "set by the operator: Delete",
	},
	kinds.Addon.GVK.Kind: {
		"chart":            "set by the operator from the chart pin",
		"repoURL":          "set by the operator from the chart pin",
		"version":          "set by the operator from the chart pin",
		"releaseName":      "set by the operator: the workload name",
		"targetNamespace":  "set by the operator: the order namespace",
		"path":             "git sources are not allowed",
		"valuesSources":    "values sources read cluster objects and are not allowed",
		"initDependencies": "init dependencies are not allowed",
	},
	kinds.HTTPRoute.GVK.Kind: {},
}

// CheckRendered — отрисованный spec не пишет ключей оператора.
func CheckRendered(k kinds.Kind, rendered map[string]any) error {
	keys, ok := operatorKeys[k.GVK.Kind]
	if !ok {
		return &Violations{Kind: k.GVK.Kind, Items: []Violation{{Field: "kind", Reason: "kind is not rendered from a template"}}}
	}
	return refuseOperatorKeys(k.GVK.Kind, rendered, keys)
}

// Check проверяет итоговый объект (после полей оператора) против
// политики. Возвращает все нарушения разом (*Violations) либо nil.
func (c *Config) Check(k kinds.Kind, o Order, u *unstructured.Unstructured) error {
	v := &Violations{Kind: k.GVK.Kind}
	if u.GetAPIVersion() != k.GVK.GroupVersion().String() || u.GetKind() != k.GVK.Kind {
		v.add("kind", "object is %s %s, the step writes %s", u.GetAPIVersion(), u.GetKind(), k.GVK)
		return v.err()
	}
	spec, _, err := unstructured.NestedMap(u.Object, "spec")
	if err != nil || spec == nil {
		v.add("spec", "is required")
		return v.err()
	}
	switch k.GVK {
	case kinds.S3BucketClaim.GVK:
		c.checkStorage(v, o, u, spec)
	case kinds.VaultClaim.GVK:
		c.checkSecrets(v, o, u, spec)
	case kinds.IamServiceAccount.GVK:
		c.checkServiceAccount(v, o, u, spec)
	case kinds.IamAccessBinding.GVK:
		c.checkAccessBinding(v, o, u, spec)
	case kinds.Addon.GVK:
		c.checkAddon(v, o, u, spec)
	case kinds.HTTPRoute.GVK:
		c.checkRoute(v, o, u, spec)
	default:
		v.add("kind", "kind %s is not written by the operator", k.GVK)
	}
	return v.err()
}

// absorb переносит нарушение строгого разбора в общий список.
func absorb(v *Violations, err error) bool {
	if err == nil {
		return false
	}
	var vs *Violations
	if errors.As(err, &vs) {
		v.Items = append(v.Items, vs.Items...)
		return true
	}
	v.add("spec", "%v", err)
	return true
}

func (c *Config) checkName(v *Violations, u *unstructured.Unstructured, wantName, wantNS string) {
	if u.GetName() != wantName {
		v.add("metadata.name", "must be %q", wantName)
	}
	if u.GetNamespace() != wantNS {
		v.add("metadata.namespace", "must be %q", wantNS)
	}
}

type nameRef struct {
	Name string `json:"name"`
}

// ---------------------------------------------------------------- storage

type s3Spec struct {
	VaultConfigRef nameRef  `json:"vaultConfigRef"`
	ClusterRef     nameRef  `json:"clusterRef"`
	CustomerLogin  string   `json:"customerLogin"`
	Bucket         s3Bucket `json:"bucket"`
	Region         string   `json:"region,omitempty"`
	Vault          s3Vault  `json:"vault"`
	DeletionPolicy string   `json:"deletionPolicy,omitempty"`
}

type s3Bucket struct {
	ManagedBy string `json:"managedBy,omitempty"`
	Public    *bool  `json:"public,omitempty"`
}

type s3Vault struct {
	SecretsPrefix   string `json:"secretsPrefix"`
	DestinationPath string `json:"destinationPath"`
}

func (c *Config) checkStorage(v *Violations, o Order, u *unstructured.Unstructured, spec map[string]any) {
	name := c.ResourceName(o.ID, "")
	c.checkName(v, u, name, o.Login)
	var s s3Spec
	if absorb(v, decodeStrict(kinds.S3BucketClaim.GVK.Kind, spec, &s)) {
		return
	}
	p := c.Storage
	if s.VaultConfigRef.Name != p.VaultConfig {
		v.add("spec.vaultConfigRef.name", "must be %q", p.VaultConfig)
	}
	if s.ClusterRef.Name != name {
		v.add("spec.clusterRef.name", "must be the claim name %q", name)
	}
	if s.CustomerLogin != o.Login {
		v.add("spec.customerLogin", "must be the order namespace %q", o.Login)
	}
	if s.Bucket.ManagedBy != "" && s.Bucket.ManagedBy != "SYSTEM" {
		v.add("spec.bucket.managedBy", "must be SYSTEM")
	}
	if s.Bucket.Public != nil && *s.Bucket.Public {
		v.add("spec.bucket.public", "must be false")
	}
	if s.Region != "" && !slices.Contains(p.Regions, s.Region) {
		v.add("spec.region", "must be one of %v", p.Regions)
	}
	if s.Vault.SecretsPrefix != p.SecretsPrefix {
		v.add("spec.vault.secretsPrefix", "must be %q", p.SecretsPrefix)
	}
	if want := c.DestinationPath(o.Login, o.ID); s.Vault.DestinationPath != want {
		v.add("spec.vault.destinationPath", "must be %q", want)
	}
	if s.DeletionPolicy != "Retain" {
		v.add("spec.deletionPolicy", "must be Retain: Purge is set only while the order is deleted")
	}
}

// ---------------------------------------------------------------- secrets

type vcSpec struct {
	VaultConfigRef nameRef    `json:"vaultConfigRef"`
	SecretsPrefix  string     `json:"secretsPrefix"`
	Auth           vcAuth     `json:"auth"`
	Roles          []vcRole   `json:"roles"`
	Policies       []vcPolicy `json:"policies,omitempty"`
	DeletionPolicy string     `json:"deletionPolicy,omitempty"`
}

type vcAuth struct {
	MountPath    string `json:"mountPath"`
	ManageConfig *bool  `json:"manageConfig,omitempty"`
	AutoCreate   *bool  `json:"autoCreate,omitempty"`
	RolePruning  string `json:"rolePruning,omitempty"`
}

type vcRole struct {
	Name                 string   `json:"name"`
	BoundServiceAccounts vcBound  `json:"boundServiceAccounts"`
	Policies             []string `json:"policies"`
	Audience             string   `json:"audience,omitempty"`
	TokenTTL             string   `json:"tokenTTL,omitempty"`
	TokenMaxTTL          string   `json:"tokenMaxTTL,omitempty"`
	TokenExplicitMaxTTL  string   `json:"tokenExplicitMaxTTL,omitempty"`
	TokenType            string   `json:"tokenType,omitempty"`
	TokenNoDefaultPolicy *bool    `json:"tokenNoDefaultPolicy,omitempty"`
	AliasNameSource      string   `json:"aliasNameSource,omitempty"`
}

// vcBound — привязка роли к ServiceAccount: только форма names[] гостевого
// режима и ровно одна учётка zot заказа.
type vcBound struct {
	Names     []string `json:"names"`
	Namespace string   `json:"namespace"`
}

// vcPolicy — собственная политика заказа: только identityPaths на лист
// своей учётки, без произвольного HCL.
type vcPolicy struct {
	Name          string           `json:"name"`
	Rules         *string          `json:"rules,omitempty"`
	IdentityPaths []vcIdentityPath `json:"identityPaths,omitempty"`
}

type vcIdentityPath struct {
	Path         string   `json:"path"`
	Capabilities []string `json:"capabilities"`
}

func (c *Config) checkSecrets(v *Violations, o Order, u *unstructured.Unstructured, spec map[string]any) {
	name := c.ResourceName(o.ID, "")
	c.checkName(v, u, name, o.Login)
	var s vcSpec
	if absorb(v, decodeStrict(kinds.VaultClaim.GVK.Kind, spec, &s)) {
		return
	}
	p := c.Secrets
	if s.VaultConfigRef.Name != p.VaultConfig {
		v.add("spec.vaultConfigRef.name", "must be %q", p.VaultConfig)
	}
	if s.SecretsPrefix != p.SecretsPrefix {
		v.add("spec.secretsPrefix", "must be %q", p.SecretsPrefix)
	}
	if s.Auth.MountPath != p.AuthMount {
		v.add("spec.auth.mountPath", "must be the platform mount %q", p.AuthMount)
	}
	if s.Auth.ManageConfig == nil || *s.Auth.ManageConfig {
		v.add("spec.auth.manageConfig", "must be false: the order is a guest on the platform mount")
	}
	if s.Auth.AutoCreate == nil || *s.Auth.AutoCreate {
		v.add("spec.auth.autoCreate", "must be false")
	}
	if s.Auth.RolePruning != "Owned" {
		v.add("spec.auth.rolePruning", "must be Owned: the order removes only its own role")
	}
	own := map[string]bool{}
	leaf := c.LeafIdentityPath()
	for i, pol := range s.Policies {
		f := fmt.Sprintf("spec.policies[%d]", i)
		if !strings.HasPrefix(pol.Name, name+"-") || !dnsLabel.MatchString(strings.TrimPrefix(pol.Name, name+"-")) {
			v.add(f+".name", "must be %q followed by a DNS label", name+"-")
		}
		own[pol.Name] = true
		if pol.Rules != nil {
			v.add(f+".rules", "must be absent: an order policy grants only its own leaf through identityPaths")
		}
		if len(pol.IdentityPaths) != 1 {
			v.add(f+".identityPaths", "must hold exactly the leaf of the workload service account")
		}
		for j, ip := range pol.IdentityPaths {
			if ip.Path != leaf {
				v.add(fmt.Sprintf("%s.identityPaths[%d].path", f, j), "must be %q", leaf)
			}
			if !slices.Equal(ip.Capabilities, []string{"read"}) {
				v.add(fmt.Sprintf("%s.identityPaths[%d].capabilities", f, j), "must be exactly [read]")
			}
		}
	}
	if len(s.Roles) != 1 {
		v.add("spec.roles", "must hold exactly one role")
	}
	for i, r := range s.Roles {
		f := fmt.Sprintf("spec.roles[%d]", i)
		if r.Name != name {
			v.add(f+".name", "must be %q", name)
		}
		b := r.BoundServiceAccounts
		if len(b.Names) != 1 || b.Names[0] != name {
			v.add(f+".boundServiceAccounts.names", "must bind exactly the workload service account %q", name)
		}
		if b.Namespace != o.Login {
			v.add(f+".boundServiceAccounts.namespace", "must be the order namespace %q", o.Login)
		}
		if r.Audience != p.Audience {
			v.add(f+".audience", "must be %q: a role without an audience accepts any token of the account", p.Audience)
		}
		namesOwn := false
		for _, pol := range r.Policies {
			switch {
			case own[pol]:
				namesOwn = true
			case slices.Contains(p.RolePolicies, pol):
			default:
				v.add(f+".policies", "policy %q is neither the order leaf policy nor a platform role policy", pol)
			}
		}
		if !namesOwn {
			v.add(f+".policies", "must name the order leaf policy")
		}
		if r.TokenType != "" && r.TokenType != "service" {
			v.add(f+".tokenType", "must be service")
		}
	}
	if s.DeletionPolicy != "Retain" {
		v.add("spec.deletionPolicy", "must be Retain: a guest never disables the platform mount")
	}
}

// ---------------------------------------------------------------- identity

type saSpec struct {
	Name           string            `json:"name"`
	Description    string            `json:"description,omitempty"`
	Enabled        *bool             `json:"enabled,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	Keys           []saKey           `json:"keys,omitempty"`
	DeletionPolicy string            `json:"deletionPolicy,omitempty"`
}

type saKey struct {
	Name       string      `json:"name"`
	Kind       string      `json:"kind,omitempty"`
	TTLSeconds *int64      `json:"ttlSeconds,omitempty"`
	Audience   []string    `json:"audience,omitempty"`
	Rotation   *saRotation `json:"rotation,omitempty"`
	Delivery   saDelivery  `json:"delivery"`
}

type saRotation struct {
	RenewBeforeSeconds *int64 `json:"renewBeforeSeconds,omitempty"`
	OverlapSeconds     *int64 `json:"overlapSeconds,omitempty"`
}

type saDelivery struct {
	Vault saVault `json:"vault"`
}

type saVault struct {
	App      string `json:"app"`
	Instance string `json:"instance"`
}

func (c *Config) checkServiceAccount(v *Violations, o Order, u *unstructured.Unstructured, spec map[string]any) {
	c.checkName(v, u, o.ServiceAccountName, o.Login)
	var s saSpec
	if absorb(v, decodeStrict(kinds.IamServiceAccount.GVK.Kind, spec, &s)) {
		return
	}
	if s.Name != u.GetName() {
		v.add("spec.name", "must be the object name %q", u.GetName())
	}
	instance := c.ResourceName(o.ID, "")
	for i, k := range s.Keys {
		f := fmt.Sprintf("spec.keys[%d]", i)
		if k.Kind != "" && k.Kind != "Secret" {
			v.add(f+".kind", "must be Secret")
		}
		if k.TTLSeconds != nil && *k.TTLSeconds > c.Identity.MaxKeyTTLSeconds {
			v.add(f+".ttlSeconds", "must not exceed %d", c.Identity.MaxKeyTTLSeconds)
		}
		if k.Delivery.Vault.App != c.Identity.KeyDeliveryApp {
			v.add(f+".delivery.vault.app", "must be %q", c.Identity.KeyDeliveryApp)
		}
		if k.Delivery.Vault.Instance != instance {
			v.add(f+".delivery.vault.instance", "must be the registry instance %q", instance)
		}
	}
	if s.DeletionPolicy != "Delete" {
		v.add("spec.deletionPolicy", "must be Delete: a deleted order must not leave a live robot key")
	}
}

type abSpec struct {
	Subjects       []abSubject       `json:"subjects"`
	RoleRef        nameRef           `json:"roleRef"`
	Scope          *abScope          `json:"scope,omitempty"`
	Target         abTarget          `json:"target"`
	Labels         map[string]string `json:"labels,omitempty"`
	DeletionPolicy string            `json:"deletionPolicy,omitempty"`
}

// abSubject — субъект выдачи: только учётка заказа либо системный класс;
// пользователей и групп заказ не называет (их нет в зеркале).
type abSubject struct {
	ServiceAccountRef *nameRef `json:"serviceAccountRef,omitempty"`
	SystemClass       *nameRef `json:"systemClass,omitempty"`
}

type abScope struct {
	Type string `json:"type,omitempty"`
}

type abTarget struct {
	Resources []abResource `json:"resources"`
}

type abResource struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (c *Config) checkAccessBinding(v *Violations, o Order, u *unstructured.Unstructured, spec map[string]any) {
	if u.GetNamespace() != o.Login {
		v.add("metadata.namespace", "must be %q", o.Login)
	}
	var s abSpec
	if absorb(v, decodeStrict(kinds.IamAccessBinding.GVK.Kind, spec, &s)) {
		return
	}
	p := c.Identity
	if len(s.Subjects) == 0 || len(s.Subjects) > 2 {
		v.add("spec.subjects", "must hold one or two subjects")
	}
	for i, sub := range s.Subjects {
		f := fmt.Sprintf("spec.subjects[%d]", i)
		switch {
		case sub.SystemClass != nil:
			// Системный класс — подстановка «любой». Публичность регистри —
			// один механизм, флаг --public прокси из spec.public; выдача
			// anonymous или authenticated была бы вторым, и authenticated
			// открыл бы частный регистри каждому вошедшему субъекту.
			v.add(f, "a system class is never granted: a public registry is served by the proxy flag from spec.public")
		case sub.ServiceAccountRef == nil:
			v.add(f, "must name the order service account")
		case sub.ServiceAccountRef.Name != o.ServiceAccountName:
			v.add(f+".serviceAccountRef.name", "must be the order service account %q", o.ServiceAccountName)
		}
	}
	if !slices.Contains(p.AllowedRoles, s.RoleRef.Name) {
		v.add("spec.roleRef.name", "must be one of %v", p.AllowedRoles)
	}
	if s.Scope != nil && s.Scope.Type != "" && s.Scope.Type != "iam.project" {
		v.add("spec.scope.type", "must be iam.project")
	}
	if len(s.Target.Resources) != 1 || s.Target.Resources[0].Type != p.TargetType || s.Target.Resources[0].ID != o.ID {
		v.add("spec.target.resources", "must be exactly [%s:%s]", p.TargetType, o.ID)
	}
	if s.DeletionPolicy != "Delete" {
		v.add("spec.deletionPolicy", "must be Delete: a deleted order revokes its grants")
	}
}

// ---------------------------------------------------------------- route

type routeSpec struct {
	ParentRefs []routeParent `json:"parentRefs"`
	Hostnames  []string      `json:"hostnames"`
	Rules      []routeRule   `json:"rules"`
}

type routeParent struct {
	Group       *string `json:"group,omitempty"`
	Kind        *string `json:"kind,omitempty"`
	Namespace   *string `json:"namespace,omitempty"`
	Name        string  `json:"name"`
	SectionName *string `json:"sectionName,omitempty"`
}

type routeRule struct {
	Name        *string          `json:"name,omitempty"`
	Matches     []routeMatch     `json:"matches"`
	BackendRefs []routeBackend   `json:"backendRefs"`
	Timeouts    *routeTimeouts   `json:"timeouts,omitempty"`
	Filters     []map[string]any `json:"filters,omitempty"`
}

type routeTimeouts struct {
	Request        string `json:"request,omitempty"`
	BackendRequest string `json:"backendRequest,omitempty"`
}

type routeMatch struct {
	Path *routePath `json:"path"`
}

type routePath struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type routeBackend struct {
	Group     *string `json:"group,omitempty"`
	Kind      *string `json:"kind,omitempty"`
	Name      string  `json:"name"`
	Namespace *string `json:"namespace,omitempty"`
	Port      *int32  `json:"port,omitempty"`
	Weight    *int32  `json:"weight,omitempty"`
}

func deref(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

func (c *Config) routeNamespaceAllowed(ns, login string) bool {
	return slices.Contains(c.Route.Namespaces, ns) || (c.Route.AllowOrderNamespace && ns == login)
}

func (c *Config) checkRoute(v *Violations, o Order, u *unstructured.Unstructured, spec map[string]any) {
	name := c.ResourceName(o.ID, "")
	ns := u.GetNamespace()
	if u.GetName() != name {
		v.add("metadata.name", "must be %q", name)
	}
	if ns != o.RouteNamespace || !c.routeNamespaceAllowed(ns, o.Login) {
		v.add("metadata.namespace", "namespace %q is not a platform route namespace", ns)
	}
	var s routeSpec
	if absorb(v, decodeStrict(kinds.HTTPRoute.GVK.Kind, spec, &s)) {
		return
	}
	p := c.Route
	got := make([]ParentRef, 0, len(s.ParentRefs))
	for _, pr := range s.ParentRefs {
		got = append(got, ParentRef{
			Group: deref(pr.Group, "gateway.networking.k8s.io"), Kind: deref(pr.Kind, "Gateway"),
			Namespace: deref(pr.Namespace, ns), Name: pr.Name, SectionName: deref(pr.SectionName, ""),
		})
	}
	want := make([]ParentRef, 0, len(p.ParentRefs))
	for _, pr := range p.ParentRefs {
		want = append(want, ParentRef{
			Group: orDefault(pr.Group, "gateway.networking.k8s.io"), Kind: orDefault(pr.Kind, "Gateway"),
			Namespace: pr.Namespace, Name: pr.Name, SectionName: pr.SectionName,
		})
	}
	if !slices.Equal(got, want) {
		v.add("spec.parentRefs", "must be exactly the platform gateways %v", want)
	}
	if len(s.Hostnames) == 0 {
		v.add("spec.hostnames", "must name a platform host")
	}
	for _, h := range s.Hostnames {
		if !slices.Contains(p.Hostnames, h) {
			v.add("spec.hostnames", "host %q is not a platform host", h)
		}
	}
	if len(s.Rules) != 1 {
		v.add("spec.rules", "must hold exactly one rule")
		return
	}
	r := s.Rules[0]
	if len(r.Filters) > 0 {
		v.add("spec.rules[0].filters", "must be empty")
	}
	if len(r.Matches) != 1 || r.Matches[0].Path == nil ||
		r.Matches[0].Path.Type != "PathPrefix" || r.Matches[0].Path.Value != RoutePath(o.ID) {
		v.add("spec.rules[0].matches", "must be exactly one PathPrefix %q", RoutePath(o.ID))
	}
	if len(r.BackendRefs) != 1 {
		v.add("spec.rules[0].backendRefs", "must hold exactly one backend")
		return
	}
	b := r.BackendRefs[0]
	if deref(b.Group, "") != "" || deref(b.Kind, "Service") != "Service" || b.Name != name {
		v.add("spec.rules[0].backendRefs[0]", "must be the Service %q", name)
	}
	if bns := deref(b.Namespace, ns); bns != o.Login {
		v.add("spec.rules[0].backendRefs[0].namespace", "must be the order namespace %q", o.Login)
	}
	if b.Port == nil || *b.Port != p.BackendPort {
		v.add("spec.rules[0].backendRefs[0].port", "must be %d", p.BackendPort)
	}
	if b.Weight != nil && *b.Weight < 1 {
		v.add("spec.rules[0].backendRefs[0].weight", "must be positive")
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
