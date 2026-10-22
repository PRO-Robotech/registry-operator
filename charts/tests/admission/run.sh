#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# run.sh — пробы политик допуска V1–V15 на кластере, куда смотрит kubectl (kind в CI):
# CRD видов с закреплённых ревизий, namespace примера, рендер registry-platform с
# включёнными политиками, затем probe.py. Код — код probe.py.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
T="$ROOT/charts/tests"
CRDS="${CRDS:?CRDS — каталог CRD (charts/tests/crds.sh)}"
for f in "$CRDS"/*.yaml; do kubectl create -f "$f" >/dev/null; done
if [ -d "$ROOT/config/crd/bases" ]; then
  for f in "$ROOT"/config/crd/bases/*.yaml; do kubectl create -f "$f" >/dev/null; done
fi
kubectl wait --for=condition=Established crd --all --timeout=120s >/dev/null
for ns in registry-system argocd registry-edge cust02; do kubectl create namespace "$ns" >/dev/null; done
kubectl create namespace cust01 >/dev/null
kubectl label namespace cust01 registry.in-cloud.io/tenant=true >/dev/null
kubectl create clusterrolebinding probe-authors --clusterrole=cluster-admin --group=system:authenticated >/dev/null
helm template registry-platform "$ROOT/charts/registry-platform" \
  -f "$T/cases/platform-default.yaml" -f "$T/cases/platform-all.yaml" -f "$T/admission/values-probe.yaml" \
  --show-only templates/admission.yaml > "$T/out-admission.yaml"
kubectl create -f "$T/out-admission.yaml" >/dev/null
echo "admission: $(grep -c '^kind: ValidatingAdmissionPolicy$' "$T/out-admission.yaml") policies applied"
python3 "$T/admission/probe.py" "$T/admission/cases.yaml"
