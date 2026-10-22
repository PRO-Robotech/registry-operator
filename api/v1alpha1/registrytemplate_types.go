// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NamespaceSelector — равенство меток namespace. Выражений нет намеренно:
// селектор ограничен по размеру, а пустой (всё) запрещён.
type NamespaceSelector struct {
	// MatchLabels — все метки обязаны совпасть.
	// +kubebuilder:validation:MinProperties=1
	// +kubebuilder:validation:MaxProperties=16
	MatchLabels map[string]string `json:"matchLabels"`
}

// ClaimPolicy — кто вправе заказывать по ревизии.
type ClaimPolicy struct {
	// NamespaceSelector — namespace заказа обязан подходить под селектор.
	NamespaceSelector NamespaceSelector `json:"namespaceSelector"`
}

// ResourceTemplates — ревизии observed-шаблонов шести ресурсов пайплайна.
// Вид цели зашит в вид шаблона: ссылка на шаблон не той группы ресурсов
// невыразима.
type ResourceTemplates struct {
	// S3BucketClaim — ObservedS3BucketClaimTemplate (шаг storage).
	S3BucketClaim TemplateRef `json:"s3BucketClaim"`
	// IamServiceAccount — ObservedIamServiceAccountTemplate (шаг identity).
	IamServiceAccount TemplateRef `json:"iamServiceAccount"`
	// VaultClaim — ObservedVaultClaimTemplate (шаг secrets).
	VaultClaim TemplateRef `json:"vaultClaim"`
	// IamAccessBinding — ObservedIamAccessBindingTemplate (шаг access).
	IamAccessBinding TemplateRef `json:"iamAccessBinding"`
	// Addon — ObservedAddonTemplate (шаг workload).
	Addon TemplateRef `json:"addon"`
	// HTTPRoute — ObservedHTTPRouteTemplate (шаг route).
	HTTPRoute TemplateRef `json:"httpRoute"`
}

// RegistryTemplateSpec — неизменяемая ревизия шаблона заказа.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="RegistryTemplate spec is immutable; publish a new version"
type RegistryTemplateSpec struct {
	// TemplateName — имя линии ревизий.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]{0,40}[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=42
	TemplateName string `json:"templateName"`
	// Version — ревизия линии.
	// +kubebuilder:validation:Pattern=`^v[0-9]{1,6}$`
	// +kubebuilder:validation:MaxLength=7
	Version string `json:"version"`

	// ClaimPolicy — допуск namespace заказа. Обязателен и непуст.
	ClaimPolicy ClaimPolicy `json:"claimPolicy"`

	// Params — константы площадки, доступные шаблонам как .Params:
	// YAML-документ с отображением на верхнем уровне. Строкой, а не
	// объектом: размер ограничен, и неизменяемость spec проверяется
	// сравнением с предсказуемой ценой.
	// +kubebuilder:validation:MaxLength=32768
	// +optional
	Params string `json:"params,omitempty"`

	// Templates — ревизии шаблонов шести ресурсов пайплайна.
	Templates ResourceTemplates `json:"templates"`

	// StallTimeout — сколько заказ может быть неготов, прежде чем получит
	// условие Stalled. Работа при этом продолжается.
	// +kubebuilder:default="15m"
	// +optional
	StallTimeout *metav1.Duration `json:"stallTimeout,omitempty"`
}

// Типы условий ревизий шаблонов.
const (
	// ConditionAccepted — ревизия разобрана, прошла AST-гейт и проверки
	// своего вида; у RegistryTemplate — и каждая ссылка принята.
	ConditionAccepted = "Accepted"
)

// Причины условий ревизий шаблонов.
const (
	ReasonAccepted        = "Accepted"
	ReasonTemplateInvalid = "TemplateInvalid"
)

// TemplateStatus — состояние ревизии шаблона (общая форма всех видов).
type TemplateStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=regtpl,categories=registry-templates
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=`.spec.templateName`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == self.spec.templateName + '-' + self.spec.version",message="metadata.name must be <templateName>-<version>"

// RegistryTemplate — ревизия шаблона заказа. Ставит платформа.
type RegistryTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec RegistryTemplateSpec `json:"spec"`
	// +optional
	Status TemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RegistryTemplateList — список ревизий.
type RegistryTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RegistryTemplate `json:"items"`
}
