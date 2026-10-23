// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package kaname — запись ресурсов регистри в модель прав IAM. Пишет её сам
// registry-operator как служба registry: внутренний листенер kaname (:9091),
// RegisterResource и UnregisterResource по mTLS. Других писателей типов
// registry_* нет, поэтому оператор снимает только то, что записал сам.
//
// Контракт приёмника, который пакет держит на своей стороне до вызова:
//   - пустая цепь предков в RegisterResource — не умолчание, а утверждение
//     «предков нет», и kaname стирает уже записанных; регистрация без цепи
//     поэтому не отправляется вовсе;
//   - source_version применяется только строго более новая; снятие
//     действует, если его версия не старше записанной;
//   - повтор регистрации и снятие отсутствующего — успех.
package kaname

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Типы и отношения модели прав, которые пишет оператор.
const (
	// TypeRegistry — тип ресурса регистри.
	TypeRegistry = "registry_registry"
	// TypeRepository — тип репозитория регистри.
	TypeRepository = "registry_repository"
	// TypeProject — тип проекта IAM.
	TypeProject = "project"

	// RelationProject — ребро «регистри принадлежит проекту».
	RelationProject = "project"
	// RelationParent — ребро «репозиторий принадлежит регистри».
	RelationParent = "parent"
)

// Registration — одна регистрация (или снятие) ресурса.
type Registration struct {
	// Subject — ближайший предок: "<type>:<id>".
	Subject string
	// Relation — отношение предка к объекту.
	Relation string
	// Object — регистрируемый объект: "<type>:<id>".
	Object string
	// ParentChain — цепь предков от ближнего к дальнему; первый — Subject.
	ParentChain []string
	// ParentProjectID и ParentAccountID — область объекта в зеркале IAM.
	ParentProjectID string
	ParentAccountID string
	// Version — монотонная версия состояния объекта у владельца.
	Version time.Time
	// TraceID — корреляция в журналах IAM.
	TraceID string
}

// Registrar — запись и снятие ресурсов в IAM.
type Registrar interface {
	Register(ctx context.Context, r Registration) error
	Unregister(ctx context.Context, r Registration) error
}

var (
	// ErrRefused — IAM отказал по существу (права, форма); повтор того же
	// запроса не поможет.
	ErrRefused = errors.New("kaname refused the registration")
	// ErrUnavailable — IAM не ответил по существу; повтор возможен.
	ErrUnavailable = errors.New("kaname is unavailable")
	// ErrInvalid — запрос не отправлен: он нарушил бы контракт приёмника.
	ErrInvalid = errors.New("registration violates the kaname contract")
)

// idPart — допустимый идентификатор внутри объекта модели: без
// разделителей модели (":" "#" "@" "*") и пробелов. "/" законен только у
// репозитория (<id>/<имя>).
var (
	idPart   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	repoPart = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
)

// MaxRepositoryName — предел имени репозитория внутри регистри.
const MaxRepositoryName = 128

// ValidID — идентификатор годится в объект модели.
func ValidID(id string) bool { return idPart.MatchString(id) }

// ValidRepository — имя репозитория по грамматике Distribution и пределу.
func ValidRepository(name string) bool {
	return len(name) <= MaxRepositoryName && repoPart.MatchString(name)
}

// RegistryObject — объект регистри.
func RegistryObject(id string) string { return TypeRegistry + ":" + id }

// RepositoryObject — объект репозитория регистри.
func RepositoryObject(id, repo string) string { return TypeRepository + ":" + id + "/" + repo }

// ProjectObject — объект проекта.
func ProjectObject(project string) string { return TypeProject + ":" + project }

// Scope — проект и аккаунт регистри.
type Scope struct {
	Project string
	Account string
}

// RegistryRegistration — регистрация ресурса регистри под проектом.
func RegistryRegistration(id string, s Scope, version time.Time) Registration {
	return Registration{
		Subject:         ProjectObject(s.Project),
		Relation:        RelationProject,
		Object:          RegistryObject(id),
		ParentChain:     []string{ProjectObject(s.Project)},
		ParentProjectID: s.Project,
		ParentAccountID: s.Account,
		Version:         version,
		TraceID:         id,
	}
}

// RepositoryRegistration — регистрация репозитория под регистри: цепь
// [регистри, проект] — каскад выдачи по регистри или по области доходит
// до репозитория только по ней.
func RepositoryRegistration(id, repo string, s Scope, version time.Time) Registration {
	return Registration{
		Subject:         RegistryObject(id),
		Relation:        RelationParent,
		Object:          RepositoryObject(id, repo),
		ParentChain:     []string{RegistryObject(id), ProjectObject(s.Project)},
		ParentProjectID: s.Project,
		ParentAccountID: s.Account,
		Version:         version,
		TraceID:         id,
	}
}

// Version — монотонная версия ресурса регистри: момент создания заказа
// плюс поколение в микросекундах. Заказ с тем же id не появляется дважды
// (резерв id — надгробие), поколение только растёт.
func Version(created time.Time, generation int64) time.Time {
	return created.UTC().Truncate(time.Second).Add(time.Duration(generation) * time.Microsecond)
}

// splitObject — тип и идентификатор объекта "<type>:<id>".
func splitObject(o string) (string, string, bool) {
	t, id, ok := strings.Cut(o, ":")
	return t, id, ok && t != "" && id != ""
}

func validObject(o string) error {
	t, id, ok := splitObject(o)
	if !ok {
		return fmt.Errorf("%q is not <type>:<id>", o)
	}
	switch t {
	case TypeRegistry, TypeProject:
		if !ValidID(id) {
			return fmt.Errorf("%q: id is not a model identifier", o)
		}
	case TypeRepository:
		reg, repo, ok := strings.Cut(id, "/")
		if !ok || !ValidID(reg) || !ValidRepository(repo) {
			return fmt.Errorf("%q: id is not <registry>/<repository>", o)
		}
	default:
		return fmt.Errorf("%q: type %s is not written by the registry module", o, t)
	}
	return nil
}

// Validate — запрос держит контракт приёмника. register — регистрация (а
// не снятие): у неё цепь обязательна и начинается с субъекта.
func (r Registration) Validate(register bool) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
	}
	if err := validObject(r.Subject); err != nil {
		return bad("subject %v", err)
	}
	if err := validObject(r.Object); err != nil {
		return bad("object %v", err)
	}
	objType, _, _ := splitObject(r.Object)
	if objType != TypeRegistry && objType != TypeRepository {
		return bad("object type %s is not a registry resource", objType)
	}
	switch r.Relation {
	case RelationProject, RelationParent:
	default:
		return bad("relation %q is not a hierarchy relation", r.Relation)
	}
	if r.Version.IsZero() {
		return bad("source version is required: an unversioned write is applied over a newer one")
	}
	if !register {
		return nil
	}
	if len(r.ParentChain) == 0 {
		return bad("parent chain is empty: kaname would erase the ancestors of %s", r.Object)
	}
	if r.ParentChain[0] != r.Subject {
		return bad("parent chain starts with %s, the subject is %s", r.ParentChain[0], r.Subject)
	}
	for _, p := range r.ParentChain {
		if err := validObject(p); err != nil {
			return bad("parent chain: %v", err)
		}
	}
	if r.ParentProjectID == "" || ProjectObject(r.ParentProjectID) != r.ParentChain[len(r.ParentChain)-1] {
		return bad("parent chain must end with the project %s", r.ParentProjectID)
	}
	return nil
}
