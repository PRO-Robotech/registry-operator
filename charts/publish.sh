#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# publish.sh <тег> — публикация чарта OCI в ghcr.io/pro-robotech/charts из CI.
# Тег — <чарт>-<версия>; версия обязана совпасть с Chart.yaml. Опубликованная версия
# неизменяема: если она уже есть в реестре, публикация отказывает (новая версия — новый
# тег), поэтому digest, закреплённый в AddonTemplate, не меняется под ним. Digest
# публикации печатается в итог задания — его и закрепляют в instanceChart.pins.
# Окружение: ACTOR, TOKEN (GITHUB_TOKEN с packages: write), RUNNER_TEMP, GITHUB_STEP_SUMMARY.
set -euo pipefail
ref="${1:?тег}"
case "$ref" in
  registry-instance-*) chart=registry-instance ;;
  registry-platform-*) chart=registry-platform ;;
  *) echo "publish: tag $ref names no chart" >&2; exit 64 ;;
esac
version="${ref#"$chart"-}"
root="$(cd "$(dirname "$0")" && pwd)"
declared="$(sed -n 's/^version: //p' "$root/$chart/Chart.yaml")"
[ "$declared" = "$version" ] || { echo "publish: tag version $version, Chart.yaml version $declared" >&2; exit 1; }
repo=oci://ghcr.io/pro-robotech/charts
printf '%s' "$TOKEN" | helm registry login ghcr.io -u "$ACTOR" --password-stdin
if out="$(helm show chart "$repo/$chart" --version "$version" 2>&1)"; then
  echo "publish: $chart $version is already published; published versions are immutable — bump the version" >&2
  exit 1
elif ! grep -qiE 'not found|manifest unknown|name unknown' <<<"$out"; then
  echo "publish: cannot tell whether $chart $version exists, refusing: $out" >&2
  exit 1
fi
pkg="$RUNNER_TEMP/pkg"; mkdir -p "$pkg"
helm package "$root/$chart" -d "$pkg"
out="$(helm push "$pkg/$chart-$version.tgz" "$repo" 2>&1)"
echo "$out"
digest="$(sed -n 's/^Digest: //p' <<<"$out")"
[[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || { echo "publish: no digest in helm push output" >&2; exit 1; }
echo "published \`$repo/$chart:$version@$digest\`" >> "$GITHUB_STEP_SUMMARY"
