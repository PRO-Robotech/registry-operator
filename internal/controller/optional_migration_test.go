// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package controller_test

import (
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/samples"
)

// Переход 6→4 не является неявным приказом удалить клиентские credentials.
func TestOptionalPairMigrationRefusesOwnedIdentity(t *testing.T) {
	const ns, id = "pairmigration", "pairmig01"
	tenant(t, ns, "prj01", "acc01")
	order(t, ns, id, nil)
	identity := children(ns, id)[api.StepIdentity]
	eventually(t, wait, "owned identity exists", func() error { return exists(identity) })
	before, err := get(identity)
	if err != nil {
		t.Fatal(err)
	}
	set, err := samples.Load(samplesDir)
	if err != nil {
		t.Fatal(err)
	}
	service := set.RegistryTemplate.DeepCopy()
	service.Name = "pair-service-v1"
	service.Spec.TemplateName = "pair-service"
	service.Spec.Templates.IamServiceAccount = nil
	service.Spec.Templates.IamAccessBinding = nil
	if err := k8s.Create(ctx(), service); err != nil {
		t.Fatal(err)
	}
	eventually(t, wait, "switch template", func() error {
		r := registry(t, ns, id)
		r.Spec.TemplateRef = api.TemplateRef{Name: "pair-service", Version: "v1"}
		return k8s.Update(ctx(), r)
	})
	eventually(t, wait, "migration refused", func() error {
		c := meta.FindStatusCondition(registry(t, ns, id).Status.Conditions, api.ConditionReady)
		if c == nil || c.Status != metav1.ConditionFalse || c.Reason != api.ReasonPolicyViolation {
			return fmt.Errorf("condition %v", c)
		}
		return nil
	})
	consistently(t, 2*time.Second, "identity remains owned and unchanged", func() error {
		now, err := get(identity)
		if err != nil {
			return err
		}
		if now.GetUID() != before.GetUID() || now.GetDeletionTimestamp() != nil {
			return fmt.Errorf("identity was replaced or deleted")
		}
		return nil
	})
}
