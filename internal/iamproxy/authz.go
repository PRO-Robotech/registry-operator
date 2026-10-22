// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package iamproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	iamv1 "github.com/PRO-Robotech/kaname/pkg/api/kaname/cloud/iam/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Глаголы модели прав, которые спрашивает прокси.
const (
	VerbGet    = "v_get"
	VerbList   = "v_list"
	VerbUpdate = "v_update"
	VerbCreate = "v_create"
	VerbDelete = "v_delete"
)

// actionOf — действие каталога прав для глагола и типа объекта: kaname
// требует его в запросе, а отношение берёт из required_relation.
func actionOf(verb, objType string) string {
	res := "repositories"
	if objType == "registry_registry" {
		res = "registries"
	}
	return "registry." + res + "." + strings.TrimPrefix(verb, "v_")
}

var (
	// ErrUnauthenticated — kaname не признал токен клиента.
	ErrUnauthenticated = errors.New("kaname did not authenticate the token")
	// ErrCheckUnavailable — решения нет: kaname не ответил по существу.
	ErrCheckUnavailable = errors.New("authorization check is unavailable")
)

// Checker — вопрос о праве субъекта токеном самого субъекта.
type Checker interface {
	Check(ctx context.Context, id Identity, verb, object string) (bool, error)
}

// KanameChecker — публичный AuthorizeService.Check kaname. Спрашивает
// всегда о субъекте токена и его же токеном: своих прав у прокси нет, и
// спросить о чужом субъекте он не может по построению.
type KanameChecker struct {
	RPC     iamv1.AuthorizeServiceClient
	Timeout time.Duration
}

// NewKanameChecker — клиент публичного gRPC kaname.
func NewKanameChecker(conn *grpc.ClientConn, timeout time.Duration) *KanameChecker {
	return &KanameChecker{RPC: iamv1.NewAuthorizeServiceClient(conn), Timeout: timeout}
}

// Check — право субъекта токена на объект.
func (k *KanameChecker) Check(ctx context.Context, id Identity, verb, object string) (bool, error) {
	typ, oid, ok := strings.Cut(object, ":")
	if !ok {
		return false, fmt.Errorf("object %q is not <type>:<id>", object)
	}
	ctx, cancel := context.WithTimeout(ctx, k.Timeout)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+id.Token)
	resp, err := k.RPC.Check(ctx, &iamv1.AuthorizeCheckRequest{
		Subject:          id.Subject,
		Resource:         &iamv1.ResourceRef{Type: typ, Id: oid},
		Action:           actionOf(verb, typ),
		RequiredRelation: verb,
	})
	switch status.Code(err) {
	case codes.OK:
		return resp.GetAllowed(), nil
	case codes.Unauthenticated:
		return false, ErrUnauthenticated
	case codes.PermissionDenied:
		// Вопрос о себе отвергнут только тогда, когда kaname видит в токене
		// другого субъекта: решения о праве нет, есть отказ.
		return false, nil
	default:
		return false, fmt.Errorf("%w: %s", ErrCheckUnavailable, status.Code(err))
	}
}

// CachedChecker — кэш вердиктов чтения: ключ — хэш токена, объект и
// глагол, срок не больше TTL. Глаголы записи идут мимо кэша: отзыв права
// на запись действует со следующего запроса.
type CachedChecker struct {
	Inner Checker
	TTL   time.Duration
	// MaxEntries — предел записей: переполнение снимает кэш целиком.
	MaxEntries int
	Now        func() time.Time

	mu      sync.Mutex
	entries map[string]cached
}

type cached struct {
	allowed bool
	until   time.Time
}

// cacheable — глагол чтения.
func cacheable(verb string) bool { return verb == VerbGet || verb == VerbList }

// Check — вердикт из кэша либо от Inner.
func (c *CachedChecker) Check(ctx context.Context, id Identity, verb, object string) (bool, error) {
	if !cacheable(verb) || c.TTL <= 0 {
		return c.Inner.Check(ctx, id, verb, object)
	}
	sum := sha256.Sum256([]byte(id.Token))
	key := hex.EncodeToString(sum[:]) + "|" + verb + "|" + object
	now := c.Now()
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && now.Before(e.until) {
		c.mu.Unlock()
		return e.allowed, nil
	}
	c.mu.Unlock()
	allowed, err := c.Inner.Check(ctx, id, verb, object)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= c.MaxEntries {
		c.entries = map[string]cached{}
	}
	c.entries[key] = cached{allowed: allowed, until: now.Add(c.TTL)}
	return allowed, nil
}
