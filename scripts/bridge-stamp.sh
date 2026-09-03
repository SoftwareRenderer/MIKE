#!/usr/bin/env bash
# Record / verify what the sandbox-bridge binary was built from.
#
# sandbox/sandbox-bridge is a committed artifact handed to every sandbox container, so an
# artifact that was not rebuilt is an edit that never runs — silently, because the server
# and the sandbox both start fine. `make bridge` records a stamp; `make bridge-check`
# verifies it.
#
# The stamp hashes the bridge's inputs, not the binary: `go list -deps` covers a new import
# with no file list to maintain, and a toolchain upgrade rewrites the artifact while
# changing no source — a check that cries wolf there gets ignored.
#
# Usage:
#   scripts/bridge-stamp.sh sources                # what the artifact is built from
#   scripts/bridge-stamp.sh record  <stamp-file>   # after building the binary
#   scripts/bridge-stamp.sh check   <stamp-file>   # fail if the sources moved
set -euo pipefail

# Package the artifact is built from. Overridable so scripts/bridge-stamp_test.sh can point
# it at something that resolves to nothing.
BRIDGE_MAIN="${BRIDGE_MAIN:-./pkg/bridge/agent}"

# Paths are listed relative to the module root. An absolute path would fold the checkout's
# location into a committed stamp, and a clone anywhere else would read as stale forever.
MODULE_DIR="$(go list -m -f '{{.Dir}}')"
cd "$MODULE_DIR"

# The module-internal sources the bridge is compiled from. std is skipped: it belongs to the
# toolchain, not to this repository.
bridge_sources() {
    # The template needs {{$.Dir}} — a bare {{Dir}} is "function Dir not defined".
    local deps
    if ! deps=$(go list -deps -f \
        '{{if not .Standard}}{{range .GoFiles}}{{$.Dir}}/{{.}}{{"\n"}}{{end}}{{end}}' "$BRIDGE_MAIN"); then
        echo "bridge-stamp: go list -deps $BRIDGE_MAIN failed" >&2
        return 1
    fi
    # A template matching nothing prints nothing, and a stamp over just go.mod/go.sum would
    # agree with itself forever. The bridge's own package is always in the list.
    if [[ -z "$deps" ]] || ! grep -q 'pkg/bridge/agent' <<<"$deps"; then
        echo "bridge-stamp: no module sources resolved for $BRIDGE_MAIN" >&2
        return 1
    fi
    {
        local prefix="$MODULE_DIR/"
        while IFS= read -r file; do
            printf '%s\n' "${file#"$prefix"}"
        done <<<"$deps"
        printf 'go.mod\ngo.sum\n'
    } | sort -u
}

input_hash() {
    # Every path is hashed with its contents folded in, so a rename that keeps the same
    # content is still a different build input.
    bridge_sources | xargs sha256sum | sha256sum | cut -d' ' -f1
}

cmd="${1:-}"
stamp_file="${2:-}"

# `sources` needs no stamp; everything else reads or writes one.
if [[ -z "$cmd" || ( "$cmd" != "sources" && -z "$stamp_file" ) ]]; then
    echo "usage: $0 {sources|record|check} [<stamp-file>]" >&2
    exit 2
fi

want="$(input_hash)"

case "$cmd" in
sources)
    # The list itself, for tests and for "why did the stamp move?".
    bridge_sources
    ;;
record)
    printf '%s\n' "$want" >"$stamp_file"
    echo "bridge inputs recorded: ${want:0:12} -> $stamp_file"
    ;;
check)
    if [[ ! -f "$stamp_file" ]]; then
        echo "$stamp_file is missing (the artifact predates the stamp) — run: make bridge" >&2
        exit 1
    fi
    have="$(tr -d '[:space:]' <"$stamp_file")"
    if [[ "$have" != "$want" ]]; then
        echo "sandbox-bridge is stale — its sources changed since it was built. Run: make bridge" >&2
        exit 1
    fi
    echo "sandbox-bridge inputs match this tree (${want:0:12})"
    ;;
*)
    echo "usage: $0 {sources|record|check} [<stamp-file>]" >&2
    exit 2
    ;;
esac
