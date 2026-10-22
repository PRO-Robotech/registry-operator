{{/*
Правила чарта (держат values.schema.json и пробы charts/tests):
  - каждое значение печатается через toJson; форма значений закрыта схемой;
  - tpl, lookup, .Capabilities и metadata.namespace не используются: namespace ставит
    Argo CD по destination Application, рендер не зависит от кластера;
  - имена объектов — zot-<id>; ownerReferences нет — снятие идёт через Argo CD;
  - чарт ставит только zot и iam-proxy: ресурсов с состоянием в нём нет.
*/}}

{{- define "ri.base" -}}
{{- printf "zot-%s" .Values.instance.id -}}
{{- end -}}

{{/* Пути внутри пода: CA площадки и projected-токен входа в эндпоинт регистрации. */}}
{{- define "ri.caPath" -}}
{{- printf "/etc/registry/ca/%s" .Values.site.caBundle.key -}}
{{- end -}}

{{- define "ri.registrationTokenDir" -}}
/var/run/secrets/registry-operator
{{- end -}}

{{/*
ri.noBraces отказывает, если значение могло бы открыть или закрыть действие шаблона
vault-agent. Схема значений таких символов не пропускает; страж стоит там, где
значение входит в текст шаблона, чтобы правка схемы его не сняла.
*/}}
{{- define "ri.noBraces" -}}
{{- if or (contains "{" .) (contains "}" .) -}}
{{- fail (printf "value %q must not contain template delimiters" .) -}}
{{- end -}}
{{- . -}}
{{- end -}}

{{/*
ri.check сверяет пути заказа с его id и login: под читает только лист своего заказа
и входит только ролью своего SA. Схема проверяет форму каждого значения отдельно,
связь между ними — здесь.
*/}}
{{- define "ri.check" -}}
{{- $base := include "ri.base" . -}}
{{- if hasPrefix "reg" .Values.instance.id -}}
{{- fail (printf "instance.id %q must not start with \"reg\"" .Values.instance.id) -}}
{{- end -}}
{{- if ne .Values.vault.role $base -}}
{{- fail (printf "vault.role %q must equal %q" .Values.vault.role $base) -}}
{{- end -}}
{{- $leaf := printf "/namespaces/%s/apps/docker-registry/%s" .Values.instance.login $base -}}
{{- if not (hasSuffix $leaf .Values.vault.secretPath) -}}
{{- fail (printf "vault.secretPath %q must end with %q" .Values.vault.secretPath $leaf) -}}
{{- end -}}
{{- if not (contains "/data/" .Values.vault.secretPath) -}}
{{- fail (printf "vault.secretPath %q must be a KV v2 read path (<mount>/data/...)" .Values.vault.secretPath) -}}
{{- end -}}
{{- if eq .Values.site.vault.tokenAudience .Values.site.registryOperator.tokenAudience -}}
{{- fail (printf "site.vault.tokenAudience and site.registryOperator.tokenAudience must differ: one token audience %q would let each peer replay the token to the other" .Values.site.vault.tokenAudience) -}}
{{- end -}}
{{- end -}}

{{- define "ri.labels" -}}
app.kubernetes.io/name: zot
app.kubernetes.io/instance: {{ include "ri.base" .root | toJson }}
app.kubernetes.io/part-of: registry
app.kubernetes.io/component: {{ .component | toJson }}
app.kubernetes.io/managed-by: registry-operator
registry.in-cloud.io/id: {{ .root.Values.instance.id | toJson }}
registry.in-cloud.io/namespace: {{ .root.Values.instance.login | toJson }}
registry.in-cloud.io/owner-uid: {{ .root.Values.instance.registryUID | toJson }}
{{- end -}}

{{- define "ri.selectorLabels" -}}
app.kubernetes.io/name: zot
registry.in-cloud.io/id: {{ .Values.instance.id | toJson }}
{{- end -}}

{{- define "ri.image" -}}
{{- if .tag -}}
{{- printf "%s:%s@%s" .repository .tag .digest -}}
{{- else -}}
{{- printf "%s@%s" .repository .digest -}}
{{- end -}}
{{- end -}}

{{/* ri.url разбирает https-адрес в {host, port}; порт по умолчанию 443. */}}
{{- define "ri.url" -}}
{{- $u := urlParse . -}}
{{- $hp := splitList ":" $u.host -}}
{{- $port := 443 -}}
{{- if gt (len $hp) 1 -}}{{- $port = last $hp | atoi -}}{{- end -}}
{{- dict "host" (first $hp) "port" $port | toJson -}}
{{- end -}}

{{/*
Шаблон vault-agent для config.json zot. Всё, кроме ключей бакета, — значения заказа,
напечатанные toJson; ключи (accessKey, secretKey) vault-agent берёт из листа, который
пишет S3BucketClaim. Нет листа — ошибка шаблона: init-контейнер vault-agent завершается
с отказом и перезапускается, пока лист не появится; пустой конфиг не пишется.
*/}}
{{- define "ri.zotConfigTemplate" -}}
{{- $s := .Values.storage -}}
{{- $driver := dict "name" "s3" "rootdirectory" "/zot" "secure" true "forcepathstyle" true "region" (include "ri.noBraces" $s.region) "regionendpoint" (include "ri.noBraces" $s.endpoint) "bucket" (include "ri.noBraces" $s.bucket) "accesskey" "@@ACCESS_KEY@@" "secretkey" "@@SECRET_KEY@@" -}}
{{- $storage := dict "rootDirectory" "/var/lib/zot" "dedupe" false "remoteCache" false "gc" true "storageDriver" $driver -}}
{{- $cfg := dict "distSpecVersion" "1.1.1" "http" (dict "address" "127.0.0.1" "port" "5000") "log" (dict "level" "info") "storage" $storage -}}
{{- $json := $cfg | toJson -}}
{{- $json = replace "\"@@ACCESS_KEY@@\"" "{{ .Data.data.accessKey | toJSON }}" $json -}}
{{- $json = replace "\"@@SECRET_KEY@@\"" "{{ .Data.data.secretKey | toJSON }}" $json -}}
{{- printf "{{- with secret %q -}}%s{{- end -}}" (include "ri.noBraces" .Values.vault.secretPath) $json -}}
{{- end -}}

{{/*
Имена DNS, которые под вправе разрешать: хост S3, FQDN-пиры и хосты адресов Vault,
публичного gRPC kaname (Check), его набора ключей и эндпоинта регистрации. Выход по L3 держат
отдельные правила; DNS лишь не отвечает на прочие имена.
*/}}
{{- define "ri.dnsNames" -}}
{{- $n := .Values.site.network -}}
{{- $names := list (include "ri.url" .Values.storage.endpoint | fromJson).host -}}
{{- range (concat $n.vault $n.kaname $n.registryOperator) -}}
{{- if .fqdn -}}{{- $names = append $names .fqdn -}}{{- end -}}
{{- end -}}
{{- $names = append $names (first (splitList ":" .Values.site.kaname.address)) -}}
{{- range (list .Values.site.vault.address .Values.site.kaname.jwksURL .Values.site.registryOperator.registrationURL) -}}
{{- $names = append $names (include "ri.url" . | fromJson).host -}}
{{- end -}}
{{- $names | uniq | sortAlpha | toJson -}}
{{- end -}}
