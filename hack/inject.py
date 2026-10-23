#!/usr/bin/env python3
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
"""inject.py — таблица инъекций: каждая проба доказывает, что способна упасть.

Для каждой строки hack/injections.yaml:
  1. контроль — названные пробы на неправленом дереве ЗЕЛЕНЫ;
  2. инъекция — в прод-код вносится ровно один дефект (замена строки,
     встречающейся в файле ровно один раз), и те же пробы КРАСНЕЮТ, причём
     красное — от названной пробы (--- FAIL: <имя>), а не от сборки;
  3. файл восстанавливается.
Строка, чья замена не применяется, контроль которой красный или инъекция
которой зелёная, — отказ всего прогона. Печатается перепись: строк N,
доказано M.
"""
import os
import re
import subprocess
import sys

import yaml

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def run(pkg, tests):
    pattern = "^(" + "|".join(tests) + ")$"
    p = subprocess.run(
        ["go", "test", "-count=1", "-run", pattern, pkg],
        cwd=ROOT, capture_output=True, text=True, env=os.environ.copy(),
    )
    return p.returncode, p.stdout + p.stderr


def main():
    with open(os.path.join(ROOT, "hack", "injections.yaml")) as f:
        table = yaml.safe_load(f)["injections"]
    only = sys.argv[1:]
    proven, failed = 0, []
    rows = [r for r in table if not only or r["name"] in only]
    if not rows:
        print("inject: no injection selected — nothing was proven", file=sys.stderr)
        return 3
    for row in rows:
        name, path = row["name"], os.path.join(ROOT, row["file"])
        with open(path) as f:
            original = f.read()
        if original.count(row["old"]) != 1:
            failed.append(f"{name}: the replaced text occurs {original.count(row['old'])} times in {row['file']}")
            continue
        code, out = run(row["package"], row["tests"])
        if code != 0:
            failed.append(f"{name}: control is red\n{out[-2000:]}")
            continue
        try:
            with open(path, "w") as f:
                f.write(original.replace(row["old"], row["new"], 1))
            code, out = run(row["package"], row["tests"])
        finally:
            with open(path, "w") as f:
                f.write(original)
        fails = set(re.findall(r"--- FAIL: (\S+)", out))
        named = [t for t in row["tests"] if any(x == t or x.startswith(t + "/") for x in fails)]
        if code == 0 or not named:
            failed.append(f"{name}: injection is not caught by {row['tests']} (exit {code})\n{out[-2000:]}")
            continue
        proven += 1
        print(f"inject: {name}: control green, injected red in {', '.join(sorted(named))}")
    print(f"inject: rows {len(rows)}, proven {proven}, failed {len(failed)}")
    for f in failed:
        print("inject: FAILED " + f, file=sys.stderr)
    return 0 if not failed and proven == len(rows) else 1


if __name__ == "__main__":
    sys.exit(main())
