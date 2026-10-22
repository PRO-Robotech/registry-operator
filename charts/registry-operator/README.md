# registry-operator chart

Ставит оператор заказов `Registry`: CRD (`crds/`), учётку, ClusterRole из маркеров
(`files/role.yaml`, `make manifests`: запись — только шести ведомых видов и своих объектов,
прочее — чтение), Role выборов лидера, конфигурацию, Service (эндпоинт заявок и метрики),
Deployment, два Certificate cert-manager и NetworkPolicy: вход на эндпоинт заявок — только
от подов zot (`networkPolicy.zotPodLabels`) из namespace арендаторов
(`networkPolicy.tenantNamespaceSelector`), метрики — от `networkPolicy.metricsFrom` (пусто —
от любого пода). Кластеру нужен CNI, исполняющий NetworkPolicy.

## Обязательные значения

| ключ | что |
|---|---|
| `image.tag`, `image.digest` | образ по digest; без digest рендер отказывает |
| `config.policy` и остальная конфигурация оператора | см. `config/samples/operator-config.yaml`; пути файлов kaname и эндпоинта ставит чарт |
| `kaname.certificate.issuerRef.name` | центр, которому доверяет внутренний листенер kaname; лист выпускается с URI SAN `spiffe://<kaname.trustDomain>/ns/<namespace релиза>/sa/kacho-<kaname.moduleName>` |
| `enrollment.certificate.issuerRef.name` | центр серверного сертификата эндпоинта заявок |

`certificate.create: false` — секреты `secretName` ставит платформа (ключи `tls.crt`,
`tls.key`, `ca.crt`).

## Что требуется от платформы

- kaname: учётка службы `registry` (личность листа выше) с отношением `fga_writer` на
  `cluster:cluster_root` и манифест модуля `registry` (типы `registry_registry`,
  `registry_repository`);
- метки namespace арендатора `kaname.in-cloud.io/project-id` и
  `kaname.in-cloud.io/account-id`;
- маршрут заказа в namespace края ссылается на Service в namespace арендатора — нужен
  ReferenceGrant на namespace арендатора (объект платформы);
- центр серверного сертификата эндпоинта заявок — в namespace арендатора, для iam-proxy
  (`--enroll-ca`).
