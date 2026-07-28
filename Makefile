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

.PHONY: check
check: ## Run TypeScript type check
	cd frontend && npm run check

# ---------------------------------------------------------------------------
# Test
# ---------------------------------------------------------------------------

.PHONY: test
test: test-go test-frontend ## Run all tests (Go unit + frontend unit)

.PHONY: test-go
test-go: ## Run Go unit tests (~60s, parallel)
	go test ./backend/... ./pkg/... -count=1 -parallel 8

.PHONY: test-frontend
test-frontend: ## Run frontend unit tests (vitest, ~1s)
	cd frontend && npm test

.PHONY: test-e2e
test-e2e: ## Run e2e tests (Playwright, manages its own server)
	cd frontend && npx playwright test

# ---------------------------------------------------------------------------
# Dev
# ---------------------------------------------------------------------------

.PHONY: run
run: ## Start dev server (background, no TLS)
	LOGIN_PASSWORD=test KANBAN_TLS=0 ./kanban &

.PHONY: stop
stop: ## Stop dev server
	pkill -f "./kanban"

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
