#!/usr/bin/env bash
# The iteration lane: what one edit is worth checking, in seconds rather than minutes.
#
# `make check` is the only claim of done and it costs minutes (docs/testing.md → Measured gate cost), so
# mid-loop an agent either pays it on every edit or skips it. This is the middle: the packages holding the
# change, the frontend files the change touched, and the three `make check` gates cheap enough to always
# run. It is `make test-diff` and `npm run test:related` composed with the rest of an edit. What the
# composition cannot catch is in docs/testing.md → Running less than the gate.
#
# What runs, cheapest first:
#   gofmt       `gofmt -l` over the touched .go files (scripts/touched-packages.sh files)
#   build       `go build` over GO_SCOPE. This lane tests no dependent of a touched package, so a signature
#               change that breaks one is caught by this build and by nothing else here — which is why the
#               build is never skipped, not even with no changed .go file.
#   vet, test   the touched packages (scripts/touched-packages.sh). `go test` keeps its result cache ON:
#               that is the whole saving, and `make test-go` stays the target that defeats it.
#   frontend    `npm run test:related` over the changed src/ files, then the one typecheck compile
#               (npm run check). scripts/touched-frontend-files.sh decides whether this lane runs at all.
#               The typecheck is whole-tree, because svelte-check has no per-file mode; its cache key is the
#               file contents, so an unchanged tree costs a fraction of a second.
#   gates       archgate, docs-check, bridge-check — the `make check` sub-gates that cost seconds
#
# What does not run, and where the full version lives:
#   every package's tests with the cache defeated ......... make test-go (and make check-race for the
#                                                         packages that own the detached goroutines)
#   staticcheck, deadcode, gofmt over untouched files ..... make lint
#   knip and the frontend cleanliness gates ............... make test-frontend
#   the -tags release build ............................... make check-release
#   the e2e suite ......................................... (cd frontend &&) npx playwright test
#   the gate's own self-tests ............................. make test-archgate, make test-docs-check
#
# A step that cannot run prints why in the summary rather than reading green: a skipped lane is a fact
# about the edit, not a pass.
#
# Env:
#   GO_SCOPE   the package scope for the build (the Makefile passes the same value this default holds)
set -uo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT" || exit 1

GO_SCOPE=${GO_SCOPE:-./backend/... ./pkg/... .}
read -r -a go_scope_args <<<"$GO_SCOPE"

names=()
secs=()
codes=()
notes=()
failed=()
skipped=()
start=$(date +%s.%N)

elapsed() { awk -v a="$1" -v b="$2" 'BEGIN { printf "%.2f", b - a }'; }

# record <name> <seconds> <exit> <note> — one row of the summary. "skip" is never a pass, so it goes on
# the skipped list rather than the green column.
record() {
    names+=("$1")
    secs+=("$2")
    codes+=("$3")
    notes+=("$4")
    case "$3" in
    skip) skipped+=("$1: $4") ;;
    0) ;;
    *) failed+=("$1 (exit $3)") ;;
    esac
}

# step <name> <cmd...> — run it with its output on the terminal, where a failure's reason belongs.
step() {
    local name=$1 rc t0 t1
    shift
    t0=$(date +%s.%N)
    "$@"
    rc=$?
    t1=$(date +%s.%N)
    record "$name" "$(elapsed "$t0" "$t1")" "$rc" ''
}

# step_in <dir> <name> <cmd...> — the same, from a directory. A subshell would lose the record.
step_in() {
    local dir=$1 name=$2 rc t0 t1
    shift 2
    t0=$(date +%s.%N)
    (cd "$dir" && "$@")
    rc=$?
    t1=$(date +%s.%N)
    record "$name" "$(elapsed "$t0" "$t1")" "$rc" ''
}

# ---------------------------------------------------------------------------
# Go
# ---------------------------------------------------------------------------

mapfile -t go_files < <(bash scripts/touched-packages.sh files)
mapfile -t go_pkgs < <(bash scripts/touched-packages.sh | tr ' ' '\n' | grep -v '^$')

# gofmt prints the unformatted paths on stdout and an unreadable path on stderr, and anything it prints is
# a failure — the same reading `make lint` uses.
gofmt_touched() {
    local out
    out=$(gofmt -l "${go_files[@]}" 2>&1)
    if [ -n "$out" ]; then
        printf '%s\n' "$out" | sed 's/^/    /'
        echo "    fix with: gofmt -w <the paths listed>"
        return 1
    fi
}

if [ ${#go_files[@]} -gt 0 ]; then
    echo "==> gofmt: ${go_files[*]}"
    step gofmt gofmt_touched
else
    record gofmt - skip 'no changed .go files'
fi

step build go build "${go_scope_args[@]}"

if [ ${#go_pkgs[@]} -gt 0 ]; then
    echo "==> go vet / go test: ${go_pkgs[*]}"
    step vet go vet "${go_pkgs[@]}"
    # No -count=1: a defeated result cache is `make test-go`'s cost, not this lane's.
    step 'go test' go test -parallel 4 "${go_pkgs[@]}"
else
    # The message `make test-diff` prints, and the same reason: the build above ran whatever this decides.
    echo "check-fast: no changed .go files under $GO_SCOPE"
    record vet - skip "no changed .go files under $GO_SCOPE"
    record 'go test' - skip "no changed .go files under $GO_SCOPE"
fi

# ---------------------------------------------------------------------------
# Frontend
# ---------------------------------------------------------------------------

mapfile -t fe_files < <(bash scripts/touched-frontend-files.sh)
if [ ${#fe_files[@]} -eq 0 ]; then
    echo "check-fast: no changed file under frontend/ — the frontend lane is skipped"
    record 'vitest related' - skip 'no changed file under frontend/'
    record typecheck - skip 'no changed file under frontend/'
else
    echo "==> frontend: ${fe_files[*]}"
    src_files=()
    for f in "${fe_files[@]}"; do
        [[ $f == src/* ]] && src_files+=("$f")
    done
    if [ ${#src_files[@]} -gt 0 ]; then
        step_in frontend 'vitest related' npm run test:related -- "${src_files[@]}"
    else
        record 'vitest related' - skip 'no changed file under frontend/src'
    fi
    step_in frontend typecheck npm run check
fi

# ---------------------------------------------------------------------------
# The cheap always-run gates, by way of their own targets so the definition stays one
# ---------------------------------------------------------------------------

step archgate make --no-print-directory archgate
step docs-check make --no-print-directory docs-check
step bridge-check make --no-print-directory bridge-check

# ---------------------------------------------------------------------------
# One summary: every step, what it cost, and what it decided
# ---------------------------------------------------------------------------

end=$(date +%s.%N)
wall=$(elapsed "$start" "$end")
printf '\n==> check-fast summary (%ss wall)\n' "$wall"
printf '  %-16s %8s %6s  %s\n' step seconds exit note
for i in "${!names[@]}"; do
    printf '  %-16s %8s %6s  %s\n' "${names[$i]}" "${secs[$i]}" "${codes[$i]}" "${notes[$i]}"
done
echo
if [ ${#skipped[@]} -gt 0 ]; then
    joined=$(printf '%s; ' "${skipped[@]}")
    printf '  skipped: %s\n' "${joined%; }"
fi

if [ ${#failed[@]} -gt 0 ]; then
    joined=$(printf '%s; ' "${failed[@]}")
    echo "check-fast: FAILED — ${joined%; }"
    echo "check-fast is the iteration lane: make check stays the claim of done"
    exit 1
fi
if [ ${#skipped[@]} -gt 0 ]; then
    echo "check-fast: green in ${wall}s, with ${#skipped[@]} steps skipped — make check stays the claim of done"
else
    echo "check-fast: green in ${wall}s — make check stays the claim of done"
fi
