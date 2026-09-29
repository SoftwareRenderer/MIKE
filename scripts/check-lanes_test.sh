#!/usr/bin/env bash
# Tests for scripts/check-lanes.sh. Run: scripts/check-lanes_test.sh (`make test-check-lanes`)
#
# The failure that matters is a green row over a red gate: a lane that never started, a lane killed
# partway, or a child make that inherited `-i` from the parent all read as success unless the runner's
# verdict comes from files it reads back. Every case here forces one of those and asserts the run says
# so by name.
#
# Nothing here runs a real gate: MIKE_CHECK_LANES points at a generated table of `true`/`false`/`touch`
# steps and MIKE_CHECK_LOGS points at a temp directory, so the real data/check-lanes logs survive.
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
RUNNER=scripts/check-lanes.sh
fail=0

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

check() { # name expected_status actual_status
    if [[ "$2" == "$3" ]]; then
        echo "ok   $1"
    else
        echo "FAIL $1: exit $3, want $2"
        fail=1
    fi
}

says() { # name file needle
    if grep -qF -- "$3" "$2"; then
        echo "ok   $1"
    else
        echo "FAIL $1: '$3' is not in the run's output"
        sed 's/^/       /' "$2" | head -30
        fail=1
    fi
}

says_re() { # name file extended-pattern
    if grep -qE -- "$3" "$2"; then
        echo "ok   $1"
    else
        echo "FAIL $1: /$3/ does not match a line in the run's output"
        sed 's/^/       /' "$2" | head -30
        fail=1
    fi
}

silent() { MIKE_CHECK_LOGS="$work/logs" MIKE_CHECK_LANES="$1" bash "$RUNNER" >"$work/out" 2>&1; }

# --- the step table is the gate's shape ----------------------------------------------------

table="$work/defaults.tsv"
bash "$RUNNER" --lanes >"$table"
check "--lanes prints the step table" 0 $?

# A renamed make target or npm script would otherwise fail only when the lane reached it, minutes in.
missing=""
while IFS=$'\t' read -r lane step note cmd; do
    case "${lane:-}" in '' | '#'*) continue ;; esac
    case "$cmd" in
    'make --no-print-directory '*)
        target=${cmd#'make --no-print-directory '}
        grep -Eq "^${target}:" Makefile || missing="$missing $lane/$step:$target" ;;
    'cd frontend && npm run '*)
        script=${cmd#'cd frontend && npm run '}
        grep -qF "\"$script\":" frontend/package.json || missing="$missing $lane/$step:$script" ;;
    'cd frontend && '*'')
        ;;
    *)
        command -v "${cmd%% *}" >/dev/null || missing="$missing $lane/$step:${cmd%% *}" ;;
    esac
done <"$table"
check "every step in the table names a check that exists${missing:+ —$missing}" "" "$missing"

# Both halves of the gate are present, or the split has quietly dropped a sub-gate.
for want in lint archgate test-archgate bridge-check build check-release test-go \
            docs-check test-docs-check svelte-check test-frontend; do
    if awk -F'\t' -v w="$want" '$2 == w { found = 1 } END { exit !found }' "$table"; then
        echo "ok   the table still runs $want"
    else
        echo "FAIL the table no longer runs $want"
        fail=1
    fi
done
lanes=$(awk -F'\t' 'NF > 1 && $1 !~ /^#/ { print $1 }' "$table" | LC_ALL=C sort -u | tr '\n' ' ')
check "the table has two lanes ($lanes)" "frontend go " "$lanes"

# --- an all-green run keeps the gate's own success message ---------------------------------

printf 'go\tok\tgreen\ttrue\nfrontend\tok\tgreen\ttrue\n' >"$work/green.tsv"
silent "$work/green.tsv"
check "an all-green table exits 0" 0 $?
says "an all-green run prints the gate's success message" "$work/out" "make check: all gates green"

# --- one red lane cannot hide the other ----------------------------------------------------

cat >"$work/red.tsv" <<TABLE
go	green-one	touches a marker	touch $work/go1
go	red	exits 4	exit 4
go	never-reached	would touch a marker	touch $work/go2
frontend	green-one	touches a marker	touch $work/fe1
frontend	green-two	touches a marker	touch $work/fe2
TABLE
silent "$work/red.tsv"
check "a red lane exits the run non-zero" 1 $?
says "the red lane carries the step's own exit code" "$work/out" "go/red (exit 4)"
says_re "the green lane's own row reads pass" "$work/out" "^  frontend +[0-9.]+ +pass "
check "the other lane ran to its own end" "yes" \
    "$([ -f "$work/fe1" ] && [ -f "$work/fe2" ] && echo yes || echo no)"
check "the step after the failure never ran" "no" \
    "$([ -f "$work/go2" ] && echo yes || echo no)"
says "a step a stopped lane skipped prints as skip, not green" "$work/out" "never-reached"

# --- a lane that produced no verdict is a failure ------------------------------------------

cat >"$work/killed.tsv" <<TABLE
go	killed	kills its own lane	kill -9 \$PPID
frontend	green	touches a marker	touch $work/alive
TABLE
silent "$work/killed.tsv"
check "a lane killed mid-step exits the run non-zero" 1 $?
says "a lane with no exit code is named" "$work/out" "no exit code"
check "the sibling lane of a killed lane still ran" "yes" \
    "$([ -f "$work/alive" ] && echo yes || echo no)"

# --- the guards that stop a vacuous pass ---------------------------------------------------

: >"$work/empty.tsv"
silent "$work/empty.tsv"
check "an empty step table is refused" 1 $?
says "an empty step table says why" "$work/out" "nothing would be checked"

printf 'go\tmissing-command\t\n' >"$work/malformed.tsv"
silent "$work/malformed.tsv"
check "a row with no command is refused" 1 $?

printf 'go\tsame\tgreen\ttrue\ngo\tsame\tgreen\ttrue\n' >"$work/dup.tsv"
silent "$work/dup.tsv"
check "a lane and step named twice is refused" 1 $?
says "the duplicate is named" "$work/out" "used twice — go/same"

printf 'go/x\tsame\tgreen\ttrue\n' >"$work/badlane.tsv"
silent "$work/badlane.tsv"
check "a lane name that cannot name a log file is refused" 1 $?

# --- a nested make must not inherit the parent's flags -------------------------------------

# `make -i check` runs every line and ignores its exit code. MAKEFLAGS carries that `-i` into a child
# make, which would then answer 0 for a failing gate — a green table over a red gate. The fixture below
# is the child: it fails with 3, and only a child that never saw `-i` says so.
printf 'fail:\n\t@exit 3\n' >"$work/fixture.mk"
printf 'go	nested	fail with 3 through make\tmake -f %s/fixture.mk fail\ngo\tgreen\tgreen\ttrue\n' \
    "$work" >"$work/nested.tsv"
MAKEFLAGS=-i MIKE_CHECK_LOGS="$work/logs" MIKE_CHECK_LANES="$work/nested.tsv" bash "$RUNNER" >"$work/out" 2>&1
check "MAKEFLAGS=-i does not swallow a nested make's failure" 1 $?
says "the nested failure reaches the log with the child's own code" "$work/out" "Error 3"

exit "$fail"
