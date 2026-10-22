#!/usr/bin/env python3
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
"""Пробы политик допуска на кластере: kubectl --dry-run=server от имени авторов.

probe.py <cases.yaml> — код 0, только если нет красных и исполнена хотя бы одна проба.
Отказ засчитывается, только если в тексте отказа названа ожидаемая политика.
"""
import copy
import json
import subprocess
import sys
import time

import yaml


def kubectl(args, identity=None, stdin=None):
    cmd = ["kubectl"] + args
    if identity:
        cmd += ["--as", identity["user"]]
        for g in identity.get("groups", []):
            cmd += ["--as-group", g]
    r = subprocess.run(cmd, input=stdin, capture_output=True, text=True, timeout=60)
    return r.returncode, r.stdout + r.stderr


def set_path(obj, path, value):
    keys = path.split(".")
    # ключи меток и аннотаций содержат точки: путь кончается на них целиком
    for i, k in enumerate(keys):
        if k in ("labels", "annotations", "variables") and i + 1 < len(keys):
            keys = keys[: i + 1] + [".".join(keys[i + 1:])]
            break
    cur = obj
    for k in keys[:-1]:
        cur = cur.setdefault(k, {})
    cur[keys[-1]] = value


def build(case):
    obj = copy.deepcopy(case["object"])
    if "rename" in case:
        obj["metadata"]["name"] = case["rename"]
    for path, value in (case.get("set") or {}).items():
        set_path(obj, path, value)
    return obj


def run_case(case, ids):
    who = ids[case["as"]]
    verb = case["verb"]
    if verb == "create":
        rc, out = kubectl(["create", "--dry-run=server", "-o", "name", "-f", "-"], who, yaml.safe_dump(build(case)))
    elif verb == "patch":
        args = ["patch", case["kind"], case["target"], "--type=merge", "--dry-run=server", "-p", json.dumps(case["patch"])]
        if case.get("namespace"):
            args += ["-n", case["namespace"]]
        rc, out = kubectl(args, who)
    elif verb == "delete":
        args = ["delete", case["kind"], case["target"], "--dry-run=server"]
        if case.get("namespace"):
            args += ["-n", case["namespace"]]
        rc, out = kubectl(args, who)
    else:
        return False, f"unknown verb {verb}"
    denied_by = f"ValidatingAdmissionPolicy '{case['policy']}'" in out
    if case["expect"] == "deny":
        return rc != 0 and denied_by, out.strip()
    return rc == 0, out.strip()


def main(path):
    spec = yaml.safe_load(open(path, encoding="utf-8"))
    ids = spec["identities"]
    for obj in spec["setup"]:
        rc, out = kubectl(["create", "-f", "-"], None, yaml.safe_dump(obj))
        if rc != 0:
            print(f"SETUP FAILED {obj['kind']}/{obj['metadata']['name']}: {out}")
            return 1
    # Политики вступают в силу не сразу: ждём, пока первая отрицательная проба откажет.
    sentinel = next(c for c in spec["cases"] if c["expect"] == "deny")
    for _ in range(45):
        ok, _out = run_case(sentinel, ids)
        if ok:
            break
        time.sleep(2)
    else:
        print(f"policies did not take effect: {sentinel['name']}: {_out}")
        return 1
    rc, out = kubectl(["get", "validatingadmissionpolicies", "-o", "json"])
    warnings = []
    for item in json.loads(out)["items"]:
        for w in (item.get("status", {}).get("typeChecking", {}) or {}).get("expressionWarnings", []) or []:
            warnings.append(f"{item['metadata']['name']}: {w.get('fieldRef')}: {w.get('warning')}")
    passed, failed = 0, []
    for w in warnings:
        failed.append(f"type check: {w}")
    for case in spec["cases"]:
        ok, out = run_case(case, ids)
        if ok:
            passed += 1
            print(f"PASS {case['name']} ({case['expect']})")
        else:
            failed.append(case["name"])
            print(f"FAIL {case['name']} (expected {case['expect']}):\n  | " + out.replace("\n", "\n  | "))
    rc, _ = kubectl(["get", "crd", "registries.registry.in-cloud.io"])
    skipped = spec.get("needsRegistryCRDs", []) if rc != 0 else []
    for name in skipped:
        print(f"NOT EXECUTED {name}: CRD registries.registry.in-cloud.io is not installed")
    print(f"admission: passed {passed}, failed {len(failed)}, not executed {len(skipped)}")
    return 0 if not failed and passed > 0 else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1]))
