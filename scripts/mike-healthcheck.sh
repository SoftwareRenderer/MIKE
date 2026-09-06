#!/bin/sh
# Container healthcheck (wired by the Dockerfile's HEALTHCHECK, inherited by compose.yaml).
#
# Three independent things have to be true, and each one fails with its own line, because
# "unhealthy" in `docker ps` tells an operator nothing:
#   1. runsc still runs — the sandbox layer refuses to start a task without it, and it is the
#      piece a host kernel upgrade or a missing /dev/fuse breaks first.
#   2. podman's store still resolves — a container can be "running" with storage gone.
#   3. the API answers. 401 counts: /api/config is session-authed, so an unauthenticated 401
#      is a live, *secured* server. A 200 here would mean auth is not working.
#
# Nothing here starts a sandbox: a healthcheck that creates a container every 30s would be
# its own worst subsystem.

set -u

port="${MIKE_PORT:-3000}"
scheme=https
[ "${MIKE_TLS:-1}" = "0" ] && scheme=http

if ! runsc --version >/dev/null 2>&1; then
    echo "runsc --version failed (gVisor missing or not executable)"
    exit 1
fi

if ! podman info --format '{{.Store.GraphDriverName}}' >/dev/null 2>&1; then
    echo "podman info failed (container storage unusable)"
    exit 1
fi

# -k on purpose: the certificate is signed by this server's own local CA, which the health
# check has no reason to trust and no reason to verify twice — it is testing liveness.
code="$(curl -sk -o /dev/null -w '%{http_code}' --max-time 10 "${scheme}://127.0.0.1:${port}/api/config" 2>/dev/null || true)"
case "$code" in
    200 | 401) exit 0 ;;
    000) echo "no answer from ${scheme}://127.0.0.1:${port}/api/config" ;;
    *) echo "${scheme}://127.0.0.1:${port}/api/config answered HTTP $code (want 200 or 401)" ;;
esac
exit 1
