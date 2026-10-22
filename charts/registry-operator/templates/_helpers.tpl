{{- define "ro.name" -}}registry-operator{{- end -}}

{{- define "ro.labels" -}}
app.kubernetes.io/name: {{ include "ro.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "ro.selector" -}}
app.kubernetes.io/name: {{ include "ro.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "ro.image" -}}
{{- $i := .Values.image -}}
{{- if or (not $i.tag) (not (regexMatch "^sha256:[a-f0-9]{64}$" $i.digest)) -}}
{{- fail "image.tag and image.digest (sha256) are required" -}}
{{- end -}}
{{- printf "%s:%s@%s" $i.repository $i.tag $i.digest -}}
{{- end -}}

{{/* Конфигурация оператора с путями, которые ставит чарт. */}}
{{- define "ro.config" -}}
{{- if not .Values.config.policy -}}
{{- fail "config.policy is required: the operator refuses to start without the platform policy" -}}
{{- end -}}
{{- $c := deepCopy .Values.config -}}
{{- $k := default dict $c.kaname -}}
{{- $_ := set $k "certFile" "/etc/registry-operator/kaname/tls.crt" -}}
{{- $_ := set $k "keyFile" "/etc/registry-operator/kaname/tls.key" -}}
{{- $_ := set $k "caFile" "/etc/registry-operator/kaname/ca.crt" -}}
{{- $_ := set $c "kaname" $k -}}
{{- $e := default dict $c.enrollment -}}
{{- $_ := set $e "listen" (printf ":%v" .Values.enrollment.port) -}}
{{- $_ := set $e "certFile" "/etc/registry-operator/enrollment/tls.crt" -}}
{{- $_ := set $e "keyFile" "/etc/registry-operator/enrollment/tls.key" -}}
{{- $_ := set $c "enrollment" $e -}}
{{- toYaml $c -}}
{{- end -}}
