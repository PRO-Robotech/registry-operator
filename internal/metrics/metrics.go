// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package metrics — метрики оператора в реестре controller-runtime (их
// отдаёт сервер метрик менеджера). Метки — только закрытые перечни (шаг,
// исход, фаза, код причины): ни id заказа, ни namespace в метки не попадают,
// число серий не растёт с числом заказов.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// StepOutcomes — исход шага пайплайна за проход: applied, ready,
	// waiting, refused, failed.
	StepOutcomes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "registry_operator_step_outcomes_total",
		Help: "Pipeline step outcomes per reconcile pass, by step and outcome.",
	}, []string{"step", "outcome"})

	// Refusals — отказы заказа по коду причины (политика, рендер, чужой
	// объект, занятый id, пин чарта).
	Refusals = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "registry_operator_refusals_total",
		Help: "Orders refused, by reason code.",
	}, []string{"reason"})

	// PhaseTransitions — переходы фазы заказа.
	PhaseTransitions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "registry_operator_phase_transitions_total",
		Help: "Registry phase transitions, by target phase.",
	}, []string{"phase"})

	// SyncEpochAdvances — продвижения эпохи синхронизации рабочей нагрузки.
	SyncEpochAdvances = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "registry_operator_sync_epoch_advances_total",
		Help: "Sync epoch advances after exhausted Argo CD retries.",
	})

	// ChartVerifications — сверки пина чарта: match, mismatch, unreachable.
	ChartVerifications = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "registry_operator_chart_verifications_total",
		Help: "Chart digest verifications, by outcome.",
	}, []string{"outcome"})

	// Registrations — регистрации ресурса регистри в IAM: registered,
	// refused, unavailable.
	Registrations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "registry_operator_registrations_total",
		Help: "Registry resource registrations in IAM, by outcome.",
	}, []string{"outcome"})

	// Enrollments — заявки рабочей нагрузки zot на регистрацию репозитория,
	// по исходу (закрытый перечень кодов эндпоинта).
	Enrollments = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "registry_operator_enrollments_total",
		Help: "Repository enrollment requests, by outcome.",
	}, []string{"outcome"})

	// DeletionSteps — шаги удаления: deleted, waiting, released.
	DeletionSteps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "registry_operator_deletion_steps_total",
		Help: "Deletion pipeline steps, by step and outcome.",
	}, []string{"step", "outcome"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(StepOutcomes, Refusals, PhaseTransitions, SyncEpochAdvances, ChartVerifications,
		Registrations, Enrollments, DeletionSteps)
}
