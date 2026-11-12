#!/bin/sh
# Clear podman's transient state at container start, before the entrypoint probes the store
# (scripts/docker-entrypoint.sh). Test: scripts/docker-podman-state_test.sh.
#
# Usage: docker-podman-state.sh clear [dir ...]   # default: the two dirs podman names
#
# /run/libpod and /run/containers/storage hold state that may not outlive a boot: the boot ID podman
# cached in /run/libpod/alive, and files naming mounts that are already gone. On a host both
# directories are a tmpfs. In a container they are part of the writable layer, so the cached ID
# survives a host reboot that the kernel's own ID does not, and podman then refuses every command.
#
# Clearing them is safe here because no sandbox is running before the entrypoint finishes: a task's
# sandbox is a process of this container's PID namespace. libpod's database and the image store are
# under the graph root, which is never a default argument. A directory that is itself a mount point
# is skipped rather than emptied, since its contents belong to whoever mounted it.

set -eu

[ "${1:-}" = "clear" ] || {
    echo "usage: $0 clear [dir ...]" >&2
    exit 2
}
shift

dirs="$*"
[ -n "$dirs" ] || dirs="/run/libpod /run/containers/storage"

for dir in $dirs; do
    case "$dir" in
        /*) ;;
        *)
            echo "mike-podman-state: refusing to clear a relative directory: $dir" >&2
            exit 2
            ;;
    esac

    [ -d "$dir" ] || continue

    if awk -v target="$dir" '$5 == target { found = 1 } END { exit !found }' /proc/self/mountinfo; then
        echo "mike-podman-state: $dir is a mount point, leaving it alone" >&2
        continue
    fi

    rm -rf -- "$dir"
    echo "mike-podman-state: cleared podman's transient state in $dir"
done
