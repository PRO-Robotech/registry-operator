// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package kinds — закрытый перечень видов, которые ведёт оператор, и порядок
// пайплайна заказа. Вид цели зашит в шаг: шаблон не выбирает, что создать,
// он заполняет spec заранее известного вида. Для каждого вида — своё правило
// готовности по его status: Lua-проверок Argo CD нет, готовность заказа
// считает оператор сам.
package kinds

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
)

// Kind — вид ресурса, который оператор создаёт и ведёт.
type Kind struct {
	// Step — шаг пайплайна вида.
	Step registryv1alpha1.Step
	// GVK — группа, версия и вид цели.
	GVK schema.GroupVersionKind
	// Resource — имя ресурса API (множественное число).
	Resource string
	// Namespaced — ресурс уровня namespace.
	Namespaced bool
	// StatusKey — ключ вида в .Status контекста следующих шагов.
	StatusKey string
	// Stateful — ресурс держит данные; его spec.deletionPolicy ставит
	// оператор, а не шаблон.
	Stateful bool
	// Ready — правило готовности по живому объекту.
	Ready func(u *unstructured.Unstructured) Result
}

// GroupResource — группа и ресурс вида.
func (k Kind) GroupResource() schema.GroupResource {
	return schema.GroupResource{Group: k.GVK.Group, Resource: k.Resource}
}

// Виды шагов пайплайна.
var (
	S3BucketClaim = Kind{
		Step:       registryv1alpha1.StepStorage,
		GVK:        schema.GroupVersionKind{Group: "vault.in-cloud.io", Version: "v1alpha1", Kind: "S3BucketClaim"},
		Resource:   "s3bucketclaims",
		Namespaced: true,
		StatusKey:  "S3BucketClaim",
		Stateful:   true,
		Ready:      s3BucketClaimReady,
	}
	IamServiceAccount = Kind{
		Step:       registryv1alpha1.StepIdentity,
		GVK:        schema.GroupVersionKind{Group: "iam.kaname.in-cloud.io", Version: "v1alpha1", Kind: "IAMServiceAccount"},
		Resource:   "iamserviceaccounts",
		Namespaced: true,
		StatusKey:  "IamServiceAccount",
		Ready:      iamServiceAccountReady,
	}
	VaultClaim = Kind{
		Step:       registryv1alpha1.StepSecrets,
		GVK:        schema.GroupVersionKind{Group: "vault.in-cloud.io", Version: "v1alpha1", Kind: "VaultClaim"},
		Resource:   "vaultclaims",
		Namespaced: true,
		StatusKey:  "VaultClaim",
		Stateful:   true,
		Ready:      vaultClaimReady,
	}
	IamAccessBinding = Kind{
		Step:       registryv1alpha1.StepAccess,
		GVK:        schema.GroupVersionKind{Group: "iam.kaname.in-cloud.io", Version: "v1alpha1", Kind: "IAMAccessBinding"},
		Resource:   "iamaccessbindings",
		Namespaced: true,
		StatusKey:  "IamAccessBinding",
		Ready:      iamAccessBindingReady,
	}
	Addon = Kind{
		Step:      registryv1alpha1.StepWorkload,
		GVK:       schema.GroupVersionKind{Group: "addons.in-cloud.io", Version: "v1alpha1", Kind: "Addon"},
		Resource:  "addons",
		StatusKey: "Addon",
		Ready:     addonReady,
	}
	HTTPRoute = Kind{
		Step:       registryv1alpha1.StepRoute,
		GVK:        schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"},
		Resource:   "httproutes",
		Namespaced: true,
		StatusKey:  "HTTPRoute",
		Ready:      httpRouteReady,
	}
)

// Виды, которые оператор только читает: рабочая нагрузка Addon и
// Application Argo CD.
var (
	Deployment = Kind{
		Step:       registryv1alpha1.StepWorkload,
		GVK:        schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Resource:   "deployments",
		Namespaced: true,
		Ready:      deploymentAvailable,
	}
	Application = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"}
)

// Stages — пайплайн: шаги одной стадии идут параллельно, следующая стадия
// создаётся только после готовности каждого шага предыдущей. Регистрация
// ресурса в IAM (кортеж, не вид) — шаг первой стадии: её ведёт код
// оператора (RegistrationStage).
var Stages = [][]Kind{
	{S3BucketClaim, VaultClaim, IamServiceAccount},
	{IamAccessBinding, Addon},
	{HTTPRoute},
}

// RegistrationStage — номер стадии, в которой идёт регистрация в IAM.
const RegistrationStage = 0

// TeardownStep — шаг удаления: вид либо регистрация в IAM.
type TeardownStep struct {
	// Kind — вид шага; пусто у шага регистрации.
	Kind Kind
	// Registration — снятие регистрации ресурса регистри и его
	// репозиториев в IAM.
	Registration bool
}

// DeletionOrder — снятие строго в обратном порядке пайплайна, по одному
// шагу: следующий снимается, когда предыдущий исчез. Регистрация в IAM
// снимается после выдачи и до учётки: выдача ссылается на ресурс, а учётка
// — субъект выдачи.
var DeletionOrder = []TeardownStep{
	{Kind: HTTPRoute},
	{Kind: Addon},
	{Kind: IamAccessBinding},
	{Registration: true},
	{Kind: IamServiceAccount},
	{Kind: VaultClaim},
	{Kind: S3BucketClaim},
}

// Written — все виды, которые оператор пишет (для RBAC, наблюдения и
// уборки): ровно шесть.
var Written = []Kind{S3BucketClaim, VaultClaim, IamServiceAccount, IamAccessBinding, Addon, HTTPRoute}

// ForStep — вид шага.
func ForStep(s registryv1alpha1.Step) (Kind, bool) {
	for _, st := range Stages {
		for _, k := range st {
			if k.Step == s {
				return k, true
			}
		}
	}
	return Kind{}, false
}

// FirstNotReadyStage — номер первой стадии, у которой не готов хотя бы один
// шаг, либо len(Stages), если готовы все. ready — готовность по шагу;
// отсутствующий шаг не готов. Шаг регистрации судится в своей стадии.
func FirstNotReadyStage(ready map[registryv1alpha1.Step]bool) int {
	for i, st := range Stages {
		if i == RegistrationStage && !ready[registryv1alpha1.StepRegistration] {
			return i
		}
		for _, k := range st {
			if !ready[k.Step] {
				return i
			}
		}
	}
	return len(Stages)
}

// OptionalClientKind — ресурсы клиента, которые новый сервис сам не заказывает.
func OptionalClientKind(k Kind) bool {
	return k.Step == registryv1alpha1.StepIdentity || k.Step == registryv1alpha1.StepAccess
}
