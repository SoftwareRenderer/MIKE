# MIKE: Integrated Kanban Environment

# The version stamped into a binary (`/api/version`) and used in a package filename. Its source is a
# git tag: `git tag v1.2.3 && make package`. With no tag `git describe` answers a commit hash, which
# is not a dotted number, so packages fall back to 0.0.0; `make package VERSION=v1.2.3` overrides.
VERSION  ?= dev
GIT_VER  := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^V/v/')
ifeq ($(VERSION),dev)
VERSION  := $(or $(GIT_VER),dev)
endif
LDFLAGS  := -X mike/backend/server.Version=$(VERSION)

# A build someone other than the developer runs: reads no frontend directory and refuses
# --frontend-dist / MIKE_FRONTEND_DIST. `make check-release` keeps this variant compiled.
RELEASE_TAGS := release

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

.PHONY: build
build: frontend ## Build binary with version injected + frontend
	go build -trimpath -ldflags "$(LDFLAGS)" -o mike .

.PHONY: build-backend
build-backend: ## Build Go binary only (no frontend rebuild)
	go build -trimpath -ldflags "$(LDFLAGS)" -o mike .

.PHONY: frontend
frontend: ## Build frontend (Vite)
	cd frontend && npm run build
	@# Vite empties the output directory; one file must stay inside it, or `go:embed` matches nothing.
	@touch frontend/dist/.gitkeep

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
# Scope
# ---------------------------------------------------------------------------

# The package scope every Go command here uses. The trailing `.` is load-bearing: the root package
# holds main.go and the startup-config tests, and `./backend/... ./pkg/...` skipped them while
# `make test` still printed green. `./...` is never used - it fails at HEAD on the pulled artifacts
# under extensions/, which are not this tree.
GO_SCOPE := ./backend/... ./pkg/... .

# Every Go file this repo owns, and only those. Not `.`: that also walks extensions/ (a pulled
# artifact from another module) and node_modules/, and would recurse into backend/ and pkg/ and
# report each violation twice.
# The root package is listed file by file: `.` would walk extensions/ (pulled artifacts that do not
# format) and frontend/. Every file in it belongs here — a root file left out drifts while this step prints clean.
GOFMT_DIRS := backend pkg scripts $(wildcard *.go)

# ---------------------------------------------------------------------------
# Test
# ---------------------------------------------------------------------------

.PHONY: test
test: test-go test-frontend ## Run all tests (Go unit + frontend unit)

.PHONY: test-go
test-go: ## Run Go unit tests (~60s, parallel)
	go test $(GO_SCOPE) -count=1 -parallel 8

# The executor, stream manager and sandbox/terminal layers run ~120 detached goroutines between them.
# -parallel is lower than test-go because the race detector multiplies the work per test, and the
# sandbox packages need a working podman + runsc on top of it.
.PHONY: test-go-race
test-go-race: ## Run Go unit tests under the race detector (~4min)
	go test -race $(GO_SCOPE) -count=1 -parallel 4

.PHONY: test-frontend
test-frontend: ## Run frontend unit tests + the gates (vitest, ~10s)
	cd frontend && npm test && npm run check:i18n && npm run check:i18n-surface-test && npm run check:i18n-surface && npm run check:sinks-test && npm run check:sinks
	@echo "==> knip (unused files, exports, types, duplicates, dependencies)"
	cd frontend && npm run check:dead
	@echo "==> unused declarations (noUnusedLocals + noUnusedParameters)"
	cd frontend && npm run check:unused

.PHONY: test-sinks
test-sinks: ## Fail if an HTML sink in core is unlisted, renders a translation, or lost the mechanism it declares (~1s)
	cd frontend && npm run check:sinks-test && npm run check:sinks

.PHONY: test-i18n-surface
test-i18n-surface: ## Fail if any user-facing string is still a core literal (~5s)
	cd frontend && npm run check:i18n-surface

.PHONY: test-e2e
test-e2e: ## Run e2e tests (Playwright, manages its own server)
	cd frontend && npx playwright test

# A locale pack fails silently in both directions, so neither failure shows up in the UI: a
# missing message key falls back to English per key (a pack missing 300 keys still looks
# translated), and a prompt overlay that renames a placeholder or drops a {{if}} only fails when
# the executor renders that prompt inside a task. Both are measured instead.
.PHONY: i18n-parity
i18n-parity: ## Check a locale pack against the English contract (make i18n-parity PACK=../MIKE-extensions/i18n/fr)
	go run ./scripts/i18nparity $(if $(PACK),--pack $(PACK),) $(if $(STRICT),--strict,)

# Structural debt, measured rather than argued about: HTTP decisions outside the transport packages,
# environment reads below startup, hand-written keyed lock tables, routes registered without a
# method, a route no caller reaches, a state write on a cancellable GET, the app-stream event
# vocabulary, and the shape of the executor's split: who may build it, what a collaborator may hold,
# how long a function may get.
# Every rule runs against a frozen baseline, so the count only shrinks and a fixed violation must be
# shrunk out of the register. scripts/archgate holds the reasons, the commands that re-measure them, and
# the derivations that turned out to be wrong.
.PHONY: archgate
archgate: ## Report structural debt against its frozen baselines (~0.2s)
	@go run ./scripts/archgate $(if $(RULE),--rule $(RULE),) $(if $(UPDATE),-update,)

.PHONY: test-archgate
test-archgate: ## Test the structural gate itself, both directions (~0.2s)
	@go test ./scripts/archgate/ -count=1

.PHONY: test-i18n-parity
test-i18n-parity: ## Run the i18n parity checker's own tests (~0.2s)
	go test ./scripts/i18nparity/ -count=1

# Documentation has the same failure mode as code: a pointer that names a heading the last trim
# deleted, an environment variable that was renamed, a line number that now points at a blank
# line. scripts/docs-check.py measures that instead of arguing about it, and its register
# (scripts/docscheck/baseline.json) is a ceiling like archgate's baselines — `make docs-check
# UPDATE=1` re-freezes it, and only on purpose.
.PHONY: docs-check
docs-check: ## Check the docs: pointers, indexes, line refs, env table, ADR shape, ratchet (~0.2s)
	@python3 scripts/docs-check.py $(if $(UPDATE),--update,)

.PHONY: test-docs-check
test-docs-check: ## Test the documentation gate itself, both directions (~0.2s)
	@python3 scripts/docs-check_test.py

# ---------------------------------------------------------------------------
# Quality gate
# ---------------------------------------------------------------------------

# AGENTS.md tells every agent to run gofmt, scoped go vet, staticcheck and deadcode - and nothing in
# the repo ran any of them. This target is the gate; `make check` is what "done" means.
#
# Order is cost, cheapest first. None of these may skip: scripts/lint-tools.sh turns a missing linter
# into an error carrying its install command, because a gate that passes an unrun check is the same
# false "done" with better formatting.
.PHONY: lint
lint: ## gofmt + scoped vet + staticcheck (all checks) + deadcode (both roots)
	@echo "==> gofmt"
	@# stderr is folded in on purpose: gofmt reports an unreadable path on stderr and exits non-zero,
	@# while the list on stdout stays empty — so a deleted or renamed entry used to leave this step
	@# printing nothing and passing, with that path unchecked. Anything gofmt prints is a failure.
	@unformatted=$$(gofmt -l $(GOFMT_DIRS) 2>&1); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt reported:"; echo "$$unformatted" | sed 's/^/    /'; \
		echo "    fix with: gofmt -w <the paths listed> (a path it cannot read is a GOFMT_DIRS entry that no longer exists)"; \
		exit 1; \
	fi
	@echo "==> go vet"
	@go vet $(GO_SCOPE)
	@echo "==> staticcheck -checks=all"
	@$$(scripts/lint-tools.sh path staticcheck) -checks=all $(GO_SCOPE)
	@echo "==> deadcode"
	@scripts/deadcode-check.sh

.PHONY: check-release
check-release: ## Compile and test the -tags release variant that make package ships (~5s)
	@# Nothing else here compiles a `-tags` variant, so without this step the release build rots.
	@echo "==> go build -tags $(RELEASE_TAGS)"
	@go build -tags $(RELEASE_TAGS) -trimpath -o /dev/null .
	@echo "==> go vet -tags $(RELEASE_TAGS)"
	@go vet -tags $(RELEASE_TAGS) ./backend/startup/ .
	@echo "==> go test -tags $(RELEASE_TAGS) ./backend/startup/"
	@go test -tags $(RELEASE_TAGS) ./backend/startup/ -count=1

.PHONY: check
check: ## THE GATE: lint + archgate + docs-check + bridge-check + build + Go tests + frontend gates
	@$(MAKE) --no-print-directory lint
	@echo "==> archgate (structural debt against its baselines)"
	@$(MAKE) --no-print-directory archgate
	@$(MAKE) --no-print-directory test-archgate
	@echo "==> docs-check (a dead pointer is a rule nobody can follow)"
	@$(MAKE) --no-print-directory docs-check
	@$(MAKE) --no-print-directory test-docs-check
	@echo "==> bridge-check (a stale sandbox-bridge is an edit that never runs)"
	@$(MAKE) --no-print-directory bridge-check
	@echo "==> build"
	@go build -trimpath -o mike .
	@$(MAKE) --no-print-directory check-release
	@echo "==> go test"
	@$(MAKE) --no-print-directory test-go
	@echo "==> svelte-check"
	@cd frontend && npm run check
	@echo "==> frontend unit tests + gates"
	@$(MAKE) --no-print-directory test-frontend
	@echo ""
	@echo "make check: all gates green"

# The packages that own the detached goroutines and the executor's lock discipline. A full -race
# sweep is ~4min and would not get run; these are where races have actually hidden. AGENTS.md: "A
# green ./backend/exec/ is not proof" - the executor's locking bugs surface only under -race in
# ./backend/server/, so both are here.
RACE_PKGS := ./backend/git/ ./backend/exec/ ./backend/server/ ./backend/stream/ ./backend/api/ ./pkg/logging/

.PHONY: check-race
check-race: ## -race over the executor/stream/terminal layers (~2min) — required for any concurrency change
	@echo "==> go test -race $(RACE_PKGS)"
	@go test -race $(RACE_PKGS) -count=1 -parallel 4

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
	MIKE_ENCRYPTION_KEY=$(MIKE_ENCRYPTION_KEY) MIKE_LOGIN_PASSWORD=$(MIKE_LOGIN_PASSWORD) MIKE_TLS=$(MIKE_TLS) ./mike &

.PHONY: stop
stop: ## Stop dev server (exact process name; -f would match any path containing */mike)
	pkill -x mike

# ---------------------------------------------------------------------------
# Clean
# ---------------------------------------------------------------------------

.PHONY: clean
clean: ## Remove build artifacts
	rm -f mike mike-server
	rm -rf frontend/dist
	rm -rf dist
	@# Recreated: `go:embed` of the SPA needs one file inside frontend/dist.
	@mkdir -p frontend/dist && touch frontend/dist/.gitkeep
	rm -f data/kanban.trace data/kanban.trace.old

# ---------------------------------------------------------------------------
# Release
# ---------------------------------------------------------------------------

# The version a package filename carries: a dotted number from VERSION, else 0.0.0 — a commit hash is
# not a name for a download.
pkg_version = $(shell printf '%s' '$(VERSION)' | grep -Eq '^[Vv]?[0-9]+(\.[0-9]+)*$$' && printf '%s' '$(VERSION)' | tr -d 'Vv' || printf 0.0.0)
PKG_VERSION ?= $(call pkg_version)

# The sandbox needs Linux, podman and gVisor, so these are the only two targets.
PACKAGE_TARGETS := linux/amd64 linux/arm64

.PHONY: release
release: ## Build release binary with git tag as version (set VERSION=vX.Y.Z)
	@echo "Building version $(VERSION) (release: no frontend directory)"
	cd frontend && npm run build
	go build -tags $(RELEASE_TAGS) -trimpath -ldflags "$(LDFLAGS)" -o mike .

.PHONY: package
package: frontend ## Build dist/mike-server-<version>-<os>-<arch>.tar.gz for every target + sha256sum.txt
	@VERSION=$(PKG_VERSION) RELEASE_TAGS=$(RELEASE_TAGS) scripts/package-release.sh $(PACKAGE_TARGETS)

.PHONY: package-amd64
package-amd64: frontend ## Package linux/amd64 only
	@VERSION=$(PKG_VERSION) RELEASE_TAGS=$(RELEASE_TAGS) scripts/package-release.sh linux/amd64

.PHONY: package-arm64
package-arm64: frontend ## Package linux/arm64 only
	@VERSION=$(PKG_VERSION) RELEASE_TAGS=$(RELEASE_TAGS) scripts/package-release.sh linux/arm64

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
