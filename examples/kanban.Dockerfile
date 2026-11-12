# Kanban Development Environment
#
# Full dev stack: Go backend, Svelte/Vite frontend, gVisor sandboxing,
# Playwright e2e tests, Pi coding agent.
#
# Build:  docker build -f examples/kanban.Dockerfile -t kanban-dev .
# Run:    docker run --rm -it -v $(pwd):/app kanban-dev

FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

# ── Basic tools ──────────────────────────────────────────────
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    gnupg \
    wget \
    less \
    vim \
    jq \
    build-essential \
    lsof \
    procps \
    sudo \
    && rm -rf /var/lib/apt/lists/*

# ── Go (latest stable release) ───────────────────────────────
ADD https://go.dev/VERSION?m=text /tmp/go-latest.txt
RUN ver="$(head -n1 /tmp/go-latest.txt)" \
    && curl -fsSL "https://go.dev/dl/${ver}.linux-amd64.tar.gz" | tar -C /usr/local -xzf - \
    && rm -f /tmp/go-latest.txt \
    && rm -rf /usr/local/go/doc /usr/local/go/test

ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOPATH="/go"
ENV PATH="${GOPATH}/bin:${PATH}"
RUN echo 'export PATH="/usr/local/go/bin:/go/bin:$PATH"' >> /etc/bash.bashrc

# Needed since tool calls run in bash non-interactive mode
RUN echo 'export PATH="/usr/local/go/bin:/go/bin:$PATH"' > /etc/profile.d/go.sh
ENV BASH_ENV=/etc/profile.d/go.sh
ENV ENV=/etc/profile.d/go.sh

# ── Go linters (make lint, and therefore make check) ─────────
# `make check` stops when either linter is missing. They are installed with `go install` and not as `tool`
# directives in go.mod, so build-only tools stay out of the dependency notices (AGENTS.md → Licensing).
RUN go install honnef.co/go/tools/cmd/staticcheck@latest \
    && go install golang.org/x/tools/cmd/deadcode@latest \
    && staticcheck --version

# ── Node.js 22 (Robust NodeSource setup) ─────────────────────
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y nodejs \
    && rm -rf /var/lib/apt/lists/*

# ── Python 3 (diagnostic scripts) ────────────────────────────
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 \
    python3-pip \
    && rm -rf /var/lib/apt/lists/*

# ── gVisor (runsc) + podman ─────────────────────────────────
RUN apt-get update && apt-get install -y --no-install-recommends \
    bzip2 \
    podman \
    && rm -rf /var/lib/apt/lists/*

ARG GVISOR_RELEASE=latest
RUN ARCH="$(uname -m)" && \
    URL="https://storage.googleapis.com/gvisor/releases/release/${GVISOR_RELEASE}/${ARCH}" && \
    TMPDIR="$(mktemp -d)" && \
    wget -q "${URL}/gvisor.tar.bz2" -O "${TMPDIR}/gvisor.tar.bz2" && \
    wget -q "${URL}/gvisor.tar.bz2.sha512" -O "${TMPDIR}/gvisor.tar.bz2.sha512" && \
    (cd "$TMPDIR" && sha512sum -c gvisor.tar.bz2.sha512) && \
    tar -xjf "${TMPDIR}/gvisor.tar.bz2" -C "$TMPDIR" runsc && \
    install -m 0755 "${TMPDIR}/runsc" /usr/local/bin/runsc \
    && rm -rf "$TMPDIR" && \
    runsc --version

# ── Playwright browsers (e2e tests) ──────────────────────────
# Every Playwright release expects one Chromium build, so the browsers must be installed by the version the
# lockfile pins. A bare `npx playwright install` downloads the newest release instead, and then
# `make test-e2e` starts no browser. The two manifests are the only repo content needed here; the checkout
# mounts at /app later with its own node_modules. npm's own install script is skipped, so Chromium is
# downloaded once, by the command below.
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1
COPY frontend/package.json frontend/package-lock.json /opt/playwright-pin/
RUN cd /opt/playwright-pin \
    && npm ci --no-audit --no-fund --ignore-scripts \
    && npx playwright install --with-deps chromium \
    && rm -rf /opt/playwright-pin /root/.npm

# ── Pi coding agent ──────────────────────────────────────────
RUN curl -fsSL https://pi.dev/install.sh | bash

# ── Workdir ──────────────────────────────────────────────────
WORKDIR /app
