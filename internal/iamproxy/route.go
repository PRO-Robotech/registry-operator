// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package iamproxy

import (
	"net/http"
	"strings"

	"github.com/PRO-Robotech/registry-operator/internal/kaname"
)

// Action — класс запроса протокола Distribution по праву, которое он
// требует.
type Action int

const (
	// ActionNone — запрос вне протокола регистри: наружу не проксируется.
	ActionNone Action = iota
	// ActionPull — чтение манифеста, блоба, ссылающихся.
	ActionPull
	// ActionList — перечень тегов репозитория.
	ActionList
	// ActionPush — загрузка блоба и запись манифеста.
	ActionPush
	// ActionDelete — удаление манифеста или блоба.
	ActionDelete
)

func (a Action) String() string {
	switch a {
	case ActionPull:
		return "pull"
	case ActionList:
		return "list"
	case ActionPush:
		return "push"
	case ActionDelete:
		return "delete"
	}
	return "none"
}

// Route — разобранный запрос к репозиторию регистри.
type Route struct {
	Action Action
	// Repository — имя репозитория внутри регистри (без <id>/).
	Repository string
	// MountFrom — репозиторий-источник межрепозиторного монтирования блоба
	// (без <id>/); пусто — монтирования нет либо источник вне регистри.
	MountFrom string
	// ForeignMount — запрошено монтирование из репозитория вне регистри:
	// параметры снимаются, загрузка идёт обычным порядком.
	ForeignMount bool
}

// Parse разбирает запрос к регистри id. Путь вне /v2/<id>/<repo>/… и
// служебные пути (/v2/_catalog, /v2/_zot, расширения zot) — ActionNone.
// Имя репозитория может содержать сегменты с именами конечных точек
// протокола, поэтому конечная точка ищется с конца пути.
func Parse(id string, r *http.Request) Route {
	// Экранированный путь (%2F и подобное) zot прочёл бы иначе, чем этот
	// разбор: такой запрос не проксируется вовсе.
	if r.URL.RawPath != "" {
		return Route{}
	}
	p := r.URL.Path
	rest, ok := strings.CutPrefix(p, "/v2/"+id+"/")
	if !ok {
		return Route{}
	}
	segs := strings.Split(rest, "/")
	n := len(segs)
	if n < 3 {
		return Route{}
	}
	repoOf := func(k int) (string, bool) {
		repo := strings.Join(segs[:k], "/")
		return repo, kaname.ValidRepository(repo)
	}
	m := r.Method
	switch {
	case segs[n-2] == "tags" && segs[n-1] == "list":
		repo, ok := repoOf(n - 2)
		if ok && m == http.MethodGet {
			return Route{Action: ActionList, Repository: repo}
		}
	case n >= 4 && segs[n-3] == "blobs" && segs[n-2] == "uploads":
		repo, ok := repoOf(n - 3)
		if !ok {
			return Route{}
		}
		if segs[n-1] == "" {
			if m == http.MethodPost {
				return uploadStart(id, repo, r)
			}
			return Route{}
		}
		switch m {
		case http.MethodPatch, http.MethodPut, http.MethodGet, http.MethodDelete:
			return Route{Action: ActionPush, Repository: repo}
		}
	case segs[n-2] == "blobs" && segs[n-1] == "uploads" && m == http.MethodPost:
		if repo, ok := repoOf(n - 2); ok {
			return uploadStart(id, repo, r)
		}
	case segs[n-1] == "":
		return Route{}
	case segs[n-2] == "manifests":
		repo, ok := repoOf(n - 2)
		if !ok {
			return Route{}
		}
		switch m {
		case http.MethodGet, http.MethodHead:
			return Route{Action: ActionPull, Repository: repo}
		case http.MethodPut:
			return Route{Action: ActionPush, Repository: repo}
		case http.MethodDelete:
			return Route{Action: ActionDelete, Repository: repo}
		}
	case segs[n-2] == "blobs":
		repo, ok := repoOf(n - 2)
		if !ok {
			return Route{}
		}
		switch m {
		case http.MethodGet, http.MethodHead:
			return Route{Action: ActionPull, Repository: repo}
		case http.MethodDelete:
			return Route{Action: ActionDelete, Repository: repo}
		}
	case segs[n-2] == "referrers":
		if repo, ok := repoOf(n - 2); ok && m == http.MethodGet {
			return Route{Action: ActionPull, Repository: repo}
		}
	}
	return Route{}
}

// uploadStart — начало загрузки блоба, возможно с монтированием из другого
// репозитория: источник внутри этого регистри сохраняется (и потребует
// права чтения), источник вне его снимается.
func uploadStart(id, repo string, r *http.Request) Route {
	rt := Route{Action: ActionPush, Repository: repo}
	q := r.URL.Query()
	if !q.Has("mount") && !q.Has("from") {
		return rt
	}
	src, inRegistry := strings.CutPrefix(q.Get("from"), id+"/")
	if inRegistry && kaname.ValidRepository(src) && q.Get("mount") != "" {
		rt.MountFrom = src
	} else {
		rt.ForeignMount = true
	}
	return rt
}
