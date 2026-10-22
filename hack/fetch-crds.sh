#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# fetch-crds.sh — целевые CRD проб envtest по hack/crds.lock: каждый файл
# скачивается по закреплённой ревизии и сверяется по sha256. Расхождение —
# отказ: проба не идёт против CRD, которых никто не закреплял.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
dest="${1:-$root/bin/crds}"
mkdir -p "$dest"
fetched=0
while read -r name sum url; do
  [[ -z "${name}" || "${name}" == \#* ]] && continue
  out="$dest/$name"
  if [[ -f "$out" ]] && echo "$sum  $out" | sha256sum -c --status; then
    fetched=$((fetched + 1))
    continue
  fi
  tmp="$(mktemp "$dest/.fetch.XXXXXX")"
  trap 'rm -f "$tmp"' EXIT
  curl -fsSL --retry 3 -o "$tmp" "$url"
  if ! echo "$sum  $tmp" | sha256sum -c --status; then
    echo "fetch-crds: $name does not match its pinned sha256 ($url)" >&2
    exit 1
  fi
  mv "$tmp" "$out"
  trap - EXIT
  fetched=$((fetched + 1))
done < "$root/hack/crds.lock"
want=$(grep -cv '^\s*\(#\|$\)' "$root/hack/crds.lock")
if [[ "$fetched" -ne "$want" ]]; then
  echo "fetch-crds: $fetched of $want CRD files are in place" >&2
  exit 1
fi
echo "fetch-crds: $fetched of $want CRD files verified in $dest"
