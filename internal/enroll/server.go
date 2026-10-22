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
//   - опись не превысила предел, темп заявок регистри не превышен.
//
// Запись идёт намерением: сначала опись (резерв id), затем IAM. Опись —
// источник истины для снятия: репозиторий, записанный в IAM, всегда есть в
// описи.
package enroll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

// Limits — предел описи и темп заявок одного регистри.
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

	// global — общий темп проверок токенов: неаутентифицированный поток не
	// превращает эндпоинт в усилитель нагрузки на API-сервер.
	global *rate.Limiter
	mu     sync.Mutex
	perReg map[string]*rate.Limiter
}

// Handler — маршруты эндпоинта.
func (s *Server) Handler() http.Handler {
	s.global = rate.NewLimiter(rate.Limit(50), 100)
	s.perReg = map[string]*rate.Limiter{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+api.Path, s.enroll)
	return mux
}

func (s *Server) limiter(id string) *rate.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.perReg[id]
	if !ok {
		l = rate.NewLimiter(rate.Every(time.Minute/time.Duration(s.Limits.RatePerMinute)), s.Limits.Burst)
		s.perReg[id] = l
	}
	return l
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
	if !s.global.Allow() {
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
	reg, res, scope, outcome := s.registry(ctx, id, ns)
	if outcome != "" {
		s.reply(w, outcome)
		return
	}
	// Темп считает только новые заявки: повтор заявленного (каждый запрос
	// push, пока IAM материализует права потомков) темп не расходует.
	known := slices.ContainsFunc(res.Status.Repositories, func(r registryv1alpha1.RegisteredRepository) bool { return r.Name == body.Repository })
	if !known && !s.limiter(id).Allow() {
		s.reply(w, api.OutcomeLimited)
		return
	}
	s.reply(w, s.register(ctx, reg, scope, body))
}

// registry — заказ id в namespace вызывающего, годный для заявки.
func (s *Server) registry(ctx context.Context, id, ns string) (*registryv1alpha1.Registry, *registryv1alpha1.RegistryIDReservation, kaname.Scope, string) {
	res := &registryv1alpha1.RegistryIDReservation{}
	if err := s.Reader.Get(ctx, types.NamespacedName{Name: id}, res); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, kaname.Scope{}, api.OutcomeUnknown
		}
		return nil, nil, kaname.Scope{}, api.OutcomeUnavailable
	}
	ref := res.Spec.RegistryRef
	if res.Status.Phase != registryv1alpha1.ReservationActive || ref.Namespace != ns {
		// Регистри другого арендатора неотличим от несуществующего.
		return nil, nil, kaname.Scope{}, api.OutcomeUnknown
	}
	reg := &registryv1alpha1.Registry{}
	if err := s.Reader.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, reg); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, kaname.Scope{}, api.OutcomeUnknown
		}
		return nil, nil, kaname.Scope{}, api.OutcomeUnavailable
	}
	if string(reg.UID) != ref.UID {
		return nil, nil, kaname.Scope{}, api.OutcomeUnknown
	}
	rec := res.Status.Registration
	switch {
	case !reg.DeletionTimestamp.IsZero() || (rec != nil && rec.ClosingAt != nil):
		return nil, nil, kaname.Scope{}, api.OutcomeClosing
	case rec == nil || rec.RegisteredAt == nil:
		// Репозиторий без записанного регистри выпал бы из каскада.
		return nil, nil, kaname.Scope{}, api.OutcomeUnavailable
	}
	return reg, res, kaname.Scope{Project: rec.Project, Account: rec.Account}, ""
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

	err = s.Kaname.Register(ctx, kaname.RepositoryRegistration(id, body.Repository, scope, kaname.Version(reg.CreationTimestamp.Time, reg.Generation)))
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
		// Снятие началось между записью в IAM и отметкой: снимаем свою
		// запись сами, снятие заказа её уже не увидит.
		if uerr := s.Kaname.Unregister(ctx, kaname.RepositoryRegistration(id, body.Repository, scope, kaname.Version(reg.CreationTimestamp.Time, reg.Generation))); uerr != nil {
			s.Log.Error("enrollment: compensation failed", "registry", id, "repository", body.Repository, "err", uerr)
		}
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
