// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package desired собирает желаемый объект шага: рендер observed-шаблона в
// закрытом контексте, поля, которые ставит только оператор (имя, namespace,
// метки, политика данных, пин чарта, переменные заказа), и проверка
// политикой площадки. Объект, не прошедший политику, наружу не выходит.
package desired

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	observedv1alpha1 "github.com/PRO-Robotech/registry-operator/api/observed/v1alpha1"
	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/kinds"
	"github.com/PRO-Robotech/registry-operator/internal/policy"
	"github.com/PRO-Robotech/registry-operator/internal/render"
)

// Input — заказ и всё, что выведено для него до рендера шага.
type Input struct {
	Registry *registryv1alpha1.Registry
	// Template — ревизия RegistryTemplate (её имя — в метке объектов).
	Template registryv1alpha1.TemplateRef
	// Params — константы площадки ревизии.
	Params map[string]any
	// Status — готовые ресурсы предыдущих шагов.
	Status map[string]render.Child
	// SyncEpoch — эпоха синхронизации рабочей нагрузки.
	SyncEpoch int64
	// Names — имена ресурсов заказа.
	Names  Names
	Policy *policy.Config
}

// Names — имена и namespace ресурсов заказа, выведенные из ревизии.
type Names struct {
	Storage        string
	ServiceAccount string
	Secrets        string
	AccessBinding  string
	Addon          string
	Route          string
	RouteNamespace string
	// Workload — имя релиза и рабочей нагрузки zot (Deployment, Service,
	// k8s ServiceAccount): на него привязана роль Vault.
	Workload string
}

// NamesFor выводит имена ресурсов заказа из политики и шаблонов ревизии.
func NamesFor(p *policy.Config, id, login string, sa *observedv1alpha1.ObservedIamServiceAccountTemplate,
	ab *observedv1alpha1.ObservedIamAccessBindingTemplate, route *observedv1alpha1.ObservedHTTPRouteTemplate,
) Names {
	n := Names{
		Storage:        p.ResourceName(id, ""),
		ServiceAccount: p.ResourceName(id, sa.Spec.NameSuffix),
		Secrets:        p.ResourceName(id, ""),
		AccessBinding:  p.ResourceName(id, ab.Spec.NameSuffix),
		Addon:          p.AddonName(id),
		Route:          p.ResourceName(id, ""),
		RouteNamespace: route.Spec.TargetNamespace,
		Workload:       p.ResourceName(id, ""),
	}
	if n.RouteNamespace == "" {
		n.RouteNamespace = login
	}
	return n
}

// ObjectKey — имя и namespace объекта шага.
func (n Names) ObjectKey(k kinds.Kind, login string) (name, namespace string) {
	switch k.Step {
	case registryv1alpha1.StepStorage:
		return n.Storage, login
	case registryv1alpha1.StepIdentity:
		return n.ServiceAccount, login
	case registryv1alpha1.StepSecrets:
		return n.Secrets, login
	case registryv1alpha1.StepAccess:
		return n.AccessBinding, login
	case registryv1alpha1.StepWorkload:
		return n.Addon, ""
	case registryv1alpha1.StepRoute:
		return n.Route, n.RouteNamespace
	}
	return "", ""
}

// RenderError — шаблон не отрисовался для заказа (код RenderFailed).
type RenderError struct{ Err error }

func (e *RenderError) Error() string { return "render: " + e.Err.Error() }
func (e *RenderError) Unwrap() error { return e.Err }

// PolicyError — объект нарушает политику площадки (код PolicyViolation).
type PolicyError struct{ Err error }

func (e *PolicyError) Error() string { return e.Err.Error() }
func (e *PolicyError) Unwrap() error { return e.Err }

// IsRender, IsPolicy — классы отказа сборки.
func IsRender(err error) bool { var e *RenderError; return errors.As(err, &e) }

// IsPolicy — объект нарушает политику.
func IsPolicy(err error) bool { var e *PolicyError; return errors.As(err, &e) }

// Labels — метки владения объекта заказа.
func Labels(reg *registryv1alpha1.Registry, tpl registryv1alpha1.TemplateRef, step registryv1alpha1.Step) map[string]string {
	return map[string]string{
		registryv1alpha1.LabelManagedBy: registryv1alpha1.ManagedByValue,
		registryv1alpha1.LabelID:        reg.Spec.ID,
		registryv1alpha1.LabelNamespace: reg.Namespace,
		registryv1alpha1.LabelOwnerUID:  string(reg.UID),
		registryv1alpha1.LabelTemplate:  tpl.ObjectName(),
		registryv1alpha1.LabelStep:      string(step),
	}
}

// Order — сверяемое политикой из входа.
func (in Input) Order(chart policy.Chart) policy.Order {
	return policy.Order{
		ID:                 in.Registry.Spec.ID,
		Login:              in.Registry.Namespace,
		UID:                string(in.Registry.UID),
		SyncEpoch:          in.SyncEpoch,
		ServiceAccountName: in.Names.ServiceAccount,
		RouteNamespace:     in.Names.RouteNamespace,
		Public:             in.Registry.Spec.Public,
		Chart:              chart,
	}
}

func (in Input) context() render.Context {
	reg := in.Registry
	dp := string(reg.Spec.DeletionPolicy)
	if dp == "" {
		dp = string(registryv1alpha1.DeletionPolicyRetain)
	}
	status := in.Status
	if status == nil {
		status = map[string]render.Child{}
	}
	return render.Context{
		Registry: render.Registry{
			Name: reg.Name, Namespace: reg.Namespace, UID: string(reg.UID),
			DeletionPolicy: dp, SyncEpoch: in.SyncEpoch,
		},
		ID:       reg.Spec.ID,
		Login:    reg.Namespace,
		Params:   in.Params,
		Template: render.TemplateInfo{Name: in.Template.Name, Version: in.Template.Version},
		Status:   status,
	}
}

// Build отрисовывает шаблон шага и собирает итоговый объект. Ошибка —
// *RenderError либо *PolicyError.
func Build(k kinds.Kind, tpl observedv1alpha1.Template, in Input) (*unstructured.Unstructured, error) {
	t, err := render.Compile(tpl.GetName(), tpl.Body().Value)
	if err != nil {
		return nil, &RenderError{Err: err}
	}
	spec, err := t.Execute(in.context())
	if err != nil {
		return nil, &RenderError{Err: err}
	}
	if err := policy.CheckRendered(k, spec); err != nil {
		return nil, &PolicyError{Err: err}
	}
	login := in.Registry.Namespace
	name, namespace := in.Names.ObjectKey(k, login)
	var chart policy.Chart
	annotations := map[string]string{}
	switch k.GVK {
	case kinds.S3BucketClaim.GVK, kinds.VaultClaim.GVK:
		spec["deletionPolicy"] = string(registryv1alpha1.DeletionPolicyRetain)
	case kinds.IamServiceAccount.GVK:
		spec["name"] = name
		spec["deletionPolicy"] = "Delete"
	case kinds.IamAccessBinding.GVK:
		spec["deletionPolicy"] = "Delete"
	case kinds.Addon.GVK:
		at, ok := tpl.(*observedv1alpha1.ObservedAddonTemplate)
		if !ok {
			return nil, &PolicyError{Err: fmt.Errorf("workload step needs an ObservedAddonTemplate, got %T", tpl)}
		}
		if err := policy.RenderedVariablesFree(spec); err != nil {
			return nil, &PolicyError{Err: err}
		}
		pin := at.Spec.Chart
		chart = policy.Chart{RepoURL: pin.RepoURL, Name: pin.Name, Version: pin.Version, Digest: pin.Digest}
		spec["repoURL"], spec["chart"], spec["version"] = pin.RepoURL, pin.Name, pin.Version
		spec["releaseName"] = in.Names.Workload
		spec["targetNamespace"] = login
		vars, _ := spec["variables"].(map[string]any)
		if vars == nil {
			vars = map[string]any{}
		}
		for key, val := range in.Order(chart).OrderVariableValues() {
			vars[key] = val
		}
		spec["variables"] = vars
		annotations[registryv1alpha1.AnnotationChartDigest] = pin.Digest
	case kinds.HTTPRoute.GVK:
	default:
		return nil, &PolicyError{Err: fmt.Errorf("kind %s is not rendered from a template", k.GVK)}
	}
	u := newObject(k, name, namespace, Labels(in.Registry, in.Template, k.Step), annotations, spec)
	if err := in.Policy.Check(k, in.Order(chart), u); err != nil {
		return nil, &PolicyError{Err: err}
	}
	return u, nil
}

func newObject(k kinds.Kind, name, namespace string, labels, annotations map[string]string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(k.GVK)
	u.SetName(name)
	if k.Namespaced {
		u.SetNamespace(namespace)
	}
	u.SetLabels(labels)
	if len(annotations) > 0 {
		u.SetAnnotations(annotations)
	}
	u.Object["spec"] = spec
	return u
}
