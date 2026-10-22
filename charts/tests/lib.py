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
    # JSON — подмножество YAML, где строка всегда в кавычках: «1e3» и «true» остаются
    # строками, как в Addon.spec.variables, а не становятся числом или логическим.
    with open(out, "w", encoding="utf-8") as f:
        json.dump({"variables": variables, "claim": claim}, f, sort_keys=True, indent=1)
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
            f"exactly zot with its iam-proxy is rendered, got {kinds}")
    text = yaml.safe_dump_all(ds)
    p.check("PersistentVolumeClaim" not in text and "persistentVolumeClaim" not in text
            and "volumeClaimTemplates" not in text and "hostPath" not in text,
            "no persistent or host volumes")
    p.check("<path:" not in text and "argocdPlugins" not in text, "no secret-plugin substitutions")
    p.check("pkiCert" not in text and "tls.pem" not in text and "Certificate" not in kinds,
            "no client certificate: iam-proxy has no identity of its own")
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
        p.check(names == ["zot", "iam-proxy"], f"containers zot and iam-proxy, got {names}")
        zot = pod["containers"][0]
        proxy = pod["containers"][1] if len(pod["containers"]) > 1 else {"args": [], "ports": [], "volumeMounts": []}
        img = site["images"]["iamProxy"]
        p.check(proxy.get("image", "").startswith(img["repository"] + ":" + img["tag"] + "@"),
                "iam-proxy runs the site image of registry-iam-proxy")
        args = proxy["args"]
        want = {
            "--registry-id": v["id"],
            "--anonymous-pull": v["public"],
            "--kaname-api-url": site["kaname"]["apiURL"],
            "--jwks-url": site["kaname"]["jwksURL"],
            "--issuer": site["kaname"]["issuer"],
            "--audience": site["kaname"]["audience"],
            "--decision-cache-ttl": site["iamProxy"]["decisionCacheTTL"],
            "--registration-url": site["registryOperator"]["registrationURL"],
            "--upstream": "http://127.0.0.1:5000",
        }
        got = dict(a.split("=", 1) for a in args if "=" in a)
        for k, val in want.items():
            p.check(got.get(k) == val, f"iam-proxy {k}={val}, got {got.get(k)}")
        p.check(not any("9091" in a for a in args), "iam-proxy never names the kaname internal listener")
        ttl = int(got.get("--decision-cache-ttl", "99s")[:-1])
        p.check(1 <= ttl <= 10, "verdict cache is at most 10 s")
        p.check([(pp["name"], pp["containerPort"]) for pp in proxy["ports"]] == [("proxy", 8080), ("health", 8081)],
                "iam-proxy listens on :8080 (proxy) and :8081 (probes)")
        p.check(proxy["readinessProbe"]["httpGet"]["port"] == "health" and proxy["livenessProbe"]["httpGet"]["port"] == "health",
                "probes go to the health port")
        p.check("ports" not in zot, "zot exposes no port")
        p.check(zot["args"] == ["serve", "/vault/secrets/config.json"], "zot reads the rendered config")
        vols = {x["name"]: x for x in pod["volumes"]}
        p.check(all("emptyDir" in vols[n] for n in ("scratch", "tmp")), "temporary storage is emptyDir")
        tok = vols.get("registry-operator-token", {}).get("projected", {}).get("sources", [{}])[0].get("serviceAccountToken", {})
        p.check(tok.get("audience") == site["registryOperator"]["tokenAudience"], "registration token has the registry-operator audience")
        vt = vols.get("vault-token", {}).get("projected", {}).get("sources", [{}])[0].get("serviceAccountToken", {})
        p.check(vt.get("audience") == site["vault"]["tokenAudience"] != tok.get("audience"), "Vault token has its own audience")
        mounts = {c["name"]: {m["name"] for m in c.get("volumeMounts", [])} for c in pod["containers"]}
        p.check([n for n, ms in mounts.items() if "registry-operator-token" in ms] == ["iam-proxy"],
                "registration token is mounted into iam-proxy only")
        p.check(not any("vault-token" in ms for ms in mounts.values()), "Vault token is mounted by the injector only")
        p.check(ann.get("vault.hashicorp.com/agent-inject-containers") == "zot", "bucket keys reach the zot container only")
        p.check(ann.get("vault.hashicorp.com/auth-path") == "auth/" + v["vaultAuthMount"], "vault-agent logs in at auth/<mount>")
        p.check(ann.get("vault.hashicorp.com/role") == base == v["vaultRole"], "vault-agent role is zot-<id>")
        p.check(ann.get("vault.hashicorp.com/service") == site["vault"]["address"], "vault-agent talks to the site Vault over https")
        p.check(ann.get("vault.hashicorp.com/agent-init-first") == "true"
                and ann.get("vault.hashicorp.com/agent-pre-populate") == "true"
                and ann.get("vault.hashicorp.com/agent-pre-populate-only") == "true",
                "init vault-agent renders config before zot starts, no agent sidecar")
        p.check(ann.get("vault.hashicorp.com/template-config-exit-on-retry-failure") == "true",
                "init vault-agent fails and is restarted until the leaf exists")
        p.check(ann.get("vault.hashicorp.com/agent-inject-secret-config.json") == v["vaultSecretPath"],
                "config is rendered from the order leaf")
        p.check(sorted(k for k in ann if k.startswith("vault.hashicorp.com/agent-inject-secret-")) == ["vault.hashicorp.com/agent-inject-secret-config.json"],
                "vault-agent renders exactly one file")
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
        p.check([(x["port"], x["targetPort"]) for x in svc["spec"]["ports"]] == [(8080, "proxy")], "Service exposes only iam-proxy")
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
            p.check(ing[1].get("fromEntities") == ["host"] and ports_of(ing[1]) == {("8081", "TCP")}, "kubelet probes to :8081 only")
        eg = spec["egress"]
        dns = eg[0]
        p.check(ports_of(dns) == {("53", "UDP"), ("53", "TCP")} and "rules" in dns["toPorts"][0], "egress 1: DNS with name rules")
        s3host = re.sub(r":\d+$", "", v["endpoint"][len("https://"):])
        s3port = v["endpoint"].rsplit(":", 1)[1] if v["endpoint"].count(":") == 2 else "443"
        p.check(eg[1] == {"toFQDNs": [{"matchName": s3host}], "toPorts": [{"ports": [{"port": s3port, "protocol": "TCP"}]}]},
                "egress 2: the order S3 endpoint only")
        net = site["network"]
        peers = net["vault"] + net["kaname"] + net["registryOperator"]
        p.check(len(eg) == 2 + len(peers), f"egress rules: DNS, S3 and {len(peers)} Vault/kaname/registry-operator peers, nothing else")
        for rule, peer in zip(eg[2:], peers):
            p.check(ports_of(rule) == {(str(x), "TCP") for x in peer["ports"]}, f"peer {peer} ports exactly")
            if "fqdn" in peer:
                p.check(rule.get("toFQDNs") == [{"matchName": peer["fqdn"]}], f"peer {peer['fqdn']} by name only")
            elif "namespace" in peer:
                want = {"k8s:io.kubernetes.pod.namespace": peer["namespace"]}
                want.update({"k8s:" + k: val for k, val in peer["labels"].items()})
                p.check(rule.get("toEndpoints") == [{"matchLabels": want}], f"peer {peer['namespace']} by namespace and labels")
        all_ports = {pp for rule in eg[2:] for pp, _ in ports_of(rule)}
        p.check("9091" not in all_ports, f"no egress to the kaname internal listener, got {sorted(all_ports)}")
        dns_names = {r.get("matchName") for r in dns["toPorts"][0]["rules"]["dns"] if "matchName" in r}
        hosts = {s3host} | {re.sub(r"^https://([^/:]+).*$", r"\1", u) for u in (
            site["vault"]["address"], site["kaname"]["apiURL"], site["kaname"]["jwksURL"], site["registryOperator"]["registrationURL"])}
        p.check(hosts <= dns_names, f"S3, Vault, kaname and registry-operator hosts resolvable, missing {sorted(hosts - dns_names)}")
    return p.done()


def manifest_of(ds):
    for d in ds:
        if d["kind"] == "ConfigMap" and "registry.manifest.yaml" in d.get("data", {}):
            return d, yaml.safe_load(d["data"]["registry.manifest.yaml"])
    return None, None


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
            p.check("public: {{ .Vars.public | toString | toJson }}" in d["spec"]["template"],
                    "Addon carries the public variable of the order")
        if d["kind"] == "HTTPRoute":
            paths = [m["path"] for r in d["spec"]["rules"] for m in r["matches"]]
            p.check(len(paths) == 1 and paths[0]["type"] == "Exact" and paths[0]["value"] in ("/v2/", "/iam/token"),
                    f"shared route {d['metadata']['name']} matches one exact path")
    cm, m = manifest_of(ds)
    p.check(cm is not None, "kaname module manifest of registry is rendered")
    if cm is not None:
        p.check(list(cm["data"]) == ["registry.manifest.yaml"], "the manifest ConfigMap carries exactly one delivery key")
        p.check(cm["metadata"]["labels"].get("app.kubernetes.io/part-of") == "registry", "manifest ConfigMap is labelled part-of registry")
        p.check(m.get("apiVersion") == "iam/v1" and m.get("module") == "registry", "manifest of module registry, iam/v1")
        res = {r["objectType"]: r for r in m.get("resources", [])}
        p.check(sorted(res) == ["registry_registry", "registry_repository"], f"types registry_registry and registry_repository, got {sorted(res)}")
        p.check(res.get("registry_registry", {}).get("parents") == ["project"], "a registry hangs under its project")
        p.check(res.get("registry_repository", {}).get("parents") == [{"name": "parent", "type": "registry_registry"}],
                "a repository hangs under its registry")
        verbs = {r: {x["name"] for x in res.get(r, {}).get("verbs", [])} for r in res}
        p.check({"get", "create", "update"} <= verbs.get("registry_registry", set()), "registry has get, create, update")
        p.check({"get", "update"} <= verbs.get("registry_repository", set()), "repository has get, update")
        roles = {r["id"]: r for r in m.get("roles", [])}
        p.check(sorted(roles) == ["registry.admin", "registry.puller", "registry.pusher"], f"roles puller, pusher, admin, got {sorted(roles)}")
        want = {"registry.puller": (["get"], ["get", "list"]),
                "registry.pusher": (["get", "create"], ["get", "list", "update"]),
                "registry.admin": (["get", "list", "create", "update", "delete"], ["get", "list", "update", "delete"])}
        for rid, (reg, repo) in want.items():
            rules = roles.get(rid, {}).get("rules", [])
            ok = (len(rules) == 1 and rules[0].get("resources") == ["registries"] and rules[0].get("classes") == reg
                  and rules[0].get("children") == [{"resources": ["repositories"], "classes": repo}])
            p.check(ok, f"{rid}: {reg} on the registry and {repo} on each of its repositories through children")
        seed = m.get("seed", {})
        sas = [s["name"] for s in seed.get("serviceAccounts", [])]
        p.check(sas == ["kacho-registry"], f"seed: only the module identity kacho-registry, got {sas}")
        joins = [(j["serviceAccount"]["name"], j["group"]["name"]) for j in seed.get("joins", [])]
        p.check(joins == [("kacho-registry", "module-relation-writers")], f"kacho-registry writes ownership tuples, got {joins}")
    vap = kinds.get("ValidatingAdmissionPolicy", [])
    bind = kinds.get("ValidatingAdmissionPolicyBinding", [])
    proj = kinds.get("AppProject", [])
    others = [n for n in kinds.get("ConfigMap", []) if cm is None or n != cm["metadata"]["name"]]
    if mode == "default":
        p.check(not vap and not bind, "no admission policies by default")
        p.check(not proj, "no AppProject by default")
        p.check(not others, f"no admission parameters by default, got {others}")
    else:
        ids = sorted(int(n.split("-")[0][1:]) for n in vap)
        p.check(ids == list(range(1, 16)), f"admission policies V1..V15, got {ids}")
        p.check(sorted(bind) == sorted(vap), "every policy has its binding")
        p.check(others == ["registry-platform-admission"], f"admission parameters, got {others}")
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
            if d["kind"] == "ConfigMap" and d["metadata"]["name"] == "registry-platform-admission":
                p.check("registrar" not in d["data"] and "pkiPolicy" not in d["data"],
                        "no second registry writer and no data-plane PKI in the parameters")
    return p.done()


def cmd_no_lua(paths):
    p = Probe("no-lua")
    seen = 0
    files = []
    for root in map(pathlib.Path, paths):
        files += [root] if root.is_file() else sorted(x for x in root.rglob("*") if x.is_file())
    for f in files:
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
