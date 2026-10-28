#!/bin/sh
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# apt-mirror.sh — источники apt Debian с deb.debian.org на зеркало Яндекса (#70). Замер из пода
# раннера CI 2026-10-28 (под todo-netprobe): Packages.xz bookworm main 8,8 МБ —
# deb.debian.org 1,34–1,35 с (6,2 МБ/с), mirror.yandex.ru 0,19–0,24 с (35–45 МБ/с); раньше
# deb.debian.org с раннеров и вовсе не соединялся (pipeline 350102, 350103). Целостность — прежняя:
# Release проверяется подписью по ключу Signed-By источника, файлы — суммами из Release, и зеркало
# этого не меняет. Схема — https при хранилище доверия в образе, иначе http (подпись Release та же).
# Зовут ci/image/Dockerfile (до apt-get update) и ci/gitlab/bootstrap.sh (apt лишь за недостающим).
# Коды: 0 — источники на зеркале (строка «apt-mirror: …» с числом файлов); 1 — файла источников
# с deb.debian.org нет и на зеркале нет ни одного: подменять нечего — образ другой, а не «готово».
set -eu
if [ -s /etc/ssl/certs/ca-certificates.crt ]; then scheme=https; else scheme=http; fi
mirror="$scheme://mirror.yandex.ru"
n=0
for f in /etc/apt/sources.list /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/*.sources; do
  [ -f "$f" ] || continue
  if grep -Eq 'https?://deb\.debian\.org/debian(-security)?' "$f"; then
    sed -i -E "s#https?://deb\.debian\.org/debian#$mirror/debian#g" "$f"
  fi
  if grep -q "://mirror\.yandex\.ru/debian" "$f"; then n=$((n + 1)); fi
done
if [ "$n" = 0 ]; then
  echo "apt-mirror: ни в одном файле источников apt нет deb.debian.org и mirror.yandex.ru — подменять нечего" >&2
  exit 1
fi
echo "apt-mirror: источники apt на $mirror — файлов $n"
