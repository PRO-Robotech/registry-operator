// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package iamproxy

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"time"
)

// ReadyChecker — готовность проверки токенов (набор ключей издателя).
type ReadyChecker interface {
	Ready(ctx context.Context) bool
}

// readyTimeout — срок одного вопроса о готовности zot.
const readyTimeout = 2 * time.Second

// Readiness — готовность пода: проверка токенов готова (набор ключей
// получен) и zot отвечает 200 на своём пути готовности. Иначе — 503: под,
// который не проверит токен либо не дойдёт до zot, трафика не получает.
func Readiness(tokens ReadyChecker, upstream *url.URL, readyPath string, client *http.Client) http.Handler {
	target := upstream.ResolveReference(&url.URL{Path: readyPath}).String()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tokens.Ready(r.Context()) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		resp, err := client.Do(req) //nolint:gosec // адрес — zot на петле пода (флаг запуска), не вход запроса.
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
