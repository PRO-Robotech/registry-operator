// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package samples читает примеры config/samples — ревизию zot-v1 площадки
// site-a, шесть observed-шаблонов и конфигурацию оператора. Примеры — тот же
// контракт, который проверяют пробы: golden-рендер, envtest и e2e берут их
// отсюда, а не держат свою копию.
package samples

import (
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"

	observedv1alpha1 "github.com/PRO-Robotech/registry-operator/api/observed/v1alpha1"
	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/kinds"
)

// Set — ревизия шаблона заказа и шесть её шаблонов.
type Set struct {
	RegistryTemplate  *registryv1alpha1.RegistryTemplate
	S3BucketClaim     *observedv1alpha1.ObservedS3BucketClaimTemplate
	IamServiceAccount *observedv1alpha1.ObservedIamServiceAccountTemplate
	VaultClaim        *observedv1alpha1.ObservedVaultClaimTemplate
	IamAccessBinding  *observedv1alpha1.ObservedIamAccessBindingTemplate
	Addon             *observedv1alpha1.ObservedAddonTemplate
	HTTPRoute         *observedv1alpha1.ObservedHTTPRouteTemplate
}

func load(path string, into any) error {
	b, err := os.ReadFile(path) //nolint:gosec // путь — каталог примеров репозитория.
	if err != nil {
		return err
	}
	if err := yaml.UnmarshalStrict(b, into); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Load читает ревизию и шаблоны из <dir>/templates.
func Load(dir string) (*Set, error) {
	s := &Set{
		RegistryTemplate:  &registryv1alpha1.RegistryTemplate{},
		S3BucketClaim:     &observedv1alpha1.ObservedS3BucketClaimTemplate{},
		IamServiceAccount: &observedv1alpha1.ObservedIamServiceAccountTemplate{},
		VaultClaim:        &observedv1alpha1.ObservedVaultClaimTemplate{},
		IamAccessBinding:  &observedv1alpha1.ObservedIamAccessBindingTemplate{},
		Addon:             &observedv1alpha1.ObservedAddonTemplate{},
		HTTPRoute:         &observedv1alpha1.ObservedHTTPRouteTemplate{},
	}
	t := filepath.Join(dir, "templates")
	for file, into := range map[string]any{
		"registrytemplate.yaml":  s.RegistryTemplate,
		"s3bucketclaim.yaml":     s.S3BucketClaim,
		"iamserviceaccount.yaml": s.IamServiceAccount,
		"vaultclaim.yaml":        s.VaultClaim,
		"iamaccessbinding.yaml":  s.IamAccessBinding,
		"addon.yaml":             s.Addon,
		"httproute.yaml":         s.HTTPRoute,
	} {
		if err := load(filepath.Join(t, file), into); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// LoadRegistry читает заказ-пример.
func LoadRegistry(dir string) (*registryv1alpha1.Registry, error) {
	r := &registryv1alpha1.Registry{}
	if err := load(filepath.Join(dir, "registry.yaml"), r); err != nil {
		return nil, err
	}
	return r, nil
}

// ConfigPath — путь конфигурации оператора-примера.
func ConfigPath(dir string) string { return filepath.Join(dir, "operator-config.yaml") }

// ForKind — шаблон шага.
func (s *Set) ForKind(k kinds.Kind) observedv1alpha1.Template {
	switch k.Step {
	case registryv1alpha1.StepStorage:
		return s.S3BucketClaim
	case registryv1alpha1.StepIdentity:
		return s.IamServiceAccount
	case registryv1alpha1.StepSecrets:
		return s.VaultClaim
	case registryv1alpha1.StepAccess:
		return s.IamAccessBinding
	case registryv1alpha1.StepWorkload:
		return s.Addon
	case registryv1alpha1.StepRoute:
		return s.HTTPRoute
	}
	return nil
}

// Templates — шаблоны набора (без ревизии).
func (s *Set) Templates() []observedv1alpha1.Template {
	return []observedv1alpha1.Template{s.S3BucketClaim, s.IamServiceAccount, s.VaultClaim, s.IamAccessBinding, s.Addon, s.HTTPRoute}
}
