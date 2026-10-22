// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package kanametest — двойник kaname для проб: внутренний листенер
// (RegisterResource/UnregisterResource по mTLS) и публичный
// AuthorizeService.Check по токену вызывающего. Двойник держит контракт
// настоящего, а не своё упрощение:
//   - внутренний вызов без листа доверенного центра или с SAN не службы
//     registry — отказ PermissionDenied;
//   - пустая цепь в RegisterResource стирает предков объекта (и это
//     отмечается: проба судит, что оператор так не делает);
//   - source_version применяется только строго более новая, снятие — если
//     не старше записанной; повтор — успех;
//   - Check: спросить вправе только о себе (субъект запроса — субъект
//     токена), иначе PermissionDenied; неизвестный токен — Unauthenticated;
//     право на репозиторий — только у зарегистрированного с цепью
//     [регистри, проект], каскад выдачи на аккаунт покрывает и будущие
//     регистри аккаунта.
package kanametest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	iamv1 "github.com/PRO-Robotech/kaname/pkg/api/kaname/cloud/iam/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/PRO-Robotech/registry-operator/internal/kaname"
)

// Object — запись зеркала двойника.
type Object struct {
	Subject         string
	Relation        string
	ParentChain     []string
	ParentProjectID string
	ParentAccountID string
	Version         time.Time
}

// Call — вызов внутреннего листенера в порядке поступления.
type Call struct {
	Method string // Register | Unregister
	Object string
	Chain  []string
}

// Role — роль модели прав: глаголы на регистри и на его репозитории
// (вложенное правило children).
type Role struct {
	RegistryVerbs   []string
	RepositoryVerbs []string
}

// Роли модуля registry.
var (
	RolePuller = Role{RegistryVerbs: []string{"v_get"}, RepositoryVerbs: []string{"v_get", "v_list"}}
	RolePusher = Role{RegistryVerbs: []string{"v_get", "v_create"}, RepositoryVerbs: []string{"v_get", "v_list", "v_update"}}
	RoleAdmin  = Role{RegistryVerbs: []string{"v_get", "v_create", "v_update", "v_delete"}, RepositoryVerbs: []string{"v_get", "v_list", "v_update", "v_delete"}}
	// RoleCreateOnly — только заведение репозиториев: права на запись в
	// существующие нет (F3).
	RoleCreateOnly = Role{RegistryVerbs: []string{"v_get", "v_create"}}
)

// Grant — выдача роли субъекту: на один регистри либо на весь аккаунт.
type Grant struct {
	Subject  string
	Role     Role
	Registry string // id регистри; пусто — каскад на Account
	Account  string
}

// Fake — двойник kaname.
type Fake struct {
	mu      sync.Mutex
	objects map[string]Object
	calls   []Call
	erased  []string
	grants  []Grant
	tokens  map[string]string
	checks  int

	allowedSAN  string
	failNext    []codes.Code
	refuse      map[string]bool
	unavailable map[string]bool
}

// New — двойник; allowedSAN — URI SAN листа, которому разрешена запись
// (spiffe://<td>/ns/<ns>/sa/kacho-registry).
func New(allowedSAN string) *Fake {
	return &Fake{objects: map[string]Object{}, tokens: map[string]string{}, refuse: map[string]bool{}, unavailable: map[string]bool{}, allowedSAN: allowedSAN}
}

// FailNext — следующие вызовы внутреннего листенера получат эти коды.
func (f *Fake) FailNext(c ...codes.Code) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = append(f.failNext, c...)
}

// Refuse — регистрация объекта отвергается по существу.
func (f *Fake) Refuse(object string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuse[object] = true
}

// SetUnavailable — регистрация объекта отвечает Unavailable, пока флаг
// не снят.
func (f *Fake) SetUnavailable(object string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unavailable[object] = on
}

// Objects — снимок зеркала.
func (f *Fake) Objects() map[string]Object {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]Object, len(f.objects))
	for k, v := range f.objects {
		out[k] = v
	}
	return out
}

// Calls — журнал вызовов.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// Erased — объекты, чьи предки стёрты пустой цепью.
func (f *Fake) Erased() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.erased)
}

// Checks — сколько вопросов Check получено.
func (f *Fake) Checks() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks
}

// AddToken — токен и субъект, за которого он говорит.
func (f *Fake) AddToken(token, subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens[token] = subject
}

// AddGrant — выдача.
func (f *Fake) AddGrant(g Grant) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grants = append(f.grants, g)
}

// Revoke — отзыв всех выдач субъекта.
func (f *Fake) Revoke(subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grants = slices.DeleteFunc(f.grants, func(g Grant) bool { return g.Subject == subject })
}

// RegisterDirect — запись в зеркало мимо листенера (фикстура проб).
func (f *Fake) RegisterDirect(object string, o Object) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[object] = o
}

func (f *Fake) popFailure() (codes.Code, bool) {
	if len(f.failNext) == 0 {
		return codes.OK, false
	}
	c := f.failNext[0]
	f.failNext = f.failNext[1:]
	return c, true
}

// ---------------------------------------------------------------- internal

type internalServer struct {
	iamv1.UnimplementedInternalIAMServiceServer
	f *Fake
}

func (s *internalServer) authorize(ctx context.Context) error {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return status.Error(codes.PermissionDenied, "no peer")
	}
	ti, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(ti.State.VerifiedChains) == 0 || len(ti.State.PeerCertificates) == 0 {
		return status.Error(codes.PermissionDenied, "no verified client certificate")
	}
	for _, u := range ti.State.PeerCertificates[0].URIs {
		if u.String() == s.f.allowedSAN {
			return nil
		}
	}
	return status.Error(codes.PermissionDenied, "caller is not the registry module")
}

func (s *internalServer) RegisterResource(ctx context.Context, req *iamv1.RegisterResourceRequest) (*iamv1.RegisterResourceResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	return s.f.applyRegister(req)
}

func (f *Fake) applyRegister(req *iamv1.RegisterResourceRequest) (*iamv1.RegisterResourceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.popFailure(); ok {
		return nil, status.Error(c, "injected")
	}
	if !strings.HasPrefix(req.GetObject(), "registry_") {
		return nil, status.Error(codes.PermissionDenied, "type is not owned by the registry module")
	}
	if f.refuse[req.GetObject()] {
		return nil, status.Error(codes.InvalidArgument, "refused")
	}
	if f.unavailable[req.GetObject()] {
		return nil, status.Error(codes.Unavailable, "unavailable")
	}
	f.calls = append(f.calls, Call{Method: "Register", Object: req.GetObject(), Chain: slices.Clone(req.GetParentChain())})
	v := versionOf(req.GetSourceVersion())
	cur, exists := f.objects[req.GetObject()]
	if exists && !v.IsZero() && !v.After(cur.Version) {
		return &iamv1.RegisterResourceResponse{}, nil
	}
	if len(req.GetParentChain()) == 0 {
		f.erased = append(f.erased, req.GetObject())
	}
	f.objects[req.GetObject()] = Object{
		Subject: req.GetSubjectId(), Relation: req.GetRelation(), ParentChain: slices.Clone(req.GetParentChain()),
		ParentProjectID: req.GetParentProjectId(), ParentAccountID: req.GetParentAccountId(), Version: v,
	}
	return &iamv1.RegisterResourceResponse{}, nil
}

func (s *internalServer) UnregisterResource(ctx context.Context, req *iamv1.UnregisterResourceRequest) (*iamv1.UnregisterResourceResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	return s.f.applyUnregister(req)
}

func (f *Fake) applyUnregister(req *iamv1.UnregisterResourceRequest) (*iamv1.UnregisterResourceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.popFailure(); ok {
		return nil, status.Error(c, "injected")
	}
	f.calls = append(f.calls, Call{Method: "Unregister", Object: req.GetObject()})
	v := versionOf(req.GetSourceVersion())
	if cur, ok := f.objects[req.GetObject()]; ok && (v.IsZero() || !v.Before(cur.Version)) {
		delete(f.objects, req.GetObject())
	}
	return &iamv1.UnregisterResourceResponse{}, nil
}

func versionOf(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

// Direct — Registrar поверх двойника без сети и без mTLS (пробы
// контроллера и эндпоинта): тот же контракт приёмника и та же проверка
// запроса клиентом, что у kaname.Client.
func (f *Fake) Direct() kaname.Registrar { return direct{f: f} }

type direct struct{ f *Fake }

func (d direct) Register(_ context.Context, r kaname.Registration) error {
	if err := r.Validate(true); err != nil {
		return err
	}
	_, err := d.f.applyRegister(kaname.RegisterRequest(r))
	return kaname.Classify(err)
}

func (d direct) Unregister(_ context.Context, r kaname.Registration) error {
	if err := r.Validate(false); err != nil {
		return err
	}
	_, err := d.f.applyUnregister(kaname.UnregisterRequest(r))
	return kaname.Classify(err)
}

// ---------------------------------------------------------------- public

type authorizeServer struct {
	iamv1.UnimplementedAuthorizeServiceServer
	f *Fake
}

func (s *authorizeServer) Check(ctx context.Context, req *iamv1.AuthorizeCheckRequest) (*iamv1.AuthorizeCheckResponse, error) {
	f := s.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	md, _ := metadata.FromIncomingContext(ctx)
	var token string
	if v := md.Get("authorization"); len(v) == 1 {
		token, _ = strings.CutPrefix(v[0], "Bearer ")
	}
	caller, ok := f.tokens[token]
	if !ok || token == "" || caller == "" {
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	if req.GetSubject() != caller {
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}
	if req.GetAction() == "" || req.GetResource().GetType() == "" || req.GetResource().GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Illegal argument")
	}
	allowed := f.allowed(caller, req.GetRequiredRelation(), req.GetResource().GetType()+":"+req.GetResource().GetId())
	return &iamv1.AuthorizeCheckResponse{Allowed: allowed, CheckedAt: timestamppb.Now()}, nil
}

// allowed — решение модели двойника под замком.
func (f *Fake) allowed(subject, relation, object string) bool {
	typ, id, _ := strings.Cut(object, ":")
	var registry string
	var verbsOf func(Role) []string
	switch typ {
	case "registry_registry":
		if _, ok := f.objects[object]; !ok {
			return false
		}
		registry = id
		verbsOf = func(r Role) []string { return r.RegistryVerbs }
	case "registry_repository":
		repo, ok := f.objects[object]
		reg, _, _ := strings.Cut(id, "/")
		if !ok || len(repo.ParentChain) == 0 || repo.ParentChain[0] != "registry_registry:"+reg {
			return false
		}
		registry = reg
		verbsOf = func(r Role) []string { return r.RepositoryVerbs }
	default:
		return false
	}
	regObj, ok := f.objects["registry_registry:"+registry]
	if !ok || len(regObj.ParentChain) == 0 {
		return false
	}
	for _, g := range f.grants {
		if g.Subject != subject {
			continue
		}
		covers := g.Registry == registry || (g.Registry == "" && g.Account != "" && g.Account == regObj.ParentAccountID)
		if covers && slices.Contains(verbsOf(g.Role), relation) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- serving

// Servers — поднятые листенеры двойника.
type Servers struct {
	// InternalAddr — внутренний листенер (mTLS).
	InternalAddr string
	// PublicAddr — публичный листенер (TLS, токен вызывающего).
	PublicAddr string
	stop       func()
}

// Stop останавливает листенеры.
func (s *Servers) Stop() { s.stop() }

// Serve поднимает оба листенера на localhost: внутренний требует лист
// центра clientCA, публичный — только TLS сервера.
func (f *Fake) Serve(serverCert tls.Certificate, clientCA *x509.CertPool) (*Servers, error) {
	return f.ServeOn(serverCert, clientCA, "127.0.0.1:0", "127.0.0.1:0")
}

// ServeOn — то же на заданных адресах.
func (f *Fake) ServeOn(serverCert tls.Certificate, clientCA *x509.CertPool, internalAddr, publicAddr string) (*Servers, error) {
	internalLn, err := net.Listen("tcp", internalAddr)
	if err != nil {
		return nil, err
	}
	publicLn, err := net.Listen("tcp", publicAddr)
	if err != nil {
		_ = internalLn.Close()
		return nil, err
	}
	internal := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    clientCA,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	})))
	iamv1.RegisterInternalIAMServiceServer(internal, &internalServer{f: f})
	public := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCert},
	})))
	iamv1.RegisterAuthorizeServiceServer(public, &authorizeServer{f: f})
	go func() { _ = internal.Serve(internalLn) }()
	go func() { _ = public.Serve(publicLn) }()
	return &Servers{
		InternalAddr: internalLn.Addr().String(),
		PublicAddr:   publicLn.Addr().String(),
		stop: func() {
			internal.Stop()
			public.Stop()
		},
	}, nil
}

// String — снимок для сообщений проб.
func (o Object) String() string {
	return fmt.Sprintf("%s#%s chain=%v v=%s", o.Subject, o.Relation, o.ParentChain, o.Version.Format(time.RFC3339Nano))
}
