# MIKE application image. Includes the server, podman, and gVisor (runsc).
#
# The server uses podman and runsc to create task sandboxes and mounts the task's git worktree.
# This image runs the server (requires privileged mode for nested runtimes); actual agent
# work happens inside the gVisor containers with all capabilities dropped.
#
#   docker build -t getmike/mike:local -f Dockerfile .
#   docker compose up -d --build
#
# Build inputs are the source tree. DB, TLS keys, extensions, and node_modules are excluded by .dockerignore.

# syntax=docker/dockerfile:1

ARG GO_IMAGE=golang:1.27-alpine
ARG NODE_IMAGE=node:22-slim
# Debian trixie is used for podman 5.x compatibility, which is required for the `--runtime-flag ignore-cgroups=true` option.
ARG RUNTIME_IMAGE=debian:trixie-slim
# gVisor release channel or date (e.g., 20260928). Defaults are handled in the runtime stage.
ARG GVISOR_RELEASE
# The release tag stamped into the binary as docker-<tag>. The build context has no .git directory, so a
# build that knows its release passes it with --build-arg VERSION=. Without it the server reports docker.
ARG VERSION

# ── Stage 1: frontend (Vite → /build/dist) ───────────────────────────────────
FROM ${NODE_IMAGE} AS web
WORKDIR /build
# Copy lockfiles first to cache dependencies.
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# ── Stage 2: Go binaries ────────────────────────────────────────────────────
FROM ${GO_IMAGE} AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Embed the compiled SPA into the server binary.
COPY --from=web /build/dist ./frontend/dist
# Disable CGO for static binaries (uses pure-Go SQLite). Disable buildvcs as .git is excluded.
ENV CGO_ENABLED=0
ARG VERSION
# Build the server (release tags) and the sandbox bridge.
RUN version="docker${VERSION:+-$VERSION}" && \
    go build -tags release -trimpath -buildvcs=false -ldflags "-s -w -X mike/backend/server.Version=${version}" \
      -o /out/mike . && \
    go build -trimpath -buildvcs=false -ldflags "-s -w" \
      -o /out/sandbox-bridge ./pkg/bridge/agent

# ── Stage 3: runtime ────────────────────────────────────────────────────────
FROM ${RUNTIME_IMAGE} AS runtime
ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8

# Install system dependencies: git and ssh for worktree operations; podman and its runtime components for sandboxing; networking tools for containers; fuse-overlayfs for storage; zstd for extracting the gVisor bundle.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      git \
      openssh-client \
      podman \
      crun \
      conmon \
      catatonit \
      netavark \
      aardvark-dns \
      nftables \
      iproute2 \
      uidmap \
      fuse-overlayfs \
      slirp4netns \
      passt \
      zstd \
    && rm -rf /var/lib/apt/lists/*

# Download and verify the gVisor release bundle. Extract it to /usr/local/bin, keeping the gvisor-bin directory required by runsc.
ARG GVISOR_RELEASE
RUN arch="$(dpkg --print-architecture)" && \
    case "$arch" in \
      amd64) garch=x86_64 ;; \
      arm64) garch=aarch64 ;; \
      *) echo "gVisor: unsupported architecture $arch" >&2; exit 1 ;; \
    esac && \
    : "${GVISOR_RELEASE:=latest}" && \
    url="https://storage.googleapis.com/gvisor/releases/release/${GVISOR_RELEASE}/${garch}" && \
    tmp="$(mktemp -d)" && cd "$tmp" && \
    curl -fsSL -O "${url}/gvisor.tar.zstd" -O "${url}/gvisor.tar.zstd.sha512" && \
    sha512sum -c gvisor.tar.zstd.sha512 && \
    tar --zstd -xf gvisor.tar.zstd -C /usr/local/bin && \
    rm -f /usr/local/bin/containerd-shim-runsc-v1 && \
    cd / && rm -rf "$tmp" && \
    runsc --version

# Configure Podman: use file logging instead of journald, keep crun as default runtime, and enforce docker.io for short-name resolution.
RUN mkdir -p /etc/containers/registries.conf.d && \
    printf '%s\n' \
      '[engine]' \
      '  events_logger = "file"' \
      '  runtime = "crun"' \
      > /etc/containers/containers.conf && \
    printf '%s\n' \
      'unqualified-search-registries = ["docker.io"]' \
      'short-name-mode = "enforcing"' \
      > /etc/containers/registries.conf.d/90-mike.conf && \
    # Set subuid/subgid range for rootless podman.
    for f in /etc/subuid /etc/subgid; do \
      grep -q '^root:' "$f" 2>/dev/null || echo 'root:100000:65536' >> "$f"; \
    done

# Set up the application layout for binary and asset discovery.
WORKDIR /mike
COPY --from=go  /out/mike             /mike/mike
COPY --from=go  /out/sandbox-bridge   /mike/sandbox-bridge
# Copy prompt templates and skill packages.
COPY prompts/ /mike/prompts/
COPY scripts/docker-entrypoint.sh /usr/local/bin/mike-entrypoint.sh
COPY scripts/mike-healthcheck.sh  /usr/local/bin/mike-healthcheck
RUN chmod 0755 /mike/mike /mike/sandbox-bridge /usr/local/bin/mike-entrypoint.sh /usr/local/bin/mike-healthcheck && \
    mkdir -p /mike/data /mike/worktrees /mike/extensions /mike/projects && \
    chmod 0700 /mike/data

# Verify podman flags and gVisor installation during build to prevent runtime failures.
RUN podman --runtime-flag ignore-cgroups=true --version && \
    runsc --version && \
    [ "$(command -v runsc)" = /usr/local/bin/runsc ] && \
    [ -x /usr/local/bin/gvisor-bin/gvisor_sentry ]

# Agent adapters may require host CLI tools (e.g. `pi`). Install them here if needed.
# Everything an agent edits, builds and tests still runs inside the gVisor sandbox.
#
# Root is used because nested runtimes need netlink, cgroup, and device access.
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=15s --start-period=90s --retries=3 \
  CMD ["/usr/local/bin/mike-healthcheck"]

ENTRYPOINT ["/usr/local/bin/mike-entrypoint.sh"]
CMD []
