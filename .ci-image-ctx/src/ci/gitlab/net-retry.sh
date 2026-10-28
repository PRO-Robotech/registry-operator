#!/bin/sh
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
# Внешняя загрузка с повтором по ТЕКСТУ отказа инструмента (#18). Один источник перечня для
# Dockerfile*, ci/gitlab/generated-inject.sh, ci/gitlab/images-build.sh, go_warm и docker_pull
# (ci/gitlab/environment.sh); оболочка POSIX — её исполняют dash сборщика, bash и busybox kaniko.
#
#   net-retry.sh <попыток> <срок попытки, с> <пауза, с> <команда…>
#     Сетевой отказ — повтор после паузы (пауза × номер попытки); исчерпав попытки — строка
#     «net-retry: NOT EXECUTED» и код 75: вердикта о дереве нет, задание повторит GitLab
#     (retry exit_codes [75]). Отказ не сетевой — без повтора, код инструмента: красное.
#   net-retry.sh --is-net <код> <журнал>
#     0 — отказ сетевой, 1 — нет.
#   net-retry.sh --verdict <код> <журнал сборки>
#     Код сборки (kaniko, docker build), которая сама кодов RUN не передаёт: сборку остановил
#     net-retry с кодом 75 (строка NOT EXECUTED в журнале) либо загрузка самого kaniko упала по
#     сети (строка «error building image:» с текстом сети) — 75; иначе код сборки.
#
# Сетевой отказ — текст из NET (pipeline 350968: «curl 56 Recv failure: Connection reset by
# peer» при установке controller-gen, «dial tcp …:443: i/o timeout» proxy.golang.org в kaniko;
# pipeline 350156: «stream error … INTERNAL_ERROR»; задания 1247915–1247916: «TLS handshake
# timeout», «context deadline exceeded»; pipeline 350974 и 350981: «Failed to connect to
# go.googlesource.com port 443 after 130720 ms: Couldn't connect to server», «GnuTLS recv error
# (-110): The TLS connection was non-properly terminated.»; curl 8 образа golang:1.26.10 — «…:
# Could not connect to server», опыт K0 check-verifier; конвейер 350996, задание 1248202 —
# containerd узла kind: «rpc error: code = DeadlineExceeded … net/http: timeout awaiting response
# headers») либо срок попытки (timeout: 124) и
# снятие сигналом KILL (137: добивание timeout -k либо OOM — печатается «прервано сигналом», а
# не «сеть»). Текст «не сеть» (NOT_NET) сильнее и текста сети, и срока попытки: неверная сумма,
# нет версии, нет манифеста; «no such host» — NXDOMAIN, детерминированный дефект имени в дереве
# (временный сбой DNS Go пишет «server misbehaving» либо «i/o timeout» — они в NET); ответ
# прокси «нет такого» (404 Not Found, 410 Gone, «server response: not found») — сетевые слова в
# теле такого ответа сети не делают. Код 75 значит только «сеть»: 75 самого инструмента без
# текста сети — 1.
#
# GOPROXY с «|» (окружение раннера: <зеркало>|proxy.golang.org|direct; умолчание environment.sh —
# proxy.golang.org|direct): go уходит дальше по перечню
# и в direct на ЛЮБОМ отказе прокси, и журнал показывает лишь отказ прямого пути (go.googlesource.com). Исчерпав попытки на
# команде go (строка «go: » в журнале), net-retry запускает её ещё раз только через первый
# прокси перечня и печатает его ответ строками «net-retry: прокси |»: прошло — итог 0 (сбоил
# лишь прямой путь); нет — 75 и строка «прокси … сбоил сам» либо «ответил отказом не по сети».
#
# Вывод попытки — в памяти, без файла (#70, конвейер 351650): на полном диске прежний журнал в
# /tmp терял текст отказа, и сеть читалась «не сетью». Попытка с кодом не 0 печатает за выводом
# место и inode диска (строки «net-retry: диск |»: df -Pk /tmp и каталога задания, df -Pi /tmp).
# Текст «no space left on device» (ENOSPC) — «диск узла заполнен»: код 75 и строка NOT EXECUTED
# без повтора, лишь после строк df — без них причина не названа. --is-net читает журнал и с stdin
# (аргумент «-»).
set -u
NET='dial tcp|i/o timeout|connection reset|connection refused|connection timed out|tls handshake timeout|curl[: (]+56|recv failure|stream error|internal_error|context deadline exceeded|code = deadlineexceeded|timeout awaiting response headers|client\.timeout exceeded|server misbehaving|network is unreachable|unexpected eof|bad gateway|service unavailable|gateway timeout|toomanyrequests|too many requests|couldn.t connect to server|could not connect to server|gnutls recv error|non-properly terminated'
NOT_NET='checksum mismatch|security error|unknown revision|missing go\.sum entry|manifest unknown|repository does not exist|no matching manifest|no such host|404 not found|410 gone|server response: not found'
MARK='net-retry: NOT EXECUTED'

ENOSPC='no space left on device'

# is_net <код> <текст> — 0: отказ сетевой, 1: нет.
is_net() {
  [ "$1" = 0 ] && return 1
  printf '%s\n' "$2" | grep -Eiq -- "$NOT_NET" && return 1
  case "$1" in 124|137) return 0 ;; esac
  printf '%s\n' "$2" | grep -Eiq -- "$NET"
}

# disk — место и inode диска задания строками «net-retry: диск |»; вопрос не прошёл — его строка.
disk() {
  { df -Pk /tmp ${CI_PROJECT_DIR:+"$CI_PROJECT_DIR"}; df -Pi /tmp; } 2>&1 | sed 's/^/net-retry: диск | /' >&2
}

# why <код> — чем была попытка, засчитанная «не выполнилось».
why() {
  case "$1" in
    124) echo "срок попытки ${limit:-?} с исчерпан (код 124)" ;;
    137) echo "прервано сигналом KILL (код 137: добивание по сроку либо OOM)" ;;
    *) echo "сетевой отказ (код $1)" ;;
  esac
}

number() { case "$1" in ''|*[!0-9]*) return 1 ;; esac; }

# via_proxy <команда…> — проверочный запуск только через первый прокси GOPROXY с «|»: 0 —
# прошёл, 1 — нет либо не применим. Учётные данные в адресе прокси не печатаются.
via_proxy() {
  case "${GOPROXY:-}" in *'|'*) ;; *) return 1 ;; esac
  printf '%s\n' "$out" | grep -Eq '^[[:space:]]*go: ' || return 1
  first=$(printf '%s\n' "$GOPROXY" | tr ',|' '\n\n' | grep -v -x -e direct -e off -e '' | head -n 1)
  [ -n "$first" ] || return 1
  shown=$(printf '%s\n' "$first" | sed 's#://[^/@]*@#://#')
  plimit=$limit
  [ "$plimit" -le 300 ] || plimit=300
  echo "net-retry: GOPROXY с «|» — go уходит в direct на любом отказе прокси, отказ выше может быть отказом прямого пути; проверочный запуск только через прокси $shown (срок $plimit с):" >&2
  prc=0
  pout=$(GOPROXY=$first timeout -k 10 "$plimit" "$@" 2>&1) || prc=$?
  printf '%s\n' "$pout" | tail -n 20 | sed 's/^/net-retry: прокси | /' >&2
  if [ "$prc" = 0 ]; then
    echo "net-retry: через прокси $shown — успех: сбоил лишь прямой путь, итог 0" >&2
    return 0
  fi
  if is_net "$prc" "$pout"; then
    echo "net-retry: прокси $shown сбоил сам: $(why "$prc")" >&2
  else
    echo "net-retry: прокси $shown ответил отказом не по сети (код $prc); прямой путь — по сети" >&2
  fi
  return 1
}

case "${1:-}" in
  --is-net)
    [ "$#" = 3 ] && number "$2" && { [ "$3" = - ] || [ -f "$3" ]; } || { echo "net-retry: --is-net <код> <журнал | ->" >&2; exit 64; }
    is_net "$2" "$(cat -- "$3")"
    exit $?
    ;;
  --verdict)
    [ "$#" = 3 ] && number "$2" && [ -f "$3" ] || { echo "net-retry: --verdict <код> <журнал>" >&2; exit 64; }
    [ "$2" = 0 ] && exit 0
    if grep -qF "$MARK" "$3"; then
      echo "net-retry: сборку остановил сетевой отказ загрузки (строка NOT EXECUTED) — код 75, вердикта о дереве нет" >&2
      exit 75
    fi
    own=$(grep -E '^error building image:' "$3")
    if [ -n "$own" ] && is_net 1 "$own"; then
      echo "net-retry: загрузка самого сборщика упала по сети — код 75, вердикта о дереве нет" >&2
      exit 75
    fi
    [ "$2" = 75 ] && exit 1
    exit "$2"
    ;;
esac

[ "$#" -ge 4 ] && number "$1" && number "$2" && number "$3" && [ "$1" -ge 1 ] ||
  { echo "net-retry: <попыток> <срок попытки, с> <пауза, с> <команда…>" >&2; exit 64; }
attempts=$1 limit=$2 pause=$3
shift 3
i=1
while :; do
  rc=0
  out=$(timeout -k 10 "$limit" "$@" 2>&1) || rc=$?
  [ -z "$out" ] || printf '%s\n' "$out"
  [ "$rc" = 0 ] && exit 0
  disk
  if printf '%s\n' "$out" | grep -Fiq -- "$ENOSPC"; then
    echo "$MARK — «$*»: диск узла заполнен (no space left on device, код $rc; место и inode — строки «net-retry: диск |» выше) — вердикта о дереве нет, код 75" >&2
    exit 75
  fi
  if ! is_net "$rc" "$out"; then
    echo "net-retry: «$*» — отказ не сетевой (код $rc): красное, без повтора" >&2
    [ "$rc" = 75 ] && exit 1
    exit "$rc"
  fi
  echo "net-retry: «$*» — $(why "$rc"), попытка $i из $attempts" >&2
  [ "$i" -lt "$attempts" ] || break
  sleep $((pause * i))
  i=$((i + 1))
done
via_proxy "$@" && exit 0
echo "$MARK — «$*»: ни одна из $attempts попыток не дала вердикта (сеть, срок или сигнал), код 75 (вердикта о дереве нет)" >&2
exit 75
