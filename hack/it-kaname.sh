#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# it-kaname.sh — проба оператора и iam-proxy против стенда НАСТОЯЩЕГО kaname
# (стенд поднимает stand-chart.sh клона kaname, см.
# .github/workflows/integration-kaname.yml).
#
# Скрипт доставляет стенду манифест модуля registry из
# charts/registry-platform (ConfigMap, смонтированный в выкат kaname, и его
# ручки доставки), выписывает УЦ стенда лист личности модуля
# (spiffe://<домен>/ns/<ns>/sa/kacho-registry), поднимает переадресацию к
# внутреннему gRPC, публичному gRPC, токен-эндпоинту и набору ключей и гонит
# `go test -tags=kanameit ./test/kanameit/`. Контекст кластера — ЯВНЫЙ,
# текущий не трогается. Адресов и учётных данных в дереве нет: всё приходит
# из стенда и снимается ловушкой, в том числе на падении.
#
# Окружение (обязательное):
#   REGOP_IT_KUBECONFIG, REGOP_IT_CONTEXT — кластер стенда (явный контекст)
#   REGOP_IT_NAMESPACE, REGOP_IT_RELEASE  — namespace и релиз kaname
#   REGOP_IT_CA_DIR                       — каталог УЦ стенда (ca.crt, ca.key)
# Условия (создают до вызова): стенд поднят, человек полосы входа заведён
# (stand-chart.sh seed-login-lane; Secret <релиз>-login-lane-human с email,
# password и project-id).
#
# Исходы: код `go test`; 75 — условие не создано (стенд, секрет, манифест,
# переадресация): вердикта о дереве нет.
set -uo pipefail

RC_UNMET=75
unmet() { printf 'it-kaname: CONDITION NOT CREATED: %s\n' "$*" >&2; exit "$RC_UNMET"; }

for v in REGOP_IT_KUBECONFIG REGOP_IT_CONTEXT REGOP_IT_NAMESPACE REGOP_IT_RELEASE REGOP_IT_CA_DIR; do
	[ -n "${!v:-}" ] || unmet "variable $v is not set"
done
for tool in kubectl python3 openssl go; do
	command -v "$tool" >/dev/null || unmet "no $tool"
done

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFEST="$HERE/charts/registry-platform/files/registry.manifest.yaml"
[ -s "$MANIFEST" ] || unmet "no registry module manifest in this tree: $MANIFEST"

K=(kubectl --kubeconfig "$REGOP_IT_KUBECONFIG" --context "$REGOP_IT_CONTEXT" -n "$REGOP_IT_NAMESPACE")
REL="$REGOP_IT_RELEASE"
CM="$REL-regop-it-manifests"
MOUNT=/etc/kaname-delivered

WORK="$(mktemp -d "${TMPDIR:-/tmp}/regop-it-kaname.XXXXXX")"
chmod 700 "$WORK"
PIDS=()
cleanup() {
	for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null; done
	"${K[@]}" delete configmap "$CM" --ignore-not-found >/dev/null 2>&1
	rm -rf "$WORK"
}
trap cleanup EXIT

# Доставка манифеста модуля: ConfigMap, том в выкате kaname и ручки доставки;
# служба, отвергнувшая манифест, не поднимется — это находка, не условие.
"${K[@]}" create configmap "$CM" --from-file=registry.yaml="$MANIFEST" --dry-run=client -o yaml \
	| "${K[@]}" apply -f - >/dev/null || unmet "ConfigMap $CM is not written"
container="$("${K[@]}" get deploy "$REL" -o 'jsonpath={.spec.template.spec.containers[0].name}')" \
	|| unmet "deployment $REL is not readable"
[ -n "$container" ] || unmet "deployment $REL has no container"
patch=$(cat <<EOF
{"spec":{"template":{"spec":{
  "volumes":[{"name":"regop-it-manifests","configMap":{"name":"$CM"}}],
  "containers":[{"name":"$container",
    "volumeMounts":[{"name":"regop-it-manifests","mountPath":"$MOUNT","readOnly":true}],
    "env":[{"name":"KANAME_MANIFESTS__DIR","value":"$MOUNT"},{"name":"KANAME_MANIFESTS__REQUIRED","value":"true"}]}]}}}}
EOF
)
"${K[@]}" patch deploy "$REL" --type=strategic -p "$patch" >/dev/null || unmet "deployment $REL is not patched"
if ! "${K[@]}" rollout status deploy "$REL" --timeout=300s; then
	"${K[@]}" logs "deploy/$REL" --tail=80 >&2 || true
	printf 'it-kaname: FINDING: kaname did not come up with the registry module manifest of this tree\n' >&2
	exit 1
fi

secret_key() { # secret key file
	"${K[@]}" get secret "$1" -o "jsonpath={.data.${2//./\\.}}" | base64 -d > "$3" || unmet "secret $1/$2 is not readable"
	[ -s "$3" ] || unmet "secret $1/$2 is empty"
	chmod 600 "$3"
}
secret_key "$REL-server-tls" ca.crt "$WORK/server-ca.crt"
secret_key "$REL-login-lane-human" email "$WORK/email"
secret_key "$REL-login-lane-human" password "$WORK/password"
secret_key "$REL-login-lane-human" project-id "$WORK/project-id"

# Издатель, адресат докерной полосы и домен доверия — из конфигурации службы.
"${K[@]}" get configmap "$REL-config" -o 'jsonpath={.data.config\.yaml}' > "$WORK/config.yaml" || unmet "ConfigMap $REL-config is not readable"
read -r ISSUER SERVICE TRUST_DOMAIN KEYSET_PATH < <(python3 - "$WORK/config.yaml" <<'PY'
import sys, yaml
c = yaml.safe_load(open(sys.argv[1])) or {}
rt = (c.get("api-server") or {}).get("registry-token") or {}
a = c.get("authn") or {}
ts = a.get("token-signing") or {}
print(rt.get("issuer", ""), rt.get("service", ""), a.get("trust-domain", ""),
      ts.get("key-set-path", "") or "/.well-known/kaname/jwks.json")
PY
)
[ -n "${ISSUER:-}" ] && [ -n "${SERVICE:-}" ] && [ -n "${TRUST_DOMAIN:-}" ] \
	|| unmet "the service configuration names no registry-token issuer, service or trust domain"

# Лист личности модуля registry, выписанный УЦ стенда.
[ -s "$REGOP_IT_CA_DIR/ca.crt" ] && [ -s "$REGOP_IT_CA_DIR/ca.key" ] || unmet "no ca.crt and ca.key in $REGOP_IT_CA_DIR"
printf 'subjectAltName=URI:spiffe://%s/ns/%s/sa/kacho-registry\nextendedKeyUsage=clientAuth\n' "$TRUST_DOMAIN" "$REGOP_IT_NAMESPACE" > "$WORK/registry.ext"
openssl req -newkey rsa:2048 -nodes -keyout "$WORK/registry.key" -out "$WORK/registry.csr" -subj "/CN=kacho-registry" >/dev/null 2>&1 \
	&& openssl x509 -req -in "$WORK/registry.csr" -CA "$REGOP_IT_CA_DIR/ca.crt" -CAkey "$REGOP_IT_CA_DIR/ca.key" \
		-CAcreateserial -CAserial "$WORK/ca.srl" -out "$WORK/registry.crt" -days 1 -extfile "$WORK/registry.ext" >/dev/null 2>&1 \
	|| unmet "the registry module leaf is not issued"
chmod 600 "$WORK/registry.key"

free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }
PUBLIC=$(free_port); TOKEN=$(free_port); JWKS=$(free_port); INTERNAL=$(free_port)
"${K[@]}" port-forward "svc/$REL" "$PUBLIC:9090" "$TOKEN:9096" "$JWKS:9097" > "$WORK/pf1.log" 2>&1 &
PIDS+=($!)
"${K[@]}" port-forward "svc/$REL-internal" "$INTERNAL:9091" > "$WORK/pf2.log" 2>&1 &
PIDS+=($!)
for _ in $(seq 1 30); do
	grep -q "Forwarding from 127.0.0.1:$JWKS" "$WORK/pf1.log" && grep -q "Forwarding from 127.0.0.1:$INTERNAL" "$WORK/pf2.log" && break
	sleep 1
done
grep -q "Forwarding from 127.0.0.1:$JWKS" "$WORK/pf1.log" && grep -q "Forwarding from 127.0.0.1:$INTERNAL" "$WORK/pf2.log" \
	|| unmet "port forwarding did not come up"

export REGOP_IT_INTERNAL_ADDR="127.0.0.1:$INTERNAL"
export REGOP_IT_INTERNAL_SERVER_NAME="$REL-internal.$REGOP_IT_NAMESPACE.svc"
export REGOP_IT_PUBLIC_ADDR="127.0.0.1:$PUBLIC"
export REGOP_IT_PUBLIC_SERVER_NAME="$REL.$REGOP_IT_NAMESPACE.svc"
export REGOP_IT_TOKEN_URL="https://127.0.0.1:$TOKEN/iam/token"
export REGOP_IT_JWKS_URL="https://127.0.0.1:$JWKS$KEYSET_PATH"
export REGOP_IT_ISSUER="$ISSUER"
export REGOP_IT_SERVICE="$SERVICE"
export REGOP_IT_TLS_DIR="$WORK"
REGOP_IT_HUMAN_EMAIL="$(cat "$WORK/email")"
REGOP_IT_HUMAN_PASSWORD="$(cat "$WORK/password")"
REGOP_IT_PROJECT_ID="$(cat "$WORK/project-id")"
export REGOP_IT_HUMAN_EMAIL REGOP_IT_HUMAN_PASSWORD REGOP_IT_PROJECT_ID

cd "$HERE" && go test -tags=kanameit ./test/kanameit/ -count=1 -v -timeout 15m "$@"
