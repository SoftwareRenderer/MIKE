# MIKE: Integrated Kanban Environment

VERSION  ?= dev
GIT_VER  := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^V/v/')
ifeq ($(VERSION),dev)
VERSION  := $(or $(GIT_VER),dev)
endif
LDFLAGS  := -X kanban/backend/server.Version=$(VERSION)

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

.PHONY: build
build: frontend ## Build binary with version injected + frontend
	go build -trimpath -ldflags "$(LDFLAGS)" -o kanban .

.PHONY: build-backend
build-backend: ## Build Go binary only (no frontend rebuild)
	go build -trimpath -ldflags "$(LDFLAGS)" -o kanban .

.PHONY: frontend
frontend: ## Build frontend (Vite)
	cd frontend && npm run build

# The sandbox-bridge is a second binary: static, linux-only, and it runs INSIDE every
# sandbox container. It is committed at sandbox/sandbox-bridge, so an artifact that was not
# rebuilt after an edit is an edit that never runs — silently, because the server and the
# sandbox both start fine and only the bridge behaves older.
#
# `make bridge` records what it built from; `make bridge-check` refuses a binary whose
# sources have moved. The stamp hashes the bridge's inputs (module sources plus
# go.mod/go.sum), not the artifact: a toolchain upgrade changes every byte of the binary
# while changing none of those, and a check that cries wolf gets ignored.
# Stamp logic + its test: scripts/bridge-stamp.sh, scripts/bridge-stamp_test.sh.
BRIDGE_BIN   := sandbox/sandbox-bridge
BRIDGE_STAMP := $(BRIDGE_BIN).srcs

.PHONY: bridge
bridge: ## Build the sandbox-bridge binary committed at sandbox/sandbox-bridge
	CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o $(BRIDGE_BIN) ./pkg/bridge/agent
	@scripts/bridge-stamp.sh record $(BRIDGE_STAMP)

.PHONY: bridge-check
bridge-check: ## Fail if sandbox/sandbox-bridge was not built from this tree's sources
	@scripts/bridge-stamp.sh check $(BRIDGE_STAMP)

# ---------------------------------------------------------------------------
# Test
# ---------------------------------------------------------------------------

.PHONY: test
test: test-go test-frontend ## Run all tests (Go unit + frontend unit)

.PHONY: test-go
test-go: ## Run Go unit tests (~60s, parallel)
	go test ./backend/... ./pkg/... -count=1 -parallel 8

# The executor, the stream manager and the sandbox/terminal layers run ~120 detached
# goroutines between them, which is exactly the shape where a race hides for months.
# -parallel is lower than test-go because the race detector multiplies the work per
# test, and the sandbox packages need a working podman + runsc on top of it.
.PHONY: test-go-race
test-go-race: ## Run Go unit tests under the race detector (~4min)
	go test -race ./backend/... ./pkg/... -count=1 -parallel 4

.PHONY: test-frontend
test-frontend: ## Run frontend unit tests + the i18n no-language gate (vitest, ~10s)
	cd frontend && npm test && npm run check:i18n

.PHONY: test-e2e
test-e2e: ## Run e2e tests (Playwright, manages its own server)
	cd frontend && npx playwright test

# ---------------------------------------------------------------------------
# Dev
# ---------------------------------------------------------------------------

# Dev defaults for `make run` — exporting your own vars overrides these.
# (MIKE_ENCRYPTION_KEY must match the key that encrypted values in
# data/kanban.db if you need stored secrets to decrypt.)
MIKE_ENCRYPTION_KEY ?= dev-local-key
MIKE_LOGIN_PASSWORD ?= test
MIKE_TLS ?= 0

.PHONY: run
run: ## Start dev server (background, no TLS, dev defaults)
	MIKE_ENCRYPTION_KEY=$(MIKE_ENCRYPTION_KEY) MIKE_LOGIN_PASSWORD=$(MIKE_LOGIN_PASSWORD) MIKE_TLS=$(MIKE_TLS) ./kanban &

.PHONY: stop
stop: ## Stop dev server (exact process name; -f would match any path containing */kanban)
	pkill -x kanban

# ---------------------------------------------------------------------------
# Clean
# ---------------------------------------------------------------------------

.PHONY: clean
clean: ## Remove build artifacts
	rm -f kanban
	rm -rf frontend/dist
	rm -f data/kanban.trace data/kanban.trace.old

# ---------------------------------------------------------------------------
# Release
# ---------------------------------------------------------------------------

.PHONY: release
release: ## Build release binary with git tag as version (set VERSION=vX.Y.Z)
	@echo "Building version $(VERSION)"
	cd frontend && npm run build
	go build -trimpath -ldflags "$(LDFLAGS)" -o kanban .

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
