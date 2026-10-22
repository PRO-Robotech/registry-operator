#!/usr/bin/env python3
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
"""Проверяет метки инжектора на реальном Helm-рендере; каждый отказ имеет близнеца."""
import json
import pathlib
import subprocess
import sys
import tempfile

import yaml

chart, values, site = map(pathlib.Path, sys.argv[1:4])
passed = failed = 0
OMITTED = object()


def render(labels=OMITTED):
    with tempfile.TemporaryDirectory() as directory:
        overlay = pathlib.Path(directory) / 'values.json'
        data = {} if labels is OMITTED else {'site': {'vault': {'injectorLabels': labels}}}
        overlay.write_text(json.dumps(data))
        return subprocess.run(['helm', 'template', 'zot-abc123', str(chart), '-f', str(values),
                               '-f', str(site), '-f', str(overlay)], capture_output=True, text=True, check=False)


def case(name, probe):
    global passed, failed
    try:
        probe()
    except (AssertionError, StopIteration, KeyError, TypeError, yaml.YAMLError) as error:
        failed += 1
        print(f'FAIL injector-labels/{name}: {error}', flush=True)
    else:
        passed += 1
        print(f'PASS injector-labels/{name}', flush=True)


base = render()
if base.returncode:
    print(base.stderr, file=sys.stderr)
    sys.exit(base.returncode)
base_objects = list(yaml.safe_load_all(base.stdout))
base_pod = next(o for o in base_objects if o and o['kind'] == 'Deployment')['spec']['template']['metadata']['labels']


def positive(labels):
    result = render(labels)
    assert result.returncode == 0, result.stderr
    objects = list(yaml.safe_load_all(result.stdout))
    pod = next(o for o in objects if o and o['kind'] == 'Deployment')['spec']['template']['metadata']['labels']
    assert pod == {**base_pod, **labels}, 'Pod labels differ from the requested union'
    # Убираем только проверенное приращение: все остальные поля всех объектов неизменны.
    for key in labels:
        del pod[key]
    assert objects == base_objects, 'labels changed resources outside Pod metadata'
    if not labels:
        assert result.stdout == base.stdout, 'empty map changed default bytes'


def negative(labels, twin, reserved=None):
    result = render(labels)
    assert result.returncode != 0, 'invalid input rendered successfully'
    assert 'injectorLabels' in result.stderr, result.stderr
    if reserved:
        assert reserved in result.stderr, result.stderr
    positive(twin)


def null_default():
    result = render(None)
    assert result.returncode == 0, result.stderr
    assert result.stdout == base.stdout, 'Helm-normalized null map changed default bytes'


case('empty-default-byte-stable', lambda: positive({}))
case('null-map-default-byte-stable', null_default)
def captured_selector():
    positive({'in-cloud.io/vault-agent': 'enabled'})
    if len(sys.argv) < 5:
        return  # Постоянный набор проверяет контракт; отдельный опыт передаёт живой snapshot.
    snapshot = json.loads(pathlib.Path(sys.argv[4]).read_text())
    result = render({'in-cloud.io/vault-agent': 'enabled'})
    objects = list(yaml.safe_load_all(result.stdout))
    labels = next(o for o in objects if o and o['kind'] == 'Deployment')['spec']['template']['metadata']['labels']
    hooks = snapshot['webhooks']
    assert hooks, 'captured snapshot contains no webhooks'
    for hook in hooks:
        selector = hook['objectSelector']
        assert selector, 'captured object selector is empty'
        for key, value in selector.get('matchLabels', {}).items():
            assert labels.get(key) == value, (key, value)
        for expression in selector.get('matchExpressions', []):
            key, op = expression['key'], expression['operator']
            values = expression.get('values', [])
            assert op in ('In', 'NotIn', 'Exists', 'DoesNotExist'), op
            if op == 'In':
                assert key in labels and labels[key] in values, expression
            elif op == 'NotIn':
                assert key not in labels or labels[key] not in values, expression
            elif op == 'Exists':
                assert key in labels, expression
            else:
                assert key not in labels, expression


case('site-selector-required-label', captured_selector)
case('legal-empty-value', lambda: positive({'example.org/empty': ''}))
case('legal-uppercase-name-and-value', lambda: positive({'example.org/A_b.C-1': 'A_b.C-1'}))
case('legal-name-63', lambda: positive({'a' * 63: 'v' * 63}))
prefix253 = '.'.join(['a' * 63] * 3 + ['b' * 61])
case('legal-prefix-253', lambda: positive({prefix253 + '/x': 'v'}))
# Все собственные ключи, даже с прежним значением, запрещены в пользовательском слое.
assert len(base_pod) == 8, 'update collision inventory for changed built-in labels'
for key, value in base_pod.items():
    case('collision-equal/' + key, lambda key=key, value=value: negative({key: value}, {'example.org/custom': value}, key))
    case('collision-different/' + key, lambda key=key: negative({key: 'changed'}, {'example.org/custom': 'changed'}, key))
for name, bad, good in [
    ('empty-key', '', 'a'), ('name-64', 'a' * 64, 'a' * 63),
    ('leading-dash', '-a', 'a'), ('trailing-dash', 'a-', 'a'),
    ('empty-prefix', '/a', 'a'), ('empty-name', 'example.org/', 'example.org/a'),
    ('double-slash', 'example.org/a/b', 'example.org/a'),
    ('dns-empty-segment', 'a..b/c', 'a.b/c'),
    ('dns-leading-dash', '-a.b/c', 'a.b/c'), ('dns-trailing-dash', 'a-.b/c', 'a.b/c'),
    ('dns-uppercase', 'A.b/c', 'a.b/c'), ('dns-underscore', 'a_b/c', 'a-b/c'),
    ('dns-segment-64', 'a' * 64 + '.b/c', 'a' * 63 + '.b/c'),
    ('dns-prefix-254', prefix253 + 'c/x', prefix253 + '/x'),
]:
    case('invalid-key/' + name, lambda bad=bad, good=good: negative({bad: 'v'}, {good: 'v'}))
for name, bad, good in [('64', 'v' * 64, 'v' * 63), ('space', 'a b', 'a-b'),
                        ('leading-dash', '-v', 'v'), ('slash', 'a/b', 'a-b'),
                        ('boolean', True, 'true'), ('number', 3, '3'), ('null', None, '')]:
    case('invalid-value/' + name, lambda bad=bad, good=good: negative({'example.org/a': bad}, {'example.org/a': good}))
for name, value in [('list', ['label']), ('string', 'label'), ('boolean', True), ('number', 1)]:
    case('non-map/' + name, lambda value=value: negative(value, {}))
print(f'injector labels: passed {passed}, failed {failed}', flush=True)
sys.exit(1 if failed else 0)
