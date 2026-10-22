{{/*
Правила чарта (держат values.schema.json и пробы charts/tests):
  - каждое значение печатается через toJson; форма значений закрыта схемой;
  - tpl, lookup, .Capabilities и metadata.namespace не используются: namespace ставит
    Argo CD по destination Application, рендер не зависит от кластера;
  - имена объектов — zot-<id>; ownerReferences нет — снятие идёт через Argo CD;
  - чарт ставит только рабочую нагрузку zot: ресурсов с состоянием в нём нет.
*/}}

{{- define "ri.base" -}}
{{- printf "zot-%s" .Values.instance.id -}}
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

{{/* ri.hostPort разбирает host:port в {host, port}. */}}
{{- define "ri.hostPort" -}}
{{- $hp := splitList ":" . -}}
{{- dict "host" (first $hp) "port" (last $hp | atoi) | toJson -}}
{{- end -}}

{{/*
Шаблон vault-agent для config.json zot. Всё, кроме ключей бакета, — значения заказа,
напечатанные toJson; ключи (accessKey, secretKey) vault-agent берёт из листа, который
пишет S3BucketClaim. Нет листа — ошибка шаблона: init-контейнер vault-agent завершается
с отказом и перезапускается, пока лист не появится; пустой конфиг не пишется.
*/}}
{{- define "ri.zotConfigTemplate" -}}
{{- $s := .Values.storage -}}
{{- $driver := dict "name" "s3" "rootdirectory" "/zot" "secure" true "forcepathstyle" true "region" $s.region "regionendpoint" $s.endpoint "bucket" $s.bucket "accesskey" "@@ACCESS_KEY@@" "secretkey" "@@SECRET_KEY@@" -}}
{{- $storage := dict "rootDirectory" "/var/lib/zot" "dedupe" false "remoteCache" false "gc" true "storageDriver" $driver -}}
{{- $cfg := dict "distSpecVersion" "1.1.1" "http" (dict "address" "127.0.0.1" "port" "5000") "log" (dict "level" "info") "storage" $storage -}}
{{- $json := $cfg | toJson | include "ri.noBraces" -}}
{{- $json = replace "\"@@ACCESS_KEY@@\"" "{{ .Data.data.accessKey | toJSON }}" $json -}}
{{- $json = replace "\"@@SECRET_KEY@@\"" "{{ .Data.data.secretKey | toJSON }}" $json -}}
{{- printf "{{- with secret %q -}}%s{{- end -}}" (include "ri.noBraces" .Values.vault.secretPath) $json -}}
{{- end -}}

{{/* Шаблон vault-agent клиентского сертификата сайдкара: сертификат, ключ и CA в одном PEM. */}}
{{- define "ri.tlsTemplate" -}}
{{- $p := .Values.site.vault.pki -}}
{{- $path := printf "%s/issue/%s" $p.mount $p.role | include "ri.noBraces" -}}
{{- $cn := printf "common_name=%s" $p.commonName | include "ri.noBraces" -}}
{{- $san := printf "uri_sans=%s" $p.uriSAN | include "ri.noBraces" -}}
{{- printf "{{- with pkiCert %q %q %q \"ttl=24h\" -}}{{ .Cert }}{{ .Key }}{{ .CA }}{{- end -}}" $path $cn $san -}}
{{- end -}}

{{/*
Имена DNS, которые под вправе разрешать: хост S3, FQDN-пиры и хосты адресов Vault и
kaname. Выход по L3 держат отдельные правила; DNS лишь не отвечает на прочие имена.
*/}}
{{- define "ri.dnsNames" -}}
{{- $names := list (include "ri.url" .Values.storage.endpoint | fromJson).host -}}
{{- range (concat .Values.site.network.vault .Values.site.network.kaname) -}}
{{- if .fqdn -}}{{- $names = append $names .fqdn -}}{{- end -}}
{{- end -}}
{{- $names = append $names (include "ri.url" .Values.site.vault.address | fromJson).host -}}
{{- $names = append $names (include "ri.url" .Values.site.kaname.jwksURL | fromJson).host -}}
{{- $names = append $names (include "ri.hostPort" .Values.site.kaname.checkAddress | fromJson).host -}}
{{- $names | uniq | sortAlpha | toJson -}}
{{- end -}}
