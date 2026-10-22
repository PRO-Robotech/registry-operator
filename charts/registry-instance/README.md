# registry-instance

zot одного регистри по заказу и его прокси прав `iam-proxy`. Чарт отвечает ровно на вопрос
«поставить zot с нужными путями»: всё, у чего есть своё состояние, — бакет (`S3BucketClaim`),
роль Vault (`VaultClaim`), робот и выдача kaname (`iam.kaname.in-cloud.io` `ServiceAccount`,
`AccessBinding`), кортеж ресурса `registry_registry:<id>`, маршрут края (`HTTPRoute`), —
заводит и ведёт registry-operator сам. Сюда он передаёт только пути к ним.

Ставится через `Addon` (addons-operator → Argo CD). Объекты платформы, без которых `Addon`
не развернётся, — `AddonTemplate` этого чарта, слои `AddonValue`, общие маршруты, манифест
модуля kaname — в [`registry-platform`](../registry-platform/README.md).

## Что ставит

| вид | имя | волна Argo CD | зачем |
|---|---|---|---|
| `v1` ServiceAccount | `zot-<id>` | 0 | личность пода: на неё выписана роль Vault `zot-<id>`, ею iam-proxy входит в эндпоинт регистрации; токен API не монтируется |
| `cilium.io/v2` CiliumNetworkPolicy | `zot-<id>` | 0 | сеть пода (ниже); применяется раньше Deployment |
| `v1` Service | `zot-<id>` | 0 | `:8080` → порт `proxy` iam-proxy; на него ссылается маршрут заказа |
| `apps/v1` Deployment | `zot-<id>` | 1 | `replicas: 1`, `Recreate`; zot + iam-proxy, init vault-agent |

PVC нет: данные zot — только в S3; под временное (метаданные, загрузки до выгрузки) —
`emptyDir` с пределом `site.resources.scratchSizeLimit`. Образы — только по digest.
`Certificate` нет: у iam-proxy нет своей идентичности в IAM. `metadata.namespace` чарт не
пишет: namespace ставит Argo CD по destination. `ownerReferences` нет: снятие идёт через
Argo CD.

Метки на каждом объекте: `registry.in-cloud.io/id`, `registry.in-cloud.io/namespace` (login),
`registry.in-cloud.io/owner-uid` (uid заказа `Registry`) и `app.kubernetes.io/*`.

## Под

- **zot** `v2.1.20` (`ghcr.io/project-zot/zot-linux-amd64` по digest) слушает только
  `127.0.0.1:5000`; хранилище — драйвер `s3` (`forcepathstyle`, TLS), бакет, адрес и регион —
  из значений заказа.
- **init vault-agent** (аннотации инжектора Vault, `agent-pre-populate-only`: сайдкара
  vault-agent нет) рендерит `/vault/secrets/config.json` до старта zot из листа KV v2
  `vault.secretPath`; из листа берутся только `accessKey` и `secretKey`, остальное в конфиге —
  значения заказа. Пока листа нет, шаблон отказывает, init завершается с ошибкой и kubelet
  перезапускает его — под ждёт кредов, пустой конфиг не пишется. Вход — `auth/<vault.authMount>`,
  роль `vault.role` (= `zot-<id>`), projected-токен SA с аудиторией `site.vault.tokenAudience`,
  Vault — `site.vault.address` по https с CA из `site.caBundle`. Том с конфигом получает только
  контейнер zot (`agent-inject-containers: zot`): ключи бакета iam-proxy не видит.
- **iam-proxy** (`docker.io/prorobotech/registry-iam-proxy` по digest; бинарь `cmd/iam-proxy`
  этого репозитория) — единственный слушатель пода снаружи (`:8080`). Проверяет JWT клиента
  (JWKS только https с CA площадки, `aud`, `iss`, `exp` обязателен) и спрашивает право
  публичным `AuthorizeService.Check` kaname **токеном самого клиента «о себе»**: своих прав в
  IAM у прокси нет, общей служебной идентичности нет. Кэш вердиктов — не дольше
  `site.iamProxy.decisionCacheTTL` (≤ 10 с) по (хэш токена, объект, глагол), запись мимо кэша.
  Новый репозиторий прокси заявляет registry-operator на эндпоинт регистрации, входя
  projected-токеном SA `zot-<id>` с аудиторией `site.registryOperator.tokenAudience` (том только
  в этом контейнере; оператор сверяет его через TokenReview).

Интерфейс iam-proxy, на который опирается чарт (`cmd/iam-proxy`):

| флаг | значение |
|---|---|
| `--listen=:8080` | слушатель запросов клиентов (через прокси Envoy Gateway) |
| `--health-listen=:8081` | слушатель проб: `GET /readyz`, `GET /healthz`; в Service не выходит |
| `--upstream=http://127.0.0.1:5000` | zot |
| `--registry-id=<id>` | цель проверки: `registry_registry:<id>` и `registry_repository:<id>/<repo>` |
| `--anonymous-pull=<true\|false>` | `Registry.spec.public`: pull без токена; прочее — только с токеном |
| `--kaname-api-url=<https://…>` | публичный API kaname (`AuthorizeService.Check`) |
| `--jwks-url=<https://…>` | набор ключей kaname |
| `--issuer=<iss>` | `iss` принимаемых токенов |
| `--audience=<aud>` | `aud` принимаемых токенов |
| `--ca-file=<путь>` | CA площадки для kaname и эндпоинта регистрации |
| `--decision-cache-ttl=<1s…10s>` | окно кэша вердиктов |
| `--registration-url=<https://…>` | эндпоинт регистрации нового репозитория (registry-operator) |
| `--registration-token-file=/var/run/secrets/registry-operator/token` | projected-токен SA `zot-<id>` для эндпоинта регистрации |

Права, которые спрашивает прокси: pull — `v_get` на `registry_repository:<id>/<repo>`; push в
зарегистрированный репозиторий — `v_update` на нём; новый репозиторий (не зарегистрирован) —
`v_create` на `registry_registry:<id>`, затем заявка на регистрацию. `Authorization` и `Cookie`
клиента в zot не уходят; тела ответов zot 5xx/401 наружу не отдаются; `/v2/_catalog` и
`/v2/_zot/*` закрыты.

## Сеть пода

На площадке запрет по умолчанию; `CiliumNetworkPolicy` разрешает ровно:

| направление | кому | порты |
|---|---|---|
| вход | поды прокси Envoy Gateway своего Gateway (`site.edge.proxy`: namespace и метки `gateway.envoyproxy.io/owning-gateway-{namespace,name}`) | TCP 8080 |
| вход (если `site.network.allowKubeletProbes`) | `host` — пробы kubelet | TCP 8081 |
| выход | DNS (`site.network.dns`), только имена S3, Vault, kaname и эндпоинта регистрации | UDP/TCP 53 |
| выход | S3 заказа — хост и порт из `storage.endpoint` | TCP (443 по умолчанию) |
| выход | Vault — `site.network.vault` | порты пира |
| выход | публичный API kaname (Check, JWKS) — `site.network.kaname` | порты пира, кроме 9091 |
| выход | эндпоинт регистрации registry-operator — `site.network.registryOperator` | порты пира |

Внутренний листенер kaname `:9091` из плоскости данных недостижим: схема отвергает такой порт.
Вход kubelet по умолчанию закрыт: площадка, чья политика узла сама не пропускает пробы,
включает его явно.

## Контракт значений

Значения приходят двумя слоями `AddonValue` платформы и не пересекаются по ключам:

- **слой заказа** (priority 0) — переносит `Addon.spec.variables`, которые пишет
  registry-operator, в `instance`, `storage`, `vault`;
- **слой площадки** (priority 10) — константы `site.*`.

Без обоих слоёв рендер отказывает по `values.schema.json`: обязательных умолчаний нет.
Схема закрыта (`additionalProperties: false` на каждом уровне).

### Переменные Addon заказа (пишет registry-operator)

Все значения — строки (`Addon.spec.variables` — `map[string]string`). Перечень закрыт: ровно
эти одиннадцать имён, лишнее отвергает политика V10 и проверка оператора.

| переменная | значение в чарте | откуда | форма |
|---|---|---|---|
| `id` | `instance.id` | `Registry.spec.id` | `^[a-z0-9]{6,20}$`, не `reg*` |
| `login` | `instance.login` | `Registry.metadata.namespace` | DNS-метка |
| `registryUID` | `instance.registryUID` | `Registry.metadata.uid` | uuid |
| `syncEpoch` | `instance.syncEpoch` | эпоха восстановления синхронизации | десятичное целое ≥ 0; иначе отказ схемы |
| `public` | `instance.public` | `Registry.spec.public` (по умолчанию `false`) | `true` или `false`; иначе отказ схемы |
| `bucket` | `storage.bucket` | `S3BucketClaim.status.bucketName` | имя бакета S3 |
| `endpoint` | `storage.endpoint` | `S3BucketClaim.status.endpoint` | `https://<имя>[:порт]`, без пути и учётных данных |
| `region` | `storage.region` | `S3BucketClaim.spec.region` | непусто |
| `vaultSecretPath` | `vault.secretPath` | `<kvMount>/data/` + `spec.vault.secretsPrefix` + `/` + `spec.vault.destinationPath` бакета | кончается на `/namespaces/<login>/apps/docker-registry/zot-<id>`, содержит `/data/` |
| `vaultAuthMount` | `vault.authMount` | `VaultClaim.spec.auth.mountPath` | путь mount без `auth/` |
| `vaultRole` | `vault.role` | `VaultClaim.spec.roles[0].name` | равна `zot-<id>` |

Связь между значениями проверяет шаблон: роль — своего заказа, лист — своего заказа и своего
арендатора, аудитории токенов Vault и registry-operator различны; иначе рендер отказывает.

`Addon` заказа оператор ставит на этапе 2 пайплайна: после готовности `S3BucketClaim` и
`VaultClaim` (лист с ключами записан, роль заведена), iam `ServiceAccount` робота и кортежа
`registry_registry:<id>`; параллельно — `AccessBinding` робота. Готовность zot оператор
читает сам: `Deployment zot-<id>` Available в namespace `<login>`; маршрут заказа (этап 3)
ставит на `Service zot-<id>:8080`.

### Константы площадки (`site`)

| ключ | что |
|---|---|
| `site.caBundle.{configMap,key}` | ConfigMap с CA Vault, публичного API kaname и эндпоинта регистрации в namespace арендатора |
| `site.vault.address` | `https://` Vault для vault-agent |
| `site.vault.tokenAudience` | аудитория projected-токена vault-agent; равна `audience` роли Vault заказа |
| `site.kaname.apiURL` | `https://` публичного API kaname |
| `site.kaname.jwksURL` | `https://` набора ключей kaname |
| `site.kaname.{issuer,audience}` | `iss` и `aud` принимаемых токенов |
| `site.registryOperator.registrationURL` | `https://` эндпоинта регистрации registry-operator |
| `site.registryOperator.tokenAudience` | аудитория projected-токена заявки; отлична от аудитории Vault |
| `site.iamProxy.decisionCacheTTL` | окно кэша вердиктов, `1s`…`10s` |
| `site.images.{zot,iamProxy}` | `repository`, `tag`, `digest` (обязателен) |
| `site.edge.proxy` | namespace прокси Envoy Gateway и `gateway.{name,namespace}` |
| `site.network.allowKubeletProbes` | вход kubelet на `:8081` |
| `site.network.dns` | namespace и метки DNS (непустой селектор) |
| `site.network.{vault,kaname,registryOperator}` | пиры выхода: `{fqdn, ports}` \| `{namespace, labels, ports}` \| `{cidr, ports}` |
| `site.resources` | requests/limits zot, iam-proxy, vault-agent; предел временного |

## Пробы

`charts/tests/run.sh` (в CI, `.github/workflows/charts.yml`): `helm lint --strict`, рендер по
цепочке «платформа → слой заказа с переменными → экземпляр» с golden-сверкой
(`charts/tests/golden/instance-*.yaml`), отрицательные пробы значений с законным близнецом,
проверки формы (ровно zot и iam-proxy, нет PVC и сертификата, образы по digest, токен заявки
только в iam-proxy, ровно перечисленные направления сети), kubeconform по закреплённым CRD.
