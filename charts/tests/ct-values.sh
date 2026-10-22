#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# ct-values.sh — входы ct lint (ci/*-values.yaml каждого чарта) из тех же случаев, что у
# run.sh: значения не дублируются в дереве. Экземпляр получает слой заказа abc123,
# исполненный проводником, и слой площадки из рендера платформы.
set -euo pipefail
T="$(cd "$(dirname "$0")" && pwd)"; C="$(dirname "$T")"
w="$(mktemp -d)"
mkdir -p "$C/registry-platform/ci" "$C/registry-instance/ci"
python3 - "$T/cases/platform-default.yaml" "$T/cases/platform-all.yaml" "$C/registry-platform/ci" <<'PY'
import sys, yaml
def merge(a, b):
    for k, v in b.items():
        a[k] = merge(a.get(k, {}), v) if isinstance(v, dict) and isinstance(a.get(k), dict) else v
    return a
d = yaml.safe_load(open(sys.argv[1])); a = yaml.safe_load(open(sys.argv[2]))
yaml.safe_dump(d, open(sys.argv[3] + "/default-values.yaml", "w"))
yaml.safe_dump(merge(yaml.safe_load(open(sys.argv[1])), a), open(sys.argv[3] + "/all-values.yaml", "w"))
PY
helm template registry-platform "$C/registry-platform" -f "$T/cases/platform-default.yaml" > "$w/p.yaml"
python3 "$T/lib.py" layers "$w/p.yaml" "$w" > /dev/null
python3 "$T/lib.py" vars "$T/orders/abc123.yaml" "$w/v.yaml"
helm template h "$T/harness" --show-only templates/render.yaml --set-file "text=$w/order.layer" -f "$w/v.yaml" > "$w/o.raw"
python3 "$T/lib.py" strip "$w/o.raw" "$w/o.yaml"
python3 - "$w/o.yaml" "$w/site.layer" "$C/registry-instance/ci/abc123-values.yaml" <<'PY'
import sys, yaml
o = yaml.safe_load(open(sys.argv[1])); o.update(yaml.safe_load(open(sys.argv[2])))
yaml.safe_dump(o, open(sys.argv[3], "w"))
PY
rm -rf "$w"
