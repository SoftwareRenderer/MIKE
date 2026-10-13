# MIKE application image — self-contained: the server PLUS the two dependencies it
# drives itself, podman and gVisor (runsc).
#
# MIKE execs `podman --runtime runsc create/start/exec` for every task sandbox and
# bind-mounts each task's git worktree into that container, so both binaries have to be
# on PATH *inside* this image and the container has to be able to run a nested runtime
# (`privileged`, see compose.yaml). This is the app image, not the task sandbox: the
# work an agent does runs in the gVisor container, with --cap-drop ALL.
#
#   docker build -t getmike/mike:local -f Dockerfile .
#   docker compose up -d --build
#
# Build inputs are the tree's own sources; `data/` (DB + TLS CA key), extensions/,
# node_modules and every committed artifact is excluded by .dockerignore.

# syntax=docker/dockerfile:1

ARG GO_IMAGE=golang:1.27-alpine
ARG NODE_IMAGE=node:22-slim
# Debian stable: its podman is 5.x, which is what this app's `podman --runtime-flag
# ignore-cgroups=true <cmd>` global option needs. Ubuntu 24.04 ships podman 4.9, which is
# unverified against that call — and the RUN below that asserts it fails the build rather
# than the first task.
ARG RUNTIME_IMAGE=debian:trixie-slim
# A gVisor release channel, or a pinned release spelled as its bare date (`20260928`). The bucket
# path already carries the word `release`, so `release-20260928` is a 404. No default is spelled
# here: whether a global ARG's default reaches a stage that re-declares the name is a builder detail
# (podman/buildah 5.8 hands it over), so the stage that reads it holds a `:=` default of its own.
ARG GVISOR_RELEASE
# Recorded into the binary the same way the Makefile does it; its default sits in that stage too.
ARG VERSION

# ── Stage 1: frontend (Vite → /build/dist) ───────────────────────────────────
FROM ${NODE_IMAGE} AS web
WORKDIR /build
# Lockfile first: dependencies cache as one layer, so a source edit costs no reinstall.
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
# The SPA is compiled into the server, so this stage needs the web stage's output; .dockerignore keeps
# it out of the build context, and a Go build with nothing to embed does not compile.
COPY --from=web /build/dist ./frontend/dist
# CGO off on purpose: SQLite here is modernc.org (pure Go), so both binaries are static
# and the runtime layer needs no glibc version match. -buildvcs=false because the build
# context carries no .git (see .dockerignore).
ENV CGO_ENABLED=0
ARG VERSION
# The image ships no frontend directory, so this is the release build; the bridge takes no tag.
RUN : "${VERSION:=docker}" && \
    go build -tags release -trimpath -buildvcs=false -ldflags "-s -w -X mike/backend/server.Version=${VERSION}" \
      -o /out/mike . && \
    go build -trimpath -buildvcs=false -ldflags "-s -w" \
      -o /out/sandbox-bridge ./pkg/bridge/agent

# ── Stage 3: runtime ────────────────────────────────────────────────────────
FROM ${RUNTIME_IMAGE} AS runtime
ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8

# git + openssh-client: the server runs the worktree/commit/clone side, and
# GIT_ALLOW_PROTOCOL permits ssh (see backend/git/clone.go).
# podman + crun + conmon + catatonit: the nested runtime the server drives.
# netavark/aardvark-dns/nftables: sandbox container networking; passt/slirp4netns for the
#   rootless path. uidmap + fuse-overlayfs: alternate userns / storage driver (the
#   entrypoint falls back to fuse-overlayfs or vfs when overlay lands on overlayfs).
# curl: the healthcheck only.
# zstd: the gVisor bundle below is a `.tar.zstd` and `tar --zstd` runs the `zstd` binary; a slim
#   Debian image ships neither zstd nor bzip2.
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

# gVisor: the release bundle, whose published sha512 is checked before anything is run. Since
# release/20260831 the bucket carries only the bundle — the bare `runsc` object is gone, so a
# single-binary recipe 404s — and `scripts/install-gvisor.sh` installs this same file on a host.
# The bundle is also what runsc now needs: it executes the binaries under `gvisor-bin/` at runtime
# and looks for that directory beside its own, so the tree is extracted whole and left whole. Only
# the containerd shim is dropped, this image having no containerd.
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

# Container engine configuration.
#  - events_logger=file: there is no journald socket in this container, and the default
#    journald target makes podman complain on every command.
#  - runtime=crun stays the DEFAULT: only the sandbox containers ask for `--runtime runsc`.
#    A global runsc default would also route image builds and one-off container runs
#    through gVisor, which is not what those want.
#  - short names are resolved to docker.io, enforcing: podman's own fallback asks the user
#    which registry to search and dies headless with "short-name resolution enforced but
#    cannot prompt without a TTY" (see pkg/sandboxruntime/image_ref.go).
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
    # rootless userns needs a subuid/subgid range for whoever podman runs as.
    for f in /etc/subuid /etc/subgid; do \
      grep -q '^root:' "$f" 2>/dev/null || echo 'root:100000:65536' >> "$f"; \
    done

# Layout is dictated by how the binary finds its own siblings:
#   GetAgentBinaryPath  -> <dir of exe>/sandbox-bridge
#   frontend            -> inside the binary
#   extensions dir      -> ./extensions      (a volume at runtime)
#   prompts             -> ./prompts
WORKDIR /mike
COPY --from=go  /out/mike             /mike/mike
COPY --from=go  /out/sandbox-bridge   /mike/sandbox-bridge
# The templates, and the tracked skill packages beside them. A deployed task is offered those skills at
# the read-only /mnt/skills bind; an untracked package stays out of the context.
COPY prompts/ /mike/prompts/
COPY scripts/docker-entrypoint.sh /usr/local/bin/mike-entrypoint.sh
COPY scripts/mike-healthcheck.sh  /usr/local/bin/mike-healthcheck
RUN chmod 0755 /mike/mike /mike/sandbox-bridge /usr/local/bin/mike-entrypoint.sh /usr/local/bin/mike-healthcheck && \
    mkdir -p /mike/data /mike/worktrees /mike/extensions /mike/projects && \
    chmod 0700 /mike/data

# Fails the BUILD, not the first task, if the distro's podman rejects the global flag this
# app passes (`podman --runtime-flag ignore-cgroups=true start|exec`), if runsc is not on
# PATH under the name the sandbox asks for, or if the extracted tree has no sidecars — runsc
# answers `--version` happily with `gvisor-bin/` missing and fails only on a `create`.
# `podman info` is NOT checked here: it needs the
# privileges the container gets at run time, and a swallowed failure would make this line
# a lie. The entrypoint probes it and says what it found.
RUN podman --runtime-flag ignore-cgroups=true --version && \
    runsc --version && \
    [ "$(command -v runsc)" = /usr/local/bin/runsc ] && \
    [ -x /usr/local/bin/gvisor-bin/gvisor_sentry ]

# Agent adapters (backend/agent/runner.go) start their binaries as children of the server,
# so an adapter that needs a host CLI — e.g. the Pi agent adapter, which runs `pi` — needs
# that CLI installed HERE. This image deliberately ships no node/python/agent CLI: add
#    RUN apt-get update && apt-get install -y --no-install-recommends nodejs && rm -rf /var/lib/apt/lists/*
# (or a COPY from a builder stage) for the adapters you actually enable. Everything an
# agent edits, builds and tests still runs inside the gVisor sandbox, not here.
#
# Root is deliberate: a nested runtime needs netlink, cgroup and device access that a
# remapped user does not get. Containment is the gVisor task container, not this uid.
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=15s --start-period=90s --retries=3 \
  CMD ["/usr/local/bin/mike-healthcheck"]

ENTRYPOINT ["/usr/local/bin/mike-entrypoint.sh"]
CMD []
