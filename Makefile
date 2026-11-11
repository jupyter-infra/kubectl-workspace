# Default target
.DEFAULT_GOAL := help

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Container tool for the end-to-end rig (finch or docker).
CONTAINER_TOOL ?= docker

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: build
build: ## Build bin/kubectl-workspace for this machine.
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/kubectl-workspace ./cmd/kubectl-workspace

.PHONY: test
test: ## Run the tests with the race detector.
	go test -race ./...

.PHONY: test-e2e
test-e2e: ## Run ssh and sftp through the plugin against the published sidecar and SSH server images (needs $(CONTAINER_TOOL), ssh and sftp).
	CONTAINER_TOOL=$(CONTAINER_TOOL) go test -tags e2e -count=1 -timeout 15m -v ./test/e2e/

##@ Kind end-to-end rig: the operator with token signing on, the auth middleware and Traefik

KIND ?= kind
E2E_KIND_CLUSTER ?= kubectl-workspace-e2e
E2E_OPERATOR_CHART_VERSION ?= 0.4.1
E2E_CERT_MANAGER_CHART_VERSION ?= v1.21.3
E2E_TRAEFIK_CHART_VERSION ?= 42.3.2
E2E_WORKSPACE_IMAGE ?= kubectl-workspace-e2e-workspace:local
ifeq ($(CONTAINER_TOOL),finch)
export KIND_EXPERIMENTAL_PROVIDER = finch
endif
E2E_KUBECTL := kubectl --context kind-$(E2E_KIND_CLUSTER)
E2E_HELM := helm --kube-context kind-$(E2E_KIND_CLUSTER)

.PHONY: setup-e2e-kind
setup-e2e-kind: ## Create the Kind cluster with cert-manager, the operator, the auth middleware and Traefik, and load the workspace image.
	@$(KIND) get clusters 2>/dev/null | grep -qx '$(E2E_KIND_CLUSTER)' || $(KIND) create cluster --name $(E2E_KIND_CLUSTER) --wait 90s
	$(E2E_HELM) upgrade --install cert-manager cert-manager --repo https://charts.jetstack.io --version $(E2E_CERT_MANAGER_CHART_VERSION) \
		--namespace cert-manager --create-namespace --set crds.enabled=true --set startupapicheck.enabled=false \
		--wait --timeout 180s
	$(E2E_HELM) upgrade --install jupyter-k8s oci://ghcr.io/jupyter-infra/charts/jupyter-k8s --version $(E2E_OPERATOR_CHART_VERSION) \
		--namespace jupyter-k8s-system --create-namespace --set extensionApi.jwtSecret.enable=true --wait --timeout 240s
	$(E2E_KUBECTL) wait --for=condition=Available --timeout=180s apiservice/v1alpha1.connection.workspace.jupyter.org
	$(E2E_KUBECTL) apply -f test/e2e/testdata/kind/authmiddleware.yaml
	$(E2E_HELM) upgrade --install traefik traefik --repo https://traefik.github.io/charts --version $(E2E_TRAEFIK_CHART_VERSION) \
		--namespace traefik --create-namespace \
		--set service.spec.type=ClusterIP --set providers.kubernetesCRD.allowCrossNamespace=true \
		--set tlsStore.default.defaultCertificate.secretName=traefik-e2e-tls --wait --timeout 180s
	$(E2E_KUBECTL) apply -f test/e2e/testdata/kind/traefik-tls.yaml
	$(E2E_KUBECTL) -n traefik wait --for=condition=Ready --timeout=120s certificate/traefik-e2e
	$(E2E_KUBECTL) -n jupyter-k8s-system rollout status deployment/authmiddleware --timeout=180s
	$(CONTAINER_TOOL) build -t $(E2E_WORKSPACE_IMAGE) -f test/e2e/testdata/workspace.Dockerfile test/e2e/testdata
	mkdir -p bin
	$(CONTAINER_TOOL) save $(E2E_WORKSPACE_IMAGE) -o bin/e2e-workspace.tar
	$(KIND) load image-archive bin/e2e-workspace.tar --name $(E2E_KIND_CLUSTER)

.PHONY: test-e2e-kind
test-e2e-kind: ## Run the plugin against the Kind rig through the operator, the auth middleware and Traefik (after setup-e2e-kind).
	E2E_KIND_CLUSTER=$(E2E_KIND_CLUSTER) go test -tags e2e -count=1 -timeout 20m -v ./test/e2e/kind/

.PHONY: cleanup-e2e-kind
cleanup-e2e-kind: ## Delete the Kind cluster of the end-to-end rig.
	$(KIND) delete cluster --name $(E2E_KIND_CLUSTER)

.PHONY: test-e2e-kind-full
test-e2e-kind-full: ## setup-e2e-kind, test-e2e-kind and cleanup-e2e-kind in one go; on a failure the cluster's logs land in bin/kind-logs first.
	$(MAKE) setup-e2e-kind
	$(MAKE) test-e2e-kind || { $(KIND) export logs bin/kind-logs --name $(E2E_KIND_CLUSTER); $(MAKE) cleanup-e2e-kind; exit 1; }
	$(MAKE) cleanup-e2e-kind

.PHONY: lint
lint: ## Run golangci-lint.
	golangci-lint run

.PHONY: lint-fix
lint-fix: ## Run golangci-lint and apply its fixes.
	golangci-lint run --fix

.PHONY: review
review: ## Review the branch against main with roborev, locally.
	@command -v roborev >/dev/null 2>&1 || { echo "roborev not found. Install it from https://roborev.io."; exit 1; }
	roborev review --branch --local --wait

.PHONY: deps
deps: ## Download and tidy the module dependencies.
	go mod download
	go mod tidy
