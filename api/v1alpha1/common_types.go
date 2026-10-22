// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

// TemplateRef — ссылка на ревизию шаблона: объект с именем
// <name>-<version>.
type TemplateRef struct {
	// Name — имя линии шаблона (templateName).
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]{0,40}[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=42
	Name string `json:"name"`

	// Version — ревизия линии: v1, v2, …
	// +kubebuilder:validation:Pattern=`^v[0-9]{1,6}$`
	// +kubebuilder:validation:MaxLength=7
	Version string `json:"version"`
}

// ObjectName — имя объекта ревизии шаблона.
func (r TemplateRef) ObjectName() string { return r.Name + "-" + r.Version }

// DeletionPolicy — судьба данных бакета при удалении заказа.
// +kubebuilder:validation:Enum=Retain;Purge
type DeletionPolicy string

const (
	// DeletionPolicyRetain — бакет и его ключи в Vault остаются после
	// удаления заказа. Умолчание.
	DeletionPolicyRetain DeletionPolicy = "Retain"
	// DeletionPolicyPurge — бакет стирается вместе с заказом. Оператор
	// переводит S3BucketClaim в Purge только в фазе Deleting.
	DeletionPolicyPurge DeletionPolicy = "Purge"
)

const (
	// RegistryFinalizer держит заказ, пока дочерние ресурсы не сняты в
	// обратном порядке и резерв не освобождён.
	RegistryFinalizer = "registry.in-cloud.io/finalizer"

	// InUseFinalizer держит ревизию шаблона, пока на неё ссылаются.
	InUseFinalizer = "registry.in-cloud.io/in-use"

	// LabelManagedBy — метка владельца объекта.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	// ManagedByValue — значение метки владельца у объектов оператора.
	ManagedByValue = "registry-operator"

	// LabelID — id заказа на дочернем объекте.
	LabelID = "registry.in-cloud.io/id"
	// LabelNamespace — namespace заказа на дочернем объекте (Addon уровня
	// Cluster и маршрут в чужом namespace иначе заказа не назовут).
	LabelNamespace = "registry.in-cloud.io/namespace"
	// LabelOwnerUID — uid заказа: проверка «не чужой» перед каждой записью.
	LabelOwnerUID = "registry.in-cloud.io/owner-uid"
	// LabelTemplate — ревизия RegistryTemplate, по которой отрисован объект.
	LabelTemplate = "registry.in-cloud.io/template"
	// LabelStep — шаг пайплайна, которому принадлежит объект.
	LabelStep = "registry.in-cloud.io/step"

	// AnnotationChartDigest — digest чарта, с которым оператор сверил версию
	// перед записью Addon.
	AnnotationChartDigest = "registry.in-cloud.io/chart-digest"

	// LabelNamespaceProject — метка namespace арендатора: проект IAM, под
	// которым регистрируется регистри. Ставит платформа (онбординг).
	LabelNamespaceProject = "kaname.in-cloud.io/project-id"
	// LabelNamespaceAccount — метка namespace арендатора: аккаунт IAM
	// проекта. Ставит платформа.
	LabelNamespaceAccount = "kaname.in-cloud.io/account-id"
)
