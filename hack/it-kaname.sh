#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# it-kaname.sh — проба iam-proxy и регистрации оператора против стенда
# НАСТОЯЩЕГО kaname (стенд поднимает stand-chart.sh клона kaname, см.
# .github/workflows/integration-kaname.yml).
#
# Подкоманды:
#   deliver — доставить стенду манифест модуля registry (ConfigMap, том в
#             выкате kaname, ручки доставки; маркер аккаунта системы заменён
#             аккаунтом kacho-system, как это делает чарт registry-platform) и
#             дождаться выката. Служба, отвергнувшая манифест, не поднимется —
#             это находка, не условие.
#   probe   — выписать УЦ стенда лист личности модуля registry
#             (spiffe://<домен>/ns/<ns>/sa/kacho-registry), вынести лист службы
#             для чеканки бутстрапа, поднять переадресации и прогнать
#             `go test -tags=kanameit ./test/kanameit/`. Обычный токен человека
#             — из окружения, которое записал посев церемонии
#             (stand-chart.sh seed-ceremony).
#
# Контекст кластера — ЯВНЫЙ, текущий не трогается. Адресов и учётных данных в
# дереве нет: всё приходит из стенда, снимается ловушкой и на падении, в
# журнал не печатается.
#
# Окружение (обязательное):
#   REGOP_IT_KUBECONFIG, REGOP_IT_CONTEXT — кластер стенда (явный контекст)
#   REGOP_IT_NAMESPACE, REGOP_IT_RELEASE  — namespace и релиз kaname
#   REGOP_IT_CA_DIR                       — каталог УЦ стенда (ca.crt, ca.key)
#   REGOP_IT_MANIFEST                     — манифест модуля registry (deliver)
#   REGOP_IT_CEREMONY_ENV                 — окружение посева церемонии (probe)
#
# Исходы: 0 — проба зелёная; код `go test` — её отказ; 75 — условие не
# создано (стенд, секрет, манифест, переадресация, человек): вердикта о
# дереве нет.
set -uo pipefail

RC_UNMET=75
unmet() { printf 'it-kaname: CONDITION NOT CREATED: %s\n' "$*" >&2; exit "$RC_UNMET"; }

for v in REGOP_IT_KUBECONFIG REGOP_IT_CONTEXT REGOP_IT_NAMESPACE REGOP_IT_RELEASE REGOP_IT_CA_DIR; do
	[ -n "${!v:-}" ] || unmet "variable $v is not set"
done
for tool in kubectl python3 openssl go base64; do
	command -v "$tool" >/dev/null || unmet "no $tool"
done

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
K=(kubectl --kubeconfig "$REGOP_IT_KUBECONFIG" --context "$REGOP_IT_CONTEXT" -n "$REGOP_IT_NAMESPACE")
REL="$REGOP_IT_RELEASE"
CM="$REL-regop-it-manifests"
MOUNT=/etc/kaname-delivered
# Аккаунт системы, в котором kaname заводит личности модулей и группу
# module-relation-writers (тот же, что kanameModule.systemAccount чарта
# registry-platform на площадке).
SYSTEM_ACCOUNT=kacho-system
MARKER=zzph-system-account

WORK="$(mktemp -d "${TMPDIR:-/tmp}/regop-it-kaname.XXXXXX")"
chmod 700 "$WORK"
PIDS=()
cleanup() {
	for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null; done
	rm -rf "$WORK"
}
trap cleanup EXIT

deliver() {
	[ -n "${REGOP_IT_MANIFEST:-}" ] && [ -s "$REGOP_IT_MANIFEST" ] || unmet "no registry module manifest: REGOP_IT_MANIFEST=${REGOP_IT_MANIFEST:-}"
	local markers
	markers=$(grep -o "$MARKER" "$REGOP_IT_MANIFEST" | wc -l)
	[ "$markers" -eq 3 ] || { printf 'it-kaname: FINDING: the manifest names the system account through %s markers, not 3\n' "$markers" >&2; exit 1; }
	sed "s/$MARKER/$SYSTEM_ACCOUNT/g" "$REGOP_IT_MANIFEST" > "$WORK/registry.manifest.yaml"
	"${K[@]}" create configmap "$CM" --from-file=registry.manifest.yaml="$WORK/registry.manifest.yaml" --dry-run=client -o yaml \
		| "${K[@]}" apply -f - >/dev/null || unmet "ConfigMap $CM is not written"
	local container patch
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
		# «Прежний под ждёт снятия» при одной реплике значит, что НОВЫЙ под не
		# готов: журнал нужен его, а не прежнего (deploy/ берёт любой).
		"${K[@]}" get pods -l "app=$REL" -o wide >&2 || true
		local pod
		for pod in $("${K[@]}" get pods -l "app=$REL" -o 'jsonpath={.items[*].metadata.name}'); do
			printf -- '--- %s\n' "$pod" >&2
			"${K[@]}" get pod "$pod" -o 'jsonpath={range .status.containerStatuses[*]}{.name} ready={.ready} restarts={.restartCount} {.state}{"\n"}{end}' >&2 || true
			"${K[@]}" logs "$pod" --all-containers --tail=60 >&2 || true
			"${K[@]}" logs "$pod" --all-containers --previous --tail=60 >&2 2>/dev/null || true
		done
		printf 'it-kaname: FINDING: kaname did not come up with the registry module manifest of this tree\n' >&2
		exit 1
	fi
	printf 'it-kaname: the registry module manifest is delivered (system account %s)\n' "$SYSTEM_ACCOUNT"
}

secret_key() { # secret key file
	"${K[@]}" get secret "$1" -o "jsonpath={.data.${2//./\\.}}" | base64 -d > "$3" || unmet "secret $1/$2 is not readable"
	[ -s "$3" ] || unmet "secret $1/$2 is empty"
	chmod 600 "$3"
}

probe() {
	[ -n "${REGOP_IT_CEREMONY_ENV:-}" ] && [ -s "$REGOP_IT_CEREMONY_ENV" ] \
		|| unmet "no ceremony environment (stand-chart.sh seed-ceremony): REGOP_IT_CEREMONY_ENV=${REGOP_IT_CEREMONY_ENV:-}"
	secret_key "$REL-server-tls" ca.crt "$WORK/server-ca.crt"
	# Лист службы с её SPIFFE-именем — единственный круг чеканки бутстрапа.
	secret_key "$REL-client-tls" tls.crt "$WORK/service.crt"
	secret_key "$REL-client-tls" tls.key "$WORK/service.key"

	# Обычный токен человека и его идентификатор — из окружения посева.
	python3 - "$REGOP_IT_CEREMONY_ENV" "$WORK" <<'PY' || unmet "the ceremony environment names no human token"
import json, os, sys
doc = json.load(open(sys.argv[1]))
vals = {v.get("key"): v.get("value") for v in doc.get("values", [])}
token, user = vals.get("jwtHumanCeremony") or "", vals.get("ceremonyUserId") or ""
if not token or not user:
    sys.exit(1)
for name, value in (("human.token", token), ("human.id", user)):
    path = os.path.join(sys.argv[2], name)
    with open(path, "w") as f:
        f.write(value)
    os.chmod(path, 0o600)
PY

	# Издатель, адресат публичного API и домен доверия — из конфигурации службы.
	"${K[@]}" get configmap "$REL-config" -o 'jsonpath={.data.config\.yaml}' > "$WORK/config.yaml" || unmet "ConfigMap $REL-config is not readable"
	# Адресат докерной полосы — другой адресат той же установки: токен для
	# него прокси обязан отвергнуть.
	read -r ISSUER AUDIENCE TRUST_DOMAIN KEYSET_PATH FOREIGN_AUDIENCE < <(python3 - "$WORK/config.yaml" <<'PY'
import sys, yaml
c = yaml.safe_load(open(sys.argv[1])) or {}
a = c.get("authn") or {}
ts = a.get("token-signing") or {}
pc = a.get("presented-credential") or {}
rt = (c.get("api-server") or {}).get("registry-token") or {}
print(ts.get("issuer", "") or "-", pc.get("audience", "") or "-", a.get("trust-domain", "") or "-",
      ts.get("key-set-path", "") or "/.well-known/kaname/jwks.json", rt.get("service", "") or "-")
PY
)
	for v in ISSUER AUDIENCE TRUST_DOMAIN FOREIGN_AUDIENCE; do
		[ -n "${!v:-}" ] && [ "${!v}" != "-" ] || unmet "the service configuration names no $v"
	done

	# Лист личности модуля registry, выписанный УЦ стенда.
	[ -s "$REGOP_IT_CA_DIR/ca.crt" ] && [ -s "$REGOP_IT_CA_DIR/ca.key" ] || unmet "no ca.crt and ca.key in $REGOP_IT_CA_DIR"
	printf 'subjectAltName=URI:spiffe://%s/ns/%s/sa/kacho-registry\nextendedKeyUsage=clientAuth\n' "$TRUST_DOMAIN" "$REGOP_IT_NAMESPACE" > "$WORK/registry.ext"
	openssl req -newkey rsa:2048 -nodes -keyout "$WORK/registry.key" -out "$WORK/registry.csr" -subj "/CN=kacho-registry" >/dev/null 2>&1 \
		&& openssl x509 -req -in "$WORK/registry.csr" -CA "$REGOP_IT_CA_DIR/ca.crt" -CAkey "$REGOP_IT_CA_DIR/ca.key" \
			-CAcreateserial -CAserial "$WORK/ca.srl" -out "$WORK/registry.crt" -days 1 -extfile "$WORK/registry.ext" >/dev/null 2>&1 \
		|| unmet "the registry module leaf is not issued"
	chmod 600 "$WORK/registry.key"

	# Переадресации — по ИМЕНАМ портов Service: публичный gRPC, выдача
	# токенов и публичный REST — у Service службы; внутренний gRPC и набор
	# ключей — у её внутреннего Service.
	free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }
	PUBLIC=$(free_port); TOKEN=$(free_port); REST=$(free_port); INTERNAL=$(free_port); JWKS=$(free_port)
	"${K[@]}" port-forward "svc/$REL" "$PUBLIC:grpc" "$TOKEN:registry-token" "$REST:http-rest" > "$WORK/pf1.log" 2>&1 &
	PIDS+=($!)
	"${K[@]}" port-forward "svc/$REL-internal" "$INTERNAL:grpc-internal" "$JWKS:http-jwks" > "$WORK/pf2.log" 2>&1 &
	PIDS+=($!)
	local up=0
	for _ in $(seq 1 30); do
		if grep -q "Forwarding from 127.0.0.1:$REST" "$WORK/pf1.log" && grep -q "Forwarding from 127.0.0.1:$JWKS" "$WORK/pf2.log"; then
			up=1
			break
		fi
		sleep 1
	done
	[ "$up" = 1 ] || { cat "$WORK/pf1.log" "$WORK/pf2.log" >&2; unmet "port forwarding did not come up"; }

	export REGOP_IT_INTERNAL_ADDR="127.0.0.1:$INTERNAL"
	export REGOP_IT_INTERNAL_SERVER_NAME="$REL-internal.$REGOP_IT_NAMESPACE.svc"
	export REGOP_IT_PUBLIC_ADDR="127.0.0.1:$PUBLIC"
	export REGOP_IT_PUBLIC_SERVER_NAME="$REL.$REGOP_IT_NAMESPACE.svc"
	export REGOP_IT_REST_URL="https://127.0.0.1:$REST"
	export REGOP_IT_TOKEN_URL="https://127.0.0.1:$TOKEN/iam/v1/token"
	export REGOP_IT_JWKS_URL="https://127.0.0.1:$JWKS$KEYSET_PATH"
	export REGOP_IT_ISSUER="$ISSUER"
	export REGOP_IT_AUDIENCE="$AUDIENCE"
	export REGOP_IT_FOREIGN_AUDIENCE="$FOREIGN_AUDIENCE"
	export REGOP_IT_SYSTEM_ACCOUNT="$SYSTEM_ACCOUNT"
	export REGOP_IT_TLS_DIR="$WORK"
	cd "$HERE" && go test -tags=kanameit ./test/kanameit/ -count=1 -v -timeout 20m "$@"
}

case "${1:-}" in
	deliver) deliver ;;
	probe) shift; probe "$@" ;;
	*) unmet "usage: it-kaname.sh deliver|probe [go test flags]" ;;
esac
