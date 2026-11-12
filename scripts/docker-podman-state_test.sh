#!/usr/bin/env bash
# Tests for scripts/docker-podman-state.sh. Run: scripts/docker-podman-state_test.sh
#
# What breaks without this: a container that never starts, because podman read an older boot's ID out
# of /run and refused every command after that. Two claims here — the sweeper clears exactly what it
# is pointed at and nothing else, and clearing it is what makes podman answer again.
set -uo pipefail

self="$(cd "$(dirname "$0")" && pwd)/$(basename "$0")"
cd "$(dirname "$0")/.."
SCRIPT=scripts/docker-podman-state.sh
fail=0
in_ns="${MIKE_PODMAN_STATE_TEST_IN_NS:-}"

if [ "$in_ns" != 1 ] && unshare -rm --map-root-user sh -c 'mount -t tmpfs tmpfs /run' 2>/dev/null; then
    exec env MIKE_PODMAN_STATE_TEST_IN_NS=1 unshare -rm --map-root-user "$self"
fi

ok() { echo "ok   $1"; }
bad() {
    echo "FAIL $1: $2"
    fail=1
}
check() { # name expected_status actual_status
    if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "exit $3, want $2"; fi
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# A store holding transient state, with the image store beside it.
tree() { # root
    rm -rf "$1"
    mkdir -p "$1/run/libpod/exits" "$1/run/containers/storage/overlay/abc" "$1/varlib/overlay-images"
    printf '00000000-0000-0000-0000-000000000000\n' >"$1/run/libpod/alive"
    printf 'mountpoint\n' >"$1/run/containers/storage/overlay/abc/mount"
    printf 'db\n' >"$1/varlib/db.sql"
    printf 'layer\n' >"$1/varlib/overlay-images/images.json"
}

# --- what it removes, and what it leaves ------------------------------------------------------
tree "$tmp/a"
out="$(sh "$SCRIPT" clear "$tmp/a/run/libpod" "$tmp/a/run/containers/storage" 2>&1)"
check "clearing the two transient dirs succeeds" 0 $?
for gone in "$tmp/a/run/libpod" "$tmp/a/run/containers/storage"; do
    [ ! -e "$gone" ] || bad "a cleared directory is gone" "$gone still exists"
done
for kept in "$tmp/a/varlib/db.sql" "$tmp/a/varlib/overlay-images/images.json"; do
    [ -f "$kept" ] || bad "the graph root beside them is untouched" "$kept is gone"
done
case "$out" in
*"$tmp/a/run/libpod"*) ok "the line names what it cleared" ;;
*) bad "the line names what it cleared" "said: $out" ;;
esac

# A first boot has nothing to clear; a second sweep of an empty tree is the same first boot.
sh "$SCRIPT" clear "$tmp/absent/libpod" >/dev/null 2>&1
check "an absent directory is not an error" 0 $?
sh "$SCRIPT" clear "$tmp/a/run/libpod" >/dev/null 2>&1
check "clearing an already-empty tree is not an error" 0 $?

# --- what it refuses --------------------------------------------------------------------------
tree "$tmp/b"
mkdir -p "$tmp/b/rel" && printf 'x\n' >"$tmp/b/rel/libpod"
out="$(sh "$SCRIPT" clear rel/libpod 2>&1)"
check "a relative directory is refused" 2 $?
[ -f "$tmp/b/rel/libpod" ] || bad "a refused relative directory is untouched" "it was deleted"

out="$(sh "$SCRIPT" 2>&1)"
check "a missing verb is usage" 2 $?
case "$out" in usage*) ok "usage names the verb" ;; *) bad "usage names the verb" "said: $out" ;; esac

# Wiring no build step checks: the sweep has to come before the first probe, because that probe is
# the command the stale state kills.
sweep="$(grep -n 'mike-podman-state.sh clear' scripts/docker-entrypoint.sh | head -n1 | cut -d: -f1)"
probe="$(grep -n 'storage_probe()' scripts/docker-entrypoint.sh | head -n1 | cut -d: -f1)"
if [ -n "$sweep" ] && [ -n "$probe" ] && [ "$sweep" -lt "$probe" ]; then
    ok "the store is probed only after the transient state is cleared"
else
    bad "the store is probed only after the transient state is cleared" "sweep at ${sweep:-none}, probe at ${probe:-none}"
fi

# --- the cases that mount ---------------------------------------------------------------------
# These run as root in a private mount namespace, over a tmpfs mounted at /run, so the host's /run is
# never the one being cleared. Where the kernel or its LSM declines that namespace, these three cases
# skip and say why.
if [ "$in_ns" != 1 ]; then
    echo "skip the default argument list is the two dirs podman names — no user namespace that mounts a tmpfs"
    echo "skip a stale cached boot ID stops refusing the store — no user namespace that mounts a tmpfs"
    echo "skip a mount point is refused rather than emptied — no user namespace that mounts a tmpfs"
    if [ "$fail" = 0 ]; then
        echo "PASS (the three cases that mount skipped)"
        exit 0
    fi
    exit 1
fi

if ! mount -t tmpfs tmpfs /run; then
    echo "skip the three cases that mount — cannot mount a tmpfs over /run after all"
    if [ "$fail" = 0 ]; then
        exit 0
    fi
    exit 1
fi

mkdir -p /run/libpod/exits /run/containers/storage/overlay/abc /run/state
printf '00000000-0000-0000-0000-000000000000\n' >/run/libpod/alive
printf 'mountpoint\n' >/run/containers/storage/overlay/abc/mount
printf 'db\n' >/run/state/db.sql

# The defaults are the two paths podman's own error names, so no argument here can reach them.
sh "$SCRIPT" clear >/dev/null 2>&1
check "the default argument list is the two dirs podman names" 0 $?
{
    [ ! -e /run/libpod ] &&
        [ ! -e /run/containers/storage ] &&
        [ -f /run/state/db.sql ]
} || bad "the default argument list is the two dirs podman names" "cleared the wrong tree"

# Podman refuses every command while /run/libpod/alive names a boot other than the current one. The
# shim below is that check and nothing else.
cat >"$tmp/podman" <<'SH'
#!/bin/sh
bid="$(tr -d '[:space:]' </proc/sys/kernel/random/boot_id)"
if [ -f /run/libpod/alive ] && [ "$(cat /run/libpod/alive)" != "$bid" ]; then
    echo 'Error: current system boot ID differs from cached boot ID; an unhandled reboot has occurred. Please delete directories "/run/containers/storage" and "/run/libpod" and re-run Podman' >&2
    exit 125
fi
mkdir -p /run/libpod && printf '%s\n' "$bid" >/run/libpod/alive
echo overlay
SH
chmod +x "$tmp/podman"

mkdir -p /run/libpod && printf '00000000-0000-0000-0000-000000000000\n' >/run/libpod/alive
before="$("$tmp/podman" 2>&1)"
case "$before" in
*"boot ID differs"*) ok "the shim reproduces podman's refusal" ;;
*) bad "the shim reproduces podman's refusal" "said: $before" ;;
esac
sh "$SCRIPT" clear >/dev/null 2>&1
after="$("$tmp/podman" 2>&1)"
if [ "$after" = "overlay" ]; then
    ok "a stale cached boot ID stops refusing the store"
else
    bad "a stale cached boot ID stops refusing the store" "said: $after"
fi

# A /run/libpod somebody else mounted is not this container's to clear.
if mount -t tmpfs tmpfs /run/libpod; then
    printf 'someone elses\n' >/run/libpod/alive
    out="$(sh "$SCRIPT" clear /run/libpod 2>&1)"
    if [ -f /run/libpod/alive ]; then
        ok "a refused mount point keeps its contents"
    else
        bad "a refused mount point keeps its contents" "it was emptied"
    fi
    case "$out" in
    *"mount point"*) ok "the refusal says why" ;;
    *) bad "the refusal says why" "said: $out" ;;
    esac
else
    echo "skip a mount point is refused rather than emptied — cannot mount over /run/libpod"
fi

if [ "$fail" = 0 ]; then
    echo "PASS"
else
    exit 1
fi
