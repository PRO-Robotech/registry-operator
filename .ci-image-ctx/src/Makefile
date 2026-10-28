# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0

IMG_OPERATOR ?= prorobotech/registry-operator:dev
IMG_PROXY ?= prorobotech/registry-iam-proxy:dev
CONTAINER_TOOL ?= docker

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

CONTROLLER_TOOLS_VERSION ?= v0.23.0
ENVTEST_VERSION ?= release-0.25
ENVTEST_K8S_VERSION ?= 1.37.x
GOLANGCI_LINT_VERSION ?= v2.12.2
KIND_VERSION ?= v0.33.0

CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen-$(CONTROLLER_TOOLS_VERSION)
ENVTEST ?= $(LOCALBIN)/setup-envtest-$(ENVTEST_VERSION)
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)
KIND ?= $(LOCALBIN)/kind-$(KIND_VERSION)

.PHONY: all
all: build

##@ Генерация

.PHONY: manifests
manifests: $(CONTROLLER_GEN) ## CRD и ClusterRole из маркеров.
	@mkdir -p "$(LOCALBIN)/generated-crds"
	"$(CONTROLLER_GEN)" crd paths="./api/..." output:crd:artifacts:config="$(LOCALBIN)/generated-crds"
	@mkdir -p config/crd/bases
	@set -eu; for kind in registries registrytemplates registryobserveresourcetemplates; do \
		cp "$(LOCALBIN)/generated-crds/registry.in-cloud.io_$${kind}.yaml" config/crd/bases/; \
	done
	@test "$$(find config/crd/bases -maxdepth 1 -name '*.yaml' | wc -l)" -eq 3 || { echo "unexpected installed CRD inventory"; exit 1; }
	"$(CONTROLLER_GEN)" rbac:roleName=registry-operator paths="./internal/..." output:rbac:artifacts:config=charts/registry-operator/files
	rm -rf charts/registry-operator/files/crds && mkdir -p charts/registry-operator/files/crds
	cp config/crd/bases/*.yaml charts/registry-operator/files/crds/
	@grep -q apiGroups charts/registry-operator/files/role.yaml || { echo "manifests: role.yaml has no rules"; exit 1; }

.PHONY: generate
generate: $(CONTROLLER_GEN) ## DeepCopy.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

.PHONY: verify-generated
verify-generated: manifests generate ## Сгенерированное совпадает с закоммиченным.
	@git diff --exit-code -- api config charts/registry-operator/files && \
		test -z "$$(git status --porcelain --untracked-files=all -- api config charts/registry-operator/files)" || { \
		git status --short --untracked-files=all -- api config charts/registry-operator/files; \
		echo "generated files are stale: run make manifests generate and commit"; exit 1; }
	@test ! -e charts/registry-operator/crds || { \
		echo "charts/registry-operator/crds: CRD live in files/crds (templates/crds.yaml); Helm installs crds/ as is, unlabelled"; exit 1; }

##@ Пробы

.PHONY: crds
crds: ## Целевые CRD проб envtest по hack/crds.lock.
	hack/fetch-crds.sh "$(LOCALBIN)/crds"

.PHONY: test
test: crds $(ENVTEST) ## Unit и envtest (с -race).
	KUBEBUILDER_ASSETS="$$("$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" \
	REGOP_TEST_CRDS="$(LOCALBIN)/crds" \
	go test -race -count=1 -v $$(go list ./... | grep -v /test/e2e) -coverprofile cover.out

.PHONY: inject
inject: crds $(ENVTEST) ## Таблица инъекций: каждая проба способна упасть.
	KUBEBUILDER_ASSETS="$$("$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" \
	REGOP_TEST_CRDS="$(LOCALBIN)/crds" python3 hack/inject.py

.PHONY: lint
lint: $(GOLANGCI_LINT) ## golangci-lint.
	"$(GOLANGCI_LINT)" run ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: e2e
e2e: $(KIND) ## Пробы на kind (образы собираются и грузятся в кластер).
	KIND="$(KIND)" IMG_OPERATOR="$(IMG_OPERATOR)" IMG_PROXY="$(IMG_PROXY)" \
	go test -tags=e2e -count=1 -timeout 30m ./test/e2e/ -v

.PHONY: access-matrix-census
access-matrix-census: ## Набор матрицы доступа: разбор синтаксиса и перепись «реализовано K из M» без стенда.
	@n=0; for f in $$(git ls-files -- e2e/access-matrix/run.sh "e2e/access-matrix/*.sh"); do \
		bash -n "$$f" || exit 1; n=$$((n + 1)); done; \
		echo "access-matrix: bash -n over $$n files"; test "$$n" -gt 0
	e2e/access-matrix/run.sh --census

.PHONY: access-matrix-inject
access-matrix-inject: ## Инъекции набора матрицы доступа без стенда (окно темпа, область записи, порядок прогона, фазы полного прогона); OLD=<ревизия> — тот же опыт на файлах той ревизии.
	@if [ -n "$(OLD)" ]; then d=$$(mktemp -d) && trap 'rm -rf "$$d"' EXIT && rc=0 && \
		git show "$(OLD):e2e/access-matrix/cases/s10-enroll.sh" > "$$d/s10" && { e2e/access-matrix/inject-rate-window.sh "$$d/s10" || rc=1; } && \
		{ git show "$(OLD):e2e/access-matrix/lib/fixtures.sh" > "$$d/fx" && e2e/access-matrix/inject-write-scope.sh "$$d/fx" || rc=1; } && \
		{ git show "$(OLD):e2e/access-matrix/run.sh" > "$$d/run" && e2e/access-matrix/inject-order.sh "$$d/run" || rc=1; } && \
		{ git show "$(OLD):e2e/access-matrix/phases.sh" > "$$d/ph" 2>/dev/null && e2e/access-matrix/inject-phases.sh "$$d/ph" || rc=1; }; exit $$rc; \
	else rc=0; e2e/access-matrix/inject-rate-window.sh || rc=1; e2e/access-matrix/inject-write-scope.sh || rc=1; \
		e2e/access-matrix/inject-order.sh || rc=1; e2e/access-matrix/inject-phases.sh || rc=1; exit $$rc; fi

##@ Сборка

.PHONY: build
build:
	CGO_ENABLED=0 go build -o $(LOCALBIN)/registry-operator ./cmd/registry-operator
	CGO_ENABLED=0 go build -o $(LOCALBIN)/iam-proxy ./cmd/iam-proxy

.PHONY: docker-build
docker-build: ## Оба образа.
	$(CONTAINER_TOOL) build -t $(IMG_OPERATOR) -f Dockerfile .
	$(CONTAINER_TOOL) build -t $(IMG_PROXY) -f Dockerfile.iam-proxy .

##@ Инструменты

.PHONY: kind
kind: $(KIND) ## kind закреплённой версии в bin/.

$(CONTROLLER_GEN): | $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

$(ENVTEST): | $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

$(GOLANGCI_LINT): | $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

$(KIND): | $(LOCALBIN)
	$(call go-install-tool,$(KIND),sigs.k8s.io/kind,$(KIND_VERSION))

# go-install-tool — инструмент закреплённой версии под именем с версией.
define go-install-tool
@[ -f "$(1)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "installing $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(2)")" "$(1)" ;\
}
endef
