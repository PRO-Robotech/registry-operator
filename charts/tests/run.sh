#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# run.sh — пробы чартов registry-instance и registry-platform по рендеру.
#
# Цепочка повторяет путь значений в кластере: рендер платформы → слои AddonValue и
# AddonTemplate → слой заказа, исполненный с переменными заказа (как addons-operator)
# → рендер экземпляра со слоями заказа и площадки → тело Addon из AddonTemplate.
#
# Шаги: helm lint; golden-сверка каждого рендера; отрицательные пробы значений (у
# каждой законный близнец — базовый рендер того же шага); проверки формы (lib.py);
# kubeconform по схемам из закреплённых CRD (каталог $SCHEMAS, его собирает
# schemas.sh); нет проверок здоровья Lua.
#
# Окружение: SCHEMAS — каталог схем kubeconform (обязателен); UPDATE_GOLDEN=1 —
# переписать golden (дифф — в выводе); OUT — каталог вывода (по умолчанию
# charts/tests/out). Код 0 — всё зелёное, 1 — есть красное.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
T="$ROOT/charts/tests"
OUT="${OUT:-$T/out}"
UPDATE="${UPDATE_GOLDEN:-0}"
: "${SCHEMAS:?SCHEMAS — каталог схем kubeconform (charts/tests/schemas.sh)}"
rm -rf "$OUT"; mkdir -p "$OUT/layers"

pass=0; fail=0
ok()  { pass=$((pass + 1)); echo "PASS $*"; }
bad() { fail=$((fail + 1)); echo "FAIL $*"; }
step() { # step <имя> <команда…> — зелёный по коду 0
  local name="$1"; shift
  if "$@" > "$OUT/$name.log" 2>&1; then ok "$name"; else bad "$name"; sed 's/^/  | /' "$OUT/$name.log"; fi
}
py() { python3 "$T/lib.py" "$@"; }

PLATFORM="$ROOT/charts/registry-platform"
INSTANCE="$ROOT/charts/registry-instance"
HARNESS="$T/harness"
pdef="$T/cases/platform-default.yaml"
pall="$T/cases/platform-all.yaml"

# 1. Платформа: lint и рендер по умолчанию и со всем включённым.
step lint-platform-default helm lint --strict --kube-version 1.30.0 "$PLATFORM" -f "$pdef"
step lint-platform-all helm lint --strict --kube-version 1.30.0 "$PLATFORM" -f "$pdef" -f "$pall"
render() { # render <имя> <команда…> — вывод в $OUT/<имя>.yaml; отказ — красное, прогон идёт дальше
  local name="$1"; shift
  if "$@" > "$OUT/$name.yaml" 2> "$OUT/$name.err"; then ok "render-$name"; else bad "render-$name"; sed 's/^/  | /' "$OUT/$name.err"; fi
}
render platform-default helm template registry-platform "$PLATFORM" --kube-version 1.30.0 -f "$pdef"
render platform-all helm template registry-platform "$PLATFORM" --kube-version 1.30.0 -f "$pdef" -f "$pall"
step layers py layers "$OUT/platform-default.yaml" "$OUT/layers"

# Слой площадки — как его прочтёт addons-operator.
cp "$OUT/layers/site.layer" "$OUT/layers/site.yaml" 2>/dev/null || : > "$OUT/layers/site.yaml"

harness() { # harness <текст> <вход проводника> <выход>
  if helm template h "$HARNESS" --show-only templates/render.yaml --set-file "text=$1" -f "$2" > "$3.raw" 2> "$3.err"; then
    py strip "$3.raw" "$3"
  else
    bad "harness $(basename "$3")"; sed 's/^/  | /' "$3.err"; : > "$3"
  fi
}

# 2. Заказы: слой заказа с переменными → рендер экземпляра → тело Addon.
# Строка: <заказ> <доп. слой площадки или -> <ожидание проб kubelet>
instances=(
  "abc123 - false"
  "def456 $T/cases/site-kubelet-probes.yaml true"
)
for row in "${instances[@]}"; do
  read -r order extra probes <<<"$row"
  py vars "$T/orders/$order.yaml" "$OUT/layers/$order.vars.yaml" || bad "vars $order"
  harness "$OUT/layers/order.layer" "$OUT/layers/$order.vars.yaml" "$OUT/layers/$order.order.yaml"
  site=(-f "$OUT/layers/site.yaml"); [ "$extra" = - ] || site+=(-f "$extra")
  step "lint-instance-$order" helm lint --strict --kube-version 1.30.0 "$INSTANCE" -f "$OUT/layers/$order.order.yaml" "${site[@]}"
  render "instance-$order" helm template "zot-$order" "$INSTANCE" --kube-version 1.30.0 -f "$OUT/layers/$order.order.yaml" "${site[@]}"
  merged="$OUT/layers/$order.site.yaml"
  if [ "$extra" = - ]; then cp "$OUT/layers/site.yaml" "$merged"; else
    python3 - "$OUT/layers/site.yaml" "$extra" "$merged" <<'EOF'
import sys, yaml
def merge(a, b):
    for k, v in b.items():
        a[k] = merge(a.get(k, {}), v) if isinstance(v, dict) and isinstance(a.get(k), dict) else v
    return a
base = yaml.safe_load(open(sys.argv[1])); over = yaml.safe_load(open(sys.argv[2]))
yaml.safe_dump(merge(base, over), open(sys.argv[3], "w"))
EOF
  fi
  step "probe-instance-$order" py probe-instance "$OUT/instance-$order.yaml" "$merged" "$OUT/layers/$order.vars.yaml" "$probes"
  harness "$OUT/layers/addontemplate-1.0.0.tmpl" "$OUT/layers/$order.vars.yaml" "$OUT/layers/$order.addon-spec.yaml"
  if py wrap-addon "$OUT/layers/$order.addon-spec.yaml" "reg-$order" "$OUT/addon-$order.yaml" 2> "$OUT/addon-$order.err"; then
    ok "render-addon-$order"
  else
    bad "render-addon-$order"; sed 's/^/  | /' "$OUT/addon-$order.err"
  fi
done

# 3. Golden: каждый рендер побайтно против записанного.
for f in platform-default platform-all instance-abc123 instance-def456 addon-abc123 addon-def456; do
  g="$T/golden/$f.yaml"
  if [ "$UPDATE" = 1 ]; then
    cp "$OUT/$f.yaml" "$g" 2>/dev/null && ok "golden-$f (rewritten)" || bad "golden-$f: no render"
  elif [ -f "$g" ] && diff -u "$g" "$OUT/$f.yaml" > "$OUT/golden-$f.diff"; then
    ok "golden-$f"
  else
    bad "golden-$f"; sed 's/^/  | /' "$OUT/golden-$f.diff" 2>/dev/null | head -80 || echo "  | golden $g is missing"
  fi
done

# 4. Отрицательные пробы: рендер обязан отказать с ожидаемым текстом. Близнец каждой —
# базовый рендер того же чарта выше (зелёный); проба меняет ровно один факт.
negative() { # negative <файл> <команда рендера…>
  local file="$1"; shift
  local expect; expect="$(sed -n 's/^# expect: //p' "$file")"
  [ -n "$expect" ] || { bad "negative $(basename "$file"): no «# expect:» line"; return; }
  if "$@" -f "$file" > "$OUT/neg.out" 2> "$OUT/neg.err"; then
    bad "negative $(basename "$file" .yaml): rendered, expected refusal «$expect»"
  elif grep -qF -- "$expect" "$OUT/neg.err"; then
    ok "negative $(basename "$file" .yaml)"
  else
    bad "negative $(basename "$file" .yaml): refused for another reason"; sed 's/^/  | /' "$OUT/neg.err"
  fi
}
n=0
for f in "$T"/negative/registry-instance/*.yaml; do
  n=$((n + 1))
  negative "$f" helm template zot-abc123 "$INSTANCE" --kube-version 1.30.0 -f "$OUT/layers/abc123.order.yaml" -f "$OUT/layers/site.yaml"
done
for f in "$T"/negative/registry-platform/*.yaml; do
  n=$((n + 1))
  if grep -q '^# base: all' "$f"; then
    negative "$f" helm template registry-platform "$PLATFORM" --kube-version 1.30.0 -f "$pdef" -f "$pall"
  else
    negative "$f" helm template registry-platform "$PLATFORM" --kube-version 1.30.0 -f "$pdef"
  fi
done
# Цепочка: переменная заказа вне формы доходит до схемы экземпляра и получает отказ.
for f in "$T"/negative/order/*.yaml; do
  n=$((n + 1))
  expect="$(sed -n 's/^# expect: //p' "$f")"
  python3 - "$T/orders/abc123.yaml" "$f" "$OUT/layers/neg-order.yaml" <<'EOF'
import sys, yaml
o = yaml.safe_load(open(sys.argv[1])); o.update({k: v for k, v in yaml.safe_load(open(sys.argv[2])).items()})
yaml.safe_dump(o, open(sys.argv[3], "w"))
EOF
  py vars "$OUT/layers/neg-order.yaml" "$OUT/layers/neg.vars.yaml" || bad "vars $(basename "$f")"
  harness "$OUT/layers/order.layer" "$OUT/layers/neg.vars.yaml" "$OUT/layers/neg.order.yaml"
  negative "$f" helm template zot-abc123 "$INSTANCE" --kube-version 1.30.0 -f "$OUT/layers/site.yaml" -f "$OUT/layers/neg.order.yaml"
done
[ "$n" -gt 0 ] || bad "negative: no negative probes found"

# 5. Форма платформы.
step probe-platform-default py probe-platform "$OUT/platform-default.yaml" default
step probe-platform-all py probe-platform "$OUT/platform-all.yaml" all

# 6. Схемы: рендер против закреплённых CRD; неизвестный вид — отказ.
step kubeconform kubeconform -strict -summary -kubernetes-version 1.30.0 \
  -schema-location default -schema-location "$SCHEMAS/{{.Group}}-{{.ResourceKind}}_{{.ResourceAPIVersion}}.json" \
  "$OUT/platform-default.yaml" "$OUT/platform-all.yaml" "$OUT/instance-abc123.yaml" "$OUT/instance-def456.yaml" \
  "$OUT/addon-abc123.yaml" "$OUT/addon-def456.yaml"

# 7. Нет проверок здоровья Lua ни в исходниках чартов, ни в рендере.
step no-lua py no-lua "$PLATFORM" "$INSTANCE" "$OUT/platform-default.yaml" "$OUT/platform-all.yaml"

echo "charts: passed $pass, failed $fail"
[ "$fail" -eq 0 ] && [ "$pass" -gt 0 ]
