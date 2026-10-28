# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# Образ заданий CI (#70): задания конвейера не качают из интернета ничего, кроме образов своего
# коммита (решение диспетчера 2026-10-28) — всё, что они иначе качали бы в bootstrap, go_warm и
# помощниками ci/gitlab/environment.sh, здесь: модули Go дерева (go mod download all), утилиты make
# (controller-gen, setup-envtest, golangci-lint, kind — версии Makefile) с бинариями envtest и CRD
# проб (hack/crds.lock), окружение Python с пакетами bootstrap, helm и yq, и ci/image/tools.sh —
# клиент Docker, kubectl, crane, kind, grpcurl и ct, kubeconform, zot, CRD чартов и Istio, исходник
# kaname с модулями (пины — ci/image/pins.env). Образ публичный: чартов площадки и файлов пинов с
# внутренними адресами в нём нет — контекст несёт лишь входы, выписанные ci/image/ctx.sh.
# Перечень ключей — /opt/regop/tools: задание берёт инструмент из образа по своей строке (image_tool,
# environment.sh); строки нет — красное, не загрузка. Контекст (этот файл, src/ — входы образа,
# build-args) и тег — sha256 контекста — считает ci/image/ctx.sh; образ публикует ветка ci-image
# GitHub (docker.io/prorobotech/registry-operator:ci-<тег>), а его нет — собирает задание ci-image
# (kaniko, ci/gitlab/ci-image.sh) в реестр проекта. Задания берут образ по digest.
# Внешняя загрузка — через ci/gitlab/net-retry.sh (#18): сеть после повторов — строка NOT
# EXECUTED и код 75, который ci-image.sh отдаёт заданию (net-retry.sh --verdict).
FROM golang:1.26.10@sha256:672c944cfd07599b5f1bb179869270cdc94c6254e41db4d431259693cb286b82
ARG HELM_VERSION
# Индексы модулей Go и пакетов pip — окружением раннера (решение диспетчера 2026-10-28): ci-image.sh
# передаёт GOPROXY, PIP_INDEX_URL и PIP_EXTRA_INDEX_URL задания аргументами сборки, если они заданы;
# иначе — штатные умолчания ниже (proxy.golang.org с «|direct» — переход на любом отказе, как у
# environment.sh; pypi.org). В тег образа не входят: go.sum, GOSUMDB и sha256 перечня pip делают
# содержимое независимым от индекса. ARG, а не ENV: в образе не остаётся (на образе — GOPROXY=off).
ARG GOPROXY=https://proxy.golang.org|direct
ARG PIP_INDEX_URL=https://pypi.org/simple
ARG PIP_EXTRA_INDEX_URL
ENV REGOP_CI_IMAGE_ROOT=/opt/regop
COPY src/ /src/
WORKDIR /src
# apt — с зеркала Яндекса (ci/gitlab/apt-mirror.sh: deb.debian.org → mirror.yandex.ru в источниках
# deb822; подпись Release сверяется тем же Signed-By). Замер из пода раннера 2026-10-28:
# Packages.xz 8,8 МБ — deb.debian.org 1,35 с (6,2 МБ/с), mirror.yandex.ru 0,2 с (35–45 МБ/с).
RUN sh ci/gitlab/apt-mirror.sh && \
    sh ci/gitlab/net-retry.sh 3 600 15 apt-get -o Acquire::Retries=5 update -qq && \
    sh ci/gitlab/net-retry.sh 3 600 15 apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
      python3 openssl ca-certificates make gcc libc6-dev git curl && \
    rm -rf /var/lib/apt/lists/*
# Python — как в bootstrap.sh: venv без pip, колесо pip и пакеты bootstrap (перечень
# ci/gitlab/pip-requirements.txt) — из индекса окружения (PIP_INDEX_URL выше), sha256 каждого файла.
RUN python3 -m venv --without-pip /opt/regop/venv && \
    sh ci/gitlab/net-retry.sh 3 300 15 python3 -I ci/gitlab/pip-fetch.py pip-24.3.1-py3-none-any.whl \
      3790624780082365f47549d032f3770eeb2b1e8bd1f7b2e02dace1afa361b4ed /tmp/pip-24.3.1-py3-none-any.whl && \
    /opt/regop/venv/bin/python /tmp/pip-24.3.1-py3-none-any.whl/pip install --disable-pip-version-check --no-index /tmp/pip-24.3.1-py3-none-any.whl && \
    sh ci/gitlab/net-retry.sh 3 600 15 /opt/regop/venv/bin/python -m pip install --disable-pip-version-check \
      --no-deps --require-hashes --only-binary=:all: -r ci/gitlab/pip-requirements.txt && \
    rm -f /tmp/pip-24.3.1-py3-none-any.whl
# helm — версия HELM_VERSION конвейера с зеркала Яндекса (выпуски GitHub helm/helm; замер из пода
# раннера 2026-10-28: 18,6 МБ — get.helm.sh 0,9–1,2 с, mirror.yandex.ru 0,3–0,6 с), отказ зеркала —
# штатный get.helm.sh; sha256 закреплён здесь (bootstrap сверяет по файлу суммы с get.helm.sh); yq —
# выпуск bootstrap, sha256 закреплён (зеркала выпусков mikefarah/yq нет).
RUN { [ "$HELM_VERSION" = v3.22.1 ] || { echo "ci/image/Dockerfile: sha256 helm закреплён для v3.22.1, а HELM_VERSION=$HELM_VERSION" >&2; exit 1; }; } && \
    { sh ci/gitlab/net-retry.sh 3 300 15 curl -fsSL --retry 5 --retry-all-errors -o /tmp/helm.tgz \
        "https://mirror.yandex.ru/mirrors/github/helm/helm/$HELM_VERSION/helm-linux-amd64.tar.gz" || \
      sh ci/gitlab/net-retry.sh 3 300 15 curl -fsSL --retry 5 --retry-all-errors -o /tmp/helm.tgz \
        "https://get.helm.sh/helm-$HELM_VERSION-linux-amd64.tar.gz"; } && \
    printf '896cf63684a42a723d61c980369d6e588330927deb13676ca0d85006dd6da0ea  /tmp/helm.tgz\n' | sha256sum -c - && \
    tar -xzf /tmp/helm.tgz -C /tmp && install /tmp/linux-amd64/helm /usr/local/bin/helm && \
    rm -rf /tmp/helm.tgz /tmp/linux-amd64 && \
    sh ci/gitlab/net-retry.sh 3 300 15 curl -fsSL --retry 5 --retry-all-errors -o /usr/local/bin/yq \
      https://github.com/mikefarah/yq/releases/download/v4.45.1/yq_linux_amd64 && \
    printf '654d2943ca1d3be2024089eb4f270f4070f491a0610481d128509b2834870049  /usr/local/bin/yq\n' | sha256sum -c - && \
    chmod 755 /usr/local/bin/yq
# Модули Go дерева — в GOMODCACHE образа (/go/pkg/mod), весь граф (all): go mod download и go mod
# tidy задания находят их при GOPROXY=off (environment.sh в образе).
RUN sh ci/gitlab/net-retry.sh 3 600 15 go mod download all
# Утилиты make, бинарии envtest и CRD проб — в /opt/regop/bin под теми же именами, что в LOCALBIN
# (bin/ дерева): bootstrap.sh копирует их в bin/ задания, и make их не пересобирает.
RUN printf 'regop-ci-tools: $(CONTROLLER_GEN) $(ENVTEST) $(GOLANGCI_LINT) $(KIND)\nregop-ci-envtest: $(ENVTEST)\n\t"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path\n' > /tmp/regop-ci.mk && \
    mkdir -p /opt/regop/bin && \
    sh ci/gitlab/net-retry.sh 3 900 15 make --no-print-directory -f Makefile -f /tmp/regop-ci.mk LOCALBIN=/opt/regop/bin regop-ci-tools && \
    sh ci/gitlab/net-retry.sh 3 600 15 make --no-print-directory -f Makefile -f /tmp/regop-ci.mk LOCALBIN=/opt/regop/bin regop-ci-envtest && \
    sh ci/gitlab/net-retry.sh 3 300 15 bash hack/fetch-crds.sh /opt/regop/bin/crds && \
    rm -f /tmp/regop-ci.mk
# Прочие инструменты заданий и перечень ключей /opt/regop/tools (ci/image/tools.sh); ready —
# последним: без него bootstrap считает образ неполным.
RUN bash ci/image/tools.sh && \
    rm -rf /root/.cache/go-build && \
    touch /opt/regop/ready
WORKDIR /
RUN rm -rf /src
