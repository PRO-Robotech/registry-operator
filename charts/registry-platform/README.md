# registry-platform

Объекты платформы для регистри по заказу. Ставятся один раз на кластер; registry-operator
их не создаёт. На заказ оператор пишет свои ресурсы с состоянием и `Addon` zot, а всё
общее для площадки доставляется этим чартом.

## Что включено

| объект | по умолчанию | зачем |
|---|---|---|
| `AddonTemplate registry-instance-<версия>` | включено, по одному на `instanceChart.pins[]` | форма `Addon` заказа: чарт, репозиторий, версия; digest публикации — в аннотации `registry.in-cloud.io/chart-digest` |
| `AddonValue registry-instance-order` | включено | слой заказа (priority 0): `Addon.spec.variables` → values чарта экземпляра |
| `AddonValue registry-instance-site` | включено | слой площадки (priority 10): `site.*` |
| `AddonPhase <addonPhase.addonName>` и `AddonValue <addonPhase.addonName>-edge` | включено | порядок объектов края, когда площадка ставит этот чарт через `Addon` |
| `HTTPRoute registry-api-root` (`/v2/`) и `registry-token` (`/iam/token`), `ReferenceGrant` к их бэкендам | включено | общие маршруты края; маршрут заказа `/v2/<id>/` ставит оператор |
| `Gateway` | включено | Gateway края: только `HTTPRoute` и только из namespace с метками края |
| `ValidatingAdmissionPolicy` V1–V15 с привязками и параметрами | **выключено** («пока без») | ограды писателей помимо оператора |
| `AppProject <projectPrefix><login>` | **выключено** («пока без») | проект Argo CD на арендатора |

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

## Политики допуска (выключены)

Те же проверки registry-operator исполняет до записи, поэтому без политик он запрещённого
не создаёт; политики закрывают писателей помимо оператора. `failurePolicy: Fail`, параметры —
ConfigMap `<release>-admission` в `admission.paramsNamespace`, без параметров — отказ.

| | объект | правило |
|---|---|---|
| V1 | `iam.kaname.in-cloud.io` ServiceAccount, AccessBinding `zot-*` | пишет только registry-operator; выдача — роль из `admission.allowedRoles` на `registry.registries:<свой id>` своим роботам |
| V2 | `registries/status`, `registryidreservations/status` | registry-operator; статус заказа — ещё регистратор |
| V3 | `HTTPRoute zot-*` | только registry-operator; `/v2/<id>/` на хосте и Gateway регистри к `Service zot-<id>:8080` своего арендатора |
| V4 | поды с SA `zot-*` | создаёт только replicaset-controller |
| V5 | метки арендатора на namespace | только платформа |
| V6 | `RegistryIDReservation` | создаёт registry-operator, снимает только платформа |
| V7 | аннотации `registry.in-cloud.io/*` на `Registry` | только платформа и оператор |
| V8 | SA, Service, Deployment, CNP `zot-*` в namespace арендатора | создаёт и снимает Argo CD; прочие — только метаданные вне меток |
| V9 | отметка отслеживания Argo CD `reg-*` | ставит только Argo CD |
| V10 | `Addon reg-*` | только registry-operator, и только закреплённой формы: свой namespace, проект платформы, закреплённая версия, ровно переменные заказа, только слои order и site |
| V11 | `AddonValue` слоёв чарта экземпляра | только платформа, без подстановок плагина секретов |
| V12 | `AddonPhase` | у `Addon` заказов фаз нет; фаза площадки — только платформа |
| V13 | `AppProject <projectPrefix>*` | только платформа; один destination — namespace арендатора, уровня кластера ничего, только рабочая нагрузка zot |
| V14 | `VaultClaim`, `S3BucketClaim` `zot-*` | только registry-operator; гость на mount платформы, одна роль на свой SA, чтение только своего листа; бакет приватный, новый, ключи — в свой лист |
| V15 | `Application reg-*` | создаёт и снимает addons-operator; прочие не меняют spec и владельцев |

Пробы V1, V3–V5, V8–V15 исполняются на kind в CI (`charts/tests/admission`); V2, V6, V7
требуют CRD registry-operator и исполняются, когда они есть в дереве.

## Пробы

`charts/tests/run.sh`: lint, golden рендера по умолчанию и со всем включённым
(`charts/tests/golden/platform-*.yaml`), отрицательные пробы значений, проверки формы, схемы
по закреплённым CRD, отсутствие Lua; `charts/tests/admission/run.sh` — политики на кластере.
