// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package ledger_test

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/ledger"
)

func newClient(t *testing.T, conflicts int, writes *int) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := registryv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	res := &registryv1alpha1.RegistryIDReservation{
		ObjectMeta: metav1.ObjectMeta{Name: "abc123"},
		Spec:       registryv1alpha1.RegistryIDReservationSpec{RegistryRef: registryv1alpha1.RegistryRef{Namespace: "cust01", Name: "abc123", UID: "u"}},
		Status:     registryv1alpha1.RegistryIDReservationStatus{Phase: registryv1alpha1.ReservationActive},
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(res).WithStatusSubresource(res).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				*writes++
				if conflicts > 0 {
					conflicts--
					return apierrors.NewConflict(schema.GroupResource{Group: "registry.in-cloud.io", Resource: "registryidreservations"}, obj.GetName(), nil)
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).Build()
}

// Конфликт версий — повтор с чтения; изменение применяется один раз.
func TestMutate_RetriesOnAVersionConflict(t *testing.T) {
	writes := 0
	c := newClient(t, 2, &writes)
	s := &ledger.Store{Client: c, Reader: c}
	calls := 0
	res, err := s.Mutate(context.Background(), "abc123", func(r *registryv1alpha1.RegistryIDReservation) error {
		calls++
		r.Status.Repositories = append(r.Status.Repositories, registryv1alpha1.RegisteredRepository{Name: "team/app", EnrolledAt: metav1.Now()})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || writes != 3 || len(res.Status.Repositories) != 1 {
		t.Fatalf("calls %d writes %d repositories %d", calls, writes, len(res.Status.Repositories))
	}
	cur := &registryv1alpha1.RegistryIDReservation{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "abc123"}, cur); err != nil {
		t.Fatal(err)
	}
	if len(cur.Status.Repositories) != 1 {
		t.Fatalf("stored %d repositories", len(cur.Status.Repositories))
	}
}

// Изменение не нужно — записи нет.
func TestMutate_NoChangeWritesNothing(t *testing.T) {
	writes := 0
	c := newClient(t, 0, &writes)
	s := &ledger.Store{Client: c, Reader: c}
	if _, err := s.Mutate(context.Background(), "abc123", func(*registryv1alpha1.RegistryIDReservation) error { return ledger.ErrNoChange }); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatalf("%d writes for no change", writes)
	}
}
