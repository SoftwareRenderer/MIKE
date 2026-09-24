#!/usr/bin/env bash
# Tests for scripts/gen-version-manifest.py. Run: scripts/gen-version-manifest_test.sh
#
# The failures that matter here are the silent ones: a digest that disagrees with sha256sum.txt, an
# asset quietly dropped, a manifest naming a version the archives do not carry.
set -uo pipefail

cd "$(dirname "$0")/.."
GEN=scripts/gen-version-manifest.py
fail=0
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

check() { # name expected_status actual_status
    if [[ "$2" == "$3" ]]; then
        echo "ok   $1"
    else
        echo "FAIL $1: exit $3, want $2"
        fail=1
    fi
}

digest() { # 64 hex, distinct per argument
    printf '%s' "$1" | sha256sum | cut -c1-64
}

# --- fixtures ------------------------------------------------------------------------
mkdist() { # dir version names...
    local dir="$1" version="$2"
    shift 2
    mkdir -p "$dir"
    : >"$dir/sha256sum.txt"
    local name
    for name in "$@"; do
        printf '%s  %s\n' "$(digest "$name")" "$name" >>"$dir/sha256sum.txt"
    done
}

mkdist "$work/two" 1.4.2 mike-server-1.4.2-linux-amd64.tar.gz mike-server-1.4.2-linux-arm64.tar.gz

# --- the happy path ------------------------------------------------------------------
python3 "$GEN" --dist "$work/two" --version 1.4.2 --published 2026-09-18T00:00:00Z >"$work/out" 2>&1
check "generates over a complete dist" 0 $?

python3 "$GEN" --dist "$work/two" --version 1.4.2 --check >/dev/null 2>&1
check "--check agrees with a fresh manifest" 0 $?

# Both archives present, each with the digest sha256sum.txt carries.
got=$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(len(d["assets"]), d["assets"]["linux/amd64"]["sha256"], d["assets"]["linux/amd64"]["url"])' "$work/two/version.json")
if [[ "$got" == "2 $(digest mike-server-1.4.2-linux-amd64.tar.gz) https://getmike.dev/download/mike-server-1.4.2-linux-amd64.tar.gz" ]]; then
    echo "ok   both assets listed with the checksum file's digest"
else
    echo "FAIL asset digests/urls: $got"
    fail=1
fi

# SOURCE_DATE_EPOCH feeds `published`, so a reproducible build announces a fixed instant.
SOURCE_DATE_EPOCH=1760000000 python3 "$GEN" --dist "$work/two" --version 1.4.2 >/dev/null 2>&1
check "SOURCE_DATE_EPOCH names the published instant" 0 $?
grep -q '"published": "2025-10-09T08:53:20Z"' "$work/two/version.json"
check "published is that instant in UTC" 0 $?

# --- the refusals --------------------------------------------------------------------
python3 "$GEN" --dist "$work/two" --version 9.9.9 >/dev/null 2>&1
check "a version no archive carries is refused" 1 $?

python3 "$GEN" --dist "$work/two" --version 1.4.2 --base-url http://getmike.dev/download >/dev/null 2>&1
check "a plain-http asset root is refused" 1 $?

python3 "$GEN" --dist "$work/two" --version 1.4.2 --base-url https://x/ --published not-a-date >/dev/null 2>&1
check "a published value that is not a UTC instant is refused" 1 $?

# A dropped arch must be an error, not a manifest that simply omits it.
mkdist "$work/one" 1.4.2 mike-server-1.4.2-linux-amd64.tar.gz
python3 "$GEN" --dist "$work/one" --version 1.4.2 >"$work/out" 2>&1
check "one archive is enough, and says so" 0 $?
grep -q "1 asset" "$work/out"
check "the asset count is reported" 0 $?

mkdist "$work/stray" 1.4.2 mike-server-1.4.2-linux-amd64.tar.gz README.md
python3 "$GEN" --dist "$work/stray" --version 1.4.2 >/dev/null 2>&1
check "a line that is not a release archive is refused, not skipped" 1 $?

printf '%s  mike-server-1.4.2-linux-amd64.tar.gz\n' deadbeef >"$work/one/sha256sum.txt"
python3 "$GEN" --dist "$work/one" --version 1.4.2 >/dev/null 2>&1
check "a digest that is not 64 hex is refused" 1 $?

: >"$work/one/sha256sum.txt"
python3 "$GEN" --dist "$work/one" --version 1.4.2 >/dev/null 2>&1
check "an empty checksum file is refused" 1 $?

python3 "$GEN" --dist "$work/nosuch" --version 1.4.2 >/dev/null 2>&1
check "a dist with no sha256sum.txt is refused" 1 $?

# --check must catch a manifest edited by hand, which is the whole point of --check.
mkdist "$work/tamper" 1.4.2 mike-server-1.4.2-linux-amd64.tar.gz
python3 "$GEN" --dist "$work/tamper" --version 1.4.2 >/dev/null 2>&1
sed -i 's/"schema": 1/"schema": 2/' "$work/tamper/version.json"
python3 "$GEN" --dist "$work/tamper" --version 1.4.2 --check >/dev/null 2>&1
check "--check refuses a manifest whose schema was edited" 1 $?

python3 "$GEN" --dist "$work/tamper" --version 1.4.2 >/dev/null 2>&1
sed -i 's/"published": "[^"]*"/"published": "2030-01-01T00:00:00Z"/' "$work/tamper/version.json"
python3 "$GEN" --dist "$work/tamper" --version 1.4.2 --check >/dev/null 2>&1
check "--check ignores the announcement instant" 0 $?

echo
if (( fail )); then
    echo "gen-version-manifest_test: FAILED"
    exit 1
fi
echo "gen-version-manifest_test: all ok"
