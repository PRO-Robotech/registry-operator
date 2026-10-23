// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package api — проводной контракт эндпоинта заявок на репозитории: путь,
// тело запроса и закрытый перечень исходов с их HTTP-кодами. Общий для
// сервера (registry-operator) и клиента (iam-proxy) — второго описания
// контракта нет.
package api

import "net/http"

// Path — путь заявки: POST /v1/registries/{id}/repositories.
const Path = "/v1/registries/{id}/repositories"

// Request — тело заявки.
type Request struct {
	// Repository — имя репозитория внутри регистри (без <id>/).
	Repository string `json:"repository"`
	// Subject — субъект IAM, чей push заводит репозиторий
	// (user:<id> | service_account:<id>).
	Subject string `json:"subject"`
}

// Response — тело ответа: исход одним кодом из закрытого перечня.
type Response struct {
	Outcome string `json:"outcome"`
}

// Исходы заявки.
const (
	OutcomeCreated         = "created"
	OutcomeEnrolledBySelf  = "enrolled-by-subject"
	OutcomeRegistered      = "already-registered"
	OutcomeLimited         = "limited"
	OutcomeUnauthenticated = "unauthenticated"
	OutcomeForbidden       = "forbidden"
	OutcomeUnknown         = "unknown-registry"
	OutcomeClosing         = "closing"
	OutcomeUnavailable     = "unavailable"
	OutcomeRefused         = "refused"
	OutcomeInvalid         = "invalid"
)

// StatusOf — HTTP-код исхода.
var StatusOf = map[string]int{
	OutcomeCreated:         http.StatusCreated,
	OutcomeEnrolledBySelf:  http.StatusOK,
	OutcomeRegistered:      http.StatusConflict,
	OutcomeLimited:         http.StatusTooManyRequests,
	OutcomeUnauthenticated: http.StatusUnauthorized,
	OutcomeForbidden:       http.StatusForbidden,
	OutcomeUnknown:         http.StatusNotFound,
	OutcomeClosing:         http.StatusGone,
	OutcomeUnavailable:     http.StatusServiceUnavailable,
	OutcomeRefused:         http.StatusBadGateway,
	OutcomeInvalid:         http.StatusBadRequest,
}
