#!/bin/sh
# MIKE container entrypoint (see Dockerfile, compose.yaml).
#
# Everything here is a precondition the server cannot check for itself: it verifies runsc
# and podman exist, but it does not know whether podman's *storage* came up, and a server
# that boots on broken storage answers 200 to every task and then fails the sandbox. So the
# decision is made once, here, before PID 1 is handed over — and the graph driver ends up in
# the log, because "which driver did we land on" is the first question when a sandbox fails.
#
# POSIX sh only: this runs as PID 1's parent shell on a slim distro.

set -eu

die() {
    echo "mike-entrypoint: $*" >&2
    exit 1
}

need() {
    # The message names the variable and says where it belongs, because the failure is
    # reached both by compose (which guards these in compose.yaml) and by a bare
    # `docker run`, which does not.
    [ -n "${1:-}" ] || die "$2 is not set. Put it in .env (see .env.example) or pass -e $2=... — the server refuses to start without it."
}

need "${MIKE_ENCRYPTION_KEY:-}" MIKE_ENCRYPTION_KEY
need "${MIKE_ALLOWED_HOSTS:-}" MIKE_ALLOWED_HOSTS

# The server listens where the published port can reach it, which is never the container's
# own loopback. TLS is on unless the operator turned it off deliberately.
: "${MIKE_LISTEN:=0.0.0.0}"
: "${MIKE_PORT:=3000}"
export MIKE_LISTEN MIKE_PORT

# /mike/data holds kanban.db, the local CA and its key, and the flight recorder: owner-only,
# even when the volume was created by the engine as 0755.
mkdir -p /mike/data /mike/worktrees /mike/extensions /mike/projects
chmod 0700 /mike/data

# ── podman storage ───────────────────────────────────────────────────────────
# `podman info` is the only probe that exercises the real store: it resolves the graph
# driver and mounts the storage, so a driver the kernel refuses shows up here as an error
# rather than as a sandbox that never starts.
storage_probe() {
    podman info --format '{{.Store.GraphDriverName}}' 2>&1
}

out="$(storage_probe || true)"
case "$out" in
    *"'overlay' is not supported over overlayfs"* | *'overlayfs is not supported as an overlay'* | *'mount_program'*)
        # Nested on a host whose storage sits on overlayfs too. fuse-overlayfs is the fast
        # answer; if it is not usable either, vfs is the honest slow one. Either way this is
        # logged, because it is a real performance cliff an operator would otherwise chase
        # through sandbox logs.
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
echo "mike-entrypoint: listening on ${MIKE_LISTEN}:${MIKE_PORT} (TLS $([ "${MIKE_TLS:-1}" = "0" ] && echo 'DISABLED' || echo 'on')), Host allowlist: ${MIKE_ALLOWED_HOSTS}"

if [ "${MIKE_TLS:-1}" = "0" ]; then
    echo "mike-entrypoint: WARNING — plain HTTP. Only acceptable behind a TLS-terminating proxy that sets X-Forwarded-Proto; the API hands out shells." >&2
fi

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
