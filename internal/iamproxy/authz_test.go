// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package iamproxy_test

import (
	"context"
	"testing"
	"time"

	"github.com/PRO-Robotech/registry-operator/internal/iamproxy"
)

// counting — Checker проб, считающий вопросы.
type counting struct {
	calls   int
	allowed bool
}

func (c *counting) Check(context.Context, iamproxy.Identity, string, string) (bool, error) {
	c.calls++
	return c.allowed, nil
}

// Вердикт чтения кэшируется по токену: другой токен того же субъекта
// спрашивает заново; запись не кэшируется вовсе; срок истекает.
func TestCachedChecker_KeyIsTheTokenAndWritesBypass(t *testing.T) {
	inner := &counting{allowed: true}
	ck := &clock{t: time.Now()}
	c := &iamproxy.CachedChecker{Inner: inner, TTL: 10 * time.Second, MaxEntries: 8, Now: ck.Now}
	a := iamproxy.Identity{Subject: "user:usr1", Token: "t-a"}
	b := iamproxy.Identity{Subject: "user:usr1", Token: "t-b"}
	obj := "registry_repository:abc123/team/app"
	for range 3 {
		if ok, err := c.Check(context.Background(), a, iamproxy.VerbGet, obj); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("read asked %d times within the TTL", inner.calls)
	}
	_, _ = c.Check(context.Background(), b, iamproxy.VerbGet, obj)
	if inner.calls != 2 {
		t.Fatalf("another token reused a cached verdict (%d calls)", inner.calls)
	}
	for range 3 {
		_, _ = c.Check(context.Background(), a, iamproxy.VerbUpdate, obj)
	}
	if inner.calls != 5 {
		t.Fatalf("writes were cached (%d calls)", inner.calls)
	}
	ck.Advance(11 * time.Second)
	_, _ = c.Check(context.Background(), a, iamproxy.VerbGet, obj)
	if inner.calls != 6 {
		t.Fatalf("verdict outlived its TTL (%d calls)", inner.calls)
	}
	// Переполнение снимает кэш, а не растёт без предела.
	for i := range 20 {
		_, _ = c.Check(context.Background(), iamproxy.Identity{Subject: "user:usr1", Token: string(rune('a' + i))}, iamproxy.VerbGet, obj)
	}
	if inner.calls != 26 {
		t.Fatalf("calls %d", inner.calls)
	}
}
