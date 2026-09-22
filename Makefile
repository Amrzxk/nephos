# Nephos build entry points.
#
# Run everything inside WSL (or native Linux), never from PowerShell, and keep
# the working copy on ext4 rather than under /mnt/c or /mnt/f. See
# docs/DEVELOPMENT.md.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

# CGO is disabled everywhere, without exception (ADR-0002). The pure-Go SQLite
# driver is what makes the release matrix cross-compilable.
export CGO_ENABLED := 0

GO              ?= go
GOLANGCI_LINT   ?= $(shell go env GOPATH)/bin/golangci-lint
GOLANGCI_VERSION ?= v2.13.2

BIN_DIR   := bin
DIST_DIR  := dist
CMDS      := nephos nephosd nephos-hook

# An untagged working tree reports "dev" rather than a bare SHA, so that
# version.Info.IsRelease() stays honest about what produced the binary.
VERSION ?= $(shell git describe --tags --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

VERSION_PKG := github.com/Amrzxk/nephos/internal/version
LDFLAGS := -s -w \
	-X '$(VERSION_PKG).version=$(VERSION)' \
	-X '$(VERSION_PKG).commit=$(COMMIT)' \
	-X '$(VERSION_PKG).date=$(DATE)'

# The six release targets (ROADMAP M0).
CROSS_PLATFORMS := \
	linux/amd64 linux/arm64 \
	darwin/amd64 darwin/arm64 \
	windows/amd64 windows/arm64

.PHONY: help
help: ## List the available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build every binary for the host platform into bin/
	@mkdir -p $(BIN_DIR)
	@for cmd in $(CMDS); do \
		echo "  build  $$cmd"; \
		$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$$cmd ./cmd/$$cmd; \
	done

.PHONY: test
test: ## Run the unit and golden tests
	$(GO) test -count=1 ./...

# The race detector needs cgo, which the CGO_ENABLED=0 rule in ADR-0002 does not
# forbid here: that rule governs shipped binaries, so that release tooling can
# cross-compile all six targets. A race-detector test run is not a shipped
# binary, only ever runs on Linux CI, and is how RISKS T8 (goroutines migrating
# between network namespaces) gets caught. Do not "fix" this by dropping -race.
.PHONY: test-race
test-race: ## Run the tests under the race detector (Linux, cgo required)
	CGO_ENABLED=1 $(GO) test -race -count=1 ./...

.PHONY: test-update
test-update: ## Regenerate golden files
	$(GO) test -count=1 ./... -update

.PHONY: fmt
fmt: ## Format the tree
	$(GO) fmt ./...

.PHONY: fmt-check
fmt-check: ## Fail if anything is unformatted (CI)
	@unformatted=$$(gofmt -l . | grep -v '^vendor/' || true); \
	if [ -n "$$unformatted" ]; then \
		echo "These files are not gofmt'd:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: lint
lint: ## Run golangci-lint
	@command -v $(GOLANGCI_LINT) >/dev/null 2>&1 || { \
		echo "golangci-lint not found; install $(GOLANGCI_VERSION) with:"; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)"; \
		exit 1; }
	$(GOLANGCI_LINT) run

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: tidy
tidy: ## Tidy and verify the module graph
	$(GO) mod tidy
	$(GO) mod verify

.PHONY: cross
cross: ## Cross-build every release target with CGO disabled
	@mkdir -p $(DIST_DIR)
	@for platform in $(CROSS_PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=""; if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		for cmd in $(CMDS); do \
			: "nephosd and nephos-hook run inside the appliance, so they are Linux-only."; \
			if [ "$$cmd" != "nephos" ] && [ "$$os" != "linux" ]; then continue; fi; \
			out="$(DIST_DIR)/$${cmd}_$${os}_$${arch}$${ext}"; \
			echo "  cross  $$out"; \
			GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o "$$out" ./cmd/$$cmd; \
		done; \
	done

.PHONY: generate
generate: ## Regenerate code from api/openapi.yaml
	$(GO) tool oapi-codegen -config api/server.cfg.yaml api/openapi.yaml
	$(GO) tool oapi-codegen -config api/client.cfg.yaml api/openapi.yaml

.PHONY: generate-check
generate-check: ## Fail if generated code is stale (CI)
	@tmpdir=$$(mktemp -d); trap 'rm -rf "$$tmpdir"' EXIT; \
		cp internal/apiserver/generated/server.gen.go "$$tmpdir/server.gen.go"; \
		cp pkg/client/client.gen.go "$$tmpdir/client.gen.go"; \
		$(MAKE) generate; \
		cmp -s "$$tmpdir/server.gen.go" internal/apiserver/generated/server.gen.go \
			&& cmp -s "$$tmpdir/client.gen.go" pkg/client/client.gen.go \
			|| { echo "Generated code is stale. Run 'make generate' and commit the result."; exit 1; }

.PHONY: web
web: ## Build the web console
	@echo "web: not until M6 (ADR-0009)." && exit 1

.PHONY: appliance
appliance: ## Build the appliance image
	@echo "appliance: not until M1 (ADR-0003)." && exit 1

.PHONY: e2e
e2e: ## Run the end-to-end suite (needs Docker and a privileged container)
	@echo "e2e: not until M1." && exit 1

.PHONY: ci
ci: fmt-check vet lint test test-race cross ## Everything CI runs on a pull request

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BIN_DIR) $(DIST_DIR)
