#!/bin/sh
# MIKE container entrypoint (see Dockerfile, compose.yaml).
#
# Verifies preconditions the server cannot, specifically that podman storage is operational.
# A server starting with broken storage would incorrectly report success for tasks before
# failing the sandbox. The graph driver is logged as it is the first diagnostic for sandbox failures.
#
# POSIX sh only: runs as PID 1's parent shell on a slim distro.

set -eu

die() {
    echo "mike-entrypoint: $*" >&2
    exit 1
}

need() {
    # Identify the missing variable and how to provide it, as this check is hit by both 
    # `docker compose` and bare `docker run` calls.
    [ -n "${1:-}" ] || die "$2 is not set. Put it in .env (see .env.example) or pass -e $2=... — the server refuses to start without it."
}

need "${MIKE_ENCRYPTION_KEY:-}" MIKE_ENCRYPTION_KEY

# The server listens where the published port can reach it, which is never the container's
# own loopback. TLS has no switch: the CA and its leaf land in the data volume prepared below.
: "${MIKE_LISTEN:=0.0.0.0}"
: "${MIKE_PORT:=3000}"
# Unset or empty answers loopback only: the server admits `localhost`, `127.0.0.1` and `::1` without
# being told (backend/server/middleware.go → newHostAllowlist), and a wildcard listen names no host,
# so nothing else joins the list. Every other name a browser will type is a deliberate edit here.
: "${MIKE_ALLOWED_HOSTS:=localhost}"
export MIKE_LISTEN MIKE_PORT MIKE_ALLOWED_HOSTS

# /mike/data holds kanban.db, the local CA and its key, and the flight recorder: owner-only,
# even when the volume was created by the engine as 0755.
mkdir -p /mike/data /mike/worktrees /mike/extensions /mike/projects
chmod 0700 /mike/data

# ── podman storage ───────────────────────────────────────────────────────────
# `podman info` exercises the real store to resolve the graph driver and mount storage.
# This ensures that drivers refused by the kernel cause a failure here instead of later
# during sandbox execution.
storage_probe() {
    podman info --format '{{.Store.GraphDriverName}}' 2>&1
}

out="$(storage_probe || true)"
case "$out" in
    *"'overlay' is not supported over overlayfs"* | *'overlayfs is not supported as an overlay'* | *'mount_program'*)
        # Nested on a host using overlayfs. `fuse-overlayfs` is the preferred fast fallback;
        # `vfs` is the slow, last-resort fallback. This is logged because both are
        # significant performance cliffs.
        if [ -e /dev/fuse ]; then
            echo "mike-entrypoint: kernel overlay unavailable over overlayfs; using fuse-overlayfs (/dev/fuse present)"
            mkdir -p /etc/containers
            printf '%s\n' '[storage]' 'driver = "overlay"' \
                '[storage.options.overlay]' 'mount_program = "/usr/bin/fuse-overlayfs"' \
                >/etc/containers/storage.conf
        else
            echo "mike-entrypoint: kernel overlay unavailable and no /dev/fuse; falling back to vfs (slowest, largest)"
            mkdir -p /etc/containers
            printf '%s\n' '[storage]' 'driver = "vfs"' >/etc/containers/storage.conf
        fi
        out="$(storage_probe || true)"
        ;;
esac

driver="$(printf '%s' "$out" | head -n1 | tr -d '[:space:]')"
case "$driver" in
    overlay | overlayfs | fuse-overlayfs | vfs | btrfs | zfs | native) ;;
    *) die "podman storage is not usable — 'podman info' said: $(printf '%s' "$out" | head -n3 | tr '\n' ' '). The container needs --privileged (nested runtime) and a writable /var/lib/containers." ;;
esac

echo "mike-entrypoint: podman $(podman --version | awk '{print $3}'), storage driver: $driver"
echo "mike-entrypoint: gVisor: $(runsc --version 2>&1 | head -n1)"
echo "mike-entrypoint: listening on ${MIKE_LISTEN}:${MIKE_PORT} over TLS, Host allowlist: ${MIKE_ALLOWED_HOSTS}"

# Nothing here signs a person in: a browser pairs a passkey, and the first boot of an empty
# data directory prints the one-time code that buys that registration. Say where to read it,
# because "how do I get in" is the first question an operator of a fresh container asks.
echo "mike-entrypoint: no password is set or needed. On a data directory with no passkey yet, this service's log prints a boxed pairing code — read it with: docker compose logs mike"

# The default sandbox image is pulled by the first task that needs it, silently, from inside
# the gVisor-isolated create path. Opt in to paying for it at boot instead.
if [ "${MIKE_PREPULL_IMAGE:-}" = "1" ]; then
    (
        podman pull docker.io/library/ubuntu:latest >/tmp/prepull.log 2>&1 ||
            echo "mike-entrypoint: sandbox base image pull failed (see /tmp/prepull.log)" >&2
    ) &
fi

# exec, so the server IS PID 1's replacement and SIGTERM reaches its bounded shutdown path
# (backend/server/shutdown.go). A shell left in front of it would eat the signal and leave
# the containers running.
exec /mike/mike "$@"
