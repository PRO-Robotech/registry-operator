// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package policy — платформенная политика ресурсов заказа: те же проверки,
// что политики допуска (VAP) платформы, исполненные оператором до записи.
// Без VAP на кластере оператор не пишет объект, который VAP бы отвергла.
// Параметры политики — те же значения, что параметры VAP (ставит
// платформа). Форма каждого вида закрыта зеркальной структурой разрешённых
// полей: поле вне неё — нарушение.
package policy

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var (
	dnsLabel     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	dnsSubdomain = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
	vaultPath    = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?(/[a-z0-9]([a-z0-9._-]*[a-z0-9])?)*$`)
	roleName     = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)
	ociRepo      = regexp.MustCompile(`^oci://[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?(/[a-z0-9]([a-z0-9._-]*[a-z0-9])?)+$`)
	digestRe     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	namePrefix   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}-$`)
	targetType   = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,31}$`)
)

// StoragePolicy — форма S3BucketClaim (VAP V14, часть s3bucketclaims).
type StoragePolicy struct {
	// VaultConfig — VaultConfig, через который пишутся ключи бакета.
	VaultConfig string `json:"vaultConfig"`
	// SecretsPrefix — префикс путей Vault площадки (clusters/<кластер>).
	SecretsPrefix string `json:"secretsPrefix"`
	// DestinationPath — путь листа ключей под SecretsPrefix с подстановками
	// {login}, {id} и {name}: namespaces/{login}/apps/docker-registry/{name}.
	DestinationPath string `json:"destinationPath"`
	// Regions — разрешённые регионы; пусто — регион в заказе не задаётся.
	Regions []string `json:"regions,omitempty"`
}

// SecretsPolicy — форма VaultClaim гостевого режима (VAP V14, часть
// vaultclaims).
type SecretsPolicy struct {
	// VaultConfig — VaultConfig, через который пишется роль.
	VaultConfig string `json:"vaultConfig"`
	// SecretsPrefix — префикс путей Vault площадки.
	SecretsPrefix string `json:"secretsPrefix"`
	// AuthMount — существующий mount kubernetes-auth площадки (гость его не
	// включает, не настраивает и не снимает).
	AuthMount string `json:"authMount"`
	// KVMount — mount KV v2, под которым лежат листы ключей бакетов.
	KVMount string `json:"kvMount"`
	// Audience — аудитория projected-токена, которую роль заказа обязана
	// проверять: без неё роль приняла бы любой токен учётки.
	Audience string `json:"audience"`
	// RolePolicies — политики площадки, которые роль заказа вправе называть
	// сверх собственной политики листа (например, PKI площадки). Пусто —
	// роль называет только свою политику.
	RolePolicies []string `json:"rolePolicies,omitempty"`
}

// IdentityPolicy — форма iam ServiceAccount и AccessBinding (VAP V1).
type IdentityPolicy struct {
	// AllowedRoles — роли, которые выдача вправе называть для учётки заказа.
	AllowedRoles []string `json:"allowedRoles"`
	// SystemClassRoles — роли, которые вправе получать системные классы
	// (anonymous, authenticated): только публичное чтение.
	SystemClassRoles []string `json:"systemClassRoles,omitempty"`
	// TargetType — тип ресурса цели выдачи: registry.registries.
	TargetType string `json:"targetType"`
	// KeyDeliveryApp — приложение доставки ключа робота в Vault.
	KeyDeliveryApp string `json:"keyDeliveryApp"`
	// MaxKeyTTLSeconds — наибольший срок ключа робота.
	MaxKeyTTLSeconds int64 `json:"maxKeyTTLSeconds"`
}

// ParentRef — родитель маршрута (Gateway площадки).
type ParentRef struct {
	Group       string `json:"group,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	SectionName string `json:"sectionName,omitempty"`
}

// RoutePolicy — форма HTTPRoute заказа (VAP V3).
type RoutePolicy struct {
	// Namespaces — namespace, в которых вправе жить маршрут заказа.
	Namespaces []string `json:"namespaces,omitempty"`
	// AllowOrderNamespace — маршрут вправе жить в namespace заказа.
	AllowOrderNamespace bool `json:"allowOrderNamespace,omitempty"`
	// Hostnames — имена хостов площадки; маршрут называет непустое
	// подмножество.
	Hostnames []string `json:"hostnames"`
	// ParentRefs — Gateway площадки; маршрут называет ровно их.
	ParentRefs []ParentRef `json:"parentRefs"`
	// BackendPort — порт Service zot.
	BackendPort int32 `json:"backendPort"`
}

// Config — параметры политики. Ставит платформа; те же значения —
// параметры VAP в charts/registry-platform.
type Config struct {
	// ResourceNamePrefix — префикс имён ресурсов заказа: <prefix><id>.
	ResourceNamePrefix string `json:"resourceNamePrefix"`
	// AddonNamePrefix — префикс имени Addon заказа: <prefix><id>.
	AddonNamePrefix string         `json:"addonNamePrefix"`
	Storage         StoragePolicy  `json:"storage"`
	Secrets         SecretsPolicy  `json:"secrets"`
	Identity        IdentityPolicy `json:"identity"`
	Route           RoutePolicy    `json:"route"`
	Addon           AddonPolicy    `json:"addon"`
}

// Validate — fail-closed проверка параметров целиком; неполные — отказ
// старта.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if !namePrefix.MatchString(c.ResourceNamePrefix) {
		add("resourceNamePrefix %q must match %s", c.ResourceNamePrefix, namePrefix)
	}
	if !namePrefix.MatchString(c.AddonNamePrefix) {
		add("addonNamePrefix %q must match %s", c.AddonNamePrefix, namePrefix)
	}

	s := c.Storage
	if !dnsSubdomain.MatchString(s.VaultConfig) {
		add("storage.vaultConfig is required")
	}
	if !vaultPath.MatchString(s.SecretsPrefix) {
		add("storage.secretsPrefix must be a Vault path")
	}
	if !strings.Contains(s.DestinationPath, "{login}") || !strings.Contains(s.DestinationPath, "{name}") ||
		strings.Contains(s.DestinationPath, "{id}") ||
		!vaultPath.MatchString(expand(s.DestinationPath, "cust01", "abc123", "zot-abc123")) {
		add("storage.destinationPath must be a Vault path naming {login} and {name} (and not {id}: the role reads it by service account)")
	}
	for _, r := range s.Regions {
		if !dnsLabel.MatchString(r) {
			add("storage.regions: %q is not a DNS label", r)
		}
	}

	v := c.Secrets
	if !dnsSubdomain.MatchString(v.VaultConfig) {
		add("secrets.vaultConfig is required")
	}
	if !vaultPath.MatchString(v.SecretsPrefix) {
		add("secrets.secretsPrefix must be a Vault path")
	}
	if !vaultPath.MatchString(v.AuthMount) {
		add("secrets.authMount must be a Vault path")
	}
	if !vaultPath.MatchString(v.KVMount) || strings.Contains(v.KVMount, "/") {
		add("secrets.kvMount must be a single Vault path segment")
	}
	if !dnsSubdomain.MatchString(v.Audience) {
		add("secrets.audience is required")
	}
	if s.SecretsPrefix != v.SecretsPrefix {
		add("secrets.secretsPrefix must equal storage.secretsPrefix: the role reads the leaf the bucket keys are written to")
	}
	for _, p := range v.RolePolicies {
		if !roleName.MatchString(p) || p == "root" {
			add("secrets.rolePolicies: %q is not an allowed policy name", p)
		}
	}

	i := c.Identity
	if len(i.AllowedRoles) == 0 {
		add("identity.allowedRoles must not be empty")
	}
	for _, r := range append(slices.Clone(i.AllowedRoles), i.SystemClassRoles...) {
		if !roleName.MatchString(r) {
			add("identity: role %q is not a role name", r)
		}
	}
	if !targetType.MatchString(i.TargetType) {
		add("identity.targetType is required")
	}
	if !dnsLabel.MatchString(i.KeyDeliveryApp) {
		add("identity.keyDeliveryApp is required")
	}
	if i.MaxKeyTTLSeconds < 3600 || i.MaxKeyTTLSeconds > 31536000 {
		add("identity.maxKeyTTLSeconds must be within [3600, 31536000]")
	}

	r := c.Route
	if len(r.Namespaces) == 0 && !r.AllowOrderNamespace {
		add("route: either namespaces or allowOrderNamespace is required")
	}
	for _, ns := range r.Namespaces {
		if !dnsLabel.MatchString(ns) {
			add("route.namespaces: %q is not a DNS label", ns)
		}
	}
	if len(r.Hostnames) == 0 {
		add("route.hostnames must not be empty")
	}
	for _, h := range r.Hostnames {
		if !dnsSubdomain.MatchString(h) || len(h) > 253 {
			add("route.hostnames: %q is not a host name", h)
		}
	}
	if len(r.ParentRefs) == 0 {
		add("route.parentRefs must not be empty")
	}
	for _, p := range r.ParentRefs {
		if !dnsLabel.MatchString(p.Namespace) || !dnsSubdomain.MatchString(p.Name) {
			add("route.parentRefs: namespace and name are required")
		}
	}
	if r.BackendPort < 1 || r.BackendPort > 65535 {
		add("route.backendPort must be a port")
	}

	if err := c.Addon.Validate(c.AddonNamePrefix); err != nil {
		errs = append(errs, fmt.Errorf("addon: %w", err))
	}
	return errors.Join(errs...)
}

// expand подставляет {login}, {id} и {name} в шаблон пути.
func expand(tpl, login, id, name string) string {
	return strings.NewReplacer("{login}", login, "{id}", id, "{name}", name).Replace(tpl)
}

// ResourceName — имя ресурса заказа: <prefix><id><suffix>.
func (c *Config) ResourceName(id, suffix string) string { return c.ResourceNamePrefix + id + suffix }

// AddonName — имя Addon заказа.
func (c *Config) AddonName(id string) string { return c.AddonNamePrefix + id }

// DestinationPath — лист ключей бакета заказа под SecretsPrefix.
func (c *Config) DestinationPath(login, id string) string {
	return expand(c.Storage.DestinationPath, login, id, c.ResourceName(id, ""))
}

// LeafIdentityPath — путь листа ключей бакета в форме identityPaths
// vault-operator: тот же лист, что DestinationPath, с подстановками учётки
// вместо namespace и имени. Политика роли читает ровно лист своей учётки.
func (c *Config) LeafIdentityPath() string {
	leaf := strings.NewReplacer("{login}", "{serviceAccountNamespace}", "{name}", "{serviceAccountName}").Replace(c.Storage.DestinationPath)
	return c.Secrets.KVMount + "/data/" + c.Secrets.SecretsPrefix + "/" + leaf
}

// RoutePath — префикс пути маршрута заказа.
func RoutePath(id string) string { return "/v2/" + id + "/" }
