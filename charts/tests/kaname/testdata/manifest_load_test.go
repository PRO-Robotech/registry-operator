// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// manifest_load_test.go — манифест модуля registry, доставленный чартом
// registry-platform, проходит загрузку kaname ЕГО ЖЕ кодом.
//
// # Где исполняется
//
// Файл не принадлежит ни одному модулю этого репозитория: каталог testdata
// инструмент go пропускает, и `go test ./...` его не видит. Задание CI
// `kaname-manifest` (.github/workflows/charts.yml) кладёт его в клон
// PRO-Robotech/kaname на закреплённой ревизии и исполняет там: пакеты загрузки
// у kaname внутренние, извне модуля они не импортируются. Код kaname в этот
// репозиторий не копируется и с ним не распространяется.
//
// # Вход
//
// REGOP_DELIVERY — каталог доставки, собранный из РЕНДЕРА чарта: ключ
// registry.manifest.yaml ConfigMap модуля с подставленным аккаунтом системы
// (lib.py manifest). Судится то, что получит kaname, а не исходник чарта.
//
// # Стадии — те, что проходит доставка в kaname
//
//	чтение доставки (manifest.LoadDelivered, путь старта)
//	→ композиция модели (modelcompose.Compose над каноном образа)
//	→ допуск собранной модели (authzmodel.Admit)
//	→ суд правил ролей (roleexport.Check) и экспорт прав (roleexport.ExportRoleRules)
//
// Каталог прав для двух последних — встроенный посев kaname
// (seed.LoadPermissionRegistry и seed.LiteralRows): на работающем kaname тот же
// посев лежит строками базы, саму базу проба не поднимает.
//
// # Близнецы — каждый меняет РОВНО ОДИН факт
//
//	доставка как есть                         → все стадии без находок
//	+ неизвестный раздел верхнего уровня      → отказ чтения доставки
//	+ класс create у вложенного правила puller → находка суда ролей о repositories
//
// Первый близнец держит, что чтение доставки судит именно этот файл; второй —
// что суд ролей читает его правила, а не молчит над пустым перечнем.
package registryoperatorprobe_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/PRO-Robotech/kaname/internal/apps/kaname/seed"
	"github.com/PRO-Robotech/kaname/internal/authzmodel"
	"github.com/PRO-Robotech/kaname/internal/manifest"
	"github.com/PRO-Robotech/kaname/internal/manifest/roleexport"
	"github.com/PRO-Robotech/kaname/internal/modelcompose"
	"github.com/PRO-Robotech/kaname/internal/testsupport/catalogfixture"
)

// deliveryKey — имя файла в каталоге доставки: ключ ConfigMap модуля.
const deliveryKey = "registry.manifest.yaml"

// wantRoles — роли, которые объявляет модуль; выдаёт их registry-operator.
var wantRoles = []string{"registry.admin", "registry.puller", "registry.pusher"}

// delivered — текст доставки из рендера чарта.
func delivered(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("REGOP_DELIVERY")
	if dir == "" {
		t.Fatal("REGOP_DELIVERY не задан: каталог доставки из рендера чарта не подан, судить нечего")
	}
	data, err := os.ReadFile(filepath.Join(dir, deliveryKey))
	if err != nil {
		t.Fatalf("доставка не прочитана: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("доставка пуста")
	}
	return string(data)
}

// twin — текст доставки, в котором old заменён на repl ровно один раз.
func twin(t *testing.T, doc, old, repl string) string {
	t.Helper()
	if n := strings.Count(doc, old); n != 1 {
		t.Fatalf("близнец: строка %q встречается %d раз, нужна ровно одна", old, n)
	}
	return strings.Replace(doc, old, repl, 1)
}

// deliveryDir — каталог доставки с одним файлом, как его монтирует kaname.
func deliveryDir(t *testing.T, doc string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, deliveryKey), []byte(doc), 0o600); err != nil {
		t.Fatalf("каталог доставки: %v", err)
	}
	return dir
}

// load — чтение доставки путём старта kaname; отказ — красное.
func load(t *testing.T, doc string) *manifest.Manifest {
	t.Helper()
	report, err := manifest.LoadDelivered(deliveryDir(t, doc))
	t.Logf("чтение доставки: файлов %d, манифестов %d, находок %d, модули %v",
		report.PathsSeen, report.ManifestsRead, len(report.Findings), report.Modules())
	if err != nil {
		t.Fatalf("kaname отверг доставку: %v", err)
	}
	if report.ManifestsRead != 1 || len(report.Manifests) != 1 {
		t.Fatalf("прочитано манифестов %d, разобрано %d, ожидался один", report.ManifestsRead, len(report.Manifests))
	}
	if got := report.Modules(); !slices.Equal(got, []string{"registry"}) {
		t.Fatalf("модули доставки %v, ожидался [registry]", got)
	}
	return report.Manifests[0]
}

// mustActions — действия каталога прав, привязанные к ресурсам модулей, тем же
// путём, что у применителя ролей на старте kaname.
func mustActions(t *testing.T) []roleexport.Action {
	t.Helper()
	reg, err := seed.LoadPermissionRegistry(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("каталог прав не прочитан: %v", err)
	}
	rows := reg.All()
	entries := make([]roleexport.CatalogEntry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, roleexport.CatalogEntry{
			FQN:              r.FQN,
			RequiredRelation: r.RequiredRelation,
			ScopeObjectType:  r.ScopeExtractor.ObjectType,
		})
	}
	actions, _ := roleexport.Attribute(entries)
	if len(actions) == 0 {
		t.Fatal("привязано ноль действий каталога: судить роли нечем")
	}
	return actions
}

// TestDeliveredRegistryManifestPassesEveryKanameStage — предмет: доставка
// чарта проходит все стадии kaname без находок, и каждая стадия её читала.
func TestDeliveredRegistryManifestPassesEveryKanameStage(t *testing.T) {
	doc := delivered(t)
	m := load(t, doc)

	if !m.RolesDeclared() || len(m.Roles) != len(wantRoles) {
		t.Fatalf("ролей разобрано %d (раздел объявлен: %v), ожидалось %d", len(m.Roles), m.RolesDeclared(), len(wantRoles))
	}

	composed, crep, err := modelcompose.Compose(authzmodel.DSL, []*manifest.Manifest{m})
	t.Logf("композиция: %s", crep.Census())
	if err != nil {
		t.Fatalf("kaname отверг композицию модели: %v", err)
	}
	if crep.ManifestsSeen != 1 {
		t.Fatalf("композиция осмотрела манифестов %d, ожидался один", crep.ManifestsSeen)
	}
	// Раздел resources объявляет два типа канона образа: композиция обязана
	// ПЕРЕУТВЕРДИТЬ оба побайтово — не добавить нового и не принять прежнюю
	// редакцию переходно.
	wantTypes := []string{"registry_registry", "registry_repository"}
	if crep.ResourcesSeen != 2 || len(crep.Composed) != 0 || len(crep.PriorEdition) != 0 ||
		!slices.Equal(crep.Reaffirmed, wantTypes) {
		t.Fatalf("композиция: ресурсов %d, добавлено %v, прежняя редакция %v, переутверждено %v; "+
			"ожидалось 2 ресурса и переутверждены %v", crep.ResourcesSeen, crep.Composed,
			crep.PriorEdition, crep.Reaffirmed, wantTypes)
	}

	adm, err := authzmodel.Admit(composed)
	t.Logf("допуск: %s", adm.Census())
	if err != nil {
		t.Fatalf("допуск собранной модели: %v", err)
	}
	if len(adm.Findings) != 0 {
		t.Fatalf("допуск отверг собранную модель: %v", adm.Findings)
	}

	actions := mustActions(t)
	rep := roleexport.Check(catalogfixture.Facts(), m, actions)
	t.Logf("суд ролей: %s", rep.Summary())
	if len(rep.Faults) != 0 {
		t.Fatalf("суд ролей нашёл %d: %v", len(rep.Faults), rep.Faults)
	}
	if !rep.RulesJudged || !rep.NamedJudged {
		t.Fatalf("стадии суда ролей не исполнялись: правила %v, поимённая форма %v", rep.RulesJudged, rep.NamedJudged)
	}
	// Три роли по два правила (верхнее и вложенное); пар (ресурс, класс):
	// на регистри 1 + 2 + 5, на репозиториях 2 + 3 + 4.
	if rep.Rules.RolesRead != 3 || rep.Rules.RulesRead != 6 || rep.Rules.PairsJudged != 17 {
		t.Fatalf("суд ролей осмотрел ролей %d, правил %d, пар %d; ожидалось 3, 6, 17",
			rep.Rules.RolesRead, rep.Rules.RulesRead, rep.Rules.PairsJudged)
	}

	rules, faults, _ := roleexport.ExportRoleRules(catalogfixture.Facts(), m, actions)
	if len(faults) != 0 {
		t.Fatalf("экспорт прав нашёл %d: %v", len(faults), faults)
	}
	for _, id := range wantRoles {
		if len(rules[id]) == 0 {
			t.Errorf("роль %s не дала ни одного правила прав", id)
		}
	}
}

// TestDeliveryWithAnUnknownSectionIsRefused — близнец: тот же текст и
// неизвестный раздел верхнего уровня; чтение доставки отказывает.
func TestDeliveryWithAnUnknownSectionIsRefused(t *testing.T) {
	doc := twin(t, delivered(t), "\nmodule: registry\n", "\nmodule: registry\nunknownSection: true\n")
	report, err := manifest.LoadDelivered(deliveryDir(t, doc))
	t.Logf("чтение доставки: манифестов %d, находок %d", report.ManifestsRead, len(report.Findings))
	if err == nil {
		t.Fatal("доставка с неизвестным разделом принята: чтение не судит этот файл")
	}
	if report.ManifestsRead != 1 || len(report.Findings) == 0 {
		t.Fatalf("отказ не о файле доставки: манифестов %d, находок %v", report.ManifestsRead, report.Findings)
	}
}

// TestPullerWithCreateOnRepositoriesIsRefused — близнец: puller получает класс
// create на репозиториях, которого у типа registry_repository нет; суд ролей
// называет находку о repositories.
func TestPullerWithCreateOnRepositoriesIsRefused(t *testing.T) {
	doc := twin(t, delivered(t), "classes: [get, list]\n", "classes: [get, list, create]\n")
	m := load(t, doc)
	rep := roleexport.Check(catalogfixture.Facts(), m, mustActions(t))
	t.Logf("суд ролей: %s", rep.Summary())
	var hit bool
	for _, f := range rep.Faults {
		var got roleexport.Finding
		if errors.As(f, &got) && got.Role == "registry.puller" && got.Resource == "repositories" {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("находки о create на repositories у registry.puller нет: %v", rep.Faults)
	}
}
