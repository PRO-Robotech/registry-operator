{{- define "rp.labels" -}}
app.kubernetes.io/name: registry-platform
app.kubernetes.io/instance: {{ .Release.Name | toJson }}
app.kubernetes.io/part-of: registry
app.kubernetes.io/managed-by: {{ .Release.Service | toJson }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | toJson }}
{{- end -}}

{{/*
rp.inertValues отказывает, если данные платформы содержат то, что исполнил бы кто-то
после нас: addons-operator исполняет строку слоя и шаблона Addon как шаблон sprig, едва
видит «{{»; плагин подстановки секретов Argo CD реагирует на «<path:» и ключ
argocdPlugins. Данные платформы исполняемого текста не несут; исполняемое — только
фиксированный текст чарта, вставляемый после этой проверки.
*/}}
{{- define "rp.inertValues" -}}
{{- range list "{{" "}}" "<path:" "argocdPlugins" -}}
{{- if contains . $ -}}
{{- fail (printf "platform values must not contain %q" .) -}}
{{- end -}}
{{- end -}}
{{- $ -}}
{{- end -}}

{{/* Переменные Addon заказа: их пишет registry-operator, их читает слой заказа. */}}
{{- define "rp.orderVariables" -}}
{{- list "id" "login" "registryUID" "syncEpoch" "public" "bucket" "endpoint" "region" "vaultSecretPath" "vaultAuthMount" "vaultRole" | toJson -}}
{{- end -}}

{{/*
rp.same — выражение CEL «поле не изменилось», годное и для отсутствующего поля:
обращение к отсутствующему полю в CEL — ошибка, а не пустое значение.
*/}}
{{- define "rp.same" -}}
{{- printf "(has(object.%s) == has(oldObject.%s) && (!has(object.%s) || object.%s == oldObject.%s))" . . . . . -}}
{{- end -}}
