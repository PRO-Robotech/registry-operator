// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RegistrySpec — заказ регистри. Всё, что выводится из шаблона, в заказе не
// пишется: заказ несёт id, ревизию шаблона и судьбу бакета при удалении.
type RegistrySpec struct {
	// ID — неизменяемый идентификатор регистри; он же имя объекта и основа
	// имён дочерних ресурсов. Повторно не выдаётся (RegistryIDReservation).
	// +kubebuilder:validation:Pattern=`^[a-z0-9]{6,20}$`
	// +kubebuilder:validation:MaxLength=20
	// +kubebuilder:validation:XValidation:rule="!self.startsWith('reg')",message="id must not start with 'reg'"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="id is immutable"
	ID string `json:"id"`

	// TemplateRef — ревизия RegistryTemplate. Меняется только version:
	// смена версии — переход заказа на новую ревизию.
	// +kubebuilder:validation:XValidation:rule="self.name == oldSelf.name",message="templateRef.name is immutable; only templateRef.version may change"
	TemplateRef TemplateRef `json:"templateRef"`

	// DeletionPolicy — судьба бакета при удалении заказа. Retain (умолчание)
	// оставляет бакет и его ключи; Purge стирает их. Purge доходит до
	// S3BucketClaim только в фазе Deleting.
	// +kubebuilder:default=Retain
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// Public — анонимное чтение образов регистри: pull без токена. По
	// умолчанию закрыто; запись анонимной не бывает никогда.
	// +kubebuilder:default=false
	// +optional
	Public bool `json:"public,omitempty"`
}

// Step — шаг пайплайна заказа: один ресурс со своим состоянием.
// +kubebuilder:validation:Enum=storage;identity;secrets;registration;access;workload;route
type Step string

const (
	// StepStorage — S3BucketClaim: бакет и его ключи в Vault.
	StepStorage Step = "storage"
	// StepIdentity — iam ServiceAccount: учётка CI-робота в kaname.
	StepIdentity Step = "identity"
	// StepSecrets — VaultClaim: роль Vault для k8s ServiceAccount zot.
	StepSecrets Step = "secrets"
	// StepRegistration — регистрация ресурса registry_registry:<id> в kaname:
	// кортеж с цепью предков пишет сам оператор (служба registry), шаблона у
	// шага нет.
	StepRegistration Step = "registration"
	// StepAccess — iam AccessBinding: выдача роли учётке на реестр.
	StepAccess Step = "access"
	// StepWorkload — Addon: zot с нужными путями.
	StepWorkload Step = "workload"
	// StepRoute — HTTPRoute Envoy Gateway.
	StepRoute Step = "route"
)

// RegistryPhase — сводное состояние заказа.
// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Degraded;Upgrading;Deleting;Failed
type RegistryPhase string

const (
	// PhasePending — ревизия шаблона ещё не принята или не найдена.
	PhasePending RegistryPhase = "Pending"
	// PhaseProvisioning — пайплайн идёт, заказ ещё ни разу не был готов.
	PhaseProvisioning RegistryPhase = "Provisioning"
	// PhaseReady — каждый ресурс пайплайна готов.
	PhaseReady RegistryPhase = "Ready"
	// PhaseDegraded — заказ был готов, сейчас нет.
	PhaseDegraded RegistryPhase = "Degraded"
	// PhaseUpgrading — идёт переход на новую ревизию шаблона.
	PhaseUpgrading RegistryPhase = "Upgrading"
	// PhaseDeleting — ресурсы снимаются в обратном порядке.
	PhaseDeleting RegistryPhase = "Deleting"
	// PhaseFailed — заказ отвергнут; повтор только после правки входа.
	PhaseFailed RegistryPhase = "Failed"
)

// Типы условий заказа. Писатель у них один — registry-operator.
const (
	ConditionReady             = "Ready"
	ConditionIDReserved        = "IDReserved"
	ConditionTemplateResolved  = "TemplateResolved"
	ConditionStorageReady      = "StorageReady"
	ConditionIdentityReady     = "IdentityReady"
	ConditionSecretsReady      = "SecretsReady"
	ConditionRegistrationReady = "RegistrationReady"
	ConditionAccessReady       = "AccessReady"
	ConditionWorkloadReady     = "WorkloadReady"
	ConditionRouteReady        = "RouteReady"
	ConditionSyncRecovering    = "SyncRecovering"
	ConditionStalled           = "Stalled"
	ConditionDeletionStalled   = "DeletionStalled"
)

// StepCondition — тип условия готовности шага.
func StepCondition(s Step) string {
	switch s {
	case StepStorage:
		return ConditionStorageReady
	case StepIdentity:
		return ConditionIdentityReady
	case StepSecrets:
		return ConditionSecretsReady
	case StepRegistration:
		return ConditionRegistrationReady
	case StepAccess:
		return ConditionAccessReady
	case StepWorkload:
		return ConditionWorkloadReady
	case StepRoute:
		return ConditionRouteReady
	}
	return ""
}

// Каталог причин. Перечень закрыт: в reason попадает только код отсюда, а
// message условия — фиксированный текст по коду. Сырые сообщения дочерних
// ресурсов наружу не выходят: они — во внутренней проекции
// RegistryIDReservation.status.diagnostics.
const (
	ReasonReconciled            = "Reconciled"
	ReasonIDReserved            = "IDReserved"
	ReasonIDTaken               = "IDTaken"
	ReasonTemplateNotFound      = "TemplateNotFound"
	ReasonTemplateNotAccepted   = "TemplateNotAccepted"
	ReasonTemplateResolved      = "TemplateResolved"
	ReasonNotAllowed            = "NotAllowed"
	ReasonAdoptionRefused       = "AdoptionRefused"
	ReasonRenderFailed          = "RenderFailed"
	ReasonPolicyViolation       = "PolicyViolation"
	ReasonChartDigestMismatch   = "ChartDigestMismatch"
	ReasonChartUnreachable      = "ChartUnreachable"
	ReasonApplyFailed           = "ApplyFailed"
	ReasonWaitingForDependency  = "WaitingForDependency"
	ReasonResourceReady         = "ResourceReady"
	ReasonResourceMissing       = "ResourceMissing"
	ReasonResourceProgressing   = "ResourceProgressing"
	ReasonResourceStale         = "ResourceStale"
	ReasonResourceDegraded      = "ResourceDegraded"
	ReasonSyncHealthy           = "SyncHealthy"
	ReasonSyncRecovering        = "SyncRecovering"
	ReasonSyncRecoveryNoResult  = "SyncRecoveryNoResult"
	ReasonStallTimeout          = "StallTimeout"
	ReasonProgressing           = "Progressing"
	ReasonDeleting              = "Deleting"
	ReasonDeletionPolicyPending = "DeletionPolicyPending"
	ReasonDeletionTimeout       = "DeletionTimeout"
	ReasonRegistered            = "Registered"
	ReasonProjectUnresolved     = "ProjectUnresolved"
	ReasonProjectChanged        = "ProjectChanged"
	ReasonRegistrationPending   = "RegistrationPending"
	ReasonRegistrationRefused   = "RegistrationRefused"
	ReasonReservationMissing    = "ReservationMissing"
)

// ReasonMessages — фиксированный текст условия по коду причины. Ни одной
// подстановки: текст не несёт ни имён инфраструктуры, ни сообщений чужих
// контроллеров.
var ReasonMessages = map[string]string{
	ReasonReconciled:            "registry is ready",
	ReasonIDReserved:            "registry id is reserved for this order",
	ReasonIDTaken:               "registry id is reserved by another order and is never reissued",
	ReasonTemplateNotFound:      "referenced template revision does not exist",
	ReasonTemplateNotAccepted:   "referenced template revision is not accepted",
	ReasonTemplateResolved:      "template revision is resolved and accepted",
	ReasonNotAllowed:            "namespace is not allowed by the template claim policy",
	ReasonAdoptionRefused:       "an object with the registry resource name exists and does not belong to this order",
	ReasonRenderFailed:          "template revision failed to render for this order",
	ReasonPolicyViolation:       "rendered resource violates the platform policy",
	ReasonChartDigestMismatch:   "chart version does not resolve to the pinned digest",
	ReasonChartUnreachable:      "chart digest could not be verified; the addon is not written",
	ReasonApplyFailed:           "resource could not be written",
	ReasonWaitingForDependency:  "resource waits for the previous pipeline step",
	ReasonResourceReady:         "resource is ready",
	ReasonResourceMissing:       "resource does not exist",
	ReasonResourceProgressing:   "resource is not ready yet",
	ReasonResourceStale:         "resource controller has not observed its latest generation",
	ReasonResourceDegraded:      "resource reports failure",
	ReasonSyncHealthy:           "workload synchronization is not stuck",
	ReasonSyncRecovering:        "workload synchronization retries were exhausted; sync epoch is advanced",
	ReasonSyncRecoveryNoResult:  "workload synchronization failed without a sync result; a new sync epoch does not restart it",
	ReasonStallTimeout:          "registry has not become ready within the template stall timeout",
	ReasonProgressing:           "registry is being provisioned",
	ReasonDeleting:              "registry is being deleted",
	ReasonDeletionPolicyPending: "bucket does not carry the requested deletion policy yet",
	ReasonDeletionTimeout:       "deletion has not completed within the deletion timeout",
	ReasonRegistered:            "registry resource is registered in IAM",
	ReasonProjectUnresolved:     "namespace of the order names no IAM project",
	ReasonProjectChanged:        "namespace of the order names another IAM project than the registry is registered under",
	ReasonRegistrationPending:   "IAM did not answer; registration will be retried",
	ReasonRegistrationRefused:   "IAM refused the registration of the registry resource",
	ReasonReservationMissing:    "registry id has no active reservation of this order",
}

// ResourceStatus — состояние ресурса шага (публичная проекция: только код
// причины).
type ResourceStatus struct {
	// Step — шаг пайплайна.
	Step Step `json:"step"`
	// APIVersion и Kind — вид ресурса.
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	// Namespace — пусто у ресурса уровня Cluster.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// Ready — ресурс готов на своём текущем поколении.
	Ready bool `json:"ready"`
	// Reason — код из каталога причин.
	Reason string `json:"reason"`
}

// RegistrationStatus — регистрация ресурса регистри в IAM (публичная
// проекция: объект и код причины).
type RegistrationStatus struct {
	// Object — объект модели прав: registry_registry:<id>.
	Object string `json:"object"`
	// Ready — регистрация применена с цепью предков текущего проекта.
	Ready bool `json:"ready"`
	// Reason — код из каталога причин.
	Reason string `json:"reason"`
}

// SyncRecoveryStatus — восстановление синхронизации рабочей нагрузки после
// исчерпанного повтора Argo CD: эпоха синхронизации в переменных Addon.
type SyncRecoveryStatus struct {
	// Epoch — текущая эпоха синхронизации; растёт монотонно.
	// +optional
	Epoch int64 `json:"epoch,omitempty"`
	// Attempts — подряд выполненных продвижений эпохи без восстановления.
	// +optional
	Attempts int32 `json:"attempts,omitempty"`
	// StuckSince — когда синхронизация замечена застрявшей.
	// +optional
	StuckSince *metav1.Time `json:"stuckSince,omitempty"`
	// LastAdvanceTime — когда эпоха продвинута последний раз.
	// +optional
	LastAdvanceTime *metav1.Time `json:"lastAdvanceTime,omitempty"`
}

// RegistryStatus — публичная проекция состояния заказа. Читается арендатором
// и BFF: здесь только коды и фиксированные тексты.
type RegistryStatus struct {
	// +optional
	Phase RegistryPhase `json:"phase,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Endpoint — адрес регистри: <host>/<id>.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// Template — ревизия, по которой отрисованы текущие ресурсы.
	// +optional
	Template *TemplateRef `json:"template,omitempty"`
	// Provisioned — заказ хотя бы раз был готов (защёлка).
	// +optional
	Provisioned bool `json:"provisioned,omitempty"`
	// ProgressingSince — начало текущего периода неготовности.
	// +optional
	ProgressingSince *metav1.Time `json:"progressingSince,omitempty"`
	// DeletingSince — начало удаления.
	// +optional
	DeletingSince *metav1.Time `json:"deletingSince,omitempty"`
	// Resources — ресурсы пайплайна по шагам.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Resources []ResourceStatus `json:"resources,omitempty"`
	// Registration — регистрация ресурса регистри в IAM.
	// +optional
	Registration *RegistrationStatus `json:"registration,omitempty"`
	// +optional
	Sync SyncRecoveryStatus `json:"sync,omitempty"`
	// Conditions — условия заказа: сводное Ready и по каждому шагу.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=reg,categories=registry
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.spec.id`
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=`.spec.templateRef.name`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.templateRef.version`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == self.spec.id",message="metadata.name must equal spec.id"

// Registry — заказ регистри в namespace арендатора. login арендатора —
// metadata.namespace заказа.
type Registry struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec RegistrySpec `json:"spec"`
	// +optional
	Status RegistryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RegistryList — список заказов.
type RegistryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Registry `json:"items"`
}
