// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package iamproxy — прокси прав перед zot одного регистри. zot остаётся
// неизменным и без собственной аутентификации слушает только localhost
// пода; прокси решает каждый запрос протокола Distribution правом IAM:
//   - токен клиента проверяется локально (JWKS издателя только по https с
//     объявленным центром, издатель, аудитория, обязательный срок, тип);
//   - право спрашивается публичным AuthorizeService.Check kaname ТОКЕНОМ
//     САМОГО КЛИЕНТА о нём самом — своих прав у прокси нет, общей служебной
//     личности нет, спросить о чужом праве он не может;
//   - pull — v_get на репозитории (каскад выдачи по регистри или по
//     аккаунту решает kaname), push в записанный репозиторий — v_update,
//     заведение нового — v_create на регистри и заявка оператору, где
//     «новый» — не записанный в IAM (а не «без тегов»);
//   - анонимное чтение — только у публичного регистри;
//   - Authorization и Cookie клиента в zot не уходят, тела 5xx и 401 zot
//     наружу не отдаются, /v2/_catalog и расширения zot закрыты.
package iamproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PRO-Robotech/registry-operator/internal/kaname"
)

// TokenVerifier — проверка токена клиента.
type TokenVerifier interface {
	Verify(ctx context.Context, raw string) (Identity, error)
}

// Options — прокси одного регистри.
type Options struct {
	RegistryID string
	// Public — анонимное чтение (Registry.spec.public).
	Public bool
	// Upstream — zot на localhost пода.
	Upstream *url.URL
	Verifier TokenVerifier
	Checker  Checker
	Enroller Enroller
	// Realm и Service — вызов клиента за токеном (WWW-Authenticate).
	Realm   string
	Service string
	// UpstreamTimeout — ожидание заголовков ответа zot.
	UpstreamTimeout time.Duration
	Log             *slog.Logger
	// Decisions — счётчик решений (действие, исход); может быть nil.
	Decisions func(action, outcome string)
}

// Proxy — обработчик.
type Proxy struct {
	o  Options
	rp *httputil.ReverseProxy

	// enrolled — субъект и репозиторий, чья заявка принята недавно: каждый
	// запрос одного push не ходит к оператору заново. Право создания на
	// регистри спрашивается всё равно на каждом запросе.
	mu       sync.Mutex
	enrolled map[string]time.Time
	now      func() time.Time
}

// enrolledTTL — сколько помнится принятая заявка; maxEnrolledMemo — предел
// памяти принятых заявок (переполнение сбрасывает её целиком: повтор заявки
// безвреден); defaultUpstreamTimeout — срок ответа zot по умолчанию.
const (
	enrolledTTL            = 30 * time.Second
	maxEnrolledMemo        = 4096
	defaultUpstreamTimeout = 10 * time.Minute
)

// New — прокси; конфигурация проверяется целиком.
func New(o Options) (*Proxy, error) {
	switch {
	case !kaname.ValidID(o.RegistryID):
		return nil, fmt.Errorf("registry id %q is not valid", o.RegistryID)
	case o.Upstream == nil || o.Upstream.Scheme != "http" || !loopback(o.Upstream.Hostname()):
		return nil, errors.New("upstream must be zot over http on the loopback of the pod")
	case o.Verifier == nil || o.Checker == nil || o.Enroller == nil:
		return nil, errors.New("verifier, checker and enroller are required")
	case o.Realm == "" || o.Service == "":
		return nil, errors.New("realm and service are required")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.UpstreamTimeout <= 0 {
		o.UpstreamTimeout = defaultUpstreamTimeout
	}
	p := &Proxy{o: o, enrolled: map[string]time.Time{}, now: time.Now}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(o.Upstream)
			pr.Out.Host = o.Upstream.Host
			for _, h := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
				pr.Out.Header.Del(h)
			}
		},
		Transport: &http.Transport{
			ResponseHeaderTimeout: o.UpstreamTimeout,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConnsPerHost:   64,
		},
		FlushInterval:  -1,
		ModifyResponse: maskUpstream,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			o.Log.Error("iam-proxy: upstream failed", "path", r.URL.Path, "err", err)
			writeError(w, http.StatusBadGateway, "UNAVAILABLE", "registry backend is unavailable", "")
		},
	}
	return p, nil
}

func loopback(host string) bool {
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// maskUpstream — сырые тела отказов zot наружу не идут: 5xx и 401
// заменяются фиксированным ответом, вызов за токеном zot снимается.
func maskUpstream(resp *http.Response) error {
	resp.Header.Del("WWW-Authenticate")
	resp.Header.Del("Set-Cookie")
	if resp.StatusCode < 500 && resp.StatusCode != http.StatusUnauthorized {
		return nil
	}
	_ = resp.Body.Close()
	code, msg := "UNAVAILABLE", "registry backend failed"
	status := resp.StatusCode
	if status == http.StatusUnauthorized {
		status, code, msg = http.StatusBadGateway, "UNAVAILABLE", "registry backend refused the request"
	}
	b := errorBody(code, msg)
	resp.StatusCode = status
	resp.Status = strconv.Itoa(status) + " " + http.StatusText(status)
	resp.Body = io.NopCloser(bytes.NewReader(b))
	resp.ContentLength = int64(len(b))
	resp.Header = http.Header{"Content-Type": {"application/json"}, "Content-Length": {strconv.Itoa(len(b))}, apiVersionHeader: {apiVersion}}
	return nil
}

func errorBody(code, msg string) []byte {
	b, _ := json.Marshal(map[string]any{"errors": []any{map[string]string{"code": code, "message": msg}}})
	return b
}

func writeError(w http.ResponseWriter, status int, code, msg, challenge string) {
	if challenge != "" {
		w.Header().Set("WWW-Authenticate", challenge)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(apiVersionHeader, apiVersion)
	w.WriteHeader(status)
	_, _ = w.Write(errorBody(code, msg))
}

// Версия протокола Distribution в заголовке каждого ответа прокси.
const (
	apiVersionHeader = "Docker-Distribution-Api-Version"
	apiVersion       = "registry/2.0"
)

// versionCheck — проверка версии протокола (GET и HEAD /v2/): отвечает сам
// прокси, zot её не видит. Анониму частного регистри и негодному токену —
// 401 с вызовом за токеном без области: клиент проверяет учётные данные
// при входе, а не на первом запросе к репозиторию.
func (p *Proxy) versionCheck(w http.ResponseWriter, r *http.Request) {
	id, v := p.identity(r)
	if v == allow && id == nil && !p.o.Public {
		v = unauthenticated
	}
	if p.o.Decisions != nil {
		p.o.Decisions("version", v.String())
	}
	switch v {
	case allow:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(apiVersionHeader, apiVersion)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "{}")
	case unavailable:
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "authorization is unavailable, retry later", "")
	default:
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required",
			fmt.Sprintf(`Bearer realm=%q,service=%q`, p.o.Realm, p.o.Service))
	}
}

// verdict — исход решения.
type verdict int

const (
	allow verdict = iota
	unauthenticated
	denied
	limited
	unavailable
	// unknown — чтение в репозитории, которого субъект не видит, но вправе
	// завести: ответ «не найдено» без обращения к zot. Так push нового
	// репозитория проходит проверку существования блобов (HEAD), а субъект
	// с одним правом создания из записанного репозитория не читает ничего.
	unknown
)

func (v verdict) String() string {
	return [...]string{"allow", "unauthenticated", "denied", "limited", "unavailable", "unknown"}[v]
}

// ServeHTTP — решение и проксирование.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v2/" && r.URL.RawPath == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		p.versionCheck(w, r)
		return
	}
	rt := Parse(p.o.RegistryID, r)
	if rt.Action == ActionNone {
		p.count(rt, "not-found")
		writeError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry", "")
		return
	}
	v, keepMount := p.decide(r, rt)
	p.count(rt, v.String())
	switch v {
	case unauthenticated:
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", p.challenge(rt))
		return
	case denied:
		writeError(w, http.StatusForbidden, "DENIED", "requested access to the resource is denied", "")
		return
	case limited:
		writeError(w, http.StatusTooManyRequests, "TOOMANYREQUESTS", "too many new repositories", "")
		return
	case unavailable:
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "authorization is unavailable, retry later", "")
		return
	case unknown:
		writeError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry", "")
		return
	}
	if rt.ForeignMount || (rt.MountFrom != "" && !keepMount) {
		q := r.URL.Query()
		q.Del("mount")
		q.Del("from")
		r.URL.RawQuery = q.Encode()
	}
	p.rp.ServeHTTP(w, r)
}

func (p *Proxy) count(rt Route, outcome string) {
	if p.o.Decisions != nil {
		p.o.Decisions(rt.Action.String(), outcome)
	}
}

func (p *Proxy) challenge(rt Route) string {
	scope := "pull"
	switch rt.Action {
	case ActionPush:
		scope = "pull,push"
	case ActionDelete:
		scope = "delete"
	}
	return fmt.Sprintf(`Bearer realm=%q,service=%q,scope="repository:%s/%s:%s"`, p.o.Realm, p.o.Service, p.o.RegistryID, rt.Repository, scope)
}

// identity — личность запроса: (nil, unauthenticated) у отвергнутого
// токена; (nil, allow) у запроса без токена.
func (p *Proxy) identity(r *http.Request) (*Identity, verdict) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return nil, allow
	}
	raw, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || raw == "" {
		return nil, unauthenticated
	}
	id, err := p.o.Verifier.Verify(r.Context(), raw)
	switch {
	case errors.Is(err, ErrKeys):
		p.o.Log.Error("iam-proxy: issuer keys unavailable", "err", err)
		return nil, unavailable
	case err != nil:
		return nil, unauthenticated
	}
	return &id, allow
}

// decide — решение по запросу. Второе значение — сохранить ли
// монтирование блоба из другого репозитория регистри.
func (p *Proxy) decide(r *http.Request, rt Route) (verdict, bool) {
	id, v := p.identity(r)
	if v != allow {
		return v, false
	}
	read := rt.Action == ActionPull || rt.Action == ActionList
	if id == nil {
		if read && p.o.Public {
			return allow, false
		}
		return unauthenticated, false
	}
	ctx := r.Context()
	repo := kaname.RepositoryObject(p.o.RegistryID, rt.Repository)
	switch rt.Action {
	case ActionPull, ActionList:
		if p.o.Public {
			return allow, false
		}
		verb := VerbGet
		if rt.Action == ActionList {
			verb = VerbList
		}
		v := p.check(ctx, *id, verb, repo)
		if v == denied && p.check(ctx, *id, VerbCreate, kaname.RegistryObject(p.o.RegistryID)) == allow {
			return unknown, false
		}
		return v, false
	case ActionDelete:
		return p.check(ctx, *id, VerbDelete, repo), false
	case ActionPush:
		v := p.check(ctx, *id, VerbUpdate, repo)
		if v == denied {
			v = p.create(ctx, *id, rt.Repository)
		}
		if v != allow || rt.MountFrom == "" {
			return v, false
		}
		// Монтирование читает блоб источника: без права чтения источника
		// загрузка идёт обычным порядком, а не отказом.
		keep := p.o.Public || p.check(ctx, *id, VerbGet, kaname.RepositoryObject(p.o.RegistryID, rt.MountFrom)) == allow
		return allow, keep
	}
	return denied, false
}

// create — запись в репозиторий, которого субъект вправе только завести:
// право создания на регистри и заявка оператору; репозиторий, уже
// записанный в IAM, правом создания не открывается.
func (p *Proxy) create(ctx context.Context, id Identity, repository string) verdict {
	if v := p.check(ctx, id, VerbCreate, kaname.RegistryObject(p.o.RegistryID)); v != allow {
		return v
	}
	key := id.Subject + "|" + repository
	p.mu.Lock()
	at, ok := p.enrolled[key]
	p.mu.Unlock()
	if ok && p.now().Sub(at) < enrolledTTL {
		return allow
	}
	err := p.o.Enroller.Enroll(ctx, repository, id.Subject)
	switch {
	case err == nil:
		p.mu.Lock()
		if len(p.enrolled) >= maxEnrolledMemo {
			p.enrolled = map[string]time.Time{}
		}
		p.enrolled[key] = p.now()
		p.mu.Unlock()
		return allow
	case errors.Is(err, ErrEnrollConflict), errors.Is(err, ErrEnrollClosed):
		return denied
	case errors.Is(err, ErrEnrollLimited):
		return limited
	default:
		p.o.Log.Error("iam-proxy: enrollment failed", "repository", repository, "err", err)
		return unavailable
	}
}

func (p *Proxy) check(ctx context.Context, id Identity, verb, object string) verdict {
	ok, err := p.o.Checker.Check(ctx, id, verb, object)
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return unauthenticated
	case err != nil:
		p.o.Log.Error("iam-proxy: check failed", "verb", verb, "err", err)
		return unavailable
	case ok:
		return allow
	}
	return denied
}
