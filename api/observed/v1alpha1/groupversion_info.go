// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package v1alpha1 — схема API группы observed.registry.in-cloud.io/v1alpha1:
// наблюдаемые шаблоны ресурсов заказа. Вид шаблона однозначно задаёт вид
// цели: ObservedS3BucketClaimTemplate отрисовывает только S3BucketClaim,
// ObservedVaultClaimTemplate — только VaultClaim, и так далее. Шаблон пишет
// только spec цели; имя, namespace, метки и поля владения ставит оператор.
// +kubebuilder:object:generate=true
// +groupName=observed.registry.in-cloud.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion — группа и версия объектов этого пакета.
	GroupVersion = schema.GroupVersion{Group: "observed.registry.in-cloud.io", Version: "v1alpha1"}

	// SchemeBuilder регистрирует типы пакета в схеме.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme добавляет типы пакета в схему.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&ObservedS3BucketClaimTemplate{}, &ObservedS3BucketClaimTemplateList{},
		&ObservedVaultClaimTemplate{}, &ObservedVaultClaimTemplateList{},
		&ObservedIamServiceAccountTemplate{}, &ObservedIamServiceAccountTemplateList{},
		&ObservedIamAccessBindingTemplate{}, &ObservedIamAccessBindingTemplateList{},
		&ObservedAddonTemplate{}, &ObservedAddonTemplateList{},
		&ObservedHTTPRouteTemplate{}, &ObservedHTTPRouteTemplateList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
