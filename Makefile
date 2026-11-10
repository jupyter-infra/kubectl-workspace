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
