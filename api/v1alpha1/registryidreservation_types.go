// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RegistryRef — заказ, за которым закреплён id.
type RegistryRef struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	UID string `json:"uid"`
}

// RegistryIDReservationSpec — закрепление id за заказом.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="reservation spec is immutable"
type RegistryIDReservationSpec struct {
	RegistryRef RegistryRef `json:"registryRef"`
}

// ReservationPhase — состояние резерва.
// +kubebuilder:validation:Enum=Active;Released
type ReservationPhase string

const (
	// ReservationActive — заказ жив.
	ReservationActive ReservationPhase = "Active"
	// ReservationReleased — заказ удалён; резерв остаётся надгробием и id
	// повторно не выдаётся.
	ReservationReleased ReservationPhase = "Released"
)

// ResourceDiagnostic — сырое сообщение о ресурсе шага (внутренняя проекция).
type ResourceDiagnostic struct {
	Step Step   `json:"step"`
	Kind string `json:"kind"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Message string `json:"message,omitempty"`
}

// Diagnostics — сырые сообщения рендера, политики и дочерних ресурсов.
// Объект уровня Cluster: арендатору и BFF не виден.
type Diagnostics struct {
	// Message — последняя ошибка прохода (рендер, политика, запись).
	// +kubebuilder:validation:MaxLength=2048
	// +optional
	Message string `json:"message,omitempty"`
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	// +optional
	Resources []ResourceDiagnostic `json:"resources,omitempty"`
}

// IAMRegistration — с какими предками ресурс регистри записан в IAM.
// Проект закрепляется первой регистрацией: перенос регистри под другой
// проект перенёс бы и права на все его репозитории, поэтому смена меток
// namespace такого переноса не делает — заказ получает отказ.
type IAMRegistration struct {
	// Project — проект IAM регистри (prj…).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Project string `json:"project"`
	// Account — аккаунт IAM проекта (acc…); пусто — аккаунт выводит IAM.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Account string `json:"account,omitempty"`
	// Generation — поколение заказа, с которым регистрация применена
	// последней.
	// +optional
	Generation int64 `json:"generation,omitempty"`
	// RegisteredAt — когда регистрация применена последней.
	// +optional
	RegisteredAt *metav1.Time `json:"registeredAt,omitempty"`
	// ClosingAt — когда снятие начато. С этого момента заявки на новые
	// репозитории отвергаются, а снятие репозиториев ждёт, пока истечёт
	// срок уже идущей заявки: ни одна из них не допишет запись в IAM после
	// снятия.
	// +optional
	ClosingAt *metav1.Time `json:"closingAt,omitempty"`
}

// RegisteredRepository — репозиторий регистри, записанный в IAM по заявке
// рабочей нагрузки zot. Опись — источник истины для снятия: удаление заказа
// снимает в IAM ровно то, что здесь записано.
type RegisteredRepository struct {
	// Name — имя репозитория внутри регистри (без <id>/).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Name string `json:"name"`
	// EnrolledBy — субъект IAM, чей push завёл репозиторий
	// (user:<id> | service_account:<id>).
	// +kubebuilder:validation:MaxLength=160
	// +optional
	EnrolledBy string `json:"enrolledBy,omitempty"`
	// EnrolledAt — когда заявка записана в опись.
	EnrolledAt metav1.Time `json:"enrolledAt"`
	// Registered — регистрация в IAM применена; false — заявка в описи,
	// регистрация ещё не подтверждена.
	// +optional
	Registered bool `json:"registered,omitempty"`
}

// RegistryIDReservationStatus — состояние резерва.
type RegistryIDReservationStatus struct {
	// +optional
	Phase ReservationPhase `json:"phase,omitempty"`
	// +optional
	ReleasedAt *metav1.Time `json:"releasedAt,omitempty"`
	// Registration — регистрация ресурса регистри в IAM.
	// +optional
	Registration *IAMRegistration `json:"registration,omitempty"`
	// Repositories — опись репозиториев регистри в IAM.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=1024
	// +optional
	Repositories []RegisteredRepository `json:"repositories,omitempty"`
	// +optional
	Diagnostics *Diagnostics `json:"diagnostics,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=regid,categories=registry
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.registryRef.namespace`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == self.spec.registryRef.name",message="reservation name must equal the registry id"

// RegistryIDReservation — резерв id регистри: создаётся атомарным CREATE
// (занятое имя — отказ), переживает заказ надгробием Released.
type RegistryIDReservation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec RegistryIDReservationSpec `json:"spec"`
	// +optional
	Status RegistryIDReservationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RegistryIDReservationList — список резервов.
type RegistryIDReservationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RegistryIDReservation `json:"items"`
}
