# registry-platform

Объекты платформы для регистри по заказу. Ставятся один раз на кластер; registry-operator
их не создаёт. На заказ оператор пишет свои ресурсы с состоянием и `Addon` zot, а всё
общее для площадки доставляется этим чартом.

Чарт и объекты, которые registry-operator пишет на заказ:

| этап | что пишет registry-operator | откуда здесь |
|---|---|---|
| 1 | `S3BucketClaim`, `VaultClaim`, iam `IAMServiceAccount` робота, кортеж `registry_registry:<id>` в kaname | личность модуля — манифест модуля kaname; типы — канон модели kaname |
| 2 | iam `IAMAccessBinding` робота, `Addon` zot | роли — манифест модуля; форма `Addon` — `AddonTemplate`, значения — слои `AddonValue` |
| 3 | `HTTPRoute /v2/<id>/` | Gateway и общие маршруты `/v2/`, `/iam/token` |

## Что включено

| объект | по умолчанию | зачем |
|---|---|---|
| `AddonTemplate registry-instance-<версия>` | включено, по одному на `instanceChart.pins[]` | форма `Addon` заказа: чарт, репозиторий, версия; digest публикации — в аннотации `registry.in-cloud.io/chart-digest` |
| `AddonValue registry-instance-order` | включено | слой заказа (priority 0): `Addon.spec.variables` → values чарта экземпляра |
| `AddonValue registry-instance-site` | включено | слой площадки (priority 10): `site.*` |
| `AddonPhase <addonPhase.addonName>` и `AddonValue <addonPhase.addonName>-edge` | включено | порядок объектов края, когда площадка ставит этот чарт через `Addon` |
| `HTTPRoute registry-api-root` (`/v2/`) и `registry-token` (`/iam/token`), `ReferenceGrant` к их бэкендам | включено | общие маршруты края; маршрут заказа `/v2/<id>/` ставит оператор |
| `Gateway` | включено | Gateway края: только `HTTPRoute` и только из namespace с метками края |
| `ConfigMap <kanameModule.configMapName>` (ключ `registry.manifest.yaml`, метка `kaname.in-cloud.io/module-manifest`) | включено | манифест модуля registry для kaname: роли и личность модуля |
| `ValidatingAdmissionPolicy` V1–V15 с привязками и параметрами | **выключено** («пока без») | ограды писателей помимо оператора |
| `AppProject <projectPrefix><login>` | **выключено** («пока без») | проект Argo CD на арендатора |

Для нескольких частных Registry в одном namespace включите
`sharedRoutes.apiRoot.privatePool: true` и задайте `site.routingGroup` — непустую
DNS-метку площадки для одного host и одного контракта Kaname (URL, issuer,
audience и CA). `sharedRoutes.apiRoot.backend` задаёт имя, namespace и порт
стабильного Service, который создаст этот чарт. Он выбирает существующие
private proxy Pods этой группы; публичные Registry и другие группы исключены.
Service и общий HTTPRoute принадлежат площадке, поэтому удаление одного заказа
не удаляет корень `/v2/`. Service выбирает Pods только своего namespace;
между namespace этот пул не балансирует. Не используйте имя Service экземпляра
`zot-<id>` для общего Service.

Метки `registry.in-cloud.io/routing-group` и `registry.in-cloud.io/root-membership`
выводятся чартом и запрещены в `site.vault.injectorLabels`, в том числе когда
группа не задана. Площадка должна выдавать одну группу только экземплярам с
одинаковым контрактом аутентификации. Проверка версии сохраняет `401 Basic`
без credentials, `200` для годных credentials и `401` для негодных: анонимный
`200` здесь позволил бы Docker login завершиться без проверки пароля.
По умолчанию пул выключен и существующая ссылка на backend сохраняется.

Проверок здоровья Lua для Argo CD нет: готовность заказа считает registry-operator по статусам
дочерних ресурсов.

## Закрепление чарта экземпляра

Публикация — из CI на тег `registry-instance-<версия>` (`charts/publish.sh`): опубликованная
версия неизменяема, повторная публикация той же версии отказывает. Digest из итога задания
публикации записывается в `instanceChart.pins`:

```yaml
instanceChart:
  pins:
    - version: 1.0.0
      digest: sha256:<digest публикации>
```

registry-operator пишет `Addon` только если тег версии в реестре разрешается ровно в этот
digest. Пакет в `ghcr.io/pro-robotech/charts` после первой публикации делается публичным
(или площадка даёт Argo CD учётные данные реестра).

## Форма Addon заказа

`AddonTemplate` рендерится контекстом addons-operator (`AddonClaim`): `.Values` — заказ
(`metadata.name`, `metadata.namespace`, `spec.variables`), `.Vars` — его переменные;
шаблон Go с sprig, `missingkey=error`. Тот же контекст использует registry-operator, когда
пишет `Addon` сам. Перечень переменных — в
[контракте registry-instance](../registry-instance/README.md#переменные-addon-заказа-пишет-registry-operator).

`addon.backend.projectMode`:

- `shared` — один проект `addon.backend.project` для всех заказов (стенд: проект, которым
  пользуются Addon площадки);
- `tenant` — проект `<projectPrefix><login>` на арендатора; сами проекты — `appProjects`.

## Фаза края

addons-operator связывает `AddonPhase` с `Addon` по имени. Когда площадка ставит этот чарт
через `Addon` с именем `addonPhase.addonName`, её базовый слой держит `gateway.enabled` и
`sharedRoutes.enabled` выключенными, а правило `edge` подмешивает слой
`<addonName>-edge`, который их включает, как только CRD `addonPhase.crdName` получила
`Established` (`keep: true` защёлкивает правило). Так первая синхронизация не падает на видах
Gateway API, которых ещё нет. При установке `helm install` фаза не действует: `Addon` с таким
именем нет, объекты края включены значениями.

## Манифест модуля kaname

`files/registry.manifest.yaml` — манифест модуля `registry` (`apiVersion: iam/v1`). Роли и
личность модуля ведёт registry-operator, только здесь; раздел `resources` правится лишь вслед
за каноном kaname (ниже). Рендер — `ConfigMap`
с одним ключом `registry.manifest.yaml` в `kanameModule.namespace` и меткой
`kaname.in-cloud.io/module-manifest: "true"`: по ней чарт kaname собирает каталог доставки
манифестов из нескольких ConfigMap, каждый ключ — файл `<модуль>.manifest.yaml`.

- **Типы — раздел канона kaname.** `registry_registry` (родитель — `project`) и
  `registry_repository` (родитель — `registry_registry`) несёт канон модели прав образа kaname.
  Манифест объявляет их ещё раз: вложенное правило роли (`children`) kaname судит по указателям
  `parents` самого манифеста, и без раздела `resources` роли не загружаются (проверено кодом
  kaname @93ad1b88). Раздел — дословный текст kaname (файл несёт лицензию
  `AGPL-3.0-or-later` и ревизию источника в шапке), и его рендер обязан совпасть с блоком
  канона побайтово, иначе kaname отказывает в пуске. Это судит задание CI `kaname-manifest`:
  доставка из рендера чарта проходит чтение, композицию модели, допуск и суд ролей kaname.
- **Роли** — правило верхнего уровня на регистри и вложенное (`children`) на каждый его
  репозиторий, включая созданные позже. У `registry.puller` и `registry.pusher` второе правило
  верхнего уровня — на репозитории, с теми же классами: выдачу на один репозиторий (цель
  `registry.repositories:<id>/<repo>`) kaname принимает, только если тип цели покрыт правилом
  верхнего уровня роли, вложенное его не покрывает. `registry.admin` точечно на репозиторий не
  выдаётся.

  | роль | регистри | репозитории регистри (`children`) | один репозиторий целью выдачи |
  |---|---|---|---|
  | `registry.puller` | get | get, list | get, list |
  | `registry.pusher` | get, update | get, list, update | get, list, update |
  | `registry.admin` | get, list, update, delete | get, list, update, delete | — |

  Класса `create` на регистри у ролей нет: kaname ролью модуля его не выдаёт (ни одно действие
  каталога прав его не спрашивает, суд ролей отказывает). Заведение репозитория в регистри —
  `update` на регистри.

  Роботу CI registry-operator выдаёт `registry.pusher` точечно на `registry.registries:<id>`.
  Права «на аккаунт» для людей и групп — выдача с областью `iam.account` и целью `allInScope`:
  её выдаёт платформа или владелец аккаунта, не оператор.
- **Публичного чтения модуль не объявляет.** Анонимный pull включает только флаг `--public`
  iam-proxy (переменная заказа `public`); кортежей с подстановочным субъектом и выдач
  системным классам `anonymous` и `authenticated` registry-operator не пишет. Выдачу другого
  писателя отвергает политика V1 — по содержимому выдачи (роль модуля `registry` либо цель
  `registry.*`), под любым именем и в любом namespace. V1, как и все политики допуска, по
  умолчанию выключена (ниже): пока она выключена, такую выдачу писателя помимо
  registry-operator чарт не отвергает.
- **Личность модуля** `kacho-registry` в аккаунте `kanameModule.systemAccount` и её вступление
  в `module-relation-writers`. Под ней registry-operator — единственный писатель типов
  `registry_*` — регистрирует ресурсы (`RegisterResource` на внутреннем листенере kaname по mTLS).

Модуль `registry` в доставке kaname объявляет ровно один манифест: kaname отказывает в пуске,
если имя модуля объявлено двумя манифестами одного обхода. Там, где стоит registry-operator,
другого манифеста модуля `registry` в доставке нет.

## Политики допуска (выключены)

Те же проверки registry-operator исполняет до записи, поэтому без политик он запрещённого
не создаёт; политики закрывают писателей помимо оператора. `failurePolicy: Fail`, параметры —
ConfigMap `<release>-admission` в `admission.paramsNamespace`, без параметров — отказ.

| | объект | правило |
|---|---|---|
| V1 | `iam.kaname.in-cloud.io` IAMServiceAccount и IAMAccessBinding с именем `zot-*`; IAMAccessBinding с ролью модуля `registry` (`registry.*`) либо с целью `registry.*` — под любым именем, в любом namespace | объекты `zot-*` пишет только registry-operator; его выдача — роль из `admission.allowedRoles` (только роли манифеста модуля) на `registry.registries:<свой id>` своим роботам; субъект системного класса (`anonymous`, `authenticated`) либо подстановочный (`*`) в выдаче регистри — отказ любому писателю, платформе тоже. Выдача регистри пользователю или группе под своим именем — не предмет V1 |
| V2 | `registries/status`, `registryidreservations/status` | только registry-operator — единственный их писатель |
| V3 | `HTTPRoute` с именем `zot-*` либо со ссылкой на `Service zot-*` | только registry-operator; `/v2/<id>/` на хосте и Gateway регистри к `Service zot-<id>:8080` своего арендатора. Ссылка судится наравне с именем: ReferenceGrant экземпляра открывает `zot-<id>` любому маршруту namespace края |
| V4 | поды с SA `zot-*` | создаёт только replicaset-controller |
| V5 | метки арендатора на namespace | только платформа |
| V6 | `RegistryIDReservation` | создаёт registry-operator, снимает только платформа |
| V7 | аннотации `registry.in-cloud.io/*` на `Registry` | только платформа и оператор |
| V8 | SA, Service, Deployment, CNP, ReferenceGrant `zot-*` в namespace арендатора | создаёт и снимает Argo CD; прочие — только метаданные вне меток |
| V9 | отметка отслеживания Argo CD `reg-*` | ставит только Argo CD |
| V10 | `Addon reg-*` | только registry-operator, и только закреплённой формы: свой namespace, проект платформы, закреплённая версия, ровно переменные заказа, только слои order и site |
| V11 | `AddonValue` слоёв чарта экземпляра | только платформа, без подстановок плагина секретов |
| V12 | `AddonPhase` | у `Addon` заказов фаз нет; фаза площадки — только платформа |
| V13 | `AppProject <projectPrefix>*` | только платформа; один destination — namespace арендатора, уровня кластера ничего, только рабочая нагрузка zot и разрешение её маршрута (`ReferenceGrant`) |
| V14 | `VaultClaim`, `S3BucketClaim` `zot-*` | только registry-operator; гость на mount платформы, одна роль на свой SA только со своими политиками, чтение только своего листа; бакет приватный, новый, ключи — в свой лист |
| V15 | `Application reg-*` | создаёт и снимает addons-operator; прочие не меняют spec и владельцев |

Пробы V1, V3–V5, V8–V15 исполняются на kind в CI (`charts/tests/admission`); V2, V6, V7
требуют CRD registry-operator и исполняются, когда они есть в дереве.

## Пробы

`charts/tests/run.sh`: lint, golden рендера по умолчанию и со всем включённым
(`charts/tests/golden/platform-*.yaml`), отрицательные пробы значений, проверки формы (в том
числе манифест модуля: без раздела типов, роли с `children`, личность модуля), схемы по закреплённым CRD,
отсутствие Lua; `charts/tests/admission/run.sh` — политики на кластере.

Vault claim admission derives the expected prefix from the Registry ID label:
`admission.vault.secretsPrefix + '/' + registry.in-cloud.io/id`. Its ACL must
reference that same prefix and `admission.vault.kvMount` (default `secret`).
`admission.vault.legacyGlobalPrefixTemplates` is an explicit list of exact
`{name, version}` pairs that still require the global prefix. Mirror the operator
configuration exactly. Legacy parent prefixes cannot coexist with descendant
claims; retire those legacy orders before enabling new isolated orders.
