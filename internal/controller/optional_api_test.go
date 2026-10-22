// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/kinds"
)

// mapperError отделяет отсутствие вида от отказа discovery.
type mapperError struct {
	meta.RESTMapper
	err error
}

func (m mapperError) RESTMapping(schema.GroupKind, ...string) (*meta.RESTMapping, error) {
	return nil, m.err
}

func TestOptionalIAMDiscoveryDoesNotRequireChildCRDs(t *testing.T) {
	mapper := meta.NewDefaultRESTMapper(nil)
	for _, k := range kinds.Written {
		if kinds.OptionalClientKind(k) {
			continue
		}
		scope := meta.RESTScopeNamespace
		if !k.Namespaced {
			scope = meta.RESTScopeRoot
		}
		mapper.Add(k.GVK, scope)
	}
	got, err := AvailableWritten(mapper)
	if err != nil || len(got) != 4 {
		t.Fatalf("four mandatory APIs: %d, %v", len(got), err)
	}
	if len(CacheByObject(got...)) != 4 {
		t.Fatal("cache includes absent IAM API")
	}
	for _, k := range got {
		if kinds.OptionalClientKind(k) {
			t.Fatal("absent IAM API watched")
		}
	}
	denied := errors.New("discovery forbidden")
	if _, err := AvailableWritten(mapperError{mapper, denied}); !errors.Is(err, denied) {
		t.Fatal("discovery error masked")
	}
	if _, err := AvailableWritten(meta.NewDefaultRESTMapper(nil)); !meta.IsNoMatchError(err) {
		t.Fatal("missing mandatory API ignored")
	}
}

func TestOptionalReferencesDeepCopyDoesNotMutateRevision(t *testing.T) {
	r := &registryv1alpha1.RegistryTemplate{}
	r.Spec.Templates.IamServiceAccount = &registryv1alpha1.TemplateRef{Name: "robot", Version: "v1"}
	r.Spec.Templates.IamAccessBinding = &registryv1alpha1.TemplateRef{Name: "grant", Version: "v1"}
	copy := r.DeepCopy()
	copy.Spec.Templates.IamServiceAccount.Name = "other"
	copy.Spec.Templates.IamAccessBinding.Version = "v2"
	if r.Spec.Templates.IamServiceAccount.Name != "robot" || r.Spec.Templates.IamAccessBinding.Version != "v1" {
		t.Fatal("optional references aliased immutable revision")
	}
}

// failedIAMReader моделирует исчезнувший API и отказ RBAC, а не пустой список.
type failedIAMReader struct {
	client.Reader
	err error
}

func (r failedIAMReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return r.err
}

func TestPartialPassPreservesIAMCleanupEvidence(t *testing.T) {
	for _, step := range []registryv1alpha1.Step{registryv1alpha1.StepIdentity, registryv1alpha1.StepAccess} {
		t.Run(string(step), func(t *testing.T) {
			k, _ := kinds.ForStep(step)
			reg := &registryv1alpha1.Registry{}
			reg.Status.Resources = []registryv1alpha1.ResourceStatus{{Step: step, Name: "prior-owned", Ready: true}}
			p := newPass(reg, time.Now())
			p.templateResolved, p.clientIdentity = true, true
			p.resources = []registryv1alpha1.ResourceStatus{{Step: registryv1alpha1.StepStorage, Name: "bucket"}}
			p.wait(registryv1alpha1.ReasonDependencyUnavailable, "IAM API disappeared")
			p.compose()
			if len(reg.Status.Resources) != 2 || reg.Status.Resources[1].Step != step || reg.Status.Resources[1].Ready {
				t.Fatal("partial normal pass erased cleanup evidence or retained stale Ready")
			}
			r := &RegistryReconciler{Deps: &Deps{APIReader: failedIAMReader{err: &meta.NoKindMatchError{GroupKind: k.GVK.GroupKind()}}}}
			// И teardown, и guard перехода 6→4 вызывают этот фактический reader.
			_, err := r.listOwned(context.Background(), k, reg)
			var w *waiting
			if !errors.As(err, &w) || w.reason != registryv1alpha1.ReasonDependencyUnavailable {
				t.Fatal("missing API treated as cleanup proof")
			}
			deleting := newPass(reg, time.Now())
			deleting.deleting = true
			deleting.compose()
			if len(reg.Status.Resources) != 2 {
				t.Fatal("delete pass erased evidence")
			}
			// Законный новый service-only заказ без IAM history не блокируется.
			if _, err := r.listOwned(context.Background(), k, &registryv1alpha1.Registry{}); err != nil {
				t.Fatal(err)
			}
			forbidden := apierrors.NewForbidden(k.GVK.GroupVersion().WithResource(k.Resource).GroupResource(), "", errors.New("denied"))
			r.APIReader = failedIAMReader{err: forbidden}
			if _, err := r.listOwned(context.Background(), k, &registryv1alpha1.Registry{}); !apierrors.IsForbidden(err) {
				t.Fatal("Forbidden masked as absent optional API")
			}
		})
	}
}
