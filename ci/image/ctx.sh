#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# Контекст сборки образа заданий CI (#70) и его тег — один код для GitLab (ci/gitlab/ci-image-ref.sh)
# и GitHub (.github/workflows/ci-image.yml ветки ci-image, образ docker.io/prorobotech/
# registry-operator:ci-<тег>): та же сумма — тот же вход, образ не пересобирается.
#   ctx.sh build <каталог> — собрать контекст из дерева (корень — текущий каталог) и напечатать тег;
#   ctx.sh tag <каталог>   — тег готового контекста (ветка ci-image несёт его файлами).
# Контекст — ровно входы образа: Dockerfile, build-args (HELM_VERSION конвейера) и src/ — go.mod,
# go.sum, Makefile, hack/crds.lock, hack/fetch-crds.sh, charts/tests/crds.sh, ci/image/tools.sh,
# ci/gitlab/net-retry.sh, ci/gitlab/apt-mirror.sh, ci/gitlab/pip-requirements.txt,
# ci/gitlab/pip-fetch.py и ci/image/pins.env — ревизия kaname (images.kaname.revision в
# ci/gitlab/mirror.yaml) и файл CRD Istio (crds.istio в ci/gitlab/kind-full/pins.yaml), выписанные
# отсюда: сами файлы пинов в контекст не идут — образ публичный, а они называют внутренние адреса.
# Чартов площадки в образе нет: их берёт задание kind-full (kf_chart). Тег — sha256 перечня сумм
# файлов контекста, 24 знака. Коды: 0 — тег напечатан; 1 — пин не прочитан либо вход не найден;
# 64 — вызов.
set -euo pipefail
tag_of() { (cd "$1" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | cut -c1-24); }
case "${1:-}:${2:-}" in
  tag:?*) [ -s "$2/Dockerfile" ] || { echo "ctx.sh: в $2 нет Dockerfile — контекста нет" >&2; exit 1; }; tag_of "$2"; exit 0 ;;
  build:?*) ctx=$2 ;;
  *) echo "ctx.sh: вызов — build <каталог> | tag <каталог>" >&2; exit 64 ;;
esac
: "${HELM_VERSION:?}"
# Ревизия kaname — поле revision записи «- name: kaname» раздела images (как mirror.py env).
kaname_ref=$(awk '/^images:/{s=1;next} /^[a-z]/{s=0} s&&/^- name:/{k=($3=="kaname")} s&&k&&/^  revision:/{print $2; exit}' ci/gitlab/mirror.yaml)
istio_url=$(awk '/^crds:/{c=1;next} /^[a-z]/{c=0} c&&/^  istio:/{i=1;next} c&&/^  [a-z]/{i=0} c&&i&&/^    url:/{print $2; exit}' ci/gitlab/kind-full/pins.yaml)
istio_sum=$(awk '/^crds:/{c=1;next} /^[a-z]/{c=0} c&&/^  istio:/{i=1;next} c&&/^  [a-z]/{i=0} c&&i&&/^    sha256:/{print $2; exit}' ci/gitlab/kind-full/pins.yaml)
[[ "$kaname_ref" =~ ^[0-9a-f]{40}$ ]] || { echo "ctx.sh: ревизия kaname «$kaname_ref» из ci/gitlab/mirror.yaml — не полный SHA" >&2; exit 1; }
[[ "$istio_url" =~ ^https://[A-Za-z0-9./_-]+$ ]] || { echo "ctx.sh: адрес CRD Istio «$istio_url» из ci/gitlab/kind-full/pins.yaml — не https" >&2; exit 1; }
[[ "$istio_sum" =~ ^[0-9a-f]{64}$ ]] || { echo "ctx.sh: sha256 CRD Istio «$istio_sum» из ci/gitlab/kind-full/pins.yaml — не сумма" >&2; exit 1; }
rm -rf "$ctx"
mkdir -p "$ctx/src/hack" "$ctx/src/ci/gitlab" "$ctx/src/ci/image" "$ctx/src/charts/tests"
cp ci/image/Dockerfile "$ctx/Dockerfile"
cp go.mod go.sum Makefile "$ctx/src/"
cp hack/crds.lock hack/fetch-crds.sh "$ctx/src/hack/"
cp ci/gitlab/net-retry.sh ci/gitlab/apt-mirror.sh ci/gitlab/pip-requirements.txt ci/gitlab/pip-fetch.py "$ctx/src/ci/gitlab/"
cp ci/image/tools.sh "$ctx/src/ci/image/"
cp charts/tests/crds.sh "$ctx/src/charts/tests/"
printf 'KANAME_REF=%s\nISTIO_CRDS_URL=%s\nISTIO_CRDS_SHA256=%s\n' "$kaname_ref" "$istio_url" "$istio_sum" > "$ctx/src/ci/image/pins.env"
printf 'HELM_VERSION=%s\n' "$HELM_VERSION" > "$ctx/build-args"
tag_of "$ctx"
