#!/usr/bin/env bash
# Tests for scripts/bridge-stamp.sh. Run: scripts/bridge-stamp_test.sh
#
# The failure that matters is a vacuous pass: a stamp over an empty source list agrees
# with itself forever, turning the check back into no check.
#
# Nothing here edits the tree — it may hold unstaged work. "Moving a bridge source
# invalidates the stamp" is proven instead by asserting the bridge's own sources are in the
# hashed set, and that a different input set does not match a recorded stamp.
set -uo pipefail

cd "$(dirname "$0")/.."
SCRIPT=scripts/bridge-stamp.sh
fail=0

check() { # name expected_status actual_status
    if [[ "$2" == "$3" ]]; then
        echo "ok   $1"
    else
        echo "FAIL $1: exit $3, want $2"
        fail=1
    fi
}

stamp="$(mktemp)"
cleanup() { rm -f "$stamp" "$stamp".other; }
trap cleanup EXIT

# --- the happy path ------------------------------------------------------------------
"$SCRIPT" record "$stamp" >/dev/null
"$SCRIPT" check "$stamp" >/dev/null 2>&1
check "check passes for unmodified sources" 0 $?

# A stamp is a hash, not a label: recording twice must agree.
"$SCRIPT" record "$stamp.other" >/dev/null
if cmp -s "$stamp" "$stamp.other"; then
    echo "ok   recording twice is deterministic"
else
    echo "FAIL recording twice is not deterministic"
    fail=1
fi

# --- the hashed set really is the bridge's sources -----------------------------------
sources="$("$SCRIPT" sources)"
for want_file in pkg/bridge/agent/main.go pkg/bridge/agent/bash.go pkg/bridge/agent/handler.go pkg/bridge/protocol/request.go; do
    if grep -q "$want_file" <<<"$sources"; then
        echo "ok   sources include $want_file"
    else
        echo "FAIL sources omit $want_file — editing it would not invalidate the stamp"
        fail=1
    fi
done
if grep -qx "go.mod" <<<"$sources"; then
    echo "ok   sources include go.mod (a dependency change moves the stamp)"
else
    echo "FAIL sources omit go.mod"
    fail=1
fi
# Paths must be module-relative: the stamp is committed, and an absolute path would fold
# the build machine's directory into a hash every other clone has to match.
if grep -q "^/" <<<"$sources"; then
    echo "FAIL sources contain an absolute path — the stamp would not survive another checkout"
    fail=1
else
    echo "ok   every source path is module-relative"
fi
# The stdlib belongs to the toolchain: hashing std sources would report the artifact stale
# on every Go upgrade.
if grep -q "src/runtime" <<<"$sources"; then
    echo "FAIL sources include stdlib — a toolchain upgrade would read as stale"
    fail=1
else
    echo "ok   sources exclude the stdlib"
fi

# --- a different input set must not match the recorded stamp -------------------------
# What "a bridge source changed" looks like to the checker: a recomputed hash that
# differs. The set is changed here by resolving a different package — the same code path
# without touching the tree.
"$SCRIPT" check "$stamp" >/dev/null 2>&1
check "the recorded stamp still matches" 0 $?
if BRIDGE_MAIN=./pkg/bridge/protocol "$SCRIPT" check "$stamp" >/dev/null 2>&1; then
    echo "FAIL a different input set matched the stamp"
    fail=1
else
    echo "ok   a different input set does not match the stamp"
fi

# --- the guards that stop a vacuous pass ---------------------------------------------
# go list resolving nothing must fail loudly, not hash go.mod/go.sum alone.
BRIDGE_MAIN=./pkg/does/not/exist "$SCRIPT" record "$(mktemp -u /tmp/guard-stamp-XXXX)" >/dev/null 2>&1
check "record fails when the package does not resolve" 1 $?
BRIDGE_MAIN=./pkg/does/not/exist "$SCRIPT" sources >/dev/null 2>&1
check "sources fails when the package does not resolve" 1 $?

# A missing stamp cannot vouch for anything.
"$SCRIPT" check "$(mktemp -u /tmp/no-such-stamp-XXXX)" >/dev/null 2>&1
check "check fails when the stamp file is missing" 1 $?

"$SCRIPT" >/dev/null 2>&1
check "no arguments is a usage error" 2 $?
"$SCRIPT" bogus "$stamp" >/dev/null 2>&1
check "unknown subcommand is a usage error" 2 $?

exit "$fail"
