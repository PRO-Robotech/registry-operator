// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package ledger — запись состояния резерва id (RegistryIDReservation)
// по образцу «прочитал — изменил — записал с версией объекта». Резерв
// пишут двое: проход заказа и эндпоинт заявок на репозитории; опись
// репозиториев — список, и слияние без версии потеряло бы чужую запись.
package ledger

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
)

// ErrNoChange — изменение не нужно; запись не делается.
var ErrNoChange = errors.New("reservation needs no change")

// Store — запись статуса резерва с оптимистичной блокировкой.
type Store struct {
	// Client — запись статуса.
	Client client.Client
	// Reader — чтение мимо кэша: решение строится на последней версии.
	Reader client.Reader
}

// Mutate читает резерв id, применяет fn и пишет статус с версией объекта;
// при конфликте версий — заново с чтения. fn вернула ErrNoChange — записи
// нет, возвращается прочитанный резерв; иная ошибка fn возвращается как
// есть.
func (s *Store) Mutate(ctx context.Context, id string, fn func(*registryv1alpha1.RegistryIDReservation) error) (*registryv1alpha1.RegistryIDReservation, error) {
	var out *registryv1alpha1.RegistryIDReservation
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		res := &registryv1alpha1.RegistryIDReservation{}
		if err := s.Reader.Get(ctx, types.NamespacedName{Name: id}, res); err != nil {
			return err
		}
		if err := fn(res); err != nil {
			out = res
			return err
		}
		if err := s.Client.Status().Update(ctx, res); err != nil {
			return err
		}
		out = res
		return nil
	})
	switch {
	case errors.Is(err, ErrNoChange):
		return out, nil
	case apierrors.IsConflict(err):
		return nil, fmt.Errorf("reservation %s kept changing: %w", id, err)
	}
	return out, err
}
