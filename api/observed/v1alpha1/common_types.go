// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
)

// TemplateBody — общая часть каждого observed-шаблона: линия, ревизия и
// Go-шаблон spec цели.
type TemplateBody struct {
	// TemplateName — имя линии ревизий.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]{0,40}[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=42
	TemplateName string `json:"templateName"`
	// Version — ревизия линии.
	// +kubebuilder:validation:Pattern=`^v[0-9]{1,6}$`
	// +kubebuilder:validation:MaxLength=7
	Version string `json:"version"`
	// Value — Go-шаблон spec цели (YAML). Контекст закрыт: .Registry, .ID,
	// .Login, .Params, .Template и .Status.<ресурс> уже готовых шагов.
	// Каждое печатающее действие кончается на toJson (поля заказа и статусы
	// дочерних — только так) либо на toYaml | nindent (только .Params);
	// define, template, block, объявления переменных и цепочки запрещены.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=65536
	Value string `json:"value"`
}

// Template — то общее, что контроллер шаблонов читает у каждого вида. Пакет
// API зависит только от apimachinery, поэтому интерфейс собран из его типов.
// +kubebuilder:object:generate=false
type Template interface {
	metav1.Object
	runtime.Object
	// Body — линия, ревизия и Go-шаблон.
	Body() TemplateBody
	// TemplateStatus — статус ревизии для записи условия Accepted.
	TemplateStatus() *registryv1alpha1.TemplateStatus
}
