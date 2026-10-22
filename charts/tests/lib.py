#!/usr/bin/env python3
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
"""Помощник проб чартов: разбор рендера, сборка входов цепочки и проверки формы.

Подкоманды (каждая печатает итог и возвращает 0 только при успехе):
  layers <platform.yaml> <outdir>       — слои AddonValue и шаблоны AddonTemplate из рендера платформы
  vars <order.yaml> <out.yaml>          — переменные заказа как values стенда-проводника
  strip <harness.yaml> <out.yaml>       — тело вывода проводника без заголовка helm
  wrap-addon <spec.yaml> <name> <out>   — тело Addon в объект для kubeconform
  probe-instance <render.yaml> <site.yaml> <order-vars.yaml> <probes-on:true|false>
  probe-platform <render.yaml> <default|all>
  no-lua <path>...                      — ни в одном файле нет проверок здоровья Lua для Argo CD
"""
import json
import pathlib
import re
import sys

import yaml

DIGEST = re.compile(r"@sha256:[a-f0-9]{64}$")


def docs(path):
    with open(path, encoding="utf-8") as f:
        return [d for d in yaml.safe_load_all(f) if d]


class Probe:
    def __init__(self, name):
        self.name = name
        self.passed = 0
        self.failed = []

    def check(self, ok, what):
        if ok:
            self.passed += 1
        else:
            self.failed.append(what)

    def done(self):
        for f in self.failed:
            print(f"FAIL {self.name}: {f}")
        print(f"{self.name}: checks passed {self.passed}, failed {len(self.failed)}")
        return 0 if not self.failed and self.passed > 0 else 1


def cmd_layers(platform, outdir):
    out = pathlib.Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    found = {"order": 0, "site": 0, "template": 0}
    for d in docs(platform):
        kind = d.get("kind")
        if kind == "AddonValue":
            layer = d["metadata"]["labels"].get("addons.in-cloud.io/values")
            if layer in ("order", "site"):
                (out / f"{layer}.layer").write_text(d["spec"]["values"], encoding="utf-8")
                found[layer] += 1
        elif kind == "AddonTemplate":
            version = d["metadata"]["labels"]["registry.in-cloud.io/chart-version"]
            (out / f"addontemplate-{version}.tmpl").write_text(d["spec"]["template"], encoding="utf-8")
            found["template"] += 1
    print(f"layers: order {found['order']}, site {found['site']}, templates {found['template']}")
    return 0 if found["order"] == 1 and found["site"] == 1 and found["template"] >= 1 else 1


def cmd_vars(order, out):
    with open(order, encoding="utf-8") as f:
        variables = yaml.safe_load(f)
    if not all(isinstance(v, str) for v in variables.values()):
        print("vars: Addon variables are strings; quote every value")
        return 1
    claim = {"metadata": {"name": "reg-" + variables["id"], "namespace": variables["login"]},
             "spec": {"variables": variables}}
    with open(out, "w", encoding="utf-8") as f:
        yaml.safe_dump({"variables": variables, "claim": claim}, f, sort_keys=True)
    return 0


def cmd_strip(src, out):
    with open(src, encoding="utf-8") as f:
        lines = f.read().splitlines()
    while lines and (lines[0] == "---" or lines[0].startswith("# Source:")):
        lines.pop(0)
    with open(out, "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")
    return 0


def cmd_wrap_addon(spec_file, name, out):
    with open(spec_file, encoding="utf-8") as f:
        body = yaml.safe_load(f)
    obj = {"apiVersion": "addons.in-cloud.io/v1alpha1", "kind": "Addon",
           "metadata": {"name": name}, "spec": body["spec"]}
    with open(out, "w", encoding="utf-8") as f:
        yaml.safe_dump(obj, f, sort_keys=True)
    return 0


def containers(dep):
    spec = dep["spec"]["template"]["spec"]
    return spec.get("initContainers", []) + spec.get("containers", [])


def ports_of(rule):
    got = set()
    for tp in rule.get("toPorts", []):
        for p in tp.get("ports", []):
            got.add((p["port"], p["protocol"]))
    return got


def cmd_probe_instance(render, site_file, vars_file, probes_on):
    p = Probe("instance " + pathlib.Path(render).stem)
    ds = docs(render)
    with open(site_file, encoding="utf-8") as f:
        site = yaml.safe_load(f)["site"]
    with open(vars_file, encoding="utf-8") as f:
        v = yaml.safe_load(f)["variables"]
    base = "zot-" + v["id"]
    kinds = sorted(d["kind"] for d in ds)
    p.check(kinds == ["CiliumNetworkPolicy", "Deployment", "Service", "ServiceAccount"],
            f"exactly the zot workload is rendered, got {kinds}")
    text = yaml.safe_dump_all(ds)
    p.check("PersistentVolumeClaim" not in text and "persistentVolumeClaim" not in text
            and "volumeClaimTemplates" not in text and "hostPath" not in text,
            "no persistent or host volumes")
    p.check("<path:" not in text and "argocdPlugins" not in text, "no secret-plugin substitutions")
    for d in ds:
        md = d["metadata"]
        p.check(md["name"] == base, f"{d['kind']} is named {base}")
        p.check("namespace" not in md, f"{d['kind']} sets no namespace")
        p.check("ownerReferences" not in md, f"{d['kind']} has no ownerReferences")
        p.check(md["labels"].get("registry.in-cloud.io/id") == v["id"]
                and md["labels"].get("registry.in-cloud.io/namespace") == v["login"]
                and md["labels"].get("registry.in-cloud.io/owner-uid") == v["registryUID"],
                f"{d['kind']} carries the order labels")
    by = {d["kind"]: d for d in ds}
    sa = by.get("ServiceAccount", {})
    p.check(sa.get("automountServiceAccountToken") is False, "service account token is not automounted")
    dep = by.get("Deployment")
    if dep:
        spec = dep["spec"]
        pod = spec["template"]["spec"]
        ann = spec["template"]["metadata"]["annotations"]
        p.check(spec["replicas"] == 1 and spec["strategy"]["type"] == "Recreate", "one replica, Recreate")
        p.check(pod["serviceAccountName"] == base and pod["automountServiceAccountToken"] is False,
                "pod runs as its own SA without an API token")
        for c in containers(dep):
            p.check(bool(DIGEST.search(c["image"])), f"image of {c['name']} is pinned by digest")
        names = [c["name"] for c in pod["containers"]]
        p.check(names == ["zot", "sidecar"], f"containers zot and sidecar, got {names}")
        side = pod["containers"][1]
        p.check("--mode=sidecar" in side["args"], "sidecar runs svc-registry --mode=sidecar")
        p.check(f"--registry-id={v['id']}" in side["args"], "sidecar checks rights on its own registry")
        p.check([pp["containerPort"] for pp in side["ports"]] == [8080], "only the sidecar listens outside")
        p.check(pod["containers"][0]["args"] == ["serve", "/vault/secrets/config.json"], "zot reads the rendered config")
        vols = {x["name"]: x for x in pod["volumes"]}
        p.check(all("emptyDir" in vols[n] for n in ("scratch", "tmp")), "temporary storage is emptyDir")
        p.check(ann.get("vault.hashicorp.com/auth-path") == "auth/" + v["vaultAuthMount"], "vault-agent logs in at auth/<mount>")
        p.check(ann.get("vault.hashicorp.com/role") == base == v["vaultRole"], "vault-agent role is zot-<id>")
        p.check(ann.get("vault.hashicorp.com/service") == site["vault"]["address"], "vault-agent talks to the site Vault over https")
        p.check(ann.get("vault.hashicorp.com/agent-init-first") == "true"
                and ann.get("vault.hashicorp.com/agent-pre-populate") == "true",
                "init vault-agent renders config before zot starts")
        p.check(ann.get("vault.hashicorp.com/agent-inject-secret-config.json") == v["vaultSecretPath"],
                "config is rendered from the order leaf")
        tmpl = ann.get("vault.hashicorp.com/agent-inject-template-config.json", "")
        p.check(tmpl.startswith('{{- with secret "%s" -}}' % v["vaultSecretPath"]) and tmpl.endswith("{{- end -}}"),
                "config template reads exactly the order leaf")
        inner = tmpl[len('{{- with secret "%s" -}}' % v["vaultSecretPath"]):-len("{{- end -}}")]
        probe_cfg = inner.replace("{{ .Data.data.accessKey | toJSON }}", '"A"').replace("{{ .Data.data.secretKey | toJSON }}", '"S"')
        try:
            cfg = json.loads(probe_cfg)
            drv = cfg["storage"]["storageDriver"]
            p.check(drv["name"] == "s3" and drv["bucket"] == v["bucket"] and drv["regionendpoint"] == v["endpoint"]
                    and drv["region"] == v["region"] and drv["accesskey"] == "A" and drv["secretkey"] == "S",
                    "zot stores in the order bucket with keys from the leaf only")
            p.check(cfg["http"] == {"address": "127.0.0.1", "port": "5000"}, "zot listens on loopback only")
            p.check(probe_cfg.count("{{") == 0, "no other template actions in the config")
        except (ValueError, KeyError) as e:
            p.check(False, f"config template is JSON with two leaf keys: {e}")
    svc = by.get("Service")
    if svc:
        p.check([(x["port"], x["targetPort"]) for x in svc["spec"]["ports"]] == [(8080, "proxy")], "Service exposes only the sidecar")
    cnp = by.get("CiliumNetworkPolicy")
    if cnp:
        spec = cnp["spec"]
        p.check(spec["endpointSelector"]["matchLabels"] == {"app.kubernetes.io/name": "zot", "registry.in-cloud.io/id": v["id"]},
                "policy selects only this registry's pod")
        ing = spec["ingress"]
        proxy = site["edge"]["proxy"]
        want_from = {"k8s:io.kubernetes.pod.namespace": proxy["namespace"],
                     "k8s:gateway.envoyproxy.io/owning-gateway-namespace": proxy["gateway"]["namespace"],
                     "k8s:gateway.envoyproxy.io/owning-gateway-name": proxy["gateway"]["name"]}
        p.check(len(ing) == (2 if probes_on else 1), f"ingress rules: proxy{' and kubelet' if probes_on else ' only'}")
        p.check(ing[0].get("fromEndpoints") == [{"matchLabels": want_from}] and ports_of(ing[0]) == {("8080", "TCP")},
                "ingress from the Envoy Gateway proxy of its own Gateway to :8080")
        if probes_on and len(ing) > 1:
            p.check(ing[1].get("fromEntities") == ["host"] and ports_of(ing[1]) == {("8080", "TCP")}, "kubelet probes to :8080 only")
        eg = spec["egress"]
        dns = eg[0]
        p.check(ports_of(dns) == {("53", "UDP"), ("53", "TCP")} and "rules" in dns["toPorts"][0], "egress 1: DNS with name rules")
        s3host = re.sub(r":\d+$", "", v["endpoint"][len("https://"):])
        s3port = v["endpoint"].rsplit(":", 1)[1] if v["endpoint"].count(":") == 2 else "443"
        p.check(eg[1] == {"toFQDNs": [{"matchName": s3host}], "toPorts": [{"ports": [{"port": s3port, "protocol": "TCP"}]}]},
                "egress 2: the order S3 endpoint only")
        peers = site["network"]["vault"] + site["network"]["kaname"]
        p.check(len(eg) == 2 + len(peers), f"egress rules: DNS, S3 and {len(peers)} Vault/kaname peers, nothing else")
        for rule, peer in zip(eg[2:], peers):
            p.check(ports_of(rule) == {(str(x), "TCP") for x in peer["ports"]}, f"peer {peer} ports exactly")
        kaname_ports = set()
        for rule in eg[2 + len(site["network"]["vault"]):]:
            kaname_ports |= {pp for pp, _ in ports_of(rule)}
        p.check(kaname_ports <= {"9091", "9097"}, f"kaname only on :9091/:9097, got {sorted(kaname_ports)}")
        dns_names = {r.get("matchName") for r in dns["toPorts"][0]["rules"]["dns"] if "matchName" in r}
        p.check(s3host in dns_names, "S3 host resolvable")
    return p.done()


def cmd_probe_platform(render, mode):
    p = Probe("platform " + mode)
    ds = docs(render)
    kinds = {}
    for d in ds:
        kinds.setdefault(d["kind"], []).append(d["metadata"]["name"])
    p.check(len(kinds.get("AddonTemplate", [])) >= 1, "AddonTemplate of the pinned chart is rendered")
    p.check(sorted(kinds.get("AddonValue", [])) == ["registry-instance-order", "registry-instance-site", "registry-platform-edge"],
            f"AddonValue layers order, site and edge, got {kinds.get('AddonValue')}")
    p.check(kinds.get("AddonPhase") == ["registry-platform"], "AddonPhase of the platform Addon is rendered")
    p.check(sorted(kinds.get("HTTPRoute", [])) == ["registry-api-root", "registry-token"], "shared routes /v2/ and /iam/token")
    p.check(kinds.get("Gateway") == ["registry-gw"], "edge Gateway is rendered")
    for d in ds:
        if d["kind"] == "AddonTemplate":
            ann = d["metadata"]["annotations"]["registry.in-cloud.io/chart-digest"]
            p.check(re.fullmatch(r"sha256:[a-f0-9]{64}", ann) is not None, "AddonTemplate pins the chart digest")
        if d["kind"] == "HTTPRoute":
            paths = [m["path"] for r in d["spec"]["rules"] for m in r["matches"]]
            p.check(len(paths) == 1 and paths[0]["type"] == "Exact" and paths[0]["value"] in ("/v2/", "/iam/token"),
                    f"shared route {d['metadata']['name']} matches one exact path")
    vap = kinds.get("ValidatingAdmissionPolicy", [])
    bind = kinds.get("ValidatingAdmissionPolicyBinding", [])
    proj = kinds.get("AppProject", [])
    if mode == "default":
        p.check(not vap and not bind, "no admission policies by default")
        p.check(not proj, "no AppProject by default")
        p.check("ConfigMap" not in kinds, "no admission parameters by default")
    else:
        ids = sorted(int(n.split("-")[0][1:]) for n in vap)
        p.check(ids == list(range(1, 16)), f"admission policies V1..V15, got {ids}")
        p.check(sorted(bind) == sorted(vap), "every policy has its binding")
        p.check(sorted(proj) == ["registry-cust01", "registry-cust02"], f"one AppProject per tenant, got {proj}")
        for d in ds:
            if d["kind"] == "ValidatingAdmissionPolicy":
                p.check(d["spec"]["failurePolicy"] == "Fail", f"{d['metadata']['name']} fails closed")
            if d["kind"] == "ValidatingAdmissionPolicyBinding":
                p.check(d["spec"]["paramRef"]["parameterNotFoundAction"] == "Deny"
                        and d["spec"]["validationActions"] == ["Deny"], f"{d['metadata']['name']} denies without params")
            if d["kind"] == "AppProject":
                s = d["spec"]
                p.check(len(s["destinations"]) == 1 and s["clusterResourceWhitelist"] == [] and "sourceNamespaces" not in s,
                        f"{d['metadata']['name']} has one destination and no cluster resources")
    return p.done()


def cmd_no_lua(paths):
    p = Probe("no-lua")
    seen = 0
    files = []
    for root in map(pathlib.Path, paths):
        files += [root] if root.is_file() else sorted(x for x in root.rglob("*") if x.is_file())
    for f in files:
        if f.name == "lib.py":
            continue
            seen += 1
            text = f.read_text(encoding="utf-8", errors="replace")
            p.check(f.suffix != ".lua", f"{f} is a Lua file")
            p.check("resource.customizations" not in text and "health.lua" not in text, f"{f} carries an Argo CD health check")
    print(f"no-lua: files read {seen}")
    return p.done() if seen else 1


def main(argv):
    cmd, args = argv[1], argv[2:]
    if cmd == "layers":
        return cmd_layers(*args)
    if cmd == "vars":
        return cmd_vars(*args)
    if cmd == "strip":
        return cmd_strip(*args)
    if cmd == "wrap-addon":
        return cmd_wrap_addon(*args)
    if cmd == "probe-instance":
        return cmd_probe_instance(args[0], args[1], args[2], args[3] == "true")
    if cmd == "probe-platform":
        return cmd_probe_platform(*args)
    if cmd == "no-lua":
        return cmd_no_lua(args)
    print(__doc__)
    return 64


if __name__ == "__main__":
    sys.exit(main(sys.argv))
