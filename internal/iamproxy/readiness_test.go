// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package iamproxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/PRO-Robotech/registry-operator/internal/iamproxy"
)

type readyFunc func() bool

func (f readyFunc) Ready(context.Context) bool { return f() }

// Под готов, только когда готовы обе половины: набор ключей издателя и
// zot на своём пути готовности; без ключей zot не спрашивается.
func TestReadiness_NeedsTheKeySetAndAReadyZot(t *testing.T) {
	for _, c := range []struct {
		name    string
		keys    bool
		zot     int
		down    bool
		want    int
		asksZot bool
	}{
		{"keys and a ready zot", true, http.StatusOK, false, http.StatusOK, true},
		{"zot not ready", true, http.StatusServiceUnavailable, false, http.StatusServiceUnavailable, true},
		{"zot down", true, http.StatusOK, true, http.StatusServiceUnavailable, false},
		{"no key set", false, http.StatusOK, false, http.StatusServiceUnavailable, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var asked atomic.Int64
			var seen atomic.Value
			zot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				asked.Add(1)
				seen.Store(r.URL.Path)
				w.WriteHeader(c.zot)
			}))
			u, _ := url.Parse(zot.URL)
			if c.down {
				zot.Close()
			} else {
				t.Cleanup(zot.Close)
			}
			h := iamproxy.Readiness(readyFunc(func() bool { return c.keys }), u, "/readyz", zot.Client())
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if rec.Code != c.want {
				t.Fatalf("readiness %d, want %d", rec.Code, c.want)
			}
			if got := asked.Load() > 0; got != c.asksZot {
				t.Fatalf("zot asked: %v, want %v", got, c.asksZot)
			}
			if c.asksZot && seen.Load() != "/readyz" {
				t.Fatalf("zot asked at %v", seen.Load())
			}
		})
	}
}
