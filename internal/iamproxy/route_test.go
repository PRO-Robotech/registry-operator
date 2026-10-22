// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package iamproxy_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PRO-Robotech/registry-operator/internal/iamproxy"
)

// Разбор путей протокола Distribution: право, которое требует запрос, и
// имя репозитория; всё вне протокола регистри — ActionNone.
func TestParse_DistributionPaths(t *testing.T) {
	const id = "abc123"
	d := "sha256:" + "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	cases := []struct {
		method, path string
		action       iamproxy.Action
		repo         string
	}{
		{http.MethodGet, "/v2/abc123/app/manifests/latest", iamproxy.ActionPull, "app"},
		{http.MethodHead, "/v2/abc123/team/app/manifests/" + d, iamproxy.ActionPull, "team/app"},
		{http.MethodPut, "/v2/abc123/team/app/manifests/v1", iamproxy.ActionPush, "team/app"},
		{http.MethodDelete, "/v2/abc123/team/app/manifests/v1", iamproxy.ActionDelete, "team/app"},
		{http.MethodGet, "/v2/abc123/team/app/blobs/" + d, iamproxy.ActionPull, "team/app"},
		{http.MethodDelete, "/v2/abc123/team/app/blobs/" + d, iamproxy.ActionDelete, "team/app"},
		{http.MethodPost, "/v2/abc123/team/app/blobs/uploads/", iamproxy.ActionPush, "team/app"},
		{http.MethodPatch, "/v2/abc123/team/app/blobs/uploads/0c5d", iamproxy.ActionPush, "team/app"},
		{http.MethodPut, "/v2/abc123/team/app/blobs/uploads/0c5d", iamproxy.ActionPush, "team/app"},
		{http.MethodGet, "/v2/abc123/team/app/blobs/uploads/0c5d", iamproxy.ActionPush, "team/app"},
		{http.MethodGet, "/v2/abc123/team/app/tags/list", iamproxy.ActionList, "team/app"},
		{http.MethodGet, "/v2/abc123/team/app/referrers/" + d, iamproxy.ActionPull, "team/app"},
		// Имя репозитория вправе содержать сегменты с именами конечных точек.
		{http.MethodGet, "/v2/abc123/manifests/app/manifests/latest", iamproxy.ActionPull, "manifests/app"},
		{http.MethodPut, "/v2/abc123/blobs/x/blobs/uploads/0c5d", iamproxy.ActionPush, "blobs/x"},

		{http.MethodGet, "/v2/", iamproxy.ActionNone, ""},
		{http.MethodGet, "/v2/_catalog", iamproxy.ActionNone, ""},
		{http.MethodGet, "/v2/_zot/ext/search", iamproxy.ActionNone, ""},
		{http.MethodGet, "/v2/abc123/_zot/ext/x", iamproxy.ActionNone, ""},
		{http.MethodGet, "/v2/def456/app/manifests/latest", iamproxy.ActionNone, ""},
		{http.MethodGet, "/v2/abc1234/app/manifests/latest", iamproxy.ActionNone, ""},
		{http.MethodGet, "/v2/abc123/App/manifests/latest", iamproxy.ActionNone, ""},
		{http.MethodGet, "/v2/abc123/team//app/manifests/latest", iamproxy.ActionNone, ""},
		{http.MethodGet, "/v2/abc123/team/../app/manifests/latest", iamproxy.ActionNone, ""},
		{http.MethodGet, "/v2/abc123/manifests/latest", iamproxy.ActionNone, ""},
		{http.MethodPost, "/v2/abc123/app/manifests/latest", iamproxy.ActionNone, ""},
		{http.MethodPost, "/v2/abc123/app/blobs/uploads/0c5d", iamproxy.ActionNone, ""},
		{http.MethodPut, "/v2/abc123/app/tags/list", iamproxy.ActionNone, ""},
		{http.MethodGet, "/v2/abc123/app/blobs/", iamproxy.ActionNone, ""},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			rt := iamproxy.Parse(id, httptest.NewRequest(c.method, c.path, nil))
			if rt.Action != c.action || rt.Repository != c.repo {
				t.Fatalf("got %s %q, want %s %q", rt.Action, rt.Repository, c.action, c.repo)
			}
		})
	}
}

func TestParse_EscapedPathIsNotProxied(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v2/abc123/team%2Fapp/manifests/latest", nil)
	if rt := iamproxy.Parse("abc123", r); rt.Action != iamproxy.ActionNone {
		t.Fatalf("escaped path parsed as %s %q", rt.Action, rt.Repository)
	}
}

func TestParse_MountSourceStaysInsideTheRegistry(t *testing.T) {
	for _, c := range []struct {
		query        string
		from         string
		foreignMount bool
	}{
		{"", "", false},
		{"?mount=sha256:aa&from=abc123/base", "base", false},
		{"?mount=sha256:aa&from=def456/secret", "", true},
		{"?mount=sha256:aa&from=abc123/../x", "", true},
		{"?from=abc123/base", "", true},
		{"?mount=sha256:aa", "", true},
	} {
		r := httptest.NewRequest(http.MethodPost, "/v2/abc123/app/blobs/uploads/"+c.query, nil)
		rt := iamproxy.Parse("abc123", r)
		if rt.Action != iamproxy.ActionPush || rt.MountFrom != c.from || rt.ForeignMount != c.foreignMount {
			t.Fatalf("%s: %+v", c.query, rt)
		}
	}
}
