#!/usr/bin/env python3
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
"""pip-fetch.py <файл> <sha256> <куда> — файл пакета из индекса окружения задания, сверенный по sha256 (#70).

Нужен там, где pip ещё нет: колесо самого pip для venv без pip (ci/image/Dockerfile,
ci/gitlab/bootstrap.sh). Индексы — те же, что читает pip: PIP_INDEX_URL (умолчание
https://pypi.org/simple), затем PIP_EXTRA_INDEX_URL через пробел; зеркало отдаёт раннер окружением
(решение диспетчера 2026-10-28), адреса зеркала в дереве нет. Страница проекта — PEP 503; первый
индекс, где ссылка на файл есть, даёт файл. Сумма не сошлась — красное сразу, без следующего
индекса: подмену файла не обходят. Адрес индекса может нести учётку — в журнал идёт лишь узел.

Коды: 0 — файл записан; 1 — файла нет ни в одном индексе либо сумма не сошлась; 75 — индекс не
ответил по сети, а в прочих файла нет (вердикта нет; строка «dial tcp» — сеть для net-retry.sh);
2 — вызов не по форме.
"""
import hashlib
import html.parser
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

ATTEMPTS = 3
TIMEOUT = 60


class Links(html.parser.HTMLParser):
    def __init__(self):
        super().__init__()
        self.hrefs = []

    def handle_starttag(self, tag, attrs):
        href = dict(attrs).get('href') if tag == 'a' else None
        if href:
            self.hrefs.append(href)


def host(url):
    return urllib.parse.urlsplit(url).hostname or '<без узла>'


def get(url, sink):
    """Тело ответа в sink; повтор лишь сетевого отказа. Исход: None — прочитано, 'net' — сеть, иначе HTTP-код."""
    for attempt in range(1, ATTEMPTS + 1):
        try:
            with urllib.request.urlopen(urllib.request.Request(url, headers={'Accept': 'text/html'}),
                                        timeout=TIMEOUT) as resp:
                sink(resp)
            return None
        except urllib.error.HTTPError as exc:
            if exc.code < 500:
                return exc.code
            reason = f'HTTP {exc.code}'
        except (urllib.error.URLError, OSError) as exc:
            reason = str(getattr(exc, 'reason', exc))
        print(f'pip-fetch: {host(url)} не ответил (dial tcp: {reason}), попытка {attempt} из {ATTEMPTS}',
              file=sys.stderr)
        if attempt < ATTEMPTS:
            time.sleep(5 * attempt)
    return 'net'


def main(argv):
    if len(argv) != 4 or not re.fullmatch(r'[0-9a-f]{64}', argv[2]):
        print('pip-fetch: вызов — pip-fetch.py <файл> <sha256> <куда>', file=sys.stderr)
        return 2
    name, want, out = argv[1:]
    project = re.sub(r'[-_.]+', '-', name.split('-', 1)[0]).lower()
    indexes = [os.environ.get('PIP_INDEX_URL') or 'https://pypi.org/simple']
    indexes += os.environ.get('PIP_EXTRA_INDEX_URL', '').split()
    net = False
    for index in indexes:
        page = {}

        def keep(resp, page=page):
            page['base'] = resp.geturl()
            page['body'] = resp.read().decode('utf-8', 'replace')

        outcome = get(index.rstrip('/') + f'/{project}/', keep)
        if outcome is not None:
            net = net or outcome == 'net'
            if outcome != 'net':
                print(f'pip-fetch: {host(index)} — HTTP {outcome} на странице {project}', file=sys.stderr)
            continue
        parser = Links()
        parser.feed(page['body'])
        url = next((urllib.parse.urldefrag(urllib.parse.urljoin(page['base'], h)).url for h in parser.hrefs
                    if urllib.parse.unquote(urllib.parse.urlsplit(h).path.rsplit('/', 1)[-1]) == name), None)
        if url is None:
            print(f'pip-fetch: в индексе {host(index)} файла {name} нет', file=sys.stderr)
            continue
        part = out + '.part'
        state = {}

        def save(resp, state=state):
            state['sum'] = hashlib.sha256()
            with open(part, 'wb') as f:
                for chunk in iter(lambda: resp.read(1 << 16), b''):
                    state['sum'].update(chunk)
                    f.write(chunk)

        outcome = get(url, save)
        got = state['sum'].hexdigest() if outcome is None else None
        if got != want and os.path.exists(part):
            os.unlink(part)
        if outcome is not None:
            net = net or outcome == 'net'
            print(f'pip-fetch: {host(url)} — {name} не получен ({outcome})', file=sys.stderr)
            continue
        if got != want:
            print(f'pip-fetch: {name} из {host(url)} — sha256 {got}, закреплён {want}: красное', file=sys.stderr)
            return 1
        os.replace(part, out)
        print(f'pip-fetch: {name} — из {host(url)}, sha256 сошёлся')
        return 0
    return 75 if net else 1


if __name__ == '__main__':
    sys.exit(main(sys.argv))
