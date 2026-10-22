// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package controller_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"google.golang.org/grpc/codes"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/kaname"
)

const wait = 30 * time.Second

func registeredInIAM(id, project string) error {
	o, ok := kn.Objects()[kaname.RegistryObject(id)]
	switch {
	case !ok:
		return fmt.Errorf("registry %s is not registered", id)
	case len(o.ParentChain) != 1 || o.ParentChain[0] != kaname.ProjectObject(project) || o.Relation != "project":
		return fmt.Errorf("registry %s registered as %s", id, o)
	}
	return nil
}

// Пайплайн: стадия 1 (бакет, роль Vault, учётка, регистрация в IAM) разом;
// стадия 2 (выдача, Addon) — только после готовности каждого шага стадии 1;
// маршрут — после готовности стадии 2 и рабочей нагрузки; Ready заказа —
// ровно тогда, когда готов каждый шаг.
func TestPipeline_StagesOpenInOrderAndReadyIsTheConjunction(t *testing.T) {
	const ns, id = "pipe01", "pipe01a"
	tenant(t, ns, "prj01", "acc01")
	order(t, ns, id, nil)
	ch := children(ns, id)

	for _, s := range []registryv1alpha1.Step{registryv1alpha1.StepStorage, registryv1alpha1.StepSecrets, registryv1alpha1.StepIdentity} {
		eventually(t, wait, "stage one "+string(s), func() error { return exists(ch[s]) })
	}
	eventually(t, wait, "registration", func() error { return registeredInIAM(id, "prj01") })
	consistently(t, 2*time.Second, "stage two waits for stage one", func() error {
		for _, s := range []registryv1alpha1.Step{registryv1alpha1.StepAccess, registryv1alpha1.StepWorkload, registryv1alpha1.StepRoute} {
			if err := absent(ch[s]); err != nil {
				return err
			}
		}
		return nil
	})

	markReady(t, ch[registryv1alpha1.StepStorage])
	markReady(t, ch[registryv1alpha1.StepSecrets])
	consistently(t, 2*time.Second, "stage two waits for the identity", func() error {
		return absent(ch[registryv1alpha1.StepAccess])
	})
	markReady(t, ch[registryv1alpha1.StepIdentity])
	eventually(t, wait, "access", func() error { return exists(ch[registryv1alpha1.StepAccess]) })
	eventually(t, wait, "workload", func() error { return exists(ch[registryv1alpha1.StepWorkload]) })
	if err := absent(ch[registryv1alpha1.StepRoute]); err != nil {
		t.Fatalf("route before stage two is ready: %v", err)
	}

	// Addon получил пути из статусов готовых шагов и флаг публичности.
	addon, err := get(ch[registryv1alpha1.StepWorkload])
	if err != nil {
		t.Fatal(err)
	}
	vars, _, _ := unstructured.NestedStringMap(addon.Object, "spec", "variables")
	if vars["bucket"] != "b-"+ch[registryv1alpha1.StepStorage].name || vars["vaultRole"] != ch[registryv1alpha1.StepSecrets].name || vars["public"] != "false" {
		t.Fatalf("addon variables %v", vars)
	}

	markReady(t, ch[registryv1alpha1.StepAccess])
	workload(t, ns, id, true)
	markReady(t, ch[registryv1alpha1.StepWorkload])
	eventually(t, wait, "route", func() error { return exists(ch[registryv1alpha1.StepRoute]) })
	consistently(t, time.Second, "not ready before the route", func() error {
		return conditionIs(registry(t, ns, id), registryv1alpha1.ConditionReady, metav1.ConditionFalse)
	})
	markReady(t, ch[registryv1alpha1.StepRoute])
	eventually(t, wait, "registry ready", func() error {
		reg := registry(t, ns, id)
		if reg.Status.Phase != registryv1alpha1.PhaseReady {
			return fmt.Errorf("phase %s", reg.Status.Phase)
		}
		for _, typ := range []string{registryv1alpha1.ConditionStorageReady, registryv1alpha1.ConditionSecretsReady, registryv1alpha1.ConditionIdentityReady,
			registryv1alpha1.ConditionRegistrationReady, registryv1alpha1.ConditionAccessReady, registryv1alpha1.ConditionWorkloadReady, registryv1alpha1.ConditionRouteReady} {
			if err := conditionIs(reg, typ, metav1.ConditionTrue); err != nil {
				return err
			}
		}
		if reg.Status.Registration == nil || !reg.Status.Registration.Ready || reg.Status.Endpoint != "registry.example.test/"+id {
			return fmt.Errorf("registration %+v endpoint %q", reg.Status.Registration, reg.Status.Endpoint)
		}
		return nil
	})
	if n := len(kn.Erased()); n != 0 {
		t.Fatalf("%d registrations erased ancestors", n)
	}

	// Сбой посередине: выдача отвергнута в kaname — заказ больше не Ready,
	// маршрут остаётся, новых шагов не появляется.
	setStatus(t, ch[registryv1alpha1.StepAccess], func(g int64) map[string]any {
		return map[string]any{"observedGeneration": g, "id": "acb1", "conditions": []any{cond("Ready", "False", g)}}
	})
	eventually(t, wait, "degraded after a failure in the middle", func() error {
		reg := registry(t, ns, id)
		if reg.Status.Phase != registryv1alpha1.PhaseDegraded {
			return fmt.Errorf("phase %s", reg.Status.Phase)
		}
		return conditionIs(reg, registryv1alpha1.ConditionAccessReady, metav1.ConditionFalse)
	})
}

// IAM не отвечает — регистрация ждёт, стадия 2 не открывается; IAM
// ответил — пайплайн идёт дальше.
func TestPipeline_RegistrationGatesStageTwo(t *testing.T) {
	const ns, id = "pipe02", "pipe02a"
	tenant(t, ns, "prj02", "acc02")
	kn.FailNext(codes.Unavailable, codes.Unavailable, codes.Unavailable, codes.Unavailable, codes.Unavailable)
	order(t, ns, id, nil)
	ch := children(ns, id)
	for _, s := range []registryv1alpha1.Step{registryv1alpha1.StepStorage, registryv1alpha1.StepSecrets, registryv1alpha1.StepIdentity} {
		eventually(t, wait, "stage one", func() error { return exists(ch[s]) })
		markReady(t, ch[s])
	}
	eventually(t, wait, "registration recovers", func() error { return registeredInIAM(id, "prj02") })
	eventually(t, wait, "stage two after the registration", func() error { return exists(ch[registryv1alpha1.StepAccess]) })
}

// Метки проекта нет — ни одного ресурса; метка появилась — пайплайн идёт.
// Метка сменилась после регистрации — отказ, а не перенос регистри.
func TestPipeline_ProjectComesFromTheNamespaceAndIsPinned(t *testing.T) {
	const ns, id = "pipe03", "pipe03a"
	tenant(t, ns, "", "")
	order(t, ns, id, nil)
	ch := children(ns, id)
	eventually(t, wait, "project unresolved", func() error {
		return conditionIs(registry(t, ns, id), registryv1alpha1.ConditionReady, metav1.ConditionFalse)
	})
	consistently(t, 2*time.Second, "no resources without a project", func() error { return absent(ch[registryv1alpha1.StepStorage]) })

	relabel(t, ns, "prj03")
	eventually(t, wait, "registered under the labeled project", func() error { return registeredInIAM(id, "prj03") })
	eventually(t, wait, "pinned in the reservation", func() error {
		if r := reservation(t, id).Status.Registration; r == nil || r.Project != "prj03" {
			return fmt.Errorf("reservation registration %+v", r)
		}
		return nil
	})

	relabel(t, ns, "prj99")
	eventually(t, wait, "project change is refused", func() error {
		reg := registry(t, ns, id)
		if reg.Status.Phase != registryv1alpha1.PhaseFailed {
			return fmt.Errorf("phase %s", reg.Status.Phase)
		}
		c := conditionIs(reg, registryv1alpha1.ConditionReady, metav1.ConditionFalse)
		if c != nil {
			return c
		}
		for _, x := range reg.Status.Conditions {
			if x.Type == registryv1alpha1.ConditionReady && x.Reason != registryv1alpha1.ReasonProjectChanged {
				return fmt.Errorf("reason %s", x.Reason)
			}
		}
		return nil
	})
	if err := registeredInIAM(id, "prj03"); err != nil {
		t.Fatalf("the registry moved: %v", err)
	}
}

func relabel(t *testing.T, ns, project string) {
	t.Helper()
	eventually(t, 10*time.Second, "relabel "+ns, func() error {
		n := &corev1.Namespace{}
		if err := k8s.Get(ctx(), types.NamespacedName{Name: ns}, n); err != nil {
			return err
		}
		if n.Labels == nil {
			n.Labels = map[string]string{}
		}
		n.Labels[registryv1alpha1.LabelNamespaceProject] = project
		return k8s.Update(ctx(), n)
	})
}

// Резерв id чужого заказа — отказ: ни одного ресурса, ни одного вызова IAM.
func TestPipeline_ReservationOfAnotherOrderIsRefused(t *testing.T) {
	const ns, id = "pipe04", "pipe04a"
	tenant(t, ns, "prj04", "acc04")
	foreign := &registryv1alpha1.RegistryIDReservation{
		ObjectMeta: metav1.ObjectMeta{Name: id},
		Spec:       registryv1alpha1.RegistryIDReservationSpec{RegistryRef: registryv1alpha1.RegistryRef{Namespace: "elsewhere", Name: id, UID: "u-elsewhere"}},
	}
	if err := k8s.Create(ctx(), foreign); err != nil {
		t.Fatal(err)
	}
	order(t, ns, id, nil)
	eventually(t, wait, "refused", func() error {
		reg := registry(t, ns, id)
		if reg.Status.Phase != registryv1alpha1.PhaseFailed {
			return fmt.Errorf("phase %s", reg.Status.Phase)
		}
		return conditionIs(reg, registryv1alpha1.ConditionIDReserved, metav1.ConditionFalse)
	})
	ch := children(ns, id)
	consistently(t, 2*time.Second, "nothing is created", func() error {
		for _, c := range ch {
			if err := absent(c); err != nil {
				return err
			}
		}
		if _, ok := kn.Objects()[kaname.RegistryObject(id)]; ok {
			return fmt.Errorf("registry %s registered under a foreign reservation", id)
		}
		return nil
	})
	// Удаление такого заказа ничего не снимает и резерв не трогает.
	if err := k8s.Delete(ctx(), registry(t, ns, id)); err != nil {
		t.Fatal(err)
	}
	eventually(t, wait, "order gone", func() error {
		err := k8s.Get(ctx(), types.NamespacedName{Namespace: ns, Name: id}, &registryv1alpha1.Registry{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("order still exists: %v", err)
	})
	if r := reservation(t, id); r.Spec.RegistryRef.UID != "u-elsewhere" || r.Status.Phase == registryv1alpha1.ReservationReleased {
		t.Fatalf("foreign reservation touched: %+v", r)
	}
}

// Удаление — строго обратно пайплайну: маршрут → Addon (и его рабочая
// нагрузка) → выдача → регистрация в IAM (регистри, затем репозитории) →
// учётка → роль Vault → бакет → резерв Released. Purge бакета — только в
// Deleting.
func TestDeletion_StrictlyReverseOrder(t *testing.T) {
	const ns, id = "pipe05", "pipe05a"
	tenant(t, ns, "prj05", "acc05")
	order(t, ns, id, func(r *registryv1alpha1.Registry) { r.Spec.DeletionPolicy = registryv1alpha1.DeletionPolicyPurge })
	ch := children(ns, id)
	for _, s := range []registryv1alpha1.Step{registryv1alpha1.StepStorage, registryv1alpha1.StepSecrets, registryv1alpha1.StepIdentity} {
		eventually(t, wait, "stage one", func() error { return exists(ch[s]) })
		hold(t, ch[s])
		markReady(t, ch[s])
	}
	eventually(t, wait, "stage two", func() error { return exists(ch[registryv1alpha1.StepWorkload]) })
	for _, s := range []registryv1alpha1.Step{registryv1alpha1.StepAccess, registryv1alpha1.StepWorkload} {
		eventually(t, wait, "stage two", func() error { return exists(ch[s]) })
		hold(t, ch[s])
	}
	markReady(t, ch[registryv1alpha1.StepAccess])
	workload(t, ns, id, true)
	markReady(t, ch[registryv1alpha1.StepWorkload])
	eventually(t, wait, "route", func() error { return exists(ch[registryv1alpha1.StepRoute]) })
	hold(t, ch[registryv1alpha1.StepRoute])
	markReady(t, ch[registryv1alpha1.StepRoute])
	eventually(t, wait, "ready", func() error {
		return conditionIs(registry(t, ns, id), registryv1alpha1.ConditionReady, metav1.ConditionTrue)
	})
	// Purge до удаления не ставится.
	if s3, _ := get(ch[registryv1alpha1.StepStorage]); s3 != nil {
		if p, _, _ := unstructured.NestedString(s3.Object, "spec", "deletionPolicy"); p != "Retain" {
			t.Fatalf("bucket deletionPolicy %q before the order is deleted", p)
		}
	}
	// Репозиторий в описи — его регистрацию снятие обязано снять.
	addRepository(t, registry(t, ns, id), "team/app")

	if err := k8s.Delete(ctx(), registry(t, ns, id)); err != nil {
		t.Fatal(err)
	}
	later := func(steps ...registryv1alpha1.Step) func() error {
		return func() error {
			for _, s := range steps {
				if err := notTerminating(ch[s]); err != nil {
					return err
				}
			}
			return nil
		}
	}
	all := []registryv1alpha1.Step{registryv1alpha1.StepRoute, registryv1alpha1.StepWorkload, registryv1alpha1.StepAccess,
		registryv1alpha1.StepIdentity, registryv1alpha1.StepSecrets, registryv1alpha1.StepStorage}
	for i, s := range all {
		if s == registryv1alpha1.StepStorage {
			// Purge ставится только теперь; снятие ждёт, пока контроллер
			// бакета увидит новое поколение.
			eventually(t, wait, "purge requested in Deleting", func() error {
				s3, err := get(ch[s])
				if err != nil {
					return err
				}
				if p, _, _ := unstructured.NestedString(s3.Object, "spec", "deletionPolicy"); p != "Purge" {
					return fmt.Errorf("deletionPolicy %q", p)
				}
				return notTerminating(ch[s])
			})
			markReady(t, ch[s])
		}
		eventually(t, wait, "deleting "+string(s), func() error { return terminating(ch[s]) })
		consistently(t, time.Second, "later steps wait while "+string(s)+" is deleted", later(all[i+1:]...))
		if s == registryv1alpha1.StepIdentity {
			// Регистрация снята раньше учётки: и регистри, и репозиторий.
			if _, ok := kn.Objects()[kaname.RegistryObject(id)]; ok {
				t.Fatal("identity is deleted while the registry is still registered")
			}
			if _, ok := kn.Objects()[kaname.RepositoryObject(id, "team/app")]; ok {
				t.Fatal("identity is deleted while a repository is still registered")
			}
		}
		if s == registryv1alpha1.StepAccess {
			if err := registeredInIAM(id, "prj05"); err != nil {
				t.Fatalf("registration removed before the access binding: %v", err)
			}
		}
		if s == registryv1alpha1.StepStorage {
			s3, _ := get(ch[s])
			if p, _, _ := unstructured.NestedString(s3.Object, "spec", "deletionPolicy"); p != "Purge" {
				t.Fatalf("bucket deleted with deletionPolicy %q, the order asks for Purge", p)
			}
		}
		release(t, ch[s])
		eventually(t, wait, string(s)+" gone", func() error { return absent(ch[s]) })
		if s == registryv1alpha1.StepWorkload {
			consistently(t, time.Second, "access waits for the workload", later(registryv1alpha1.StepAccess))
			d := &appsv1.Deployment{}
			if err := k8s.Get(ctx(), types.NamespacedName{Namespace: ns, Name: conf.Policy.ResourceName(id, "")}, d); err != nil {
				t.Fatal(err)
			}
			if err := k8s.Delete(ctx(), d); err != nil {
				t.Fatal(err)
			}
		}
	}
	eventually(t, wait, "reservation released", func() error {
		r := reservation(t, id)
		if r.Status.Phase != registryv1alpha1.ReservationReleased || r.Status.Registration != nil || len(r.Status.Repositories) != 0 {
			return fmt.Errorf("reservation %+v", r.Status)
		}
		return nil
	})
	eventually(t, wait, "order gone", func() error {
		err := k8s.Get(ctx(), types.NamespacedName{Namespace: ns, Name: id}, &registryv1alpha1.Registry{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("order still exists: %v", err)
	})
	// Снятие в IAM — регистри раньше репозиториев.
	var unreg []string
	for _, c := range kn.Calls() {
		if c.Method == "Unregister" && (c.Object == kaname.RegistryObject(id) || c.Object == kaname.RepositoryObject(id, "team/app")) {
			unreg = append(unreg, c.Object)
		}
	}
	if !slices.Equal(unreg, []string{kaname.RegistryObject(id), kaname.RepositoryObject(id, "team/app")}) {
		t.Fatalf("unregistration order %v", unreg)
	}
	// id не выдаётся повторно: новый заказ с тем же id отвергнут.
	order2 := &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: ns},
		Spec:       registryv1alpha1.RegistrySpec{ID: id, TemplateRef: registryv1alpha1.TemplateRef{Name: "zot", Version: "v1"}},
	}
	if err := k8s.Create(ctx(), order2); err != nil {
		t.Fatal(err)
	}
	eventually(t, wait, "released id is not reissued", func() error {
		return conditionIs(registry(t, ns, id), registryv1alpha1.ConditionIDReserved, metav1.ConditionFalse)
	})
}

// addRepository — запись в опись, как её оставил бы эндпоинт заявок.
func addRepository(t *testing.T, reg *registryv1alpha1.Registry, repo string) {
	t.Helper()
	id := reg.Spec.ID
	eventually(t, 10*time.Second, "ledger "+repo, func() error {
		res := reservation(t, id)
		res.Status.Repositories = append(res.Status.Repositories, registryv1alpha1.RegisteredRepository{
			Name: repo, EnrolledBy: "service_account:sva1", EnrolledAt: metav1.Now(), Registered: true,
		})
		return k8s.Status().Update(ctx(), res)
	})
	rec := reservation(t, id).Status.Registration
	if err := kn.Direct().Register(ctx(), kaname.RepositoryRegistration(id, repo,
		kaname.Scope{Project: rec.Project, Account: rec.Account}, kaname.Version(reg.CreationTimestamp.Time, reg.Generation))); err != nil {
		t.Fatal(err)
	}
}
