// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package controller — реконсиляция заказа Registry и ревизий шаблонов.
//
// Заказ ведётся пайплайном: стадия создаётся только после готовности каждого
// шага предыдущей, готовность каждого ресурса оператор читает из его status
// на текущем поколении. Каждая запись в чужой вид — после проверки «не
// чужой» (метки владения) и политики площадки; первая запись — CREATE (занятое
// имя — отказ), последующие — server-side apply с полной проверкой полей
// (fieldValidation=Strict). Удаление — строго в обратном порядке, по одному
// шагу, с ожиданием исчезновения.
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	registryv1alpha1 "github.com/PRO-Robotech/registry-operator/api/v1alpha1"
	"github.com/PRO-Robotech/registry-operator/internal/config"
	"github.com/PRO-Robotech/registry-operator/internal/kaname"
	"github.com/PRO-Robotech/registry-operator/internal/ocidigest"
)

// FieldManager — имя менеджера полей оператора.
const FieldManager = "registry-operator"

// Deps — общие зависимости контроллеров.
type Deps struct {
	Config   *config.Config
	Verifier ocidigest.Verifier
	// Kaname — запись ресурсов регистри в IAM (служба registry).
	Kaname   kaname.Registrar
	Recorder events.EventRecorder
	Clock    clock.PassiveClock
	// APIReader — чтение мимо кэша: чужие объекты (проверка «не чужой»),
	// рабочая нагрузка и Application без информеров на весь кластер.
	APIReader client.Reader
	// MaxConcurrentReconciles — параллельные проходы на вид.
	MaxConcurrentReconciles int
}

func (d *Deps) now() time.Time { return d.Clock.Now() }

// applyPatch — server-side apply: тело — объект целиком.
type applyPatch struct{}

func (applyPatch) Type() types.PatchType { return types.ApplyPatchType }

func (applyPatch) Data(obj client.Object) ([]byte, error) { return json.Marshal(obj) }

// errAdoptionRefused — объект с именем ресурса заказа существует и заказу не
// принадлежит.
var errAdoptionRefused = errors.New("object exists and does not belong to this order")

// owned — объект несёт метки владения этого заказа.
func owned(obj metav1.Object, reg *registryv1alpha1.Registry) bool {
	l := obj.GetLabels()
	return l[registryv1alpha1.LabelManagedBy] == registryv1alpha1.ManagedByValue &&
		l[registryv1alpha1.LabelOwnerUID] == string(reg.UID) &&
		l[registryv1alpha1.LabelID] == reg.Spec.ID &&
		l[registryv1alpha1.LabelNamespace] == reg.Namespace
}

// applyOwned пишет желаемый объект: CREATE, если его нет (занятое имя между
// чтением и записью — повтор с проверкой владения), иначе SSA — только при
// метках владения этого заказа. Возвращает объект, каким его вернул сервер.
func applyOwned(ctx context.Context, c client.Client, reader client.Reader, want *unstructured.Unstructured, reg *registryv1alpha1.Registry) (*unstructured.Unstructured, error) {
	cur := &unstructured.Unstructured{}
	cur.SetGroupVersionKind(want.GroupVersionKind())
	err := reader.Get(ctx, client.ObjectKeyFromObject(want), cur)
	switch {
	case apierrors.IsNotFound(err):
		created := want.DeepCopy()
		if err := c.Create(ctx, created, client.FieldOwner(FieldManager), client.FieldValidation("Strict")); err != nil {
			return nil, err
		}
		return created, nil
	case err != nil:
		return nil, err
	}
	if !owned(cur, reg) {
		return nil, errAdoptionRefused
	}
	if cur.GetDeletionTimestamp() != nil {
		return cur, nil
	}
	applied := want.DeepCopy()
	if err := c.Patch(ctx, applied, applyPatch{}, client.FieldOwner(FieldManager), client.ForceOwnership, client.FieldValidation("Strict")); err != nil {
		return nil, err
	}
	return applied, nil
}

// setCondition выставляет условие с текстом из каталога по коду причины.
func setCondition(conds *[]metav1.Condition, gen int64, typ string, ok bool, reason string) {
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}
	msg := registryv1alpha1.ReasonMessages[reason]
	if msg == "" {
		msg = reason
	}
	meta.SetStatusCondition(conds, metav1.Condition{
		Type: typ, Status: status, Reason: reason, Message: msg, ObservedGeneration: gen,
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// accepted — ревизия шаблона принята на текущем поколении.
func accepted(obj metav1.Object, conds []metav1.Condition) bool {
	c := meta.FindStatusCondition(conds, registryv1alpha1.ConditionAccepted)
	return c != nil && c.Status == metav1.ConditionTrue && c.ObservedGeneration == obj.GetGeneration()
}
