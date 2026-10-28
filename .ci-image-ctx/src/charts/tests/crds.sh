#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# crds.sh <каталог> — CRD видов, которые рендерят чарты и проверяют политики допуска,
# с закреплённых ревизий источников. Каждый файл сверяется по sha256: подменённый или
# сдвинутый источник — отказ, а не тихо другая схема.
#
# Источники (ревизия — коммит или тег выпуска):
#   addons-operator    PRO-Robotech/addons-operator @22b73fde
#   Gateway API        kubernetes-sigs/gateway-api v1.5.1 (standard)
#   Cilium             cilium/cilium v1.19.4
#   Argo CD            argoproj/argo-cd v3.3.4
#   vault-operator     PRO-Robotech/vault-operator @5f0fd9f4 (ревизия образа стенда; та же, что hack/crds.lock)
#   Envoy Gateway      envoyproxy/gateway v1.8.3 (выпуск, envoy-gateway-crds.yaml: восемь CRD
#                      gateway.envoyproxy.io; версия контроллера Envoy Gateway стенда; политики
#                      ClientTrafficPolicy и BackendTrafficPolicy чарта registry-platform)
set -euo pipefail
dir="${1:?каталог}"; mkdir -p "$dir"

ADDONS=https://raw.githubusercontent.com/PRO-Robotech/addons-operator/22b73fde37e6f824a86fd8ee33498f427798989e/config/crd/bases
GW=https://raw.githubusercontent.com/kubernetes-sigs/gateway-api/v1.5.1/config/crd/standard
CILIUM=https://raw.githubusercontent.com/cilium/cilium/v1.19.4/pkg/k8s/apis/cilium.io/client/crds/v2
ARGO=https://raw.githubusercontent.com/argoproj/argo-cd/v3.3.4/manifests/crds
VAULT=https://raw.githubusercontent.com/PRO-Robotech/vault-operator/5f0fd9f4723e69dfd565b533a8901c87f46c6dca/config/crd/bases
EG=https://github.com/envoyproxy/gateway/releases/download/v1.8.3

# имя файла · URL · sha256
while read -r name url sum; do
  [ -n "$name" ] || continue
  curl -fsSL --retry 3 --max-time 60 -o "$dir/$name" "$url"
  got="$(sha256sum "$dir/$name" | cut -d' ' -f1)"
  if [ "$got" != "$sum" ]; then
    echo "crds: $name: sha256 $got, expected $sum ($url)" >&2
    exit 1
  fi
done <<EOF
addons.yaml        $ADDONS/addons.in-cloud.io_addons.yaml                 c6e13b51a39078f03ce7b9a4ddc52daf56cd6cc3ace28a4bcd75b007732c7b7b
addonvalues.yaml   $ADDONS/addons.in-cloud.io_addonvalues.yaml            fcd50e7cf665daf002e923fd56eba5313050387c3223cb29fca25501de4cd1c7
addonphases.yaml   $ADDONS/addons.in-cloud.io_addonphases.yaml            cc469791cb23f46a97fd3be77292ff87b8ffea7f3e6cb69bdf95aa9afb83d6f8
addontemplates.yaml $ADDONS/addons.in-cloud.io_addontemplates.yaml        6c69811284377b0ce474b934982c6e689f225b7829e406933625534109d54129
gateways.yaml      $GW/gateway.networking.k8s.io_gateways.yaml            7416e8300eb4d8c0a71a23445609075bbacbe8bc80104584b019cfe1ebe77c31
httproutes.yaml    $GW/gateway.networking.k8s.io_httproutes.yaml          682203b109baecb18d38073a578f7bc2c17d22fe9b0c27bf030850abe014c370
referencegrants.yaml $GW/gateway.networking.k8s.io_referencegrants.yaml   a8902c486eed0b7339f5bb8a476ccaba0918eb038048402bdc01cbb69b0fbb51
cnp.yaml           $CILIUM/ciliumnetworkpolicies.yaml                     1b1738a904de1152c43078e6a873440aea100f30f10ce5ed4e8622524c13fa43
ccnp.yaml          $CILIUM/ciliumclusterwidenetworkpolicies.yaml          ae742b7ad66ddb4af752b6ae9787f2d3bc4597e3f29df80417967767f9bf2a79
appprojects.yaml   $ARGO/appproject-crd.yaml                              7b99337686905b444864c84b1305938a7bfbca9b5f4e2c5d5d2de4acae16ceb3
applications.yaml  $ARGO/application-crd.yaml                             ea5e7cf5facd3ed70fe25acb048a495e5f2f893bfc961a02de5cb741b9d630f6
vaultclaims.yaml   $VAULT/vault.in-cloud.io_vaultclaims.yaml              51eacf1c4dc5b0b18348876436ab9b5ec385814126a461629d9655c797363b65
s3bucketclaims.yaml $VAULT/vault.in-cloud.io_s3bucketclaims.yaml          cbc6bb033bfb24bfdb9bd79bebe501bd7e62b7d2a75f9f23099ef598fa606e61
envoy-gateway.yaml $EG/envoy-gateway-crds.yaml                         23f0913f200abd1d7c634c3fb2c3eb6021cc4e84bdd75d3296711b9334472abb
EOF
echo "crds: $(ls "$dir" | wc -l) files verified"
