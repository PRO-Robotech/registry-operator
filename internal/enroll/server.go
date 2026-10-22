// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package enroll — эндпоинт заявок рабочей нагрузки zot на регистрацию
// нового репозитория в IAM. Заявку подаёт прокси iam-proxy пода zot-<id>,
// когда субъект с правом создания репозиториев пушит в репозиторий, которого
// в IAM нет. Узость эндпоинта — в трёх проверках до записи:
//   - вызывающий — учётка zot-<id> именно этого регистри (TokenReview
//     projected-токена с аудиторией эндпоинта: токен Vault того же пода не
//     годится);
//   - регистри жив, резерв его id активен, ресурс регистри уже записан в IAM
//     и снятие не начато;
//   - опись не превысила предел, темп новых репозиториев регистри не
//     превышен.
//
// Запись идёт намерением: сначала опись (резерв id), затем IAM. Опись —
// источник истины для снятия: репозиторий, записанный в IAM, есть в описи;
// запись, опоздавшая к закрытию регистри, снимается компенсацией.
//
// Учёт нагрузки — двух родов. Темп новых репозиториев — свойство регистри
// во всём кластере: он считается по описи (отметки EnrolledAt) внутри той же
// записи под версией объекта, поэтому реплики делят один темп. Учёт проверок
// токенов — защита процесса и API-сервера от потока без годного токена: он
// по источнику (адрес пода), в памяти реплики, и поток одного источника не
// расходует учёт другого.
package enroll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/enroll/api"
	"github.com/PRO-Robotech/registry-operator/internal/kaname"
	"github.com/PRO-Robotech/registry-operator/internal/ledger"
	"github.com/PRO-Robotech/registry-operator/internal/metrics"
)

// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create

// TokenReviewer — проверка projected-токена вызывающего.
type TokenReviewer interface {
	// Review — namespace и имя учётки токена с данной аудиторией.
	Review(ctx context.Context, token, audience string) (namespace, name string, err error)
}

// ErrUnauthenticated — токен не прошёл проверку.
var ErrUnauthenticated = errors.New("token is not authenticated for the audience")

// KubeTokenReviewer — TokenReview API-сервера.
type KubeTokenReviewer struct {
	Client client.Client
}

// Review — TokenReview с аудиторией эндпоинта.
func (k KubeTokenReviewer) Review(ctx context.Context, token, audience string) (string, string, error) {
	tr := &authenticationv1.TokenReview{Spec: authenticationv1.TokenReviewSpec{Token: token, Audiences: []string{audience}}}
	if err := k.Client.Create(ctx, tr); err != nil {
		return "", "", fmt.Errorf("token review: %w", err)
	}
	if !tr.Status.Authenticated || !slices.Contains(tr.Status.Audiences, audience) {
		return "", "", ErrUnauthenticated
	}
	rest, ok := strings.CutPrefix(tr.Status.User.Username, "system:serviceaccount:")
	ns, name, ok2 := strings.Cut(rest, ":")
	if !ok || !ok2 || ns == "" || name == "" {
		return "", "", ErrUnauthenticated
	}
	return ns, name, nil
}

// Limits — предел описи и темп новых репозиториев одного регистри (темп —
// на регистри во всём кластере, см. withinRate).
type Limits struct {
	MaxRepositories int
	RatePerMinute   int
	Burst           int
	EnrollerGrace   time.Duration
	RequestTimeout  time.Duration
}

// Server — обработчик заявок.
type Server struct {
	// Reader — чтение мимо кэша: резерв и заказ.
	Reader   client.Reader
	Ledger   *ledger.Store
	Kaname   kaname.Registrar
	Reviewer TokenReviewer
	// Audience — аудитория projected-токена учётки zot.
	Audience string
	// WorkloadName — имя учётки zot регистри по id (zot-<id>).
	WorkloadName func(id string) string
	Limits       Limits
	Now          func() time.Time
	Log          *slog.Logger

	once    sync.Once
	mux     *http.ServeMux
	sources *sourceLimiter
	// pending — идущие компенсации (Drain ждёт их при остановке).
	pending sync.WaitGroup
	// halt — обрыв идущих компенсаций, когда срок остановки истёк раньше
	// их срока: каждая сама считает исход abandoned.
	haltOnce   sync.Once
	halt       context.Context
	haltCancel context.CancelFunc
}

// Учёт проверок токенов по источнику: темп и запас одного адреса, предел
// числа учитываемых адресов.
const (
	sourceRate  = rate.Limit(10)
	sourceBurst = 20
	maxSources  = 4096
)

// Компенсация записи, опоздавшей к закрытию: срок всех попыток и отступы
// между ними.
const (
	compensationBudget     = 2 * time.Minute
	compensationFirstDelay = 100 * time.Millisecond
	compensationMaxDelay   = 10 * time.Second
)

// Handler — маршруты эндпоинта; состояние создаётся один раз, повторный
// вызов отдаёт тот же обработчик.
func (s *Server) Handler() http.Handler {
	s.once.Do(func() {
		s.sources = &sourceLimiter{entries: map[string]*sourceEntry{}}
		s.mux = http.NewServeMux()
		s.mux.HandleFunc("POST "+api.Path, s.enroll)
	})
	return s.mux
}

// haltContext — контекст обрыва компенсаций (заводится при первом
// обращении).
func (s *Server) haltContext() context.Context {
	s.haltOnce.Do(func() { s.halt, s.haltCancel = context.WithCancel(context.Background()) })
	return s.halt
}

// Drain ждёт идущих компенсаций. Срок остановки (ctx) короче срока
// компенсации: истёк он раньше — идущие компенсации обрываются, каждая
// считает и пишет в журнал исход abandoned, и Drain возвращается после их
// учёта, а не оставляет их умирать вместе с процессом молча.
func (s *Server) Drain(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.pending.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
	}
	s.haltContext()
	s.haltCancel()
	<-done
	return ctx.Err()
}

// sourceLimiter — учёт проверок токенов по адресу источника.
type sourceLimiter struct {
	mu      sync.Mutex
	entries map[string]*sourceEntry
}

type sourceEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

// allow — расход одной проверки источника src. Учёт источника, молчавшего
// дольше полного пополнения, снимается при нехватке места: его запас полон,
// и новый учёт неотличим от прежнего. Мест нет и после снятия — отказ
// новому источнику (а не сброс чужого учёта).
func (l *sourceLimiter) allow(src string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[src]
	if !ok {
		if len(l.entries) >= maxSources {
			idle := time.Duration(float64(sourceBurst) / float64(sourceRate) * float64(time.Second))
			for k, x := range l.entries {
				if now.Sub(x.seen) >= idle {
					delete(l.entries, k)
				}
			}
		}
		if len(l.entries) >= maxSources {
			return false
		}
		e = &sourceEntry{lim: rate.NewLimiter(sourceRate, sourceBurst)}
		l.entries[src] = e
	}
	e.seen = now
	return e.lim.AllowN(now, 1)
}

// sourceOf — адрес источника соединения (без порта).
func sourceOf(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}
	return host
}

// withinRate — темп новых репозиториев регистри: ведро ёмкости burst,
// пополняемое perMinute жетонами в минуту, восстанавливается по отметкам
// EnrolledAt всей описи. Отметка из будущего (расхождение часов реплик)
// считается сейчас.
func withinRate(repos []registryv1alpha1.RegisteredRepository, now time.Time, perMinute, burst int) bool {
	if perMinute <= 0 || burst <= 0 {
		return false
	}
	per := float64(time.Minute) / float64(perMinute)
	times := make([]time.Time, 0, len(repos))
	for _, r := range repos {
		t := r.EnrolledAt.Time
		if t.After(now) {
			t = now
		}
		times = append(times, t)
	}
	slices.SortFunc(times, time.Time.Compare)
	tokens, capacity := float64(burst), float64(burst)
	for i, t := range times {
		if i > 0 {
			tokens = min(capacity, tokens+float64(t.Sub(times[i-1]))/per)
		}
		tokens = max(0, tokens-1)
	}
	if len(times) > 0 {
		tokens = min(capacity, tokens+float64(now.Sub(times[len(times)-1]))/per)
	}
	return tokens >= 1
}

func (s *Server) reply(w http.ResponseWriter, outcome string) {
	metrics.Enrollments.WithLabelValues(outcome).Inc()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(api.StatusOf[outcome])
	_ = json.NewEncoder(w).Encode(api.Response{Outcome: outcome})
}

// validSubject — субъект IAM, о котором прокси спрашивал Check.
func validSubject(sub string) bool {
	typ, id, ok := strings.Cut(sub, ":")
	return ok && (typ == "user" || typ == "service_account") && kaname.ValidID(id)
}

func (s *Server) enroll(w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithTimeout(req.Context(), s.Limits.RequestTimeout)
	defer cancel()
	id := req.PathValue("id")
	token, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		s.reply(w, api.OutcomeUnauthenticated)
		return
	}
	// Учёт — до TokenReview и по источнику: поток без годного токена
	// упирается в свой учёт и не тратит чужой.
	if !s.sources.allow(sourceOf(req)) {
		s.reply(w, api.OutcomeLimited)
		return
	}
	ns, sa, err := s.Reviewer.Review(ctx, token, s.Audience)
	switch {
	case errors.Is(err, ErrUnauthenticated):
		s.reply(w, api.OutcomeUnauthenticated)
		return
	case err != nil:
		s.Log.Error("enrollment: token review failed", "err", err)
		s.reply(w, api.OutcomeUnavailable)
		return
	}
	if !kaname.ValidID(id) || sa != s.WorkloadName(id) {
		s.reply(w, api.OutcomeForbidden)
		return
	}
	var body api.Request
	dec := json.NewDecoder(io.LimitReader(req.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || !kaname.ValidRepository(body.Repository) || !validSubject(body.Subject) {
		s.reply(w, api.OutcomeInvalid)
		return
	}
	reg, scope, outcome := s.registry(ctx, id, ns)
	if outcome != "" {
		s.reply(w, outcome)
		return
	}
	s.reply(w, s.register(ctx, reg, scope, body))
}

// registry — заказ id в namespace вызывающего, годный для заявки.
func (s *Server) registry(ctx context.Context, id, ns string) (*registryv1alpha1.Registry, kaname.Scope, string) {
	res := &registryv1alpha1.RegistryIDReservation{}
	if err := s.Reader.Get(ctx, types.NamespacedName{Name: id}, res); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, kaname.Scope{}, api.OutcomeUnknown
		}
		return nil, kaname.Scope{}, api.OutcomeUnavailable
	}
	ref := res.Spec.RegistryRef
	if res.Status.Phase != registryv1alpha1.ReservationActive || ref.Namespace != ns {
		// Регистри другого арендатора неотличим от несуществующего.
		return nil, kaname.Scope{}, api.OutcomeUnknown
	}
	reg := &registryv1alpha1.Registry{}
	if err := s.Reader.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, reg); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, kaname.Scope{}, api.OutcomeUnknown
		}
		return nil, kaname.Scope{}, api.OutcomeUnavailable
	}
	if string(reg.UID) != ref.UID {
		return nil, kaname.Scope{}, api.OutcomeUnknown
	}
	rec := res.Status.Registration
	switch {
	case !reg.DeletionTimestamp.IsZero() || (rec != nil && rec.ClosingAt != nil):
		return nil, kaname.Scope{}, api.OutcomeClosing
	case rec == nil || rec.RegisteredAt == nil:
		// Репозиторий без записанного регистри выпал бы из каскада.
		return nil, kaname.Scope{}, api.OutcomeUnavailable
	}
	return reg, kaname.Scope{Project: rec.Project, Account: rec.Account}, ""
}

// errOutcome — исход, найденный внутри изменения описи.
type errOutcome string

func (e errOutcome) Error() string { return string(e) }

// register — опись, затем IAM.
func (s *Server) register(ctx context.Context, reg *registryv1alpha1.Registry, scope kaname.Scope, body api.Request) string {
	id := reg.Spec.ID
	now := s.Now()
	var entry registryv1alpha1.RegisteredRepository
	created := false
	_, err := s.Ledger.Mutate(ctx, id, func(res *registryv1alpha1.RegistryIDReservation) error {
		created = false
		if rec := res.Status.Registration; rec == nil || rec.ClosingAt != nil || res.Status.Phase != registryv1alpha1.ReservationActive {
			return errOutcome(api.OutcomeClosing)
		}
		for _, r := range res.Status.Repositories {
			if r.Name != body.Repository {
				continue
			}
			entry = r
			if r.EnrolledBy == body.Subject && now.Sub(r.EnrolledAt.Time) < s.Limits.EnrollerGrace {
				return ledger.ErrNoChange
			}
			return errOutcome(api.OutcomeRegistered)
		}
		if len(res.Status.Repositories) >= s.Limits.MaxRepositories {
			return errOutcome(api.OutcomeLimited)
		}
		// Темп — по описи внутри этой же записи: решение и новая отметка
		// атомарны, реплики делят один темп. Повтор заявленного (каждый
		// запрос push, пока IAM материализует права потомков) сюда не
		// доходит и темп не расходует.
		if !withinRate(res.Status.Repositories, now, s.Limits.RatePerMinute, s.Limits.Burst) {
			return errOutcome(api.OutcomeLimited)
		}
		entry = registryv1alpha1.RegisteredRepository{Name: body.Repository, EnrolledBy: body.Subject, EnrolledAt: metav1.NewTime(now)}
		res.Status.Repositories = append(res.Status.Repositories, entry)
		created = true
		return nil
	})
	var eo errOutcome
	switch {
	case errors.As(err, &eo):
		return string(eo)
	case err != nil:
		s.Log.Error("enrollment: ledger write failed", "registry", id, "err", err)
		return api.OutcomeUnavailable
	}
	if entry.Registered {
		return api.OutcomeEnrolledBySelf
	}

	repo := kaname.RepositoryRegistration(id, body.Repository, scope, kaname.Version(reg.CreationTimestamp.Time, reg.Generation))
	err = s.Kaname.Register(ctx, repo)
	switch {
	case errors.Is(err, kaname.ErrRefused), errors.Is(err, kaname.ErrInvalid):
		s.Log.Error("enrollment: kaname refused the repository", "registry", id, "repository", body.Repository, "err", err)
		s.drop(ctx, id, body.Repository)
		return api.OutcomeRefused
	case err != nil:
		// Запись в описи остаётся: проход заказа допишет её в IAM.
		s.Log.Warn("enrollment: kaname unavailable", "registry", id, "repository", body.Repository, "err", err)
		return api.OutcomeUnavailable
	}
	_, err = s.Ledger.Mutate(ctx, id, func(res *registryv1alpha1.RegistryIDReservation) error {
		if rec := res.Status.Registration; rec == nil || rec.ClosingAt != nil {
			return errOutcome(api.OutcomeClosing)
		}
		for i := range res.Status.Repositories {
			if res.Status.Repositories[i].Name == body.Repository {
				if res.Status.Repositories[i].Registered {
					return ledger.ErrNoChange
				}
				res.Status.Repositories[i].Registered = true
				return nil
			}
		}
		return errOutcome(api.OutcomeClosing)
	})
	switch {
	case errors.As(err, &eo):
		// Снятие началось между записью в IAM и отметкой. Снятие по описи
		// могло пройти раньше этой записи и её не увидеть — запись снимаем
		// сами, с повтором.
		s.compensate(ctx, repo)
		return string(eo)
	case err != nil:
		s.Log.Error("enrollment: ledger mark failed", "registry", id, "err", err)
		return api.OutcomeUnavailable
	}
	if created {
		return api.OutcomeCreated
	}
	return api.OutcomeEnrolledBySelf
}

// compensate — снять запись репозитория в IAM, сделанную заявкой после
// начала снятия регистри. Попытки идут вне запроса (ответ заявке их не
// ждёт), в контексте без отмены запроса, с отступом, пока IAM не ответит
// либо не истечёт срок; отказ по существу не повторяется. Drain ждёт их при
// остановке и обрывает с учётом, когда срок остановки истёк раньше.
func (s *Server) compensate(parent context.Context, repo kaname.Registration) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), compensationBudget)
	unhalt := context.AfterFunc(s.haltContext(), cancel)
	s.pending.Go(func() {
		defer cancel()
		defer unhalt()
		delay := compensationFirstDelay
		for attempt := 1; ; attempt++ {
			actx, acancel := context.WithTimeout(ctx, s.Limits.RequestTimeout)
			err := s.Kaname.Unregister(actx, repo)
			acancel()
			switch {
			case err == nil:
				metrics.EnrollmentCompensations.WithLabelValues("unregistered").Inc()
				return
			case errors.Is(err, kaname.ErrRefused), errors.Is(err, kaname.ErrInvalid):
				metrics.EnrollmentCompensations.WithLabelValues("refused").Inc()
				s.Log.Error("enrollment: compensation refused", "repository", repo.Object, "err", err)
				return
			}
			s.Log.Warn("enrollment: compensation will be retried", "repository", repo.Object, "attempt", attempt, "err", err)
			t := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				t.Stop()
				metrics.EnrollmentCompensations.WithLabelValues("abandoned").Inc()
				s.Log.Error("enrollment: compensation abandoned; the repository stays in IAM outside the ledger", "repository", repo.Object, "attempts", attempt)
				return
			case <-t.C:
			}
			delay = min(2*delay, compensationMaxDelay)
		}
	})
}

// drop — снять из описи заявку, отвергнутую IAM.
func (s *Server) drop(ctx context.Context, id, repo string) {
	_, err := s.Ledger.Mutate(ctx, id, func(res *registryv1alpha1.RegistryIDReservation) error {
		n := len(res.Status.Repositories)
		res.Status.Repositories = slices.DeleteFunc(res.Status.Repositories, func(r registryv1alpha1.RegisteredRepository) bool {
			return r.Name == repo && !r.Registered
		})
		if len(res.Status.Repositories) == n {
			return ledger.ErrNoChange
		}
		return nil
	})
	if err != nil {
		s.Log.Error("enrollment: drop failed", "registry", id, "repository", repo, "err", err)
	}
}
