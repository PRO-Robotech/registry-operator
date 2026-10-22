// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/kaname"
	"github.com/PRO-Robotech/registry-operator/internal/ledger"
	"github.com/PRO-Robotech/registry-operator/internal/metrics"
)

// resolveScope — проект и аккаунт IAM заказа. Источник — метки namespace
// заказа (ставит платформа); первая регистрация закрепляет проект в резерве,
// и дальше действует закреплённый: перенос регистри под другой проект — не
// следствие смены метки.
func (r *RegistryReconciler) resolveScope(ctx context.Context, p *pass) (kaname.Scope, error) {
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: p.reg.Namespace}, ns); err != nil {
		return kaname.Scope{}, err
	}
	labeled := kaname.Scope{
		Project: ns.Labels[registryv1alpha1.LabelNamespaceProject],
		Account: ns.Labels[registryv1alpha1.LabelNamespaceAccount],
	}
	if rec := p.reservation.Status.Registration; rec != nil {
		if labeled.Project != rec.Project {
			return kaname.Scope{}, &refusal{reason: registryv1alpha1.ReasonProjectChanged,
				msg: fmt.Sprintf("registry is registered under project %s; namespace %s names %q", rec.Project, ns.Name, labeled.Project)}
		}
		return kaname.Scope{Project: rec.Project, Account: rec.Account}, nil
	}
	if !kaname.ValidID(labeled.Project) {
		return kaname.Scope{}, &waiting{reason: registryv1alpha1.ReasonProjectUnresolved,
			msg: fmt.Sprintf("namespace %s carries no valid %s label", ns.Name, registryv1alpha1.LabelNamespaceProject)}
	}
	if labeled.Account != "" && !kaname.ValidID(labeled.Account) {
		return kaname.Scope{}, &waiting{reason: registryv1alpha1.ReasonProjectUnresolved,
			msg: fmt.Sprintf("namespace %s carries an invalid %s label", ns.Name, registryv1alpha1.LabelNamespaceAccount)}
	}
	return labeled, nil
}

// registryVersion — монотонная версия ресурса регистри заказа.
func registryVersion(reg *registryv1alpha1.Registry) time.Time {
	return kaname.Version(reg.CreationTimestamp.Time, reg.Generation)
}

func (r *RegistryReconciler) reservations() *ledger.Store {
	return &ledger.Store{Client: r.Client, Reader: r.APIReader}
}

// ensureRegistration — шаг регистрации: ресурс регистри записан в IAM с
// цепью [проект]. Запись — только под активным резервом этого заказа: id
// резерва и uid заказа сверены в reserve, здесь — повторно по свежему
// чтению. Повтор не чаще темпа готового заказа: kaname сам отличает
// повтор, но лишний вызов на каждый проход не нужен.
func (r *RegistryReconciler) ensureRegistration(ctx context.Context, p *pass, scope kaname.Scope) (bool, error) {
	reg := p.reg
	obj := kaname.RegistryObject(reg.Spec.ID)
	rec := p.reservation.Status.Registration
	fresh := rec != nil && rec.RegisteredAt != nil && rec.Generation == reg.Generation &&
		p.now.Sub(rec.RegisteredAt.Time) < r.Config.Intervals.Ready.Duration
	if fresh && allRepositoriesRegistered(p.reservation) {
		p.registration = &registryv1alpha1.RegistrationStatus{Object: obj, Ready: true, Reason: registryv1alpha1.ReasonRegistered}
		return true, nil
	}

	// Намерение — до вызова: проект закреплён раньше, чем запись в IAM
	// может состояться, и снятие всегда знает, под чем снимать.
	res, err := r.reservations().Mutate(ctx, reg.Spec.ID, func(res *registryv1alpha1.RegistryIDReservation) error {
		if err := ownReservation(res, reg); err != nil {
			return err
		}
		if res.Status.Registration != nil {
			return ledger.ErrNoChange
		}
		res.Status.Registration = &registryv1alpha1.IAMRegistration{Project: scope.Project, Account: scope.Account}
		return nil
	})
	if err != nil {
		return false, err
	}
	p.reservation = res

	err = r.Kaname.Register(ctx, kaname.RegistryRegistration(reg.Spec.ID, scope, registryVersion(reg)))
	if done, err := r.registrationOutcome(p, obj, err); !done {
		return false, err
	}
	if err := r.retryRepositories(ctx, p, scope); err != nil {
		return false, err
	}
	now := metav1.NewTime(p.now)
	res, err = r.reservations().Mutate(ctx, reg.Spec.ID, func(res *registryv1alpha1.RegistryIDReservation) error {
		if err := ownReservation(res, reg); err != nil {
			return err
		}
		res.Status.Registration.Generation = reg.Generation
		res.Status.Registration.RegisteredAt = &now
		return nil
	})
	if err != nil {
		return false, err
	}
	p.reservation = res
	// Неподтверждённый репозиторий описи шаг не держит: регистри записан, а
	// повтор по репозиторию идёт каждым проходом и виден в диагностике.
	p.registration = &registryv1alpha1.RegistrationStatus{Object: obj, Ready: true, Reason: registryv1alpha1.ReasonRegistered}
	metrics.Registrations.WithLabelValues("registered").Inc()
	return true, nil
}

// registrationOutcome переводит исход вызова IAM в состояние шага. false —
// шаг не готов (с ошибкой-отказом либо без ошибки при недоступности).
func (r *RegistryReconciler) registrationOutcome(p *pass, obj string, err error) (bool, error) {
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, kaname.ErrRefused), errors.Is(err, kaname.ErrInvalid):
		metrics.Registrations.WithLabelValues("refused").Inc()
		p.registration = &registryv1alpha1.RegistrationStatus{Object: obj, Reason: registryv1alpha1.ReasonRegistrationRefused}
		return false, &refusal{reason: registryv1alpha1.ReasonRegistrationRefused, msg: err.Error()}
	default:
		metrics.Registrations.WithLabelValues("unavailable").Inc()
		p.registration = &registryv1alpha1.RegistrationStatus{Object: obj, Reason: registryv1alpha1.ReasonRegistrationPending}
		p.diagnostics = append(p.diagnostics, registryv1alpha1.ResourceDiagnostic{
			Step: registryv1alpha1.StepRegistration, Kind: "Registration", Name: obj, Message: truncate(err.Error(), 1024),
		})
		return false, nil
	}
}

// retryRepositories дописывает в IAM репозитории описи, чья регистрация
// при заявке не подтвердилась (IAM не ответил заявке, запись в опись
// осталась).
func (r *RegistryReconciler) retryRepositories(ctx context.Context, p *pass, scope kaname.Scope) error {
	reg := p.reg
	var done []string
	for _, repo := range p.reservation.Status.Repositories {
		if repo.Registered {
			continue
		}
		err := r.Kaname.Register(ctx, kaname.RepositoryRegistration(reg.Spec.ID, repo.Name, scope, registryVersion(reg)))
		if err != nil {
			// Отказ по одному репозиторию не роняет заказ: он виден в
			// диагностике, повтор — следующим проходом.
			p.diagnostics = append(p.diagnostics, registryv1alpha1.ResourceDiagnostic{
				Step: registryv1alpha1.StepRegistration, Kind: "Registration",
				Name: kaname.RepositoryObject(reg.Spec.ID, repo.Name), Message: truncate(err.Error(), 1024),
			})
			continue
		}
		done = append(done, repo.Name)
	}
	if len(done) == 0 {
		return nil
	}
	res, err := r.reservations().Mutate(ctx, reg.Spec.ID, func(res *registryv1alpha1.RegistryIDReservation) error {
		if err := ownReservation(res, reg); err != nil {
			return err
		}
		for i := range res.Status.Repositories {
			if slices.Contains(done, res.Status.Repositories[i].Name) {
				res.Status.Repositories[i].Registered = true
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	p.reservation = res
	return nil
}

func allRepositoriesRegistered(res *registryv1alpha1.RegistryIDReservation) bool {
	for _, repo := range res.Status.Repositories {
		if !repo.Registered {
			return false
		}
	}
	return true
}

// errNotOwnReservation — резерв id не принадлежит заказу (или освобождён).
var errNotOwnReservation = errors.New("reservation does not belong to this order")

// ownReservation — резерв активен и закреплён за этим заказом (namespace,
// имя и uid).
func ownReservation(res *registryv1alpha1.RegistryIDReservation, reg *registryv1alpha1.Registry) error {
	ref := res.Spec.RegistryRef
	if ref.Namespace != reg.Namespace || ref.Name != reg.Name || ref.UID != string(reg.UID) || res.Status.Phase != registryv1alpha1.ReservationActive {
		return &refusal{reason: registryv1alpha1.ReasonReservationMissing, msg: fmt.Sprintf("%v: %s", errNotOwnReservation, res.Name)}
	}
	return nil
}

// unregisterAll — шаг удаления: снятие ресурса регистри, затем его
// репозиториев из описи. true — снято всё и опись пуста.
func (r *RegistryReconciler) unregisterAll(ctx context.Context, p *pass) (bool, error) {
	reg := p.reg
	rec := p.reservation.Status.Registration
	if rec == nil && len(p.reservation.Status.Repositories) == 0 {
		return true, nil
	}
	if rec == nil {
		// Репозиторий без регистри не заводится (эндпоинт требует
		// регистрации); опись без записи регистри — порча, снимаем по
		// закреплённому ниже правилу без проекта.
		rec = &registryv1alpha1.IAMRegistration{}
	}
	// Закрытие — до снятия: новые заявки отвергаются, идущая успевает
	// закончиться в свой срок.
	if rec.ClosingAt == nil {
		now := metav1.NewTime(p.now)
		res, err := r.reservations().Mutate(ctx, reg.Spec.ID, func(res *registryv1alpha1.RegistryIDReservation) error {
			if err := ownReservation(res, reg); err != nil {
				return err
			}
			if res.Status.Registration == nil {
				res.Status.Registration = &registryv1alpha1.IAMRegistration{}
			}
			if res.Status.Registration.ClosingAt != nil {
				return ledger.ErrNoChange
			}
			res.Status.Registration.ClosingAt = &now
			return nil
		})
		if err != nil {
			return false, err
		}
		p.reservation = res
		p.message = "registration is closing; in-flight repository enrollments are given their deadline"
		return false, nil
	}
	if wait := r.enrollmentDrain() - p.now.Sub(rec.ClosingAt.Time); wait > 0 {
		p.message = "waiting for in-flight repository enrollments to finish"
		return false, nil
	}
	scope := kaname.Scope{Project: rec.Project, Account: rec.Account}
	version := registryVersion(reg)
	if rec.Project != "" {
		err := r.Kaname.Unregister(ctx, kaname.RegistryRegistration(reg.Spec.ID, scope, version))
		if err != nil {
			p.message = "registry unregistration failed: " + err.Error()
			metrics.DeletionSteps.WithLabelValues(string(registryv1alpha1.StepRegistration), "waiting").Inc()
			return false, nil
		}
	}
	var removed []string
	failed := false
	for _, repo := range p.reservation.Status.Repositories {
		err := r.Kaname.Unregister(ctx, kaname.RepositoryRegistration(reg.Spec.ID, repo.Name, scope, version))
		if err != nil {
			failed = true
			p.diagnostics = append(p.diagnostics, registryv1alpha1.ResourceDiagnostic{
				Step: registryv1alpha1.StepRegistration, Kind: "Registration",
				Name: kaname.RepositoryObject(reg.Spec.ID, repo.Name), Message: truncate(err.Error(), 1024),
			})
			continue
		}
		removed = append(removed, repo.Name)
	}
	res, err := r.reservations().Mutate(ctx, reg.Spec.ID, func(res *registryv1alpha1.RegistryIDReservation) error {
		if err := ownReservation(res, reg); err != nil {
			return err
		}
		res.Status.Repositories = slices.DeleteFunc(res.Status.Repositories, func(x registryv1alpha1.RegisteredRepository) bool {
			return slices.Contains(removed, x.Name)
		})
		if !failed && len(res.Status.Repositories) == 0 {
			res.Status.Registration = nil
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	p.reservation = res
	metrics.DeletionSteps.WithLabelValues(string(registryv1alpha1.StepRegistration), "deleted").Inc()
	return res.Status.Registration == nil && len(res.Status.Repositories) == 0, nil
}

// enrollmentDrain — сколько ждать после закрытия: срок одной заявки и
// одного вызова IAM внутри неё.
func (r *RegistryReconciler) enrollmentDrain() time.Duration {
	return r.Config.Enrollment.RequestTimeout.Duration + r.Config.Kaname.Timeout.Duration
}
