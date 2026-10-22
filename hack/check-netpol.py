#!/usr/bin/env python3
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
"""check-netpol.py — вход на эндпоинт заявок оператора сужен политикой сети.

Читает отрисованный чарт registry-operator (многодокументный YAML) и
требует:
  1. есть NetworkPolicy с типом Ingress, чей podSelector выбирает поды
     оператора (метки селектора Service оператора);
  2. каждое правило входа, открывающее порт эндпоинта заявок, называет
     источники, и каждый источник — поды zot (podSelector с метками zot);
  3. правило без перечня портов (открывает все порты) — отказ;
  4. хотя бы одно правило открывает порт заявок: без него эндпоинт закрыт
     для самих zot.
Печатает перепись: документов, политик, правил на порт заявок. Пустой
вход — отказ (не «зелёный»).

Использование: check-netpol.py <rendered.yaml> [port-name]
"""
import sys

import yaml

ZOT = {"app.kubernetes.io/name": "zot", "app.kubernetes.io/part-of": "registry"}


def fail(msg):
    print(f"check-netpol: FAILED {msg}", file=sys.stderr)
    sys.exit(1)


def main():
    if len(sys.argv) < 2:
        print("usage: check-netpol.py <rendered.yaml> [port-name]", file=sys.stderr)
        return 2
    port_name = sys.argv[2] if len(sys.argv) > 2 else "enrollment"
    with open(sys.argv[1]) as f:
        docs = [d for d in yaml.safe_load_all(f) if d]
    if not docs:
        fail("the rendered chart holds no documents: nothing was checked")
    services = [d for d in docs if d.get("kind") == "Service"]
    if len(services) != 1:
        fail(f"expected one Service of the operator, found {len(services)}")
    svc = services[0]
    selector = svc["spec"]["selector"]
    port = next((p for p in svc["spec"]["ports"] if p.get("name") == port_name), None)
    if port is None:
        fail(f"the Service has no port {port_name}")
    numbers = {port_name, port.get("targetPort"), port.get("port")}

    policies = [d for d in docs if d.get("kind") == "NetworkPolicy"
                and "Ingress" in d.get("spec", {}).get("policyTypes", [])
                and all(d["spec"].get("podSelector", {}).get("matchLabels", {}).get(k) == v for k, v in selector.items())]
    if not policies:
        fail("no Ingress NetworkPolicy selects the operator pods: the enrollment endpoint is open to the whole cluster")
    rules = 0
    for pol in policies:
        name = pol["metadata"]["name"]
        for i, rule in enumerate(pol["spec"].get("ingress") or []):
            ports = rule.get("ports")
            if not ports:
                fail(f"{name}: ingress[{i}] lists no ports and admits every port")
            if not any(p.get("port") in numbers for p in ports):
                continue
            sources = rule.get("from")
            if not sources:
                fail(f"{name}: ingress[{i}] opens {port_name} to every source")
            for j, src in enumerate(sources):
                labels = (src.get("podSelector") or {}).get("matchLabels") or {}
                if "ipBlock" in src or any(labels.get(k) != v for k, v in ZOT.items()):
                    fail(f"{name}: ingress[{i}].from[{j}] opens {port_name} to a source other than the zot pods: {src}")
                if "namespaceSelector" not in src:
                    fail(f"{name}: ingress[{i}].from[{j}] names no namespaceSelector: zot pods of tenant namespaces cannot match")
            rules += 1
    if rules == 0:
        fail(f"no ingress rule opens {port_name} to the zot pods: the endpoint is closed to its callers")
    print(f"check-netpol: documents {len(docs)}, policies {len(policies)}, rules on {port_name} {rules}: only zot pods reach it")
    return 0


if __name__ == "__main__":
    sys.exit(main())
