# registry-instance

Рабочая нагрузка zot одного регистри по заказу. Чарт отвечает ровно на вопрос
«поставить zot с нужными путями»: всё, у чего есть своё состояние, — бакет
(`S3BucketClaim`), роль Vault (`VaultClaim`), робот и выдача kaname
(`iam.kaname.in-cloud.io` `ServiceAccount`, `AccessBinding`), маршрут края (`HTTPRoute`), —
заводит и ведёт registry-operator сам. Сюда он передаёт только пути к ним.

Ставится через `Addon` (addons-operator → Argo CD). Объекты платформы, без которых `Addon`
не развернётся, — `AddonTemplate` этого чарта, слои `AddonValue`, общие маршруты — в
[`registry-platform`](../registry-platform/README.md).

## Что ставит

| вид | имя | волна Argo CD | зачем |
|---|---|---|---|
| `v1` ServiceAccount | `zot-<id>` | 0 | личность пода; на неё выписана роль Vault `zot-<id>`; токен API не монтируется |
| `cilium.io/v2` CiliumNetworkPolicy | `zot-<id>` | 0 | сеть пода (ниже); применяется раньше Deployment |
| `v1` Service | `zot-<id>` | 0 | `:8080` → порт `proxy` сайдкара; на него ссылается маршрут заказа |
| `apps/v1` Deployment | `zot-<id>` | 1 | `replicas: 1`, `Recreate`; zot + сайдкар + vault-agent |

PVC нет: данные zot — только в S3; под временное (метаданные, загрузки до выгрузки) —
`emptyDir` с пределом `site.resources.scratchSizeLimit`. Образы — только по digest.
`metadata.namespace` чарт не пишет: namespace ставит Argo CD по destination. `ownerReferences`
нет: снятие идёт через Argo CD.

Метки на каждом объекте: `registry.in-cloud.io/id`, `registry.in-cloud.io/namespace` (login),
`registry.in-cloud.io/owner-uid` (uid заказа `Registry`) и `app.kubernetes.io/*`.

## Под

- **zot** `v2.1.20` слушает только `127.0.0.1:5000`; хранилище — драйвер `s3`
  (`forcepathstyle`, TLS), бакет, адрес и регион — из значений заказа.
- **vault-agent** (инжектор Vault): init-контейнер рендерит `/vault/secrets/config.json` до
  старта zot из листа KV v2 `vault.secretPath`; из листа берутся только `accessKey` и
  `secretKey`, остальное в конфиге — значения заказа. Пока листа нет, шаблон отказывает,
  init завершается с ошибкой и kubelet перезапускает его — под ждёт кредов, пустой конфиг
  не пишется. Вход — `auth/<vault.authMount>`, роль `vault.role` (= `zot-<id>`),
  projected-токен SA с аудиторией `site.vault.tokenAudience`, Vault — `site.vault.address`
  по https с CA из `site.vault.caConfigMap`. Сайдкар vault-agent продлевает клиентский
  сертификат `/vault/secrets/tls.pem` (PKI `site.vault.pki`, 24 ч).
- **sidecar** — образ svc-registry (`site.images.sidecar`) с `--mode=sidecar`: единственный
  слушатель пода (`:8080`), проверяет JWT kaname и право на `registry.registries:<id>`
  вызовом Check по mTLS, проксирует в zot.

Интерфейс сайдкара, на который опирается чарт (svc-registry, задача #2):

| флаг | значение |
|---|---|
| `--mode=sidecar` | режим сайдкара |
| `--listen=:8080` | адрес слушателя |
| `--upstream=http://127.0.0.1:5000` | zot |
| `--registry-id=<id>` | цель проверки прав `registry.registries:<id>` |
| `--kaname-check-address=<host:9091>` | внутренний листенер kaname (Check, mTLS) |
| `--kaname-jwks-url=<https://host:9097/…>` | набор ключей kaname |
| `--audience=<aud>` | аудитория JWT |
| `--client-cert=/vault/secrets/tls.pem` | сертификат, ключ и CA одним PEM; файл перечитывается |
| `--ca-file=<путь>` | CA kaname |

Пробы: готовность `GET /readyz`, живость `GET /healthz` на `:8080`.

## Сеть пода

На площадке запрет по умолчанию; `CiliumNetworkPolicy` разрешает ровно:

| направление | кому | порты |
|---|---|---|
| вход | поды прокси Envoy Gateway своего Gateway (`site.edge.proxy`: namespace и метки `gateway.envoyproxy.io/owning-gateway-{namespace,name}`) | TCP 8080 |
| вход (если `site.network.allowKubeletProbes`) | `host` — пробы kubelet | TCP 8080 |
| выход | DNS (`site.network.dns`), только имена S3, Vault и kaname | UDP/TCP 53 |
| выход | S3 заказа — хост и порт из `storage.endpoint` | TCP (443 по умолчанию) |
| выход | Vault — `site.network.vault` | порты пира |
| выход | kaname — `site.network.kaname` | только 9091 и 9097 |

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
эти десять имён, лишнее отвергает политика V10 и проверка оператора.

| переменная | значение в чарте | откуда | форма |
|---|---|---|---|
| `id` | `instance.id` | `Registry.spec.id` | `^[a-z0-9]{6,20}$`, не `reg*` |
| `login` | `instance.login` | `Registry.metadata.namespace` | DNS-метка |
| `registryUID` | `instance.registryUID` | `Registry.metadata.uid` | uuid |
| `syncEpoch` | `instance.syncEpoch` | эпоха восстановления синхронизации | десятичное целое ≥ 0; иначе отказ схемы |
| `bucket` | `storage.bucket` | `S3BucketClaim.status.bucketName` | имя бакета S3 |
| `endpoint` | `storage.endpoint` | `S3BucketClaim.status.endpoint` | `https://<имя>[:порт]`, без пути и учётных данных |
| `region` | `storage.region` | `S3BucketClaim.spec.region` | непусто |
| `vaultSecretPath` | `vault.secretPath` | `<kvMount>/data/` + `spec.vault.secretsPrefix` + `/` + `spec.vault.destinationPath` бакета | кончается на `/namespaces/<login>/apps/docker-registry/zot-<id>`, содержит `/data/` |
| `vaultAuthMount` | `vault.authMount` | `VaultClaim.spec.auth.mountPath` | путь mount без `auth/` |
| `vaultRole` | `vault.role` | `VaultClaim.spec.roles[0].name` | равна `zot-<id>` |

Связь между значениями проверяет шаблон: роль — своего заказа, лист — своего заказа и своего
арендатора; иначе рендер отказывает.

`Addon` заказа оператор ставит только после готовности `S3BucketClaim` и `VaultClaim`
(лист с ключами уже записан, роль заведена) и `AccessBinding`. Готовность zot для оператора —
`Application` Healthy и `Deployment zot-<id>` Available; маршрут заказа оператор ставит на
`Service zot-<id>:8080` в namespace `<login>`.

### Константы площадки (`site`)

| ключ | что |
|---|---|
| `site.vault.address` | `https://` Vault для vault-agent |
| `site.vault.caConfigMap.{name,key}` | ConfigMap с CA Vault и kaname в namespace арендатора |
| `site.vault.tokenAudience` | аудитория projected-токена; равна `audience` роли Vault заказа |
| `site.vault.pki.{mount,role,commonName,uriSAN}` | выпуск клиентского сертификата сайдкара |
| `site.kaname.checkAddress` | `host:9091` |
| `site.kaname.jwksURL` | `https://host:9097/…` |
| `site.sidecar.audience` | аудитория JWT kaname |
| `site.images.{zot,sidecar}` | `repository`, `tag`, `digest` (обязателен) |
| `site.edge.proxy` | namespace прокси Envoy Gateway и `gateway.{name,namespace}` |
| `site.network.allowKubeletProbes` | вход kubelet на `:8080` |
| `site.network.dns` | namespace и метки DNS (непустой селектор) |
| `site.network.vault`, `site.network.kaname` | пиры выхода: `{fqdn, ports}` \| `{namespace, labels, ports}` \| `{cidr, ports}` |
| `site.resources` | requests/limits zot, сайдкара, vault-agent; предел временного |

## Пробы

`charts/tests/run.sh` (в CI, `.github/workflows/charts.yml`): `helm lint --strict`, рендер по
цепочке «платформа → слой заказа с переменными → экземпляр» с golden-сверкой
(`charts/tests/golden/instance-*.yaml`), отрицательные пробы значений с законным близнецом,
проверки формы (ровно рабочая нагрузка, нет PVC, образы по digest, ровно перечисленные
направления сети), kubeconform по закреплённым CRD.
