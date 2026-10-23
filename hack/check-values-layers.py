#!/usr/bin/env python3
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
"""check-values-layers.py — префикс слоёв значений в чарте оператора выбирает
ровно слои AddonValue заказа, которые ставит чарт registry-platform.

Читает отрисованный чарт registry-operator (многодокументный YAML): из
ConfigMap оператора — config.yaml, из него policy.addon.valuesAddonPrefix.
Политика оператора принимает селектор Addon заказа лишь с меткой
addons.in-cloud.io/addon, начинающейся с этого префикса, — значит префикс
решает, какие слои площадки Addon заказа вообще может выбрать.

Слои и выбор площадки берутся из эталонных рендеров чарта registry-platform
(charts/tests/golden/platform-*.yaml, их сверяет с шаблоном charts/tests):
  * слои — документы AddonValue с их метками;
  * выбор площадки — слои, которые выбирают селекторы её AddonTemplate.
Требует: множество слоёв, чья метка addons.in-cloud.io/addon начинается с
префикса, РАВНО выбору площадки, и выбор не пуст. Префикс, не достающий до
слоя заказа или площадки, — находка; префикс, захватывающий чужой слой
(край самой платформы), — находка.

Печатает перепись: документов, эталонов, слоёв, форм. Код 0 — сходится,
1 — находка, 2 — не выполнилось (нет рендера оператора или ни одного
эталона площадки: сверка не прошла, а не «зелёная»).

Использование: check-values-layers.py <rendered.yaml> <platform-golden.yaml>…
"""
import re
import sys

import yaml

LABEL_ADDON = "addons.in-cloud.io/addon"
# Действие Go-шаблона в тексте AddonTemplate стоит на месте скаляра: замена
# скаляром оставляет разбираемый YAML (так же читает эталоны проба слоёв Go).
TEMPLATE_ACTION = re.compile(r"\{\{.*?\}\}")


def not_executed(msg):
    print(f"check-values-layers: NOT EXECUTED {msg}", file=sys.stderr)
    sys.exit(2)


def load_docs(path):
    try:
        with open(path) as f:
            return [d for d in yaml.safe_load_all(f) if d]
    except OSError as e:
        return e


def values_prefix(rendered):
    docs = load_docs(rendered)
    if isinstance(docs, OSError):
        not_executed(f"rendered chart {rendered}: {docs}")
    cms = [d for d in docs if d.get("kind") == "ConfigMap" and "config.yaml" in (d.get("data") or {})]
    if len(cms) != 1:
        not_executed(f"rendered chart holds {len(docs)} documents and {len(cms)} ConfigMap with config.yaml, want 1")
    cfg = yaml.safe_load(cms[0]["data"]["config.yaml"]) or {}
    prefix = ((cfg.get("policy") or {}).get("addon") or {}).get("valuesAddonPrefix")
    if not isinstance(prefix, str) or not prefix:
        return len(docs), None
    return len(docs), prefix


def platform(paths):
    files, layers, forms, unread = 0, {}, [], []
    for path in paths:
        docs = load_docs(path)
        if isinstance(docs, OSError):
            unread.append(f"{path}: {docs.strerror}")
            continue
        files += 1
        for d in docs:
            meta = d.get("metadata") or {}
            if d.get("kind") == "AddonValue":
                layers[meta.get("name")] = meta.get("labels") or {}
            elif d.get("kind") == "AddonTemplate":
                text = TEMPLATE_ACTION.sub('"x"', (d.get("spec") or {}).get("template") or "")
                form = yaml.safe_load(text) or {}
                sels = [s.get("matchLabels") or {} for s in (form.get("spec") or {}).get("valuesSelectors") or []]
                forms.append((meta.get("name"), sels))
    return files, layers, forms, unread


def selected(sel, layers):
    return {n for n, labels in layers.items() if sel and all(labels.get(k) == v for k, v in sel.items())}


def main():
    if len(sys.argv) < 3:
        print("usage: check-values-layers.py <rendered.yaml> <platform-golden.yaml>…", file=sys.stderr)
        return 2
    ndocs, prefix = values_prefix(sys.argv[1])
    files, layers, forms, unread = platform(sys.argv[2:])
    print(f"check-values-layers: rendered documents {ndocs}, platform golden renders {files}, "
          f"AddonValue layers {len(layers)}, AddonTemplate forms {len(forms)}, valuesAddonPrefix {prefix!r}")
    for u in unread:
        print(f"check-values-layers: unread {u}", file=sys.stderr)
    if files == 0 or not layers or not forms:
        not_executed("no golden render of the platform chart with AddonValue layers and an AddonTemplate form")
    findings = []
    if prefix is None:
        findings.append("config.policy.addon.valuesAddonPrefix is absent from the rendered configuration")
    else:
        reach = {n for n, labels in layers.items() if str(labels.get(LABEL_ADDON, "")).startswith(prefix)}
        for name, sels in forms:
            want = set().union(*[selected(s, layers) for s in sels]) if sels else set()
            if not want:
                findings.append(f"AddonTemplate {name}: the platform form selects no AddonValue layer")
            for n in sorted(want - reach):
                findings.append(f"AddonTemplate {name}: platform layer {n} ({LABEL_ADDON}={layers[n].get(LABEL_ADDON)}) "
                                f"is out of reach of valuesAddonPrefix {prefix!r}")
            for n in sorted(reach - want):
                findings.append(f"AddonTemplate {name}: valuesAddonPrefix {prefix!r} reaches layer {n} "
                                f"({LABEL_ADDON}={layers[n].get(LABEL_ADDON)}), which the platform form does not select")
    findings = list(dict.fromkeys(findings))
    for f in findings:
        print(f"check-values-layers: FAILED {f}", file=sys.stderr)
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
