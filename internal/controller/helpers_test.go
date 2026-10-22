// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package controller_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/kinds"
)

// holdFinalizer — финализатор проб: держит дочерний объект в Terminating,
// пока проба не отпустит его, — так виден порядок снятия.
const holdFinalizer = "test.registry.in-cloud.io/hold"

// eventually повторяет проверку до успеха или срока.
func eventually(t *testing.T, timeout time.Duration, what string, f func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var err error
	for {
		if err = f(); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not reached within %s: %v", what, timeout, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// consistently держит проверку истинной весь срок.
func consistently(t *testing.T, d time.Duration, what string, f func() error) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if err := f(); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func ctx() context.Context { return context.Background() }

// order — заказ регистри id в namespace ns.
func order(t *testing.T, ns, id string, edit func(*registryv1alpha1.Registry)) *registryv1alpha1.Registry {
	t.Helper()
	reg := &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: ns},
		Spec:       registryv1alpha1.RegistrySpec{ID: id, TemplateRef: registryv1alpha1.TemplateRef{Name: "zot", Version: "v1"}},
	}
	if edit != nil {
		edit(reg)
	}
	if err := k8s.Create(ctx(), reg); err != nil {
		t.Fatal(err)
	}
	return reg
}

// child — ожидаемый объект шага заказа id в ns.
type child struct {
	kind kinds.Kind
	ns   string
	name string
}

func children(ns, id string) map[registryv1alpha1.Step]child {
	p := &conf.Policy
	return map[registryv1alpha1.Step]child{
		registryv1alpha1.StepStorage:  {kinds.S3BucketClaim, ns, p.ResourceName(id, "")},
		registryv1alpha1.StepSecrets:  {kinds.VaultClaim, ns, p.ResourceName(id, "")},
		registryv1alpha1.StepIdentity: {kinds.IamServiceAccount, ns, p.ResourceName(id, "-ci")},
		registryv1alpha1.StepAccess:   {kinds.IamAccessBinding, ns, p.ResourceName(id, "-ci-pusher")},
		registryv1alpha1.StepWorkload: {kinds.Addon, "", p.AddonName(id)},
		registryv1alpha1.StepRoute:    {kinds.HTTPRoute, "registry-edge", p.ResourceName(id, "")},
	}
}

func get(c child) (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(c.kind.GVK)
	err := k8s.Get(ctx(), types.NamespacedName{Namespace: c.ns, Name: c.name}, u)
	return u, err
}

func exists(c child) error {
	_, err := get(c)
	return err
}

func absent(c child) error {
	_, err := get(c)
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return err
	}
	return fmt.Errorf("%s %s/%s exists", c.kind.GVK.Kind, c.ns, c.name)
}

func terminating(c child) error {
	u, err := get(c)
	if err != nil {
		return err
	}
	if u.GetDeletionTimestamp() == nil {
		return fmt.Errorf("%s %s/%s is not being deleted", c.kind.GVK.Kind, c.ns, c.name)
	}
	return nil
}

func notTerminating(c child) error {
	u, err := get(c)
	if err != nil {
		return err
	}
	if u.GetDeletionTimestamp() != nil {
		return fmt.Errorf("%s %s/%s is being deleted out of order", c.kind.GVK.Kind, c.ns, c.name)
	}
	return nil
}

func cond(typ, status string, gen int64) map[string]any {
	return map[string]any{
		"type": typ, "status": status, "reason": "Test", "message": "",
		"lastTransitionTime": metav1.Now().UTC().Format(time.RFC3339), "observedGeneration": gen,
	}
}

// setStatus — статус объекта, как его вывел бы его контроллер.
func setStatus(t *testing.T, c child, status func(gen int64) map[string]any) {
	t.Helper()
	eventually(t, 10*time.Second, "status of "+c.name, func() error {
		u, err := get(c)
		if err != nil {
			return err
		}
		u.Object["status"] = status(u.GetGeneration())
		return k8s.Status().Update(ctx(), u)
	})
}

func markReady(t *testing.T, c child) {
	t.Helper()
	switch c.kind.GVK {
	case kinds.S3BucketClaim.GVK:
		setStatus(t, c, func(g int64) map[string]any {
			return map[string]any{"phase": "Ready", "observedGeneration": g, "bucketName": "b-" + c.name, "endpoint": "https://s3.example.test",
				"conditions": []any{cond("KeysWritten", "True", g), cond("Ready", "True", g)}}
		})
	case kinds.VaultClaim.GVK:
		setStatus(t, c, func(g int64) map[string]any {
			return map[string]any{"phase": "Ready", "observedGeneration": g, "vault": map[string]any{"authMountPath": "kubernetes-site-a"},
				"conditions": []any{cond("RolesApplied", "True", g), cond("Ready", "True", g)}}
		})
	case kinds.IamServiceAccount.GVK:
		setStatus(t, c, func(g int64) map[string]any {
			return map[string]any{"observedGeneration": g, "id": "sva" + c.name[4:10],
				"conditions": []any{cond("Ready", "True", g), cond("KeysReady", "True", g)}}
		})
	case kinds.IamAccessBinding.GVK:
		setStatus(t, c, func(g int64) map[string]any {
			return map[string]any{"observedGeneration": g, "id": "acb" + c.name[4:10], "conditions": []any{cond("Ready", "True", g)}}
		})
	case kinds.Addon.GVK:
		setStatus(t, c, func(g int64) map[string]any {
			return map[string]any{"observedGeneration": g,
				"conditions": []any{cond("Synced", "True", g), cond("Healthy", "True", g), cond("Ready", "True", g)}}
		})
	case kinds.HTTPRoute.GVK:
		setStatus(t, c, func(g int64) map[string]any {
			return map[string]any{"parents": []any{map[string]any{
				"parentRef":      map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "namespace": "registry-edge", "name": "registry-gw"},
				"controllerName": "example.test/gateway",
				"conditions":     []any{cond("Accepted", "True", g), cond("ResolvedRefs", "True", g)},
			}}}
		})
	default:
		t.Fatalf("no ready status for %s", c.kind.GVK)
	}
}

// workload — Deployment zot-<id>, как его поставил бы Argo CD.
func workload(t *testing.T, ns, id string, available bool) {
	t.Helper()
	one := int32(1)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: conf.Policy.ResourceName(id, ""), Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "zot"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "zot"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "zot", Image: "zot"}}},
			},
		},
	}
	if err := k8s.Create(ctx(), d); err != nil {
		t.Fatal(err)
	}
	if !available {
		return
	}
	eventually(t, 10*time.Second, "workload status", func() error {
		cur := &appsv1.Deployment{}
		if err := k8s.Get(ctx(), client.ObjectKeyFromObject(d), cur); err != nil {
			return err
		}
		cur.Status = appsv1.DeploymentStatus{
			ObservedGeneration: cur.Generation, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1, ReadyReplicas: 1,
			Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue}},
		}
		return k8s.Status().Update(ctx(), cur)
	})
}

// hold — финализатор проб на дочернем объекте.
func hold(t *testing.T, c child) {
	t.Helper()
	eventually(t, 10*time.Second, "hold "+c.name, func() error {
		u, err := get(c)
		if err != nil {
			return err
		}
		controllerutil.AddFinalizer(u, holdFinalizer)
		return k8s.Update(ctx(), u)
	})
}

// release снимает финализатор проб.
func release(t *testing.T, c child) {
	t.Helper()
	eventually(t, 10*time.Second, "release "+c.name, func() error {
		u, err := get(c)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		controllerutil.RemoveFinalizer(u, holdFinalizer)
		return k8s.Update(ctx(), u)
	})
}

func registry(t *testing.T, ns, id string) *registryv1alpha1.Registry {
	t.Helper()
	reg := &registryv1alpha1.Registry{}
	if err := k8s.Get(ctx(), types.NamespacedName{Namespace: ns, Name: id}, reg); err != nil {
		t.Fatal(err)
	}
	return reg
}

func conditionIs(reg *registryv1alpha1.Registry, typ string, status metav1.ConditionStatus) error {
	c := meta.FindStatusCondition(reg.Status.Conditions, typ)
	if c == nil || c.Status != status {
		return fmt.Errorf("condition %s is %v, want %s (phase %s)", typ, c, status, reg.Status.Phase)
	}
	return nil
}

func reservation(t *testing.T, id string) *registryv1alpha1.RegistryIDReservation {
	t.Helper()
	res := &registryv1alpha1.RegistryIDReservation{}
	if err := k8s.Get(ctx(), types.NamespacedName{Name: id}, res); err != nil {
		t.Fatal(err)
	}
	return res
}
