// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package enroll_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/enroll"
	"github.com/PRO-Robotech/registry-operator/internal/enroll/api"
	"github.com/PRO-Robotech/registry-operator/internal/kaname"
	"github.com/PRO-Robotech/registry-operator/internal/kaname/kanametest"
	"github.com/PRO-Robotech/registry-operator/internal/ledger"
)

const audience = "registry-operator"

// reviewer — TokenReview проб: токен → (namespace, учётка); аудитория —
// только своя.
type reviewer map[string][2]string

func (r reviewer) Review(_ context.Context, token, aud string) (string, string, error) {
	who, ok := r[token]
	if !ok || aud != audience {
		return "", "", enroll.ErrUnauthenticated
	}
	return who[0], who[1], nil
}

type env struct {
	t      *testing.T
	c      client.Client
	fake   *kanametest.Fake
	srv    *httptest.Server
	now    time.Time
	limits enroll.Limits
	kn     kaname.Registrar
}

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := registryv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func registered(t *testing.T) (*registryv1alpha1.Registry, *registryv1alpha1.RegistryIDReservation) {
	t.Helper()
	created := metav1.NewTime(time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC))
	reg := &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: "abc123", Namespace: "cust01", UID: types.UID("u-abc123"), Generation: 1, CreationTimestamp: created},
		Spec:       registryv1alpha1.RegistrySpec{ID: "abc123", TemplateRef: registryv1alpha1.TemplateRef{Name: "zot", Version: "v1"}},
	}
	at := metav1.NewTime(created.Add(time.Minute))
	res := &registryv1alpha1.RegistryIDReservation{
		ObjectMeta: metav1.ObjectMeta{Name: "abc123"},
		Spec:       registryv1alpha1.RegistryIDReservationSpec{RegistryRef: registryv1alpha1.RegistryRef{Namespace: "cust01", Name: "abc123", UID: "u-abc123"}},
		Status: registryv1alpha1.RegistryIDReservationStatus{
			Phase:        registryv1alpha1.ReservationActive,
			Registration: &registryv1alpha1.IAMRegistration{Project: "prj1", Account: "acc1", Generation: 1, RegisteredAt: &at},
		},
	}
	return reg, res
}

func newEnv(t *testing.T, edit func(*registryv1alpha1.Registry, *registryv1alpha1.RegistryIDReservation), limits enroll.Limits) *env {
	t.Helper()
	reg, res := registered(t)
	if edit != nil {
		edit(reg, res)
	}
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(reg, res).
		WithStatusSubresource(&registryv1alpha1.RegistryIDReservation{}, &registryv1alpha1.Registry{}).Build()
	f := kanametest.New("unused")
	f.RegisterDirect("registry_registry:abc123", kanametest.Object{
		Subject: "project:prj1", Relation: "project", ParentChain: []string{"project:prj1"}, ParentProjectID: "prj1", ParentAccountID: "acc1",
	})
	e := &env{t: t, c: c, fake: f, now: time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC), limits: limits, kn: f.Direct()}
	e.start()
	return e
}

func (e *env) start() {
	s := &enroll.Server{
		Reader: e.c, Ledger: &ledger.Store{Client: e.c, Reader: e.c}, Kaname: registrarFunc{e},
		Reviewer: reviewer{
			"tok-zot":          {"cust01", "zot-abc123"},
			"tok-other-sa":     {"cust01", "zot-def456"},
			"tok-other-tenant": {"cust02", "zot-abc123"},
		},
		Audience: audience, WorkloadName: func(id string) string { return "zot-" + id },
		Limits: e.limits, Now: func() time.Time { return e.now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	e.srv = httptest.NewServer(s.Handler())
	e.t.Cleanup(e.srv.Close)
}

// registrarFunc — позднее связывание: проба подменяет e.kn по ходу.
type registrarFunc struct{ e *env }

func (r registrarFunc) Register(ctx context.Context, x kaname.Registration) error {
	return r.e.kn.Register(ctx, x)
}

func (r registrarFunc) Unregister(ctx context.Context, x kaname.Registration) error {
	return r.e.kn.Unregister(ctx, x)
}

func defaultLimits() enroll.Limits {
	return enroll.Limits{MaxRepositories: 8, RatePerMinute: 600, Burst: 100, EnrollerGrace: 10 * time.Minute, RequestTimeout: 5 * time.Second}
}

func (e *env) post(token, id, repo, subject string) (int, string) {
	e.t.Helper()
	body, _ := json.Marshal(api.Request{Repository: repo, Subject: subject})
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/v1/registries/"+id+"/repositories", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out api.Response
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.Outcome
}

func (e *env) ledger() []registryv1alpha1.RegisteredRepository {
	e.t.Helper()
	res := &registryv1alpha1.RegistryIDReservation{}
	if err := e.c.Get(context.Background(), types.NamespacedName{Name: "abc123"}, res); err != nil {
		e.t.Fatal(err)
	}
	return res.Status.Repositories
}

func TestEnroll_NewRepositoryIsRecordedThenRegisteredUnderTheRegistry(t *testing.T) {
	e := newEnv(t, nil, defaultLimits())
	code, outcome := e.post("tok-zot", "abc123", "team/app", "service_account:sva1")
	if code != http.StatusCreated || outcome != api.OutcomeCreated {
		t.Fatalf("got %d %s", code, outcome)
	}
	l := e.ledger()
	if len(l) != 1 || l[0].Name != "team/app" || l[0].EnrolledBy != "service_account:sva1" || !l[0].Registered {
		t.Fatalf("ledger %+v", l)
	}
	obj, ok := e.fake.Objects()["registry_repository:abc123/team/app"]
	if !ok || obj.Subject != "registry_registry:abc123" || len(obj.ParentChain) != 2 || obj.ParentChain[1] != "project:prj1" {
		t.Fatalf("mirror %v %s", ok, obj)
	}
}

// F3: зарегистрированный репозиторий (даже пустой) — не «новый»: заявка
// другого субъекта отвергнута; заявивший в своё окно — проходит.
func TestEnroll_RegisteredRepositoryIsNotNewForAnotherSubject(t *testing.T) {
	e := newEnv(t, nil, defaultLimits())
	if code, _ := e.post("tok-zot", "abc123", "team/app", "service_account:sva1"); code != http.StatusCreated {
		t.Fatalf("first enrollment: %d", code)
	}
	if code, outcome := e.post("tok-zot", "abc123", "team/app", "service_account:sva1"); code != http.StatusOK || outcome != api.OutcomeEnrolledBySelf {
		t.Fatalf("enroller repeat: %d %s", code, outcome)
	}
	if code, outcome := e.post("tok-zot", "abc123", "team/app", "user:usr9"); code != http.StatusConflict || outcome != api.OutcomeRegistered {
		t.Fatalf("another subject: %d %s", code, outcome)
	}
	e.now = e.now.Add(11 * time.Minute)
	if code, _ := e.post("tok-zot", "abc123", "team/app", "service_account:sva1"); code != http.StatusConflict {
		t.Fatalf("enroller past its grace: %d", code)
	}
}

func TestEnroll_OnlyTheWorkloadAccountOfThisRegistryMayEnroll(t *testing.T) {
	for _, c := range []struct {
		name, token, id string
		code            int
	}{
		{"no token", "", "abc123", http.StatusUnauthorized},
		{"token of another audience or unknown", "tok-vault", "abc123", http.StatusUnauthorized},
		{"account of another registry", "tok-other-sa", "abc123", http.StatusForbidden},
		{"same account name in another tenant", "tok-other-tenant", "abc123", http.StatusNotFound},
		{"registry that does not exist", "tok-zot", "zzz999", http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, nil, defaultLimits())
			if code, _ := e.post(c.token, c.id, "team/app", "service_account:sva1"); code != c.code {
				t.Fatalf("got %d, want %d", code, c.code)
			}
			if n := len(e.ledger()); n != 0 {
				t.Fatalf("a refused enrollment wrote %d ledger entries", n)
			}
			if n := len(e.fake.Calls()); n != 0 {
				t.Fatalf("a refused enrollment reached kaname %d times", n)
			}
		})
	}
}

func TestEnroll_RepositoryLimitAndRate(t *testing.T) {
	l := defaultLimits()
	l.MaxRepositories = 2
	e := newEnv(t, nil, l)
	for _, repo := range []string{"a", "b"} {
		if code, _ := e.post("tok-zot", "abc123", repo, "service_account:sva1"); code != http.StatusCreated {
			t.Fatalf("%s: %d", repo, code)
		}
	}
	if code, outcome := e.post("tok-zot", "abc123", "c", "service_account:sva1"); code != http.StatusTooManyRequests || outcome != api.OutcomeLimited {
		t.Fatalf("over the limit: %d %s", code, outcome)
	}
	if n := len(e.ledger()); n != 2 {
		t.Fatalf("ledger holds %d", n)
	}
	// Повтор заявленного предел не расходует.
	if code, _ := e.post("tok-zot", "abc123", "a", "service_account:sva1"); code != http.StatusOK {
		t.Fatalf("repeat at the limit: %d", code)
	}

	r := defaultLimits()
	r.RatePerMinute, r.Burst = 1, 1
	e2 := newEnv(t, nil, r)
	if code, _ := e2.post("tok-zot", "abc123", "a", "service_account:sva1"); code != http.StatusCreated {
		t.Fatalf("first: %d", code)
	}
	if code, outcome := e2.post("tok-zot", "abc123", "b", "service_account:sva1"); code != http.StatusTooManyRequests || outcome != api.OutcomeLimited {
		t.Fatalf("over the rate: %d %s", code, outcome)
	}
}

func TestEnroll_RegistryNotReadyForRepositories(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*registryv1alpha1.Registry, *registryv1alpha1.RegistryIDReservation)
		code int
	}{
		{"registration is closing", func(_ *registryv1alpha1.Registry, res *registryv1alpha1.RegistryIDReservation) {
			at := metav1.Now()
			res.Status.Registration.ClosingAt = &at
		}, http.StatusGone},
		{"registry not registered yet", func(_ *registryv1alpha1.Registry, res *registryv1alpha1.RegistryIDReservation) {
			res.Status.Registration.RegisteredAt = nil
		}, http.StatusServiceUnavailable},
		{"reservation released", func(_ *registryv1alpha1.Registry, res *registryv1alpha1.RegistryIDReservation) {
			res.Status.Phase = registryv1alpha1.ReservationReleased
		}, http.StatusNotFound},
		{"reservation of another object", func(_ *registryv1alpha1.Registry, res *registryv1alpha1.RegistryIDReservation) {
			res.Spec.RegistryRef.UID = "u-other"
		}, http.StatusNotFound},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, c.edit, defaultLimits())
			if code, _ := e.post("tok-zot", "abc123", "team/app", "service_account:sva1"); code != c.code {
				t.Fatalf("got %d, want %d", code, c.code)
			}
			if n := len(e.fake.Calls()); n != 0 {
				t.Fatalf("kaname reached %d times", n)
			}
		})
	}
}

func TestEnroll_InvalidRequestIsRefusedBeforeAnyWrite(t *testing.T) {
	e := newEnv(t, nil, defaultLimits())
	for _, c := range []struct{ repo, subject string }{
		{"Team/App", "service_account:sva1"},
		{"../etc", "service_account:sva1"},
		{"team/app", "system_class:anonymous"},
		{"team/app", "user:a:b"},
	} {
		if code, _ := e.post("tok-zot", "abc123", c.repo, c.subject); code != http.StatusBadRequest {
			t.Fatalf("%q %q: %d", c.repo, c.subject, code)
		}
	}
	if n := len(e.ledger()); n != 0 {
		t.Fatalf("ledger holds %d", n)
	}
}

// IAM не ответил — запись в описи остаётся неподтверждённой; повтор той
// же заявки дописывает её.
func TestEnroll_UnavailableIAMLeavesTheIntentForARetry(t *testing.T) {
	e := newEnv(t, nil, defaultLimits())
	e.fake.FailNext(codes.Unavailable)
	if code, _ := e.post("tok-zot", "abc123", "team/app", "service_account:sva1"); code != http.StatusServiceUnavailable {
		t.Fatalf("got %d", code)
	}
	l := e.ledger()
	if len(l) != 1 || l[0].Registered {
		t.Fatalf("ledger %+v", l)
	}
	if code, _ := e.post("tok-zot", "abc123", "team/app", "service_account:sva1"); code != http.StatusOK {
		t.Fatalf("retry: %d", code)
	}
	if l := e.ledger(); !l[0].Registered {
		t.Fatalf("retry did not confirm: %+v", l)
	}
}

func TestEnroll_RefusedRepositoryLeavesNoLedgerEntry(t *testing.T) {
	e := newEnv(t, nil, defaultLimits())
	e.fake.Refuse("registry_repository:abc123/team/app")
	if code, outcome := e.post("tok-zot", "abc123", "team/app", "service_account:sva1"); code != http.StatusBadGateway || outcome != api.OutcomeRefused {
		t.Fatalf("got %d %s", code, outcome)
	}
	if n := len(e.ledger()); n != 0 {
		t.Fatalf("ledger holds %d", n)
	}
}

// Снятие началось, пока заявка писала в IAM: заявка снимает свою запись
// сама — снятие заказа её уже не увидит.
func TestEnroll_ClosingDuringTheWriteIsCompensated(t *testing.T) {
	e := newEnv(t, nil, defaultLimits())
	inner := e.kn
	e.kn = hook{inner: inner, before: func() {
		res := &registryv1alpha1.RegistryIDReservation{}
		if err := e.c.Get(context.Background(), types.NamespacedName{Name: "abc123"}, res); err != nil {
			t.Fatal(err)
		}
		at := metav1.Now()
		res.Status.Registration.ClosingAt = &at
		if err := e.c.Status().Update(context.Background(), res); err != nil {
			t.Fatal(err)
		}
	}}
	if code, _ := e.post("tok-zot", "abc123", "team/app", "service_account:sva1"); code != http.StatusGone {
		t.Fatalf("got %d", code)
	}
	if _, ok := e.fake.Objects()["registry_repository:abc123/team/app"]; ok {
		t.Fatal("the repository outlived the closing of its registry")
	}
}

// hook — Registrar, выполняющий действие перед первой регистрацией.
type hook struct {
	inner  kaname.Registrar
	before func()
}

func (h hook) Register(ctx context.Context, r kaname.Registration) error {
	if h.before != nil {
		h.before()
	}
	return h.inner.Register(ctx, r)
}

func (h hook) Unregister(ctx context.Context, r kaname.Registration) error {
	return h.inner.Unregister(ctx, r)
}

// replica — вторая реплика оператора над тем же API-сервером: свой процесс,
// своя память, общая опись.
func (e *env) replica() *httptest.Server {
	s := &enroll.Server{
		Reader: e.c, Ledger: &ledger.Store{Client: e.c, Reader: e.c}, Kaname: registrarFunc{e},
		Reviewer: reviewer{"tok-zot": {"cust01", "zot-abc123"}},
		Audience: audience, WorkloadName: func(id string) string { return "zot-" + id },
		Limits: e.limits, Now: func() time.Time { return e.now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	srv := httptest.NewServer(s.Handler())
	e.t.Cleanup(srv.Close)
	return srv
}

// send — заявка без t.Fatal: годится для конкурирующих горутин.
func send(c *http.Client, base, token, repo, subject string) (int, string, error) {
	body, _ := json.Marshal(api.Request{Repository: repo, Subject: subject})
	req, err := http.NewRequest(http.MethodPost, base+"/v1/registries/abc123/repositories", bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var out api.Response
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.Outcome, nil
}

// fromSource — клиент, чьи соединения уходят с данного адреса петли.
func fromSource(ip string) *http.Client {
	d := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}}
	return &http.Client{Transport: &http.Transport{DialContext: d.DialContext, MaxIdleConnsPerHost: 64}}
}

// Поток мусорных токенов с одного источника исчерпывает только учёт этого
// источника: заявки рабочей нагрузки с другого адреса проходят.
func TestEnroll_UnauthenticatedFloodFromOneSourceDoesNotStarveOthers(t *testing.T) {
	e := newEnv(t, nil, defaultLimits())
	flood := fromSource("127.0.0.2")
	const n = 2000
	var wg sync.WaitGroup
	var limited atomic.Int64
	jobs := make(chan int)
	for range 16 {
		wg.Go(func() {
			for range jobs {
				code, _, err := send(flood, e.srv.URL, "garbage", "team/app", "service_account:sva1")
				if err == nil && code == http.StatusTooManyRequests {
					limited.Add(1)
				}
			}
		})
	}
	for i := range n {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if limited.Load() == 0 {
		t.Fatalf("%d unauthenticated requests from one source were never limited", n)
	}
	legit := fromSource("127.0.0.1")
	for i, want := range []int{http.StatusCreated, http.StatusOK, http.StatusOK} {
		code, outcome, err := send(legit, e.srv.URL, "tok-zot", "team/app", "service_account:sva1")
		if err != nil || code != want {
			t.Fatalf("workload request %d after the flood: %d %s %v (want %d)", i, code, outcome, err, want)
		}
	}
}

// Темп новых репозиториев — на регистри во всём кластере, а не на реплику:
// вторая реплика видит заявку первой.
func TestEnroll_RateIsHeldAcrossReplicas(t *testing.T) {
	l := defaultLimits()
	l.RatePerMinute, l.Burst = 1, 1
	e := newEnv(t, nil, l)
	other := e.replica()
	c := fromSource("127.0.0.1")
	if code, outcome, err := send(c, e.srv.URL, "tok-zot", "a", "service_account:sva1"); err != nil || code != http.StatusCreated {
		t.Fatalf("first replica: %d %s %v", code, outcome, err)
	}
	if code, outcome, err := send(c, other.URL, "tok-zot", "b", "service_account:sva1"); err != nil || code != http.StatusTooManyRequests || outcome != api.OutcomeLimited {
		t.Fatalf("second replica over the cluster rate: %d %s %v", code, outcome, err)
	}
	// Близнец: темп восстановился — та же заявка на второй реплике проходит.
	e.now = e.now.Add(time.Minute)
	if code, outcome, err := send(c, other.URL, "tok-zot", "b", "service_account:sva1"); err != nil || code != http.StatusCreated {
		t.Fatalf("second replica after the rate refilled: %d %s %v", code, outcome, err)
	}
}

// flaky — Registrar, чьи первые failures снятий отвечают «недоступен».
type flaky struct {
	inner    kaname.Registrar
	before   func()
	mu       sync.Mutex
	failures int
}

func (f *flaky) Register(ctx context.Context, r kaname.Registration) error {
	if f.before != nil {
		f.before()
	}
	return f.inner.Register(ctx, r)
}

func (f *flaky) Unregister(ctx context.Context, r kaname.Registration) error {
	f.mu.Lock()
	fail := f.failures > 0
	if fail {
		f.failures--
	}
	f.mu.Unlock()
	if fail {
		return fmt.Errorf("%w: injected", kaname.ErrUnavailable)
	}
	return f.inner.Unregister(ctx, r)
}

func (e *env) closeRegistration() error {
	_, err := (&ledger.Store{Client: e.c, Reader: e.c}).Mutate(context.Background(), "abc123", func(res *registryv1alpha1.RegistryIDReservation) error {
		if res.Status.Registration.ClosingAt != nil {
			return ledger.ErrNoChange
		}
		at := metav1.NewTime(e.now)
		res.Status.Registration.ClosingAt = &at
		return nil
	})
	return err
}

// Компенсация заявки, опоздавшей к закрытию, повторяется, пока IAM не
// ответит: разовый отказ снятия не оставляет репозиторий в IAM.
func TestEnroll_CompensationIsRetriedUntilIAMAnswers(t *testing.T) {
	e := newEnv(t, nil, defaultLimits())
	f := &flaky{inner: e.kn, failures: 2}
	f.before = func() {
		if err := e.closeRegistration(); err != nil {
			t.Error(err)
		}
	}
	e.kn = f
	if code, _ := e.post("tok-zot", "abc123", "team/app", "service_account:sva1"); code != http.StatusGone {
		t.Fatalf("got %d", code)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := e.fake.Objects()["registry_repository:abc123/team/app"]; !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the repository outlived a compensation that failed twice")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Опись под конкурирующими писателями: три случая заказа ревью.
func TestEnroll_ConcurrentEnrollmentsOfOneNewRepository(t *testing.T) {
	e := newEnv(t, nil, defaultLimits())
	replicas := []string{e.srv.URL, e.replica().URL, e.replica().URL}
	c := fromSource("127.0.0.1")
	const n = 9
	statuses := make([]int, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			statuses[i], _, errs[i] = send(c, replicas[i%len(replicas)], "tok-zot", "team/app", fmt.Sprintf("user:usr%d", i))
		})
	}
	close(start)
	wg.Wait()
	created, conflict := 0, 0
	for i := range n {
		switch {
		case errs[i] != nil:
			t.Fatalf("request %d: %v", i, errs[i])
		case statuses[i] == http.StatusCreated:
			created++
		case statuses[i] == http.StatusConflict:
			conflict++
		default:
			t.Fatalf("request %d: %d", i, statuses[i])
		}
	}
	if created != 1 || conflict != n-1 {
		t.Fatalf("created %d, conflict %d of %d", created, conflict, n)
	}
	if l := e.ledger(); len(l) != 1 || !l[0].Registered {
		t.Fatalf("ledger %+v", l)
	}
}

func TestEnroll_ConcurrentEnrollmentsAtTheLastFreeSlot(t *testing.T) {
	l := defaultLimits()
	l.MaxRepositories = 4
	e := newEnv(t, func(_ *registryv1alpha1.Registry, res *registryv1alpha1.RegistryIDReservation) {
		old := metav1.NewTime(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
		for _, name := range []string{"x", "y", "z"} {
			res.Status.Repositories = append(res.Status.Repositories, registryv1alpha1.RegisteredRepository{Name: name, EnrolledBy: "user:usr0", EnrolledAt: old, Registered: true})
		}
	}, l)
	replicas := []string{e.srv.URL, e.replica().URL}
	c := fromSource("127.0.0.1")
	const n = 8
	statuses := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			statuses[i], _, _ = send(c, replicas[i%len(replicas)], "tok-zot", fmt.Sprintf("r%d", i), "service_account:sva1")
		})
	}
	close(start)
	wg.Wait()
	created, limited := 0, 0
	for i, code := range statuses {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Fatalf("request %d: %d", i, code)
		}
	}
	if created != 1 || limited != n-1 || len(e.ledger()) != 4 {
		t.Fatalf("created %d, limited %d, ledger %d", created, limited, len(e.ledger()))
	}
}

// Заявки идут, пока снятие закрывает регистри: в IAM не остаётся
// репозитория вне описи, и снятие по описи забирает всё.
func TestEnroll_ConcurrentEnrollmentsAndClosingLeaveNothingOutsideTheLedger(t *testing.T) {
	e := newEnv(t, nil, defaultLimits())
	replicas := []string{e.srv.URL, e.replica().URL}
	c := fromSource("127.0.0.1")
	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			_, _, _ = send(c, replicas[i%len(replicas)], "tok-zot", fmt.Sprintf("r%d", i), "service_account:sva1")
		})
	}
	// Снятие без срока слива: закрыть, затем снимать опись, пока заявки
	// идут, — опоздавшая запись в IAM остаётся только компенсации.
	done := make(chan struct{})
	var teardown sync.WaitGroup
	teardown.Go(func() {
		<-start
		if err := e.closeRegistration(); err != nil {
			t.Error(err)
			return
		}
		for {
			e.unregisterLedger(t)
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
			}
		}
	})
	close(start)
	wg.Wait()
	close(done)
	teardown.Wait()
	repos := func() []string {
		var out []string
		for obj := range e.fake.Objects() {
			if name, ok := strings.CutPrefix(obj, "registry_repository:abc123/"); ok {
				out = append(out, name)
			}
		}
		return out
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		inLedger := map[string]bool{}
		for _, r := range e.ledger() {
			inLedger[r.Name] = true
		}
		var outside []string
		for _, name := range repos() {
			if !inLedger[name] {
				outside = append(outside, name)
			}
		}
		if len(outside) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("repositories in IAM outside the ledger: %v", outside)
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.unregisterLedger(t)
	if left := repos(); len(left) != 0 {
		t.Fatalf("teardown by the ledger left %v in IAM", left)
	}
}

// unregisterLedger — шаг снятия по описи: снять в IAM каждый репозиторий
// описи и убрать снятые из описи.
func (e *env) unregisterLedger(t *testing.T) {
	cur := &registryv1alpha1.RegistryIDReservation{}
	if err := e.c.Get(context.Background(), types.NamespacedName{Name: "abc123"}, cur); err != nil {
		t.Error(err)
		return
	}
	version := kaname.Version(time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC), 1)
	var removed []string
	for _, r := range cur.Status.Repositories {
		reg := kaname.RepositoryRegistration("abc123", r.Name, kaname.Scope{Project: "prj1", Account: "acc1"}, version)
		if err := e.fake.Direct().Unregister(context.Background(), reg); err != nil {
			t.Error(err)
			return
		}
		removed = append(removed, r.Name)
	}
	_, err := (&ledger.Store{Client: e.c, Reader: e.c}).Mutate(context.Background(), "abc123", func(res *registryv1alpha1.RegistryIDReservation) error {
		n := len(res.Status.Repositories)
		res.Status.Repositories = slices.DeleteFunc(res.Status.Repositories, func(x registryv1alpha1.RegisteredRepository) bool {
			return slices.Contains(removed, x.Name)
		})
		if len(res.Status.Repositories) == n {
			return ledger.ErrNoChange
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
}
