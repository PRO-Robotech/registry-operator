# registry-operator

Оператор заказов регистри (`Registry`, `registry.in-cloud.io/v1alpha1`) и прокси прав
`iam-proxy` перед zot. Заказ собирает окружение регистри из ресурсов со своим состоянием;
каждым из них управляет оператор: создаёт по шаблону, ведёт по готовности и сам считает
итоговую готовность заказа по `status` дочерних объектов.

## Ресурсы API

| вид | область | назначение |
|---|---|---|
| `Registry` | namespace арендатора | заказ: `spec.id` (неизменяем, `^[a-z0-9]{6,20}$`, не начинается с `reg`, равен имени), `spec.templateRef`, `spec.deletionPolicy` (`Retain`/`Purge`), `spec.public` (анонимный pull, по умолчанию `false`) |
| `RegistryTemplate` | кластер | неизменяемая ревизия шаблона заказа: `claimPolicy.namespaceSelector` (обязателен, непуст), `params`, ссылки на шесть observed-шаблонов |
| `RegistryIDReservation` | кластер | резерв id (надгробие `Released`, id повторно не выдаётся); закреплённый проект IAM; опись репозиториев, записанных в IAM |
| `Observed*Template` (`observed.registry.in-cloud.io`) | кластер | Go-шаблоны spec шести видов: `S3BucketClaim`, `VaultClaim`, iam `ServiceAccount`, iam `AccessBinding`, `Addon`, `HTTPRoute`. Контекст закрыт, функции — `toJson`, `toYaml`, `nindent` и встроенные сравнения; `env`, `expandenv`, `getHostByName`, `include`, `define` недоступны (AST-гейт) |

## Пайплайн

| стадия | шаги (параллельно) |
|---|---|
| 1 | `S3BucketClaim`, `VaultClaim` (гостевой режим vault-operator), iam `ServiceAccount` робота CI, регистрация `registry_registry:<id>` в IAM |
| 2 | iam `AccessBinding` робота (`registry.pusher` на `registry.registries:<id>`), `Addon` (zot + iam-proxy с путями) |
| 3 | `HTTPRoute` `/v2/<id>/` |

Следующая стадия создаётся только после готовности каждого шага предыдущей; заказ `Ready`
ровно тогда, когда готов каждый шаг. Удаление — строго обратно: маршрут → Addon и его
рабочая нагрузка → выдача → регистрация в IAM (регистри, затем репозитории описи) →
учётка → роль Vault → бакет (`Purge` — только в фазе `Deleting` и только по политике
заказа) → резерв `Released`.

Перед каждой записью объект проверяется теми же правилами, что политики допуска
платформы (`internal/policy`): без VAP на кластере оператор не создаёт того, что VAP
запретила бы.

## Регистрация в IAM

Оператор — единственный писатель типов `registry_*`: внутренний листенер kaname (`:9091`),
`RegisterResource`/`UnregisterResource` по mTLS с листом службы registry
(`spiffe://<trust-domain>/ns/<ns>/sa/kacho-registry`). Цепь предков регистри —
`[project:<prj>]`, репозитория — `[registry_registry:<id>, project:<prj>]`; пустая цепь не
отправляется никогда (в kaname она стирает предков). Проект — из метки namespace заказа
`kaname.in-cloud.io/project-id` (аккаунт — `kaname.in-cloud.io/account-id`), закрепляется
первой регистрацией; смена метки — отказ заказа, а не перенос.

## Эндпоинт заявок на репозитории

`POST /v1/registries/{id}/repositories` (TLS): iam-proxy пода `zot-<id>` заявляет новый
репозиторий. Вызывающий проверяется TokenReview projected-токена с аудиторией оператора и
обязан быть учёткой `zot-<id>` этого регистри; опись пишется раньше IAM; предел описи и
темп новых заявок — на регистри.

## iam-proxy

Прокси перед zot (zot слушает только loopback пода). Токен клиента — обычный токен kaname,
тот же, что для API (служебной учётки либо человека): `Authorization: Bearer <токен>` либо
Basic с токеном паролем (`docker login`, имя любое). Своей выдачи токена у прокси нет:
без кредов — `401` с `WWW-Authenticate: Basic realm="registry"`. Токен проверяется
локально (JWKS только по https под объявленным центром, `iss`, `aud` — адресат публичного
API kaname, обязательный `exp`, `typ`, ES256, утверждения принципала kaname), право
спрашивается `AuthorizeService.Check` публичного REST-фронта kaname (`--kaname-url`,
`POST /iam/v1/authorize:check`) тем же токеном о его принципале (`service_account:<id>`
либо `user:<id>`): публичный gRPC kaname требует клиентский лист, и личность там — лист. pull — `v_get` на репозитории, push в записанный — `v_update`,
новый (не записанный в IAM) — `v_create` на регистри и заявка оператору. Разрешения чтения
кэшируются до 10 с (отказы — нет), запись — мимо кэша. Чтение в репозитории, который
субъект вправе только завести, — «не найдено» без обращения к zot. Анонимный pull — только у
публичного регистри, и это единственный механизм публичности: флаг `--public` прокси из
`Registry.spec.public`; выдач системному классу (`anonymous`, `authenticated`) оператор не
пишет никогда — политика отвергает их на любом заказе.
`Authorization`/`Cookie` клиента в zot не уходят, тела 5xx/401 zot наружу не отдаются,
`/v2/_catalog` и расширения zot закрыты.

## Контракт с чартами

`Addon.spec.variables`: переменные заказа ставит оператор (`id`, `login`, `registryUID`,
`syncEpoch`, `public`), переменные слоя заказа платформы рисует шаблон из статусов готовых
шагов (перечень закрыт — `policy.addon.templateVariables`).

## Сборка и пробы

```
make manifests generate   # CRD, ClusterRole, DeepCopy
make test                 # unit и envtest (-race) против закреплённых целевых CRD
make inject               # таблица инъекций: каждая проба способна упасть
make lint
make e2e                  # kind: оператор своим чартом под своей ролью
make docker-build         # prorobotech/registry-operator, prorobotech/registry-iam-proxy
```

Целевые CRD проб — `hack/crds.lock` (ревизия и sha256 каждого файла).
