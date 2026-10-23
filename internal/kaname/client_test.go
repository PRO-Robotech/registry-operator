// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package kaname_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/PRO-Robotech/registry-operator/internal/kaname"
	"github.com/PRO-Robotech/registry-operator/internal/kaname/kanametest"
)

const registrySAN = "spiffe://cluster.local/ns/registry-system/sa/kacho-registry"

type env struct {
	fake   *kanametest.Fake
	srv    *kanametest.Servers
	client *kaname.Client
}

// newEnv — двойник kaname с центром и клиент с листом san.
func newEnv(t *testing.T, san string) env {
	t.Helper()
	ca, err := kanametest.NewCA("test-ca")
	if err != nil {
		t.Fatal(err)
	}
	server, err := ca.Issue("kaname", []string{"kaname.test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := kanametest.New(registrySAN)
	srv, err := fake.Serve(server.TLS, ca.Pool())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	leaf, err := ca.Issue("registry-operator", nil, []string{san})
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile, caFile, err := leaf.WriteFiles(t.TempDir(), ca)
	if err != nil {
		t.Fatal(err)
	}
	c, err := kaname.Dial(kaname.ClientConfig{
		Address: srv.InternalAddr, ServerName: "kaname.test",
		CertFile: certFile, KeyFile: keyFile, CAFile: caFile, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return env{fake: fake, srv: srv, client: c}
}

var (
	scope   = kaname.Scope{Project: "prj1", Account: "acc1"}
	created = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
)

func TestRegister_RegistryLandsWithItsProjectChain(t *testing.T) {
	e := newEnv(t, registrySAN)
	ctx := context.Background()
	r := kaname.RegistryRegistration("abc123", scope, kaname.Version(created, 1))
	if err := e.client.Register(ctx, r); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Повтор — успех и ничего не меняет.
	if err := e.client.Register(ctx, r); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	got, ok := e.fake.Objects()["registry_registry:abc123"]
	if !ok {
		t.Fatal("registry is not in the mirror")
	}
	if got.Subject != "project:prj1" || got.Relation != "project" || len(got.ParentChain) != 1 || got.ParentChain[0] != "project:prj1" ||
		got.ParentProjectID != "prj1" || got.ParentAccountID != "acc1" {
		t.Fatalf("mirror holds %s", got)
	}
	if n := len(e.fake.Erased()); n != 0 {
		t.Fatalf("%d registrations erased ancestors", n)
	}
}

// Пустая цепь не уходит в kaname: там она стёрла бы предков. Близнец с
// цепью уходит.
func TestRegister_EmptyParentChainIsNeverSent(t *testing.T) {
	e := newEnv(t, registrySAN)
	bad := kaname.RegistryRegistration("abc123", scope, kaname.Version(created, 1))
	bad.ParentChain = nil
	err := e.client.Register(context.Background(), bad)
	if !errors.Is(err, kaname.ErrInvalid) {
		t.Fatalf("empty chain: %v, want ErrInvalid", err)
	}
	if n := len(e.fake.Calls()); n != 0 {
		t.Fatalf("an invalid registration reached kaname (%d calls)", n)
	}
	twin := kaname.RegistryRegistration("abc123", scope, kaname.Version(created, 1))
	if err := e.client.Register(context.Background(), twin); err != nil {
		t.Fatalf("lawful twin: %v", err)
	}
}

func TestRegister_RepositoryChainNamesTheRegistryThenTheProject(t *testing.T) {
	e := newEnv(t, registrySAN)
	ctx := context.Background()
	r := kaname.RepositoryRegistration("abc123", "team/app", scope, kaname.Version(created, 1))
	if err := e.client.Register(ctx, r); err != nil {
		t.Fatal(err)
	}
	got := e.fake.Objects()["registry_repository:abc123/team/app"]
	if got.Subject != "registry_registry:abc123" || got.Relation != "parent" ||
		len(got.ParentChain) != 2 || got.ParentChain[0] != "registry_registry:abc123" || got.ParentChain[1] != "project:prj1" {
		t.Fatalf("mirror holds %s", got)
	}
	for _, c := range []struct {
		name string
		edit func(*kaname.Registration)
	}{
		{"chain skips the registry", func(r *kaname.Registration) { r.ParentChain = []string{"project:prj1"} }},
		{"chain ends elsewhere", func(r *kaname.Registration) {
			r.ParentChain = []string{"registry_registry:abc123", "project:prj2"}
		}},
		{"foreign type", func(r *kaname.Registration) { r.Object = "vpc_network:net1" }},
		{"unversioned", func(r *kaname.Registration) { r.Version = time.Time{} }},
		{"repository name outside the grammar", func(r *kaname.Registration) { r.Object = "registry_repository:abc123/Team" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			bad := kaname.RepositoryRegistration("abc123", "team/app", scope, kaname.Version(created, 1))
			c.edit(&bad)
			if err := e.client.Register(ctx, bad); !errors.Is(err, kaname.ErrInvalid) {
				t.Fatalf("got %v, want ErrInvalid", err)
			}
		})
	}
}

// Лист другой службы kaname отвергает: SAN — личность вызывающего.
func TestRegister_LeafOfAnotherServiceIsRefused(t *testing.T) {
	e := newEnv(t, "spiffe://cluster.local/ns/registry-system/sa/kacho-vpc")
	err := e.client.Register(context.Background(), kaname.RegistryRegistration("abc123", scope, kaname.Version(created, 1)))
	if !errors.Is(err, kaname.ErrRefused) {
		t.Fatalf("got %v, want ErrRefused", err)
	}
	if len(e.fake.Objects()) != 0 {
		t.Fatal("a foreign leaf wrote the mirror")
	}
}

func TestRegister_RefusalAndUnavailabilityAreTwoOutcomes(t *testing.T) {
	e := newEnv(t, registrySAN)
	ctx := context.Background()
	r := kaname.RegistryRegistration("abc123", scope, kaname.Version(created, 1))
	e.fake.FailNext(codes.Unavailable, codes.DeadlineExceeded, codes.Internal, codes.PermissionDenied, codes.InvalidArgument)
	for _, want := range []error{kaname.ErrUnavailable, kaname.ErrUnavailable, kaname.ErrUnavailable, kaname.ErrRefused, kaname.ErrRefused} {
		if err := e.client.Register(ctx, r); !errors.Is(err, want) {
			t.Fatalf("got %v, want %v", err, want)
		}
	}
	if err := e.client.Register(ctx, r); err != nil {
		t.Fatalf("after the failures: %v", err)
	}
}

func TestUnregister_RemovesWithTheCurrentVersionAndIsIdempotent(t *testing.T) {
	e := newEnv(t, registrySAN)
	ctx := context.Background()
	if err := e.client.Register(ctx, kaname.RegistryRegistration("abc123", scope, kaname.Version(created, 3))); err != nil {
		t.Fatal(err)
	}
	if err := e.client.Unregister(ctx, kaname.RegistryRegistration("abc123", scope, kaname.Version(created, 3))); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.fake.Objects()["registry_registry:abc123"]; ok {
		t.Fatal("registry survived its unregistration")
	}
	if err := e.client.Unregister(ctx, kaname.RegistryRegistration("abc123", scope, kaname.Version(created, 3))); err != nil {
		t.Fatalf("repeat unregister: %v", err)
	}
}

func TestVersion_GrowsWithTheGeneration(t *testing.T) {
	prev := kaname.Version(created, 1)
	for g := int64(2); g < 50; g++ {
		v := kaname.Version(created, g)
		if !v.After(prev) {
			t.Fatalf("generation %d: version %s is not after %s", g, v, prev)
		}
		prev = v
	}
}
