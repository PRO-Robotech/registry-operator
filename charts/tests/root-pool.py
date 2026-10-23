#!/usr/bin/env python3
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
"""Общий private root: реальные Helm-объекты, изоляция групп и защищённые метки."""
import json
import pathlib
import subprocess
import sys
import tempfile
import yaml

platform, instance, default, all_values, order, site = map(pathlib.Path, sys.argv[1:])
passed = failed = 0

def render(chart, files, overlay):
    with tempfile.TemporaryDirectory() as directory:
        path = pathlib.Path(directory) / 'overlay.json'
        path.write_text(json.dumps(overlay))
        args = ['helm', 'template', 'root-probe', str(chart)]
        for f in [*files, path]:
            args += ['-f', str(f)]
        return subprocess.run(args, capture_output=True, text=True, check=False)

def objects(result):
    assert result.returncode == 0, result.stderr
    return [o for o in yaml.safe_load_all(result.stdout) if o]

def case(name, fn):
    global passed, failed
    try:
        fn()
    except (AssertionError, KeyError, StopIteration, TypeError, yaml.YAMLError) as error:
        failed += 1
        print(f'FAIL root-pool/{name}: {error}', flush=True)
    else:
        passed += 1
        print(f'PASS root-pool/{name}', flush=True)

def platform_render(group='registry-host-a', enabled=True):
    return render(platform, [default, all_values], {
        'site': {'routingGroup': group},
        'sharedRoutes': {'apiRoot': {'privatePool': enabled,
            'backend': {'namespace': 'tenant-a', 'name': 'shared-private-root', 'port': 8080}}}})

def labels(group, public=False, injected=None):
    overlay = {'site': {}, 'instance': {'public': public}}
    if group is not None:
        overlay['site']['routingGroup'] = group
    if injected is not None:
        overlay['site']['vault'] = {'injectorLabels': injected}
    result = render(instance, [order, site], overlay)
    if injected is not None:
        assert result.returncode != 0 and 'injectorLabels conflicts' in result.stderr, result.stderr
        return None
    return next(o for o in objects(result) if o['kind'] == 'Deployment')['spec']['template']['metadata']['labels']

def topology():
    docs = objects(platform_render())
    service = next(o for o in docs if o['kind'] == 'Service' and o['metadata']['name'] == 'shared-private-root')
    assert service['metadata']['namespace'] == 'tenant-a'
    assert not service['metadata'].get('ownerReferences')
    assert service['spec']['ports'] == [{'name': 'proxy', 'port': 8080, 'targetPort': 'proxy', 'protocol': 'TCP'}]
    selector = service['spec']['selector']
    matches = lambda pod: all(pod.get(k) == v for k, v in selector.items())
    assert matches(labels('registry-host-a'))
    assert not matches(labels('registry-host-b'))
    assert not matches(labels('registry-host-a', True))
    assert not matches({'app.kubernetes.io/name': 'zot'})
    route = next(o for o in docs if o['kind'] == 'HTTPRoute' and o['metadata']['name'] == 'registry-api-root')
    assert not route['metadata'].get('ownerReferences')
    assert route['spec']['rules'][0]['backendRefs'][0] == {'group': '', 'kind': 'Service', 'namespace': 'tenant-a', 'name': 'shared-private-root', 'port': 8080}

def absent_group():
    result = platform_render('')
    assert result.returncode != 0 and ('routingGroup' in result.stderr or 'private root pool requires' in result.stderr), result.stderr
    objects(platform_render())

def default_unchanged():
    baseline = render(platform, [default, all_values], {})
    explicit = render(platform, [default, all_values], {'sharedRoutes': {'apiRoot': {'privatePool': False}}})
    objects(baseline); objects(explicit)
    assert baseline.stdout == explicit.stdout
    assert not any(o['kind'] == 'Service' for o in objects(baseline))

case('private-same-group-only-and-stable-route', topology)
case('missing-group-refused-with-valid-twin', absent_group)
case('disabled-default-byte-stable', default_unchanged)
for key in ['registry.in-cloud.io/routing-group', 'registry.in-cloud.io/root-membership']:
    for group in ['registry-host-a', None]:
        def collision(key=key, group=group):
            labels(group, injected={key: 'private' if 'membership' in key else 'registry-host-a'})
            labels(group)
        case('protected/' + key + '/' + str(group), collision)
print(f'root pool: passed {passed}, failed {failed}', flush=True)
sys.exit(1 if failed else 0)
