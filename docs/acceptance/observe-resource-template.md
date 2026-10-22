# registry-operator — единый шаблон RegistryObserveResourceTemplate — Acceptance

> **Статус:** DRAFT
> **Статическая форма:** DRAFT; действующий вердикт выводится из внешнего события и записи
> ревью; строка статуса после ревью не меняется.
> **История review:** только дописывается строкой «дата · круг · вердикт · SHA-256 редакции ·
> путь записи»:
> - 2026-10-22 · круг 1 · ⛔ возврат (блокирующих 2: PRODUCER, DoD) ·
>   `d291fcfcf471a4b9e4ca7ad5fe48546b902872c1ff9e4d2281935f045dfd5e75` · запись вне дерева
> **Дата:** 2026-10-22
> **Задача:** PRO-Robotech/registry-operator#<N> — номер ставит `git-operator` до коммита
> (допущение Д1, §9).
> **База измерений:** ветка `8-asm-w1-k1` @`8c47fe7` (`git ls-remote origin
> refs/heads/8-asm-w1-k1` → `8c47fe70c3502d5bd366268a72b5611ac64656ed`, 2026-10-22).

## 0. Обзор

Сейчас registry-operator держит **шесть** видов observed-шаблонов (`Observed*Template`,
группа `observed.registry.in-cloud.io`), по одному на каждый порождаемый ресурс заказа, и
каждый рендерит **только `spec`** цели: apiVersion, kind и metadata оператор пишет сам, а
`RegistryTemplate.spec.templates` несёт шесть типизированных ссылок.

Изменение сводит их к **одному** виду `RegistryObserveResourceTemplate`, устроенному по
образцу `ClusterClaimObserveResourceTemplate` (cluster-claim-operator v0.0.10): шаблон несёт
**указатель на весь ресурс** — `spec.apiVersion` + `spec.kind` порождаемого — и go-template
`spec.value`, который рендерит ресурс целиком: `metadata` (метки и аннотации) и `spec`.
`RegistryTemplate` ссылается на такие шаблоны **по блокам** — `storage`, `secrets`, `identity`,
`access`, `workload`, `route`, — а сам только заказывает услугу. Порождаемые ресурсы IAM
называются видами `IAMServiceAccount` и `IAMAccessBinding`.

Поведение пайплайна — стадии, готовность, владение, порядок удаления — **не меняется**;
меняется форма контракта шаблонов и то, что шаблон вправе написать в `metadata`.

Документ описывает наблюдаемое поведение API Kubernetes (CRD, отказ сервера API, условия
`status`). Конвенции gRPC-края Kachō (плоский message, `Operation`, коды gRPC) к CRD
оператора не применяются: обёртка `apiVersion/kind/metadata/spec/status` здесь — форма API
Kubernetes, а не нарушение `api-flat-message`.

## 1. Основание

Решения владельца (дословно):

- 2026-10-22: «ObservedIamServiceAccountTemplate - сведи к одному как это сделано в
  систем-тест - с указателем на весь ресурс», «ObservedIamAccessBindingTemplate».
- ранее: «сделай так же через observed.registry.in-cloud.io в котором описывай шаблоны по
  аналогии clusterclaimobserveresourcetemplates.clusterclaim.in-cloud.io, а в регистри
  темплейте вызывай шаблоны в разных блоках, а регистри просто заказывает услугу»;
  «шаблоны могут поддерживать gotemplate».
- об именах: «Не называй ресурсы с пересечениями … придумай уникальное имя» → «не имя а
  kind» → «IAMServiceAccount ок другим тоже префиксы добавь».

Решения диспетчера (2026-10-22): вид `RegistryObserveResourceTemplate` (префикс по правилу
владельца), группа `observed.registry.in-cloud.io`, уровень Cluster; шаблон указывает на весь
порождаемый ресурс, как `ClusterClaimObserveResourceTemplate`; `RegistryTemplate` ссылается
на шаблоны по блокам; виды IAM — `IAMServiceAccount`, `IAMAccessBinding`; шесть старых видов
снимаются целиком без миграции и конверсии (выпуска не было).

Решения диспетчера по вопросам круга 1 (2026-10-22): указатель — поля `spec.apiVersion` +
`spec.kind`, `value` рендерит `metadata` + `spec`, как у образца (Д3, §9); предел времени
рендера не обещается — модель доверия названа в §4.3.

## 2. Что НЕ входит

- Переименование видов в kaname-iam-operator — отдельная задача того репозитория (§3).
- Список шаблонов в одном блоке, необязательные блоки, новые виды порождаемого сверх
  закрытого перечня §4.2, порядок стадий, задаваемый из `RegistryTemplate`.
- Правило готовности, задаваемое шаблоном (выбор — встроенная таблица, §4.6).
- Изменяемый `value` шаблона и перерисовка живых ресурсов при правке шаблона (у образца так;
  здесь — нет, §4.7).
- Предел времени исполнения рендера: этим изменением не вводится и не утверждается (§4.3).
- Миграция, конверсия, параллельное обслуживание старых видов.
- Изменения стадий, правил готовности, порядка удаления, контекста рендера сверх §4.5.

## 3. Внешняя зависимость и порядок

| что | где | предикат готовности |
|---|---|---|
| виды `IAMServiceAccount` и `IAMAccessBinding` в CRD kaname-iam-operator | PRO-Robotech/kaname-iam-operator#9 «iam: kind SA и AccessBinding → IAMServiceAccount, IAMAccessBinding» (OPEN, `blocked`; `gh issue view 9 -R PRO-Robotech/kaname-iam-operator`, 2026-10-22) | ревизия kio, CRD которой объявляет `kind: IAMServiceAccount` и `kind: IAMAccessBinding`, закреплена в **обоих** пинах IAM-CRD registry-operator: `hack/crds.lock` (envtest; сейчас kio@`2f9abc31`) и `charts/tests/crds.sh` (пробы допуска `F-ADM`; сейчас kio@`ea747526`) |

Двух имён не держим: приёмка требует новых видов, старые (`ServiceAccount`, `AccessBinding`
группы `iam.kaname.in-cloud.io`) в контракте оператора не допускаются ни на одном шаге.
Реализация этой приёмки **стартует после** того, как ревизия kio с новыми видами доступна
для пина; до этого Given сценариев с IAM-видами непостроимы (fixture envtest берёт CRD из
`hack/crds.lock`, пробы допуска — из `charts/tests/crds.sh`).

Группа и версия IAM-видов в этой приёмке — `iam.kaname.in-cloud.io/v1alpha1`, имена ресурсов
API — `iamserviceaccounts`, `iamaccessbindings` (допущение Д2, §9): если задача kio утвердит
иное, перечень §4.2 следует её одобренному контракту одной строкой, без нового круга по
остальному.

## 4. Контракт

### 4.1 `RegistryObserveResourceTemplate` (`observed.registry.in-cloud.io/v1alpha1`)

Имя CRD — `registryobserveresourcetemplates.observed.registry.in-cloud.io`; уровень Cluster;
`shortName: rort`; категория `registry-templates`; подресурс `status`.
Колонки вывода: `Template`, `Version`, `APIVersion`, `Kind`, `Accepted`.

| поле `spec` | обязательность | правило |
|---|---|---|
| `templateName` | да | как сейчас: `^[a-z0-9]([a-z0-9-]{0,40}[a-z0-9])?$`, ≤ 42 |
| `version` | да | как сейчас: `^v[0-9]{1,6}$` |
| `apiVersion` | да | указатель: вместе с `kind` — ровно одна пара из §4.2 |
| `kind` | да | указатель: вместе с `apiVersion` — ровно одна пара из §4.2 |
| `nameSuffix` | для `IAMServiceAccount` и `IAMAccessBinding` — обязателен; для прочих — запрещён | как сейчас: `^(-[a-z0-9]+){1,3}$`, ≤ 24 |
| `chart` | для `Addon` — обязателен; для прочих — запрещён | как сейчас: `repoURL` `oci://…`, `name`, `version` semver, `digest` `sha256:…` |
| `targetNamespace` | только для `HTTPRoute`, необязателен (пусто — namespace заказа); для прочих — запрещён | как сейчас: DNS label, ≤ 63 |
| `value` | да | go-template ресурса без указателя и имени: `metadata` + `spec` (§4.3); 1..65536 символов |

Правила объекта:

- `metadata.name == spec.templateName + '-' + spec.version`, иначе отказ сервера API с
  сообщением `metadata.name must be <templateName>-<version>` (как сейчас).
- `spec` неизменяем целиком: `template spec is immutable; publish a new version` (как сейчас).
- пара указателя вне §4.2 — отказ сервера API с сообщением
  `apiVersion and kind must name one of: <перечень §4.2 через запятую>`.
- `nameSuffix`/`chart`/`targetNamespace` не у своего вида либо обязательное отсутствует — отказ
  сервера API с сообщением, называющим поле и вид:
  `nameSuffix is required for IAMServiceAccount and IAMAccessBinding and forbidden for other kinds`,
  `chart is required for Addon and forbidden for other kinds`,
  `targetNamespace is allowed only for HTTPRoute`.
- `status.conditions[type=Accepted]`: `True`/`Accepted` — ревизия разобрана, прошла AST-гейт
  и проверки своего вида; `False`/`TemplateInvalid` — не прошла, сообщение называет причину;
  `False`/`InUse` — удаляется, но на неё ссылается `RegistryTemplate` (как сейчас).
- финализатор `registry.in-cloud.io/in-use` держит удаление, пока ссылается `RegistryTemplate`
  (как сейчас).

### 4.2 Закрытый перечень указателей и блоков

| блок `RegistryTemplate` | `apiVersion` | `kind` | уровень | доп. поле шаблона | стадия |
|---|---|---|---|---|---|
| `storage` | `vault.in-cloud.io/v1alpha1` | `S3BucketClaim` | Namespaced | — | 1 |
| `secrets` | `vault.in-cloud.io/v1alpha1` | `VaultClaim` | Namespaced | — | 1 |
| `identity` | `iam.kaname.in-cloud.io/v1alpha1` | `IAMServiceAccount` | Namespaced | `nameSuffix` | 1 |
| `access` | `iam.kaname.in-cloud.io/v1alpha1` | `IAMAccessBinding` | Namespaced | `nameSuffix` | 2 |
| `workload` | `addons.in-cloud.io/v1alpha1` | `Addon` | **Cluster** | `chart` | 2 |
| `route` | `gateway.networking.k8s.io/v1` | `HTTPRoute` | Namespaced | `targetNamespace` | 3 |

Перечень один: им судят сервер API (правило CRD), контроллер шаблонов, `RegistryTemplate`,
RBAC оператора и политика площадки. Единственный вид уровня Cluster — `Addon`; любой другой
вид уровня Cluster вне перечня и отвергается как вне перечня. Стадии и регистрация в IAM
внутри стадии 1 — как сейчас; порядок задаёт оператор, а не `RegistryTemplate`.

### 4.3 Что рендерит `value`

Результат рендера — YAML-отображение, верхние ключи которого — **только** `metadata` и `spec`:

- `spec` — обязательное отображение; ключи, которые ставит оператор, шаблон не пишет (как
  сейчас: `deletionPolicy` у `S3BucketClaim`/`VaultClaim`/`IAMServiceAccount`/`IAMAccessBinding`,
  `name` у `IAMServiceAccount`; `chart`, `repoURL`, `version`, `releaseName`, `targetNamespace`,
  `path`, `valuesSources`, `initDependencies` у `Addon`).
- `metadata` — необязательное отображение, в нём допустимы **только** `labels` и
  `annotations` (отображения строка → строка).
- Запрещены ключи меток и аннотаций: `app.kubernetes.io/managed-by`; любой с префиксом
  `registry.in-cloud.io/`; любой, чей префикс — `kubernetes.io`, `k8s.io` или их поддомен;
  любой, чей префикс — группа API порождаемого вида или её поддомен (например,
  `vault.in-cloud.io/…` у `VaultClaim`).
- Любой другой верхний ключ (`apiVersion`, `kind`, `status`, …) и любой другой ключ `metadata`
  (`name`, `namespace`, `generateName`, `ownerReferences`, `finalizers`, …) — отказ.

Указатель и имя пишет только оператор: `apiVersion`/`kind` — из `spec` шаблона,
`metadata.name` — из политики площадки и `nameSuffix`, `metadata.namespace` — namespace заказа
(для `HTTPRoute` — `targetNamespace`, если задан; для `Addon` — нет). Метки владения оператора
(`app.kubernetes.io/managed-by`, `registry.in-cloud.io/{id,namespace,owner-uid,template,step}`)
и аннотация `registry.in-cloud.io/chart-digest` у `Addon` добавляются к меткам и аннотациям
шаблона; пересечение невозможно, потому что такие ключи шаблону запрещены.

Допуск формы шаблона (AST-гейт) и пределы — как сейчас, без изменений: функции — закрытый
набор `toJson`, `toYaml`, `nindent` (0..64) и встроенные `and`, `or`, `not`, `eq`, `ne`, `lt`,
`le`, `gt`, `ge`, `len`, `index`, `printf`; `define`/`template`/`block`, переменные и
присваивания запрещены; `with`/`range` — только над полем `.Params`; данные заказа и статусы
печатаются только через `toJson`, `toYaml | nindent` — только для `.Params`/`.Template`;
`missingkey=error`; вывод ≤ 256 KiB; строгий YAML (дубликаты ключей — отказ, верхний
уровень — отображение). Гейт судит весь `value`, в том числе `metadata`.

**Модель доверия.** Шаблон — объект платформы: уровень Cluster, писатель — платформа, а не
арендатор. Ни одна роль в дереве не даёт арендатору записи в группу
`observed.registry.in-cloud.io`; оператор шаблоны читает и ведёт их `status`/финализатор, но
не создаёт и не удаляет (ORT-61; предикат P9 §7). Рендер ограничен закрытым набором функций и
закрытой формой шаблона (держатель — AST-гейт, ORT-11, ORT-14), `missingkey=error` (ORT-36),
пределом размера источника (ORT-12) и пределом размера вывода (ORT-35). Предел времени
исполнения рендера этим контрактом **не обещается**.

### 4.4 `RegistryTemplate.spec.templates` — по блокам

`spec.templates` — отображение с **шестью обязательными** ключами-блоками из §4.2; значение
каждого — `{name, version}` ревизии `RegistryObserveResourceTemplate` (объект
`<name>-<version>`). Прочие поля `RegistryTemplate` (`templateName`, `version`, `claimPolicy`,
`params`, `stallTimeout`, неизменяемость `spec`) — как сейчас.

```yaml
spec:
  templates:
    storage:  {name: zot-s3,     version: v1}
    secrets:  {name: zot-vault,  version: v1}
    identity: {name: zot-robot,  version: v1}
    access:   {name: zot-pusher, version: v1}
    workload: {name: zot-addon,  version: v1}
    route:    {name: zot-route,  version: v1}
```

`Accepted=True` у `RegistryTemplate` — когда `params` разобраны строго,
`params.storage.region` — непустое имя региона (как сейчас: иначе `Accepted=False`,
`TemplateInvalid`, сообщение называет `params.storage.region`), и каждая из шести ссылок
существует, не удаляется, `Accepted=True` и её указатель равен указателю блока по §4.2.
Несовпадение указателя — `Accepted=False`, reason `TemplateInvalid`, сообщение называет блок,
объект шаблона, его указатель и требуемый блоком указатель.

Со стороны заказа сверка вида сохраняется (как сейчас): собранный объект, чьи `apiVersion` и
`kind` не равны виду шага, не применяется — условие шага `False`, reason `PolicyViolation`,
diagnostics называет `kind`. Это вторая линия к `Accepted` ревизий, неизменяемости `spec` и
финализатору `in-use`, а не их замена, и при переходе не снимается.

### 4.5 Контекст рендера

Как сейчас (`.Registry`, `.ID`, `.Login`, `.Params`, `.Template`, `.Status`), с одним
изменением: ключ `.Status` — это `kind` порождаемого ресурса готового шага:
`.Status.S3BucketClaim`, `.Status.VaultClaim`, `.Status.IAMServiceAccount`,
`.Status.IAMAccessBinding`, `.Status.Addon`. Прежние ключи `.Status.IamServiceAccount` и
`.Status.IamAccessBinding` не существуют (обращение — отказ рендера по `missingkey=error`).

### 4.6 Готовность — встроенная таблица по указателю

Шаблон правила готовности не несёт. Готовность порождённого оператор судит встроенной
таблицей по указателю — так же, как образец судит готовность кодом по слоту. Правила — как
сейчас:

| `kind` | готов, когда | не готов (reason условия шага) |
|---|---|---|
| `S3BucketClaim` | не удаляется; `status.observedGeneration == metadata.generation`; `status.phase == Ready`; условие `KeysWritten=True` на текущем поколении | `phase=Failed` → `ResourceDegraded`; старое поколение → `ResourceStale`; иначе `ResourceProgressing` |
| `VaultClaim` | то же, условие `RolesApplied=True` | то же |
| `IAMServiceAccount` | `Drifted`/`Conflict` не `True`; `Ready=True` на текущем поколении; `status.id` непуст; при непустом `spec.keys` — `KeysReady=True` | `Drifted`/`Conflict=True` или `Ready=False` с reason `Invalid`/`Forbidden`/`Conflict`/`NotOwned` → `ResourceDegraded`; старое поколение → `ResourceStale`; иначе `ResourceProgressing` |
| `IAMAccessBinding` | как `IAMServiceAccount` без `KeysReady` | то же |
| `Addon` | `status.observedGeneration` текущее; `Degraded` не `True`; `Synced`, `Healthy`, `Ready` — `True`; Deployment рабочей нагрузки `Available` | старое поколение → `ResourceStale`; иначе `ResourceProgressing`/`ResourceDegraded` как сейчас |
| `HTTPRoute` | у каждого parent `Accepted=True` и `ResolvedRefs=True` на текущем поколении | `ResourceProgressing` |

Объекта нет — `ResourceMissing`. Обоснование выбора: правило готовности — часть безопасности
(IAM-выдача считается готовой только на текущем поколении и без дрейфа); правило в шаблоне
потребовало бы языка выражений в данных платформы и дало бы шаблону объявить «готово» раньше
факта. Перечень видов закрыт, поэтому встроенная таблица полна.

### 4.7 Соответствие образцу и отличия

| свойство | `ClusterClaimObserveResourceTemplate` | `RegistryObserveResourceTemplate` | почему так |
|---|---|---|---|
| указатель | `spec.apiVersion` + `spec.kind`, неизменяемы | то же | как у образца: вид известен до рендера |
| что рендерит `value` | `metadata` + `spec` | то же | как у образца — шаблон ресурса целиком |
| `apiVersion`/`kind`/`name`/`namespace` в `value` | молча отбрасываются | **отказ** с именем ключа | принятое и проигнорированное запрещено; значение выражается одним способом |
| перечень видов | открыт | закрыт (§4.2), отказ сервера API | шаблон не выбирает, какой ресурс завести за пределами пайплайна регистри |
| имя | соглашение об имени, не проверяется | `templateName-version`, правило CRD | как сейчас: ревизия адресуется однозначно |
| изменяемость | `value` изменяем, правка перерисовывает живые ресурсы всех потребителей | `spec` неизменяем; новая редакция — новая ревизия | раскатка правки — новой ревизией `RegistryTemplate`, а не мгновенно у всех заказов |
| `status` | нет | `Accepted` | платформа видит отказ шаблона до первого заказа; `RegistryTemplate` судит ссылки по нему |
| готовность | в коде по слоту | встроенная таблица по указателю | как у образца (§4.6) |
| доп. поля вида | нет | `nameSuffix`, `chart`, `targetNamespace` — каждое только у своего вида | имя объекта, пин чарта и namespace маршрута должны быть известны до рендера: по ним судят допуск ревизии и владение |
| ссылки со стороны заказа | типизированные поля `ClusterClaim` | блоки `RegistryTemplate.spec.templates` | решение владельца: блоки в `RegistryTemplate` |

## 5. Сценарии

Фикстуры (Given строятся ими; посев в этом круге не выполнялся — §8):
`F-API` — envtest с CRD из `config/crd/bases` и `hack/crds.lock` (настоящий сервер API, правила
CRD исполняются им) — `internal/controller/suite_test.go`;
`F-CTRL` — то же плюс запущенные контроллеры шаблонов и заказа, политика из
`config/samples/operator-config.yaml`;
`F-STATUS` — запись `status` порождённых объектов — `internal/testkit/status.go`;
`F-E2E` — кластер kind с образцом оператора и `config/samples` — `test/e2e`;
`F-ADM` — пробы политик допуска чарта на кластере kind — `charts/tests/admission`.

Общий Given для групп C–E: namespace `cust01` с меткой `registry.in-cloud.io/tenant: "true"`
и метками проекта/аккаунта IAM; шесть принятых шаблонов и `RegistryTemplate zot-v1` из
`config/samples/templates` в новой форме; `Registry abc123` в `cust01` по `zot-v1`.

### A. Форма шаблона (сервер API и контроллер шаблонов)

**ORT-01 — шаблон каждого вида перечня принимается**
**Given** `F-CTRL`
**When** создаётся `RegistryObserveResourceTemplate` `zot-robot-v1`:
`spec: {templateName: zot-robot, version: v1, apiVersion: iam.kaname.in-cloud.io/v1alpha1,
kind: IAMServiceAccount, nameSuffix: -ci, value: "spec:\n  enabled: true\n"}`
(и так для каждой строки §4.2 с её обязательным полем: `zot-s3-v1`, `zot-vault-v1`,
`zot-pusher-v1`, `zot-addon-v1` с разрешённым пином, `zot-route-v1` с
`targetNamespace: registry-edge`)
**Then** сервер API создаёт объект; `kubectl get rort` показывает колонки `APIVersion`,
`Kind` с указателем
**And** `status.conditions[Accepted]` = `True`, reason `Accepted`; на объекте финализатор
`registry.in-cloud.io/in-use`.

**ORT-02 — имя объекта обязано быть `templateName-version`** — близнец ORT-01
**When** как ORT-01, но `metadata.name: zot-robot-2`
**Then** отказ сервера API (HTTP 422, reason `Invalid`), сообщение
`metadata.name must be <templateName>-<version>`; объекта нет.

**ORT-03 — `spec` неизменяем** — близнец: правка метки объекта шаблона проходит
**Given** `zot-robot-v1` из ORT-01
**When** patch `spec.value` на `"spec:\n  enabled: false\n"`
**Then** отказ сервера API, сообщение `template spec is immutable; publish a new version`
**And** близнец: patch `metadata.labels.owner: platform` того же объекта — принят.

**ORT-04 — указатель только из закрытого перечня** — близнец ORT-01 той же строки перечня
| пример | `apiVersion` | `kind` | близнец |
|---|---|---|---|
| a. другая версия | `gateway.networking.k8s.io/v1beta1` | `HTTPRoute` | `…/v1` `HTTPRoute` (ORT-01) |
| b. старое имя IAM | `iam.kaname.in-cloud.io/v1alpha1` | `ServiceAccount` | `IAMServiceAccount` (ORT-01) |
| c. старое имя IAM | `iam.kaname.in-cloud.io/v1alpha1` | `AccessBinding` | `IAMAccessBinding` (ORT-01) |
| d. вид уровня Cluster вне перечня | `rbac.authorization.k8s.io/v1` | `ClusterRole` | `addons.in-cloud.io/v1alpha1` `Addon` — единственный допустимый вид уровня Cluster (ORT-01) |
| e. вид вне перечня | `v1` | `ConfigMap` | `vault.in-cloud.io/v1alpha1` `VaultClaim` (ORT-01) |

**When** создаётся шаблон с указателем из строки (прочие поля — как у близнеца; у строк b, c —
`nameSuffix: -ci`, у d, e — без доп. полей)
**Then** отказ сервера API, сообщение `apiVersion and kind must name one of: …` с полным
перечнем §4.2; объекта нет.

**ORT-05 — `nameSuffix` обязателен у IAM-видов** — близнец ORT-01 (`zot-robot-v1`)
**When** как ORT-01, без `nameSuffix`
**Then** отказ сервера API, сообщение
`nameSuffix is required for IAMServiceAccount and IAMAccessBinding and forbidden for other kinds`.

**ORT-06 — `nameSuffix` запрещён у прочих видов** — близнец ORT-01 (`zot-s3-v1`)
**When** шаблон `S3BucketClaim` с `nameSuffix: -x`
**Then** отказ сервера API с тем же сообщением, что ORT-05.

**ORT-07 — `chart` обязателен у `Addon` и запрещён у прочих** — близнецы ORT-01
(`zot-addon-v1`, `zot-vault-v1`)
**When** a) шаблон `Addon` без `chart`; b) шаблон `VaultClaim` с `chart` из `zot-addon-v1`
**Then** оба — отказ сервера API, сообщение `chart is required for Addon and forbidden for other kinds`.

**ORT-08 — `targetNamespace` только у `HTTPRoute`** — близнец ORT-01 (`zot-vault-v1`)
**When** шаблон `VaultClaim` с `targetNamespace: registry-edge`
**Then** отказ сервера API, сообщение `targetNamespace is allowed only for HTTPRoute`.

**ORT-09 — пин чарта только из разрешённых площадкой** (поведение как сейчас)
**Given** `F-CTRL`
**When** шаблон `Addon` с `chart.digest`, которого нет среди пинов политики
**Then** `Accepted=False`, reason `TemplateInvalid`, сообщение называет пин
**And** близнец: тот же шаблон с разрешённым пином — `Accepted=True`.

**ORT-10 — namespace маршрута только из разрешённых площадкой** (как сейчас)
**Given** `F-CTRL`, политика: `route.namespaces: [registry-edge]`, `allowOrderNamespace: false`
**When** a) шаблон `HTTPRoute` с `targetNamespace: other-edge`; b) без `targetNamespace`
**Then** a) `Accepted=False`, `TemplateInvalid`, сообщение называет `other-edge`;
b) `Accepted=False`, `TemplateInvalid`, сообщение
`routes in the order namespace are not allowed by the platform`
**And** близнецы: a) `targetNamespace: registry-edge` — `True`; b) та же ревизия при
`allowOrderNamespace: true` — `True`.

**ORT-11 — гейт судит весь `value`, включая `metadata`**
**Given** `F-CTRL`
**When** шаблон `VaultClaim` с `value`, где
`metadata: {labels: {registry: {{ .Registry.Name }}}}` (печать данных заказа без `toJson`)
**Then** `Accepted=False`, reason `TemplateInvalid`, сообщение называет позицию и правило
печати
**And** близнец: `{{ .Registry.Name | toJson }}` в той же позиции — `Accepted=True`.

**ORT-12 — предел размера `value`**
**When** шаблон с `value` длиной 65537 символов ASCII (рабочий шаблон, дополненный комментарием
go-template)
**Then** отказ сервера API (предел длины поля `spec.value`)
**And** близнец: тот же шаблон длиной ровно 65536 символов — создан, `Accepted=True`.

**ORT-13 — удаление ревизии, на которую ссылаются** (как сейчас)
**Given** `zot-robot-v1`, на который ссылается `RegistryTemplate zot-v1` (блок `identity`)
**When** `kubectl delete rort zot-robot-v1`
**Then** объект остаётся с `deletionTimestamp`, `Accepted=False`, reason `InUse`
**And** после удаления `zot-v1` объект исчезает
**And** близнец: ревизия без ссылок удаляется сразу.

**ORT-14 — закрытая форма шаблона** (как сейчас) — близнец ORT-01 (`zot-vault-v1`)
**Given** `F-CTRL`
| пример | конструкция в `value` шаблона `VaultClaim` | сообщение называет | близнец — меняется один факт |
|---|---|---|---|
| a. функция вне набора | `{{ env "HOME" \| toJson }}` | `"env"` | `{{ printf "%s" "HOME" \| toJson }}` |
| b. `define` | `{{ define "x" }}y{{ end }}` | `define is forbidden` | тот же `value` без `define` |
| c. переменная | `{{ $x := .Params.x }}` | `variable declarations and assignments are forbidden` | `{{ .Params.x \| toJson }}` |
| d. `range` над данными заказа | `{{ range .Registry.Spec }}…{{ end }}` | `range over order or status data is forbidden` | `{{ range .Params.list }}…{{ end }}` |

**When** создаётся шаблон с конструкцией из строки (остальной `value` — как у близнеца)
**Then** `Accepted=False`, reason `TemplateInvalid`, сообщение несёт текст из столбца
«сообщение называет»
**And** близнец строки — `Accepted=True`.

### B. `RegistryTemplate` по блокам

**ORT-20 — шесть блоков с совпадающими указателями — ревизия принята**
**Given** `F-CTRL`; шесть шаблонов ORT-01 приняты
**When** создаётся `RegistryTemplate zot-v1` с `spec.templates` из §4.4 и `params` образца
(`params.storage.region: ru-1`)
**Then** `Accepted=True`, reason `Accepted`
**And** близнец (как сейчас): та же ревизия с `params.storage.region: ""` — `Accepted=False`,
reason `TemplateInvalid`, сообщение называет `params.storage.region`.

**ORT-21 — указатель шаблона не совпадает с блоком** — близнец ORT-20
**When** как ORT-20, но `templates.storage: {name: zot-vault, version: v1}`
**Then** `Accepted=False`, reason `TemplateInvalid`; сообщение называет блок `storage`, объект
`zot-vault-v1`, его указатель `vault.in-cloud.io/v1alpha1 VaultClaim` и требуемый
`vault.in-cloud.io/v1alpha1 S3BucketClaim`.

**ORT-22 — каждый блок обязателен** — близнец ORT-20
**When** a) как ORT-20 без `templates.route`; b) `templates` в прежней форме
(`s3BucketClaim`, `iamServiceAccount`, `vaultClaim`, `iamAccessBinding`, `addon`, `httpRoute`)
**Then** отказ сервера API: обязательные поля `spec.templates.route` (a) и всех шести блоков (b)
отсутствуют.

**ORT-23 — ссылка на несуществующую ревизию; появление ревизии** (как сейчас)
**When** как ORT-20, но `templates.route: {name: zot-route, version: v2}`, а `zot-route-v2` нет
**Then** `Accepted=False`, `TemplateInvalid`, сообщение «… zot-route-v2 does not exist»
**And** после создания принятого `zot-route-v2` (`HTTPRoute`) — `Accepted=True` без правки
`RegistryTemplate`.

**ORT-24 — ссылка на непринятую ревизию** — близнец ORT-20
**When** блок `route` ссылается на ревизию с `Accepted=False` (из ORT-10a)
**Then** `Accepted=False`, `TemplateInvalid`, сообщение «… is not accepted».

### C. Рендер заказа и отказы политики

**ORT-30 — заказ порождает шесть ресурсов целиком по шаблонам**
**Given** общий Given; в шаблонах `value` несёт `metadata.labels.team: platform` и
`metadata.annotations.note: zot`; `F-STATUS` доводит каждый шаг до готовности по §4.6
**When** оператор ведёт `Registry abc123`
**Then** существуют объекты (указатель — из шаблона блока):

| блок | объект | namespace |
|---|---|---|
| storage | `S3BucketClaim zot-abc123` | `cust01` |
| secrets | `VaultClaim zot-abc123` | `cust01` |
| identity | `IAMServiceAccount zot-abc123-ci` | `cust01` |
| access | `IAMAccessBinding zot-abc123-ci-pusher` | `cust01` |
| workload | `Addon reg-abc123` | — (уровень Cluster) |
| route | `HTTPRoute zot-abc123` | `registry-edge` |

**And** у каждого `metadata.labels` = `team: platform` ∪ шесть меток владения оператора;
`metadata.annotations` = `note: zot` (у `Addon` ещё `registry.in-cloud.io/chart-digest`)
**And** объект каждого блока целиком (`apiVersion`, `kind`, `metadata`, `spec`) побайтово равен
golden своего блока (`internal/desired/testdata/golden/<блок>.yaml`, пересобран с метками и
аннотациями этого Given), ключи оператора проставлены оператором
**And** `IAMAccessBinding.spec.subjects[0].serviceAccountRef.name` = `zot-abc123-ci` — получено
шаблоном через `.Status.IAMServiceAccount.Name`
**And** условия `StorageReady`, `SecretsReady`, `IdentityReady`, `RegistrationReady`,
`AccessReady`, `WorkloadReady`, `RouteReady`, `Ready` — `True`.

**ORT-31 — верхние ключи рендера только `metadata` и `spec`** — близнец ORT-30
| пример | лишнее в `value` шаблона `secrets` |
|---|---|
| a | `apiVersion: vault.in-cloud.io/v1alpha1` верхним ключом |
| b | `kind: VaultClaim` верхним ключом |
| c | `status: {}` верхним ключом |
| d | `spec` отсутствует |

**When** оператор ведёт `Registry abc123`
**Then** условие `SecretsReady=False`, reason `PolicyViolation`; `VaultClaim zot-abc123` не
создан; `RegistryIDReservation abc123`.`status.diagnostics.resources[step=secrets].message`
называет ключ (`apiVersion` / `kind` / `status` / `spec: required`).

**ORT-32 — в `metadata` только метки и аннотации** — близнец ORT-30
| пример | лишнее в `metadata` рендера шаблона `route` |
|---|---|
| a | `name: zot-x` |
| b | `namespace: registry-edge` (даже равный `targetNamespace`) |
| c | `generateName: zot-` |
| d | `ownerReferences: [...]` |
| e | `finalizers: [example.com/hold]` |

**Then** `RouteReady=False`, reason `PolicyViolation`; `HTTPRoute` не создан; diagnostics
называет `metadata.<ключ>`.

**ORT-33 — ключи меток и аннотаций оператора и систем запрещены** — близнец ORT-30
(метка `team: platform` проходит)
| пример | ключ в `metadata.labels`/`annotations` шаблона `identity` |
|---|---|
| a | `app.kubernetes.io/managed-by: someone` (метка) |
| b | `registry.in-cloud.io/owner-uid: x` (метка) |
| c | `registry.in-cloud.io/chart-digest: x` (аннотация) |
| d | `kubernetes.io/description: x` (аннотация) |
| e | `iam.kaname.in-cloud.io/anything: x` (аннотация; префикс — группа порождаемого вида) |

**Then** `IdentityReady=False`, reason `PolicyViolation`; `IAMServiceAccount` не создан;
diagnostics называет ключ.

**ORT-34 — ключи `spec`, которые ставит оператор** (как сейчас) — близнец ORT-30
**When** шаблон `identity` рендерит `spec.deletionPolicy: Retain`
**Then** `IdentityReady=False`, `PolicyViolation`; diagnostics называет `spec.deletionPolicy`.

**ORT-35 — предел вывода рендера** (как сейчас) — близнец ORT-30
**When** шаблон `storage` с `range` над полем `.Params`, дающим вывод > 256 KiB
**Then** `StorageReady=False`, reason `RenderFailed`; объект не создан
**And** близнец: тот же `range` над полем с выводом ≤ 256 KiB — объект создан.

**ORT-36 — `.Status` по новому ключу вида** — близнец ORT-30
**When** a) шаблон `access` обращается к `.Status.IamServiceAccount.Name` (прежний ключ);
b) шаблон `identity` (стадия 1) обращается к `.Status.IAMServiceAccount.Name`
**Then** a) и b) — `AccessReady=False` / `IdentityReady=False`, reason `RenderFailed`
(ключа нет: a — прежнее имя, b — шаг ещё не готов); объект не создан.

### D. Готовность

**ORT-40 — готовность судится встроенной таблицей по указателю**
**Given** ORT-30 до шага `<блок>`; `F-STATUS` пишет `status` порождённого объекта
| блок | готовый `status` | близнец — меняется один факт → reason |
|---|---|---|
| storage | `observedGeneration=generation`, `phase: Ready`, `KeysWritten=True` | `phase: Failed` → `ResourceDegraded` |
| secrets | то же, `RolesApplied=True` | `RolesApplied=False` → `ResourceProgressing` |
| identity | `Ready=True` (текущее поколение), `id` непуст, `KeysReady=True` | `id` пуст → `ResourceProgressing` |
| access | `Ready=True` (текущее поколение), `id` непуст | `Drifted=True` → `ResourceDegraded` |
| workload | `observedGeneration` текущее, `Synced`/`Healthy`/`Ready=True`, Deployment `Available` | `observedGeneration` старое → `ResourceStale` |
| route | у parent `Accepted=True`, `ResolvedRefs=True` | `ResolvedRefs=False` → `ResourceProgressing` |

**Then** готовая строка — условие шага `True`, reason `ResourceReady`, следующая стадия
открывается; близнец — условие шага `False` с reason из таблицы, следующая стадия не
открывается.

### E. Владение и удаление — без изменений

**ORT-50 — удаление строго в обратном порядке**
**Given** `Registry abc123` в `Ready` (ORT-30)
**When** `kubectl delete registry abc123 -n cust01`
**Then** объекты исчезают по одному в порядке `HTTPRoute` → `Addon` → `IAMAccessBinding` →
регистрация в IAM → `IAMServiceAccount` → `VaultClaim` → `S3BucketClaim`; следующий
удаляется только после исчезновения предыдущего; финализатор `registry.in-cloud.io/finalizer`
снимается последним.

**ORT-51 — чужой объект не присваивается** (как сейчас)
**Given** `IAMServiceAccount zot-abc123-ci` в `cust01` с меткой `registry.in-cloud.io/owner-uid`
другого заказа
**When** оператор ведёт `Registry abc123`
**Then** `IdentityReady=False`, reason `AdoptionRefused`; объект не изменён
**And** близнец: объект с метками владения этого заказа — приводится server-side apply.

### F. Переход

**ORT-60 — в группе один вид, шести старых нет**
**Given** `F-E2E` после установки чарта `registry-operator`
**When** `kubectl api-resources --api-group=observed.registry.in-cloud.io -o name`
**Then** вывод — ровно `registryobserveresourcetemplates.observed.registry.in-cloud.io`
**And** (статическая часть того же сценария) **When** перечисляются файлы каталогов
`config/crd/bases` и `charts/registry-operator/crds` **Then** в каждом ровно четыре CRD:
`registryobserveresourcetemplates`, `registries`, `registryidreservations`, `registrytemplates`
**And** близнец статической части: тот же каталог с пятым файлом CRD (прежний
`observediamserviceaccounttemplates`) — проба состава красная и называет лишний файл.

**ORT-61 — RBAC оператора следует перечню**
**Given** сгенерированная из маркеров RBAC роль оператора `charts/registry-operator/files/role.yaml`
**When** проба роли сверяет её с перечнем §4.2
**Then** на `registryobserveresourcetemplates` роль даёт `get`, `list`, `watch`, `patch`,
`update` (и `update` на `/finalizers`; `get`, `patch`, `update` на `/status`) — без `create` и
`delete`;
ни одного `observed*templates` не названо; на порождаемые виды — ровно шесть ресурсов §4.2,
среди них `iamserviceaccounts`, `iamaccessbindings`, без `serviceaccounts`/`accessbindings`
группы `iam.kaname.in-cloud.io`
**And** близнец: проба роли краснеет, если в роль добавлен ресурс вне перечня (как сейчас) либо
глагол `create` на шаблоны.

**ORT-62 — образцы в новой форме проходят весь пайплайн**
**Given** `F-E2E`
**When** применяются `config/samples/templates` (шесть `RegistryObserveResourceTemplate` +
`RegistryTemplate`) и `config/samples/registry.yaml`
**Then** все семь ревизий `Accepted=True`; заказ доходит до `Ready` и удаляется по ORT-50.

**ORT-63 — политика допуска чарта `registry-platform` судит новые виды IAM**
**Given** `F-ADM`, CRD IAM с видами `IAMServiceAccount`/`IAMAccessBinding` из пина
`charts/tests/crds.sh` (§3); объекты-образцы проб V1 — `IAMServiceAccount zot-abc123-ci` и
`IAMAccessBinding zot-abc123-ci-pusher` в `cust01`
**When** каждый из 19 кейсов V1 (`charts/tests/admission/cases.yaml`, имена `v1-*`) создаётся
dry-run server от имени своего автора
**Then** исход каждого — тот же, что на базе для прежних видов: отказ политики `registry-iam`
с её сообщением у 14 кейсов —
`v1-stranger-creates-robot`, `v1-operator-binds-unknown-role`, `v1-operator-binds-foreign-target`,
`v1-operator-binds-anonymous`, `v1-operator-binds-authenticated`,
`v1-operator-binds-public-read-role`, `v1-operator-binds-authenticated-under-another-name`,
`v1-operator-binds-authenticated-outside-tenant`,
`v1-stranger-binds-authenticated-under-another-name`,
`v1-stranger-binds-authenticated-outside-tenant`, `v1-stranger-binds-anonymous-on-the-role-only`,
`v1-stranger-binds-wildcard-user`, `v1-platform-binds-authenticated`,
`v1-stranger-binds-authenticated-to-a-registry-with-another-module-role`
**And** допуск у 5 близнецов — `v1-operator-creates-robot`, `v1-operator-binds-pusher`,
`v1-stranger-binds-user-under-another-name`, `v1-platform-binds-robot-under-another-name`,
`v1-stranger-binds-authenticated-to-another-module`
**And** правило политики называет ресурсы `iamserviceaccounts`, `iamaccessbindings`, и каждое
сравнение имени ресурса в выражениях политики — по новому имени (предикат P8 §7).

## 6. Сценарий → производитель

Производители новых утверждений — заказ этой задачи (кода ещё нет); у утверждений «как
сейчас» — действующий код, координата @`8c47fe7`. Держатель — проба с ID в имени
(`Test<Область>_ORT<NN>_<Суть>`), отрицание и близнец — в одной пробе.

| ID | что производит «Тогда» | держатель (проба) — строка утверждения | уровень |
|---|---|---|---|
| ORT-01 | правила CRD (генерируются из маркеров типа `api/observed/v1alpha1`); контроллер шаблонов — `Accepted` (сейчас `internal/controller/template_controller.go:137`) | `TestObserveTemplate_ORT01_EveryKindOfTheListIsAccepted` — утверждает `Accepted=True` и финализатор для всех шести строк | `F-CTRL` |
| ORT-02 | правило CRD имени (как сейчас) | `…_ORT02_…` — текст сообщения и отсутствие объекта; близнец ORT-01 | `F-API` |
| ORT-03 | правило CRD `self == oldSelf` | `…_ORT03_…` — отказ patch `spec`, близнец patch метки | `F-API` |
| ORT-04 | новое правило CRD пары указателя | `…_ORT04_…` — пять строк, сообщение с полным перечнем | `F-API` |
| ORT-05…08 | новые правила CRD доп. полей | `…_ORT05_` … `_ORT08_` — точный текст сообщения | `F-API` |
| ORT-09, 10 | проверка вида контроллером шаблонов (сейчас `template_controller.go:141-160`) | `…_ORT09_`, `…_ORT10_` — reason и сообщение | `F-CTRL` |
| ORT-11 | AST-гейт (`internal/render/gate.go`), теперь над `metadata` | `…_ORT11_` | `F-CTRL` |
| ORT-12 | предел длины `spec.value` в CRD | `…_ORT12_` — 65537 / 65536 | `F-API` |
| ORT-13 | финализатор `in-use` (сейчас `template_controller.go:108-137`) | `…_ORT13_` | `F-CTRL` |
| ORT-14 | допуск формы шаблона: `render.Compile` (`internal/render/engine.go:96-109`) и AST-гейт (`internal/render/gate.go`), их зовёт контроллер шаблонов (`template_controller.go:142`) | как сейчас — `TestGate_RefusesEachForbiddenFormAndPassesItsTwin` (`internal/render/gate_test.go:14`), `TestGate_DefineIsRefused` (`:127`) — отказ и близнец на уровне гейта; новая `TestObserveTemplate_ORT14_ClosedFormIsRefusedWithItsTwin` — `Accepted=False` с текстом формы и `Accepted=True` у близнеца, четыре строки | unit + `F-CTRL` |
| ORT-20, 23, 24 | контроллер `RegistryTemplate` (сейчас `template_controller.go:219-269`), регион — `policy.CheckTemplateParams` (`internal/policy/storage.go:110`, зовётся `template_controller.go:251`) | `TestRegistryTemplate_ORT20_…` (утверждает и отказ при пустом `params.storage.region`, близнец — `ru-1`), `_ORT23_`, `_ORT24_` | `F-CTRL` |
| ORT-21 | новая сверка указателя с блоком в том же контроллере | `…_ORT21_` — блок, объект, оба указателя в сообщении | `F-CTRL` |
| ORT-22 | обязательность блоков в CRD `RegistryTemplate` | `…_ORT22_` | `F-API` |
| ORT-30 | сборка объекта (сейчас `internal/desired/desired.go` `Build`): указатель из шаблона, `metadata` из рендера | `TestBuild_ORT30_SixTemplatesRenderTheGoldenObjects` — golden; `TestPipeline_ORT30_…` — объекты и условия | unit + `F-CTRL` |
| ORT-31…33 | новая проверка формы рендера в политике (`internal/policy`); сверка вида собранного объекта с видом шага — как сейчас (`internal/policy/validate.go:110`, `CheckRendered`) | `TestCheckRendered_ORT31_…`, `_ORT32_`, `_ORT33_` — каждая строка с близнецом; `TestCheckRendered_ORT31_KindOfTheStepIsStillChecked` — объект чужого вида отвергнут с `kind`, близнец — вид шага; `TestPipeline_ORT31_…` — условие и diagnostics на одной строке | unit + `F-CTRL` |
| ORT-34 | ключи оператора (сейчас `internal/policy/validate.go:70`), по новому `kind` | `TestCheckRendered_ORT34_…` | unit |
| ORT-35 | предел вывода (`internal/render/engine.go` `MaxOutputBytes`) | `TestExecute_ORT35_…` | unit |
| ORT-36 | ключ `.Status` — `kind` (сейчас `StatusKey` в `internal/kinds/kinds.go`) | `TestExecute_ORT36_…` и `TestPipeline_ORT36_…` | unit + `F-CTRL` |
| ORT-40 | таблица готовности (сейчас `internal/kinds/readiness.go`), по новым `kind` | `TestReadiness_ORT40_…` — шесть строк с близнецами | unit + `F-STATUS` |
| ORT-50 | `kinds.DeletionOrder` (`internal/kinds/kinds.go`) | `TestDeletion_ORT50_StrictlyReverseOrder` | `F-CTRL` |
| ORT-51 | `errAdoptionRefused` (`internal/controller/common.go:65,94`) | `TestPipeline_ORT51_…` | `F-CTRL` |
| ORT-60 | состав CRD в `config/crd/bases` и чарте (генерация из `api/`) | `TestE2E_ORT60_…` — вывод `api-resources`; новая `TestCRDSet_ORT60_FourCRDsInBothDirectories` — четыре файла в каждом каталоге, близнец — пятый файл даёт отказ с его именем | `F-E2E` + unit |
| ORT-61 | `charts/registry-operator/files/role.yaml` и маркеры RBAC (`template_controller.go:34-36`) | `TestClusterRole_ORT61_…` (сейчас `internal/kinds/rbac_test.go`) — ресурсы и глаголы, близнецы: ресурс вне перечня, `create` на шаблоны | unit |
| ORT-62 | `config/samples/templates`, загрузка `internal/samples` | `TestE2E_ORT62_OrderThroughTheWholePipelineAndBack` | `F-E2E` |
| ORT-63 | политика `registry-iam` чарта `registry-platform` (`templates/admission.yaml:62-80`) | `charts/tests/admission/cases.yaml` — 19 кейсов `v1-*` на новых видах (14 deny, 5 allow), прогон `charts/tests/admission/run.sh`; deny засчитывается только с именем политики (`probe.py`) | `F-ADM` |

Сценариев без производителя — 0: у новых утверждений производитель заказан этой задачей,
у прежних — назван координатой.

## 7. DoD

1. Каждый ORT-01…ORT-63 имеет пробу из §6 с ID в имени; отрицание и близнец — в одной пробе;
   каждая проба нового поведения красная на базе `8c47fe7` (вида нет) и зелёная на голове;
   пробы «как сейчас» зелёные на обеих.
2. Golden: шесть файлов `internal/desired/testdata/golden/{storage,secrets,identity,access,workload,route}.yaml`
   пересобраны целыми объектами; `identity.yaml`/`access.yaml` несут
   `kind: IAMServiceAccount`/`IAMAccessBinding`; golden-проба ORT-30 зелёная.
3. `integration-kaname` (`.github/workflows/integration-kaname.yml`) зелёный на голове ветки.
4. `test/e2e` зелёный с образцами в новой форме (ORT-60, ORT-62).
5. Перепись в дереве на голове, обоими предикатами. Предикаты по всему дереву исключают каталог
   приёмок `docs/acceptance` (этот документ цитирует прежние имена как предмет), остальные
   ограничены путями кода и чартов; база @`8c47fe7` — в §8:
   - P1 по написанию: `git grep -l -E 'Observed[A-Za-z0-9]*Template' -- . ':!docs/acceptance' | wc -l` → **0**;
   - P2 по предмету: `git grep -l -E 'observed(s3bucketclaim|vaultclaim|iamserviceaccount|iamaccessbinding|addon|httproute)templates' -- . ':!docs/acceptance' | wc -l` → **0**;
   - P3 старые виды IAM в коде: `git grep -n -E 'Kind: *"(ServiceAccount|AccessBinding)"' -- api internal test cmd | wc -l` → **0**;
   - P4 старые виды IAM в YAML: `git grep -h -A1 -E '^ *apiVersion: *iam\.kaname\.in-cloud\.io/' -- '*.yaml' | grep -c -E '^ *kind: *(ServiceAccount|AccessBinding) *$'` → **0**;
   - P5 прежние ключи контекста: `git grep -n -E '\.Status\.Iam(ServiceAccount|AccessBinding)' -- . ':!docs/acceptance' | wc -l` → **0**;
   - P6 новая форма: `git grep -l -E '^kind: RegistryObserveResourceTemplate$' -- config/samples/templates | wc -l` → **6**;
   - P7 README (все три — корень и чарты): `git grep -n -E 'Observed\*?[A-Za-z0-9]*Template|iam .?ServiceAccount|iam .?AccessBinding' -- '*README.md' | wc -l` → **0**;
   - P8 прежнее имя ресурса выдачи IAM в коде и чартах: `git grep -n -w accessbindings -- charts/registry-platform charts/registry-operator internal config | wc -l` → **0** (слово однозначно: в API ядра такого ресурса нет; `serviceaccounts` неоднозначно — ресурс ядра, его держат ORT-61 и ORT-63 поведенчески);
   - P9 модель доверия: `git grep -l 'observed.registry.in-cloud.io' -- charts config ':!charts/registry-operator/crds' ':!config/crd' ':!config/samples' | wc -l` → **1**, и это `charts/registry-operator/files/role.yaml`.
6. Оба пина IAM-CRD закрепляют ревизию kaname-iam-operator с видами `IAMServiceAccount`,
   `IAMAccessBinding` (§3): `hack/crds.lock` и `charts/tests/crds.sh`; прежних ревизий
   (`2f9abc31`, `ea747526`) в них нет:
   `git grep -n -E '2f9abc31|ea747526' -- hack/crds.lock charts/tests/crds.sh | wc -l` → **0**.
7. `README.md` (таблица ресурсов) описывает `RegistryObserveResourceTemplate` и блоки
   `RegistryTemplate`: `git grep -c -E 'RegistryObserveResourceTemplate' -- README.md` → **≥ 1**;
   прежних строк нет — P7.
8. Полный хук отправки и CI (`ci.yml`, `charts.yml`, `integration-kaname.yml`) зелёные на
   одной голове.

## 8. Измерения базы и посев

- База — `8c47fe7` (голова `8-asm-w1-k1` по `git ls-remote`, 2026-10-22), 241 отслеживаемый файл
  (`git ls-tree -r --name-only 8c47fe7 | wc -l`). Предикаты §7 п.5 той же формы с ревизией
  (`git grep … 8c47fe7 -- …`): P1 = 33 файла, P2 = 14, P3 = 4, P4 = 4, P5 = 1, P6 = 0,
  P7 = 6 строк (`README.md` — 3, `charts/registry-platform/README.md` — 2,
  `charts/registry-instance/README.md` — 1), P8 = 7 строк (`admission.yaml` — 4, `role.yaml`,
  `registry_controller.go`, `kinds.go` — по 1), P9 = 1. Объединение P1 ∪ P2 ∪ файлы с
  `observed.registry.in-cloud.io` = 36 файлов. Каталога `docs/` на базе нет; исключение
  `':!docs/acceptance'` на базе ничего не снимает (P1, P2, P5 с ним и без него равны), а с этим
документом в копии снимает ровно его: `git grep --untracked -l …` без исключения даёт
P1 = 34, P2 = 15, P5 = 2, с исключением — 33, 14, 1. Предикат §7 п.6 на базе = 5 строк;
§7 п.7 (`RegistryObserveResourceTemplate` в `README.md`) на базе = 0 файлов.
- Чарт `registry-platform` observed-шаблонов и `RegistryTemplate` не ставит
  (`git grep -n -i -E 'observed|RegistryTemplate' 8c47fe7 -- charts/registry-platform` → пусто);
  его перевод — политика допуска V1 (ресурсы IAM, ORT-63) и проба
  `charts/tests/lib.py route-namespace` (сверяет `targetNamespace` шаблона маршрута).
- Кейсов V1 в `charts/tests/admission/cases.yaml` — 19 (`grep -c -E 'name: v1-'`), из них deny 14,
  allow 5 (ORT-63).
- Посев Given в этом круге не выполнялся (без кластера): исход для пункта «конструируемость
  посевом» — не выполнилось. Фикстуры названы в §5 и есть в дереве @`8c47fe7`; Given с
  IAM-видами строятся только после пина §3.

## 9. Допущения и открытые вопросы

- **Д1.** Задача в PRO-Robotech/registry-operator заводится `git-operator`; номер вписывается
  в шапку до коммита, текст документа от него не зависит.
- **Д2.** Задача kio#9 меняет только `kind` (и, как следствие, имена ресурсов API на
  `iamserviceaccounts`/`iamaccessbindings`), группа и версия прежние. Иное — правка одной
  строки §4.2 по одобренному контракту kio.
- **Д3.** Указатель — поля `spec.apiVersion`/`spec.kind` (как у образца), а `value` рендерит
  `metadata` + `spec`; `apiVersion`/`kind` внутри `value` — отказ (ORT-31). Основание: так
  устроен образец — на стенде system-test у 226 из 226 объектов
  `ClusterClaimObserveResourceTemplate` `apiVersion`/`kind` в `value` нет (замер 2026-10-22);
  выбрано решением диспетчера по вопросу круга 1. Отклонённые варианты: указатель только внутри
  `value` (вид известен лишь после рендера с данными заказа — закрытый перечень и сверку блока
  нельзя провести до заказа); указатель и в полях, и в `value` с требованием равенства (одно
  значение в двух местах).
- **Д4.** Площадок с установленным прежним чартом нет (выпуска не было); если найдётся —
  шесть старых CRD снимаются шагом выкатки, на кластере после неё
  `kubectl get crd -o name | grep -c 'observed.registry.in-cloud.io'` → 1.
- **Д5.** Перечень запрещённых ключей `metadata` (§4.3) — защита от столкновения с ключами
  оператора и систем, а не от злонамеренного автора: автор шаблона — платформа (модель
  доверия, §4.3). Полнота перечня подтверждена для kaname-iam-operator (единственная аннотация
  поведения на IAM-объекте — в группе `iam.kaname.in-cloud.io`, её закрывает правило группы,
  ORT-33e); для vault-operator, addons-operator и реализации Gateway перепись ключей, читаемых
  потребителями, не выполнялась — их ключи в своей группе закрыты тем же правилом, ключи вне
  своей группы не измерены.

Открытых вопросов, критичных для реализации, нет.
