// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
)

// ----------------------------------------------------------------------------
// S3BucketClaim (vault.in-cloud.io/v1alpha1), шаг storage.

// ObservedS3BucketClaimTemplateSpec — неизменяемая ревизия шаблона бакета.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="template spec is immutable; publish a new version"
type ObservedS3BucketClaimTemplateSpec struct {
	TemplateBody `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=os3t,categories=registry-templates
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=`.spec.templateName`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == self.spec.templateName + '-' + self.spec.version",message="metadata.name must be <templateName>-<version>"

// ObservedS3BucketClaimTemplate — шаблон spec S3BucketClaim заказа.
type ObservedS3BucketClaimTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ObservedS3BucketClaimTemplateSpec `json:"spec"`
	// +optional
	Status registryv1alpha1.TemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ObservedS3BucketClaimTemplateList — список ревизий.
type ObservedS3BucketClaimTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ObservedS3BucketClaimTemplate `json:"items"`
}

// Body — линия, ревизия и Go-шаблон.
func (t *ObservedS3BucketClaimTemplate) Body() TemplateBody { return t.Spec.TemplateBody }

// TemplateStatus — статус ревизии.
func (t *ObservedS3BucketClaimTemplate) TemplateStatus() *registryv1alpha1.TemplateStatus {
	return &t.Status
}

// ----------------------------------------------------------------------------
// VaultClaim (vault.in-cloud.io/v1alpha1), шаг secrets.

// ObservedVaultClaimTemplateSpec — неизменяемая ревизия шаблона VaultClaim.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="template spec is immutable; publish a new version"
type ObservedVaultClaimTemplateSpec struct {
	TemplateBody `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=ovct,categories=registry-templates
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=`.spec.templateName`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == self.spec.templateName + '-' + self.spec.version",message="metadata.name must be <templateName>-<version>"

// ObservedVaultClaimTemplate — шаблон spec VaultClaim заказа (гостевой
// режим vault-operator).
type ObservedVaultClaimTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ObservedVaultClaimTemplateSpec `json:"spec"`
	// +optional
	Status registryv1alpha1.TemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ObservedVaultClaimTemplateList — список ревизий.
type ObservedVaultClaimTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ObservedVaultClaimTemplate `json:"items"`
}

// Body — линия, ревизия и Go-шаблон.
func (t *ObservedVaultClaimTemplate) Body() TemplateBody { return t.Spec.TemplateBody }

// TemplateStatus — статус ревизии.
func (t *ObservedVaultClaimTemplate) TemplateStatus() *registryv1alpha1.TemplateStatus {
	return &t.Status
}

// ----------------------------------------------------------------------------
// ServiceAccount (iam.kaname.in-cloud.io/v1alpha1), шаг identity.

// ObservedIamServiceAccountTemplateSpec — неизменяемая ревизия шаблона
// учётки CI-робота.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="template spec is immutable; publish a new version"
type ObservedIamServiceAccountTemplateSpec struct {
	TemplateBody `json:",inline"`
	// NameSuffix — суффикс имени объекта: <prefix><id><nameSuffix>.
	// +kubebuilder:validation:Pattern=`^(-[a-z0-9]+){1,3}$`
	// +kubebuilder:validation:MaxLength=24
	NameSuffix string `json:"nameSuffix"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=oisat,categories=registry-templates
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=`.spec.templateName`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == self.spec.templateName + '-' + self.spec.version",message="metadata.name must be <templateName>-<version>"

// ObservedIamServiceAccountTemplate — шаблон spec iam ServiceAccount заказа.
type ObservedIamServiceAccountTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ObservedIamServiceAccountTemplateSpec `json:"spec"`
	// +optional
	Status registryv1alpha1.TemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ObservedIamServiceAccountTemplateList — список ревизий.
type ObservedIamServiceAccountTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ObservedIamServiceAccountTemplate `json:"items"`
}

// Body — линия, ревизия и Go-шаблон.
func (t *ObservedIamServiceAccountTemplate) Body() TemplateBody { return t.Spec.TemplateBody }

// TemplateStatus — статус ревизии.
func (t *ObservedIamServiceAccountTemplate) TemplateStatus() *registryv1alpha1.TemplateStatus {
	return &t.Status
}

// ----------------------------------------------------------------------------
// AccessBinding (iam.kaname.in-cloud.io/v1alpha1), шаг access.

// ObservedIamAccessBindingTemplateSpec — неизменяемая ревизия шаблона
// выдачи роли.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="template spec is immutable; publish a new version"
type ObservedIamAccessBindingTemplateSpec struct {
	TemplateBody `json:",inline"`
	// NameSuffix — суффикс имени объекта: <prefix><id><nameSuffix>. spec
	// выдачи неизменяем в kaname, поэтому смена роли — новый суффикс.
	// +kubebuilder:validation:Pattern=`^(-[a-z0-9]+){1,3}$`
	// +kubebuilder:validation:MaxLength=24
	NameSuffix string `json:"nameSuffix"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=oiabt,categories=registry-templates
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=`.spec.templateName`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == self.spec.templateName + '-' + self.spec.version",message="metadata.name must be <templateName>-<version>"

// ObservedIamAccessBindingTemplate — шаблон spec iam AccessBinding заказа.
type ObservedIamAccessBindingTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ObservedIamAccessBindingTemplateSpec `json:"spec"`
	// +optional
	Status registryv1alpha1.TemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ObservedIamAccessBindingTemplateList — список ревизий.
type ObservedIamAccessBindingTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ObservedIamAccessBindingTemplate `json:"items"`
}

// Body — линия, ревизия и Go-шаблон.
func (t *ObservedIamAccessBindingTemplate) Body() TemplateBody { return t.Spec.TemplateBody }

// TemplateStatus — статус ревизии.
func (t *ObservedIamAccessBindingTemplate) TemplateStatus() *registryv1alpha1.TemplateStatus {
	return &t.Status
}

// ----------------------------------------------------------------------------
// Addon (addons.in-cloud.io/v1alpha1), шаг workload.

// ChartPin — закреплённый чарт: версия вместе с digest манифеста OCI.
// Неизменяемость тега в реестре заменяется пином digest: перед записью
// Addon оператор сверяет, что версия в реестре разрешается ровно в этот
// digest.
type ChartPin struct {
	// RepoURL — репозиторий чартов OCI: oci://host[:port]/path.
	// +kubebuilder:validation:Pattern=`^oci://[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?(/[a-z0-9]([a-z0-9._-]*[a-z0-9])?)+$`
	// +kubebuilder:validation:MaxLength=512
	RepoURL string `json:"repoURL"`
	// Name — имя чарта.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// Version — версия чарта (тег OCI).
	// +kubebuilder:validation:Pattern=`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`
	// +kubebuilder:validation:MaxLength=64
	Version string `json:"version"`
	// Digest — digest манифеста OCI этой версии.
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	// +kubebuilder:validation:MaxLength=71
	Digest string `json:"digest"`
}

// ObservedAddonTemplateSpec — неизменяемая ревизия шаблона Addon.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="template spec is immutable; publish a new version"
type ObservedAddonTemplateSpec struct {
	TemplateBody `json:",inline"`
	// Chart — закреплённый чарт; repoURL, chart и version Addon оператор
	// ставит только отсюда, шаблон их не пишет.
	Chart ChartPin `json:"chart"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=oat,categories=registry-templates
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=`.spec.templateName`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Chart",type=string,JSONPath=`.spec.chart.version`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == self.spec.templateName + '-' + self.spec.version",message="metadata.name must be <templateName>-<version>"

// ObservedAddonTemplate — шаблон spec Addon заказа: поставить zot с нужными
// путями (бакет, endpoint, лист Vault, роль Vault) из статусов готовых шагов.
type ObservedAddonTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ObservedAddonTemplateSpec `json:"spec"`
	// +optional
	Status registryv1alpha1.TemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ObservedAddonTemplateList — список ревизий.
type ObservedAddonTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ObservedAddonTemplate `json:"items"`
}

// Body — линия, ревизия и Go-шаблон.
func (t *ObservedAddonTemplate) Body() TemplateBody { return t.Spec.TemplateBody }

// TemplateStatus — статус ревизии.
func (t *ObservedAddonTemplate) TemplateStatus() *registryv1alpha1.TemplateStatus {
	return &t.Status
}

// ----------------------------------------------------------------------------
// HTTPRoute (gateway.networking.k8s.io/v1), шаг route.

// ObservedHTTPRouteTemplateSpec — неизменяемая ревизия шаблона маршрута.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="template spec is immutable; publish a new version"
type ObservedHTTPRouteTemplateSpec struct {
	TemplateBody `json:",inline"`
	// TargetNamespace — namespace маршрута; пусто — namespace заказа.
	// Маршрут в другом namespace ссылается на Service zot в namespace
	// заказа; разрешение такой ссылки (ReferenceGrant на namespace
	// арендатора) — объект платформы, оператор его не пишет.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	// +optional
	TargetNamespace string `json:"targetNamespace,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=ohrt,categories=registry-templates
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=`.spec.templateName`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == self.spec.templateName + '-' + self.spec.version",message="metadata.name must be <templateName>-<version>"

// ObservedHTTPRouteTemplate — шаблон spec HTTPRoute заказа (Envoy Gateway).
type ObservedHTTPRouteTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ObservedHTTPRouteTemplateSpec `json:"spec"`
	// +optional
	Status registryv1alpha1.TemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ObservedHTTPRouteTemplateList — список ревизий.
type ObservedHTTPRouteTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ObservedHTTPRouteTemplate `json:"items"`
}

// Body — линия, ревизия и Go-шаблон.
func (t *ObservedHTTPRouteTemplate) Body() TemplateBody { return t.Spec.TemplateBody }

// TemplateStatus — статус ревизии.
func (t *ObservedHTTPRouteTemplate) TemplateStatus() *registryv1alpha1.TemplateStatus {
	return &t.Status
}

// Compile-time: каждый вид — Template.
var (
	_ Template = (*ObservedS3BucketClaimTemplate)(nil)
	_ Template = (*ObservedVaultClaimTemplate)(nil)
	_ Template = (*ObservedIamServiceAccountTemplate)(nil)
	_ Template = (*ObservedIamAccessBindingTemplate)(nil)
	_ Template = (*ObservedAddonTemplate)(nil)
	_ Template = (*ObservedHTTPRouteTemplate)(nil)
)
