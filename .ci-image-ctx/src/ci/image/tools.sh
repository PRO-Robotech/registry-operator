#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# Наполнение образа заданий CI (#70; решение диспетчера 2026-10-28: задания конвейера не качают из
# интернета ничего, кроме образов своего коммита). Всё, что задание иначе качало бы само
# помощниками ci/gitlab/environment.sh и скриптами групп, ставится здесь, один раз на вход образа:
# клиент Docker, kubectl, crane, kind, grpcurl и ct (go install), файлы ct, kubeconform и
# openapi2jsonschema, CRD чартов (charts/tests/crds.sh), zot, CRD Istio и исходник kaname
# закреплённой ревизии с его модулями Go (оба пина — ci/image/pins.env, его выписывает
# ci/image/ctx.sh из ci/gitlab/kind-full/pins.yaml и ci/gitlab/mirror.yaml). Чартов площадки в
# образе нет: образ публичный (docker.io), их берёт задание kind-full. Версии и sha256 — те же
# пины, что у помощников; каждая загрузка — под ci/gitlab/net-retry.sh (сеть после повторов — 75,
# сборщик отдаёт его заданию ci-image).
# Перечень ключей — $REGOP_CI_IMAGE_ROOT/tools, строка на инструмент: помощник задания (image_tool,
# environment.sh) берёт инструмент из образа ровно по своей строке; строки нет — красное, а не
# загрузка. Зовёт ci/image/Dockerfile из /src (контекст ci/image/ctx.sh).
set -euo pipefail
root=${REGOP_CI_IMAGE_ROOT:?}
tools="$root/tools"
share="$root/share"
pins=ci/image/pins.env
nr=ci/gitlab/net-retry.sh
mkdir -p "$share" "$root/src"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
pinned() { sed -n "s/^$1=//p" "$pins"; }
get() { sh "$nr" 3 300 15 curl -fsSL --retry 5 --retry-all-errors --connect-timeout 30 -o "$1" "$2"; }
pin() {
  printf '%s  %s\n' "$1" "$2" | sha256sum -c - > /dev/null ||
    { echo "ci/image/tools.sh: $2 — sha256 $(sha256sum < "$2" | cut -d' ' -f1), пин $1" >&2; exit 1; }
}
key() { printf '%s\n' "$*" >> "$tools"; echo "ci/image/tools.sh: в образе — $*"; }

# Поставленное прежними шагами Dockerfile — тоже строками перечня (bootstrap печатает его целиком).
"$root/venv/bin/python" -c 'import yaml, yamllint, yamale'
key pip pyyaml==6.0.2 yamllint==1.35.1 yamale==5.3.0
[ "$(helm version --template '{{.Version}}')" = "${HELM_VERSION:?}" ] || { echo "ci/image/tools.sh: helm не $HELM_VERSION" >&2; exit 1; }
key helm "$HELM_VERSION"
yq --version | grep -q ' v4\.45\.1$' || { echo "ci/image/tools.sh: yq не v4.45.1 — $(yq --version)" >&2; exit 1; }
key yq v4.45.1
key go-mod "$(sed -n 's/^module //p' go.mod)" all
for f in "$root"/bin/*; do key localbin "${f##*/}"; done

# Клиент Docker — тот же выпуск, что docker_cli_install (environment.sh), архив — с зеркала
# download.docker.com на mirror.yandex.ru (тот же путь, 82 МБ; на download.docker.com задание
# ci-image дважды падало под нагрузкой), отказ зеркала — штатный download.docker.com; бинарь
# сверяется по прежнему sha256. Пакет Debian trixie не годится: docker.io 26.1.5, а выпуск заданий
# и службы dind — 28.5.1 (ключ «docker 28.5.1»).
docker_path=linux/static/stable/x86_64/docker-28.5.1.tgz
get "$tmp/docker.tgz" "https://mirror.yandex.ru/mirrors/download.docker.com/$docker_path" ||
  get "$tmp/docker.tgz" "https://download.docker.com/$docker_path"
tar -xzf "$tmp/docker.tgz" -C "$tmp" docker/docker
pin 7b7c4d8415ffea0fc78913be750e42f98698af635ce5b3f26021052955a951f5 "$tmp/docker/docker"
install -m 755 "$tmp/docker/docker" /usr/local/bin/docker
key docker 28.5.1

get /usr/local/bin/kubectl https://dl.k8s.io/release/v1.34.0/bin/linux/amd64/kubectl
get "$tmp/kubectl.sha" https://dl.k8s.io/release/v1.34.0/bin/linux/amd64/kubectl.sha256
pin "$(cat "$tmp/kubectl.sha")" /usr/local/bin/kubectl
chmod 755 /usr/local/bin/kubectl
key kubectl v1.34.0

get "$tmp/crane.tgz" https://github.com/google/go-containerregistry/releases/download/v0.22.1/go-containerregistry_Linux_x86_64.tar.gz
pin 0ab7a1d6932a213aed964ce97666c3077fe691c8606413674a8b3e0b9ec4cda0 "$tmp/crane.tgz"
tar -xzf "$tmp/crane.tgz" -C /usr/local/bin crane
key crane v0.22.1

# go install — в GOPATH/bin образа (/go/bin): его ставит в PATH environment.sh задания.
for mod in sigs.k8s.io/kind@v0.33.0 github.com/fullstorydev/grpcurl/cmd/grpcurl@v1.9.3 github.com/helm/chart-testing/v3/ct@v3.13.0; do
  sh "$nr" 3 900 15 go install "$mod"
  key go-install "$mod"
done
mkdir -p /etc/ct
for f in chart_schema.yaml lintconf.yaml; do
  get "/etc/ct/$f" "https://raw.githubusercontent.com/helm/chart-testing/v3.13.0/etc/$f"
done
key ct-etc v3.13.0

get "$tmp/kc.tgz" https://github.com/yannh/kubeconform/releases/download/v0.7.0/kubeconform-linux-amd64.tar.gz
pin c31518ddd122663b3f3aa874cfe8178cb0988de944f29c74a0b9260920d115d3 "$tmp/kc.tgz"
tar -xzf "$tmp/kc.tgz" -C /usr/local/bin kubeconform
key kubeconform v0.7.0
get "$share/openapi2jsonschema.py" https://raw.githubusercontent.com/yannh/kubeconform/v0.7.0/scripts/openapi2jsonschema.py
pin d145babfbb765004030764e1b4e518bfb7a4bd7f111691a08fa57983b81881f3 "$share/openapi2jsonschema.py"
key openapi2jsonschema v0.7.0

# CRD чартов: charts/tests/crds.sh сверяет каждый файл по sha256 сам; скрипт — вход образа.
sh "$nr" 3 900 15 bash charts/tests/crds.sh "$share/chart-crds"
key chart-crds

get "$share/zot" https://github.com/project-zot/zot/releases/download/v2.1.20/zot-linux-amd64
pin a32e42d042d1f17b5b1317e55cc1a415a744c873dcd05c25c56b665478258bcb "$share/zot"
chmod 755 "$share/zot"
key zot v2.1.20

url=$(pinned ISTIO_CRDS_URL)
sum=$(pinned ISTIO_CRDS_SHA256)
get "$share/istio-crd-all.yaml" "$url"
pin "$sum" "$share/istio-crd-all.yaml"
key istio-crds "$sum"

# Исходник kaname закреплённой ревизии (git_fetch_rev групп kaname, kind-full,
# charts-kaname-manifest) — неглубокой выборкой коммита, и его модули Go. Без docs/: заданиям он не
# нужен (сборка kaname его не читает — .dockerignore kaname, пробы заданий его не открывают), а
# образ публичный — docs/ ни деревом, ни объектом git в него не идёт: частичная выборка без блобов
# (--filter=blob:none) и разреженное дерево без /docs/ — checkout догружает блобы лишь своих путей;
# сверка ниже — ни один блоб docs/ коммита не лежит в хранилище копии, рабочее дерево чисто.
ref=$(pinned KANAME_REF)
[[ "$ref" =~ ^[0-9a-f]{40}$ ]] || { echo "ci/image/tools.sh: ревизия kaname «$ref» из $pins — не полный SHA" >&2; exit 1; }
url=https://github.com/PRO-Robotech/kaname.git
dir="$root/src/$ref"
git init -q "$dir"
git -C "$dir" remote add origin "$url"
git -C "$dir" config core.sparseCheckout true
printf '/*\n!/docs/\n' > "$dir/.git/info/sparse-checkout"
sh "$nr" 3 600 15 git -C "$dir" fetch -q --depth 1 --filter=blob:none origin "$ref"
sh "$nr" 3 600 15 git -C "$dir" checkout -q --detach FETCH_HEAD
[ "$(git -C "$dir" rev-parse HEAD)" = "$ref" ] || { echo "ci/image/tools.sh: голова kaname не $ref" >&2; exit 1; }
[ ! -e "$dir/docs" ] || { echo "ci/image/tools.sh: в копии kaname есть docs/ — разреженное дерево не встало" >&2; exit 1; }
[ -z "$(git -C "$dir" status --porcelain --untracked-files=no)" ] || { echo "ci/image/tools.sh: копия kaname не чиста" >&2; exit 1; }
git -C "$dir" ls-tree -r --object-only HEAD -- docs | LC_ALL=C sort -u > "$tmp/kaname-docs"
git -C "$dir" cat-file --batch-all-objects --batch-check='%(objectname)' | LC_ALL=C sort -u > "$tmp/kaname-objects"
held=$(LC_ALL=C comm -12 "$tmp/kaname-docs" "$tmp/kaname-objects" | wc -l)
[ "$held" = 0 ] || { echo "ci/image/tools.sh: в хранилище копии kaname блобов docs/ $held — в публичный образ не идут" >&2; exit 1; }
echo "ci/image/tools.sh: kaname $ref — блобов docs/ в коммите $(wc -l < "$tmp/kaname-docs"), в копии 0"
(cd "$dir" && sh "$OLDPWD/$nr" 3 900 15 go mod download all)
key git "$url" "$ref"
