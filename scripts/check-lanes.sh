#!/usr/bin/env bash
# `make check`'s two lanes, side by side.
#
# The gate's eleven sub-gates split into two sets that read and write nothing the other touches: the Go
# half (lint, the two archgate targets, bridge-check, the build, the release variant, the Go suite) writes
# `./mike` and the Go build cache, and the frontend half (the two docs gates, the one typecheck compile and
# `make test-frontend`) writes `frontend/node_modules/.cache`. In series the gate costs their sum; run here it
# costs the slower lane, which is slower beside the other lane than alone.
# `docs/testing.md → Measured gate cost` prices all three numbers, and
# `docs/adr/0002-check-lanes-run-in-parallel.md` holds why this runner exists and `make -j` does not.
#
# Neither lane may hide the other: a failing step ends *its* lane and every other lane runs to its own end,
# so a red Go gate cannot hide a frontend failure. Each lane's exit code is read back from its own `.exit`
# file rather than from a wait status, so a lane that was killed or never started is a named failure and not
# a green row, and a step a stopped lane never reached prints as `skip` rather than green.
#
# The step table below is the whole definition of the gate's shape.
#
#   make check                          # both lanes, concurrently
#   scripts/check-lanes.sh --lanes      # the table, without running it
#
# Env:
#   MIKE_CHECK_LANES   a step table to run instead of the default one (test hook)
#   MIKE_CHECK_LOGS    where the per-lane logs go                (default data/check-lanes)
#
# `make -i check` still runs the whole gate and still ignores its exit code, so `check-lanes: FAILED` is the
# line to grep in a log whose exit column cannot be trusted.
#
# scripts/check-lanes_test.sh (`make test-check-lanes`) tests this runner's own behaviour, and `make check`
# runs it first: a runner tested from inside itself says nothing about a runner that failed to start.
set -uo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT" || exit 1

LOGDIR=${MIKE_CHECK_LOGS:-data/check-lanes}
# A nested make inherits MAKEFLAGS, so `make -i check` would reach the child makes and turn every failing
# step into exit 0 — a green table over a red gate. Each child make is told not to echo directories
# instead, in the table below.
unset MAKEFLAGS MFLAGS

# lane <TAB> step <TAB> what it is for <TAB> command. Lanes start together; a lane's steps run in table
# order. The two lanes stay disjoint: a step that reads what the other lane writes makes the lane wall
# clocks a race rather than a measurement. `build` is the bare `go build`, not `make build`, which would
# rebuild `frontend/dist` while the frontend lane is reading it.
default_table() {
    cat <<'TABLE'
go	lint	gofmt + scoped vet + staticcheck (all checks) + deadcode (every root)	make --no-print-directory lint
go	archgate	structural debt against its frozen baselines	make --no-print-directory archgate
go	test-archgate	the structural gate itself, both directions	make --no-print-directory test-archgate
go	bridge-check	a stale sandbox-bridge is an edit that never runs	make --no-print-directory bridge-check
go	build	go build -trimpath -o mike .	go build -trimpath -o mike .
go	check-release	the -tags release variant that make package ships	make --no-print-directory check-release
go	test-go	Go unit tests over the whole scope, result cache defeated	make --no-print-directory test-go
frontend	docs-check	pointers, indexes, line refs, env table, ADR shape, ratchet	make --no-print-directory docs-check
frontend	test-docs-check	the documentation gate itself, both directions	make --no-print-directory test-docs-check
frontend	svelte-check	the one typecheck compile, both verdicts	cd frontend && npm run check
frontend	test-frontend	frontend unit tests + the gates, in parallel	make --no-print-directory test-frontend
TABLE
}

table() {
    if [ -n "${MIKE_CHECK_LANES:-}" ] && [ -f "$MIKE_CHECK_LANES" ]; then
        cat "$MIKE_CHECK_LANES"
    else
        default_table
    fi
}

if [ "${1:-}" = --lanes ]; then
    table
    exit 0
fi

lanes=()
steps=()
notes=()
cmds=()
while IFS=$'\t' read -r lane step note cmd; do
    case "${lane:-}" in '' | '#'*) continue ;; esac
    if [ -z "${step:-}" ] || [ -z "${cmd:-}" ]; then
        echo "check-lanes: malformed step table row: $lane / $step / $cmd" >&2
        exit 1
    fi
    lanes+=("$lane")
    steps+=("$step")
    notes+=("$note")
    cmds+=("$cmd")
done < <(table)

if [ ${#lanes[@]} -eq 0 ]; then
    echo "check-lanes: the step table is empty — nothing would be checked" >&2
    exit 1
fi

# A lane and step used twice would share one log and one exit file, so one step's verdict would be read as
# two. `printf` over two arrays pairs them the wrong way round, hence the loop.
dups=$(for i in "${!lanes[@]}"; do printf '%s/%s\n' "${lanes[$i]}" "${steps[$i]}"; done | LC_ALL=C sort | uniq -d)
if [ -n "$dups" ]; then
    echo "check-lanes: lane and step used twice — $(tr '\n' ' ' <<<"$dups")" >&2
    exit 1
fi

# Lane order is the table's order of first appearance.
lane_names=()
for lane in "${lanes[@]}"; do
    known=0
    for name in "${lane_names[@]}"; do
        [ "$name" = "$lane" ] && known=1 && break
    done
    [ $known -eq 0 ] && lane_names+=("$lane")
done

for lane in "${lane_names[@]}"; do
    case "$lane" in
    *[!a-zA-Z0-9._-]*)
        echo "check-lanes: lane name '$lane' cannot name a log file" >&2
        exit 1 ;;
    esac
done

rm -rf "$LOGDIR" && mkdir -p "$LOGDIR" || exit 1

elapsed() { awk -v a="$1" -v b="$2" 'BEGIN { printf "%.2f", b - a }'; }
# read_or <file> <fallback> — a lane's stamp files are written as it goes, so an early read is normal.
read_or() { [ -f "$1" ] && cat "$1" || printf '%s' "$2"; }

# ---------------------------------------------------------------------------
# One lane: its steps in order, into its own log, its verdict in its own file
# ---------------------------------------------------------------------------

# run_lane <lane> — this lane's steps in table order. The first failure stops this lane and becomes the
# lane's exit code, so the summary names the step's own verdict rather than a bare 1. No other lane reads
# these files, so nothing here needs to be atomic against a sibling.
run_lane() {
    local lane=$1 rc=0 stopped='' i secs rc0
    local log="$LOGDIR/$lane.log" steplog="$LOGDIR/$lane.steps"
    date +%s.%N >"$LOGDIR/$lane.t0"
    : >"$log"
    printf '%-16s %8s %6s\n' step seconds exit >"$steplog"
    for i in "${!lanes[@]}"; do
        [ "${lanes[$i]}" != "$lane" ] && continue
        if [ -n "$stopped" ]; then
            printf '%-16s %8s %6s  %s\n' "${steps[$i]}" '-' skip "$stopped" >>"$steplog"
            continue
        fi
        printf '==> %s / %s: %s\n\n' "$lane" "${steps[$i]}" "${notes[$i]}" >>"$log"
        date +%s.%N >"$LOGDIR/$lane.step.t0"
        printf '%s' "${steps[$i]}" >"$LOGDIR/$lane.step"
        date +%s.%N >"$LOGDIR/$lane.now"
        bash -c "${cmds[$i]}" >>"$log" 2>&1
        rc0=$?
        secs=$(elapsed "$(cat "$LOGDIR/$lane.step.t0")" "$(date +%s.%N)")
        if [ $rc0 -eq 0 ]; then
            printf '%-16s %8s %6s\n' "${steps[$i]}" "$secs" 0 >>"$steplog"
        else
            printf '%-16s %8s %6s  FAILED\n' "${steps[$i]}" "$secs" "$rc0" >>"$steplog"
            printf '\n==> %s / %s: exit %s — this lane stops here; the other lanes are unaffected\n' \
                "$lane" "${steps[$i]}" "$rc0" >>"$log"
            # The summary names the step, not just the lane: the one line a caller greps should be enough.
            printf '%s/%s (exit %s)\n' "$lane" "${steps[$i]}" "$rc0" >"$LOGDIR/$lane.failed"
            stopped="not run: lane stopped after ${steps[$i]}"
            rc=$rc0
        fi
    done
    date +%s.%N >"$LOGDIR/$lane.t1"
    printf '%s\n' "$rc" >"$LOGDIR/$lane.exit"
}

start=$(date +%s.%N)
pids=()
ticks=0
echo "==> make check: ${#lane_names[@]} lanes in parallel ($(nproc) CPUs), ${#steps[@]} gates, logs in $LOGDIR"
for lane in "${lane_names[@]}"; do
    run_lane "$lane" &
    pids+=("$!")
done

# A wait this long is watched, so it announces itself: every 30 s, where each lane has got to.
progress() {
    local now lane step
    now=$(date +%s.%N)
    for lane in "${lane_names[@]}"; do
        if [ -f "$LOGDIR/$lane.exit" ]; then
            printf '    [%.0fs] %s: finished\n' "$(elapsed "$start" "$now")" "$lane"
        else
            step=$(read_or "$LOGDIR/$lane.step" starting)
            printf '    [%.0fs] %s: %s, %ss into the lane\n' \
                "$(elapsed "$start" "$now")" "$lane" "$step" \
                "$(elapsed "$(read_or "$LOGDIR/$lane.now" "$start")" "$now")"
        fi
    done
}

while :; do
    alive=0
    for pid in "${pids[@]}"; do
        kill -0 "$pid" 2>/dev/null && alive=1
    done
    [ $alive -eq 0 ] && break
    # 0.2 s keeps a short run's wall honest; 150 of them is the heartbeat.
    sleep 0.2
    ticks=$((ticks + 1))
    [ $((ticks % 150)) -eq 0 ] && progress
done
for pid in "${pids[@]}"; do
    wait "$pid"
done
end=$(date +%s.%N)
wall=$(elapsed "$start" "$end")

# ---------------------------------------------------------------------------
# Each lane's log under its own header, then one table of what the run decided
# ---------------------------------------------------------------------------

for lane in "${lane_names[@]}"; do
    code=$(read_or "$LOGDIR/$lane.exit" missing)
    echo
    echo "==> lane $lane (exit $code) — full log: $LOGDIR/$lane.log"
    if [ -f "$LOGDIR/$lane.steps" ]; then
        sed 's/^/    /' "$LOGDIR/$lane.steps"
    else
        echo "    no step list — the lane did not run"
    fi
    if [ -f "$LOGDIR/$lane.log" ]; then
        sed 's/^/    /' "$LOGDIR/$lane.log"
    else
        echo "    no log — the lane did not run"
    fi
done

fail=()
ran=0
printf '\n==> lane results (%ss wall)\n' "$wall"
printf '  %-10s %8s %7s  %s\n' lane seconds verdict log
for lane in "${lane_names[@]}"; do
    if [ -f "$LOGDIR/$lane.exit" ]; then
        code=$(cat "$LOGDIR/$lane.exit")
        ran=$((ran + 1))
    else
        code=missing
        fail+=("$lane (no exit code — the lane did not finish)")
    fi
    secs=$(elapsed "$(read_or "$LOGDIR/$lane.t0" "$start")" "$(read_or "$LOGDIR/$lane.t1" "$end")")
    verdict=pass
    if [ "$code" != 0 ]; then
        verdict=FAIL
        case "$code" in
        missing) ;;
        *) fail+=("$(read_or "$LOGDIR/$lane.failed" "$lane (exit $code)")") ;;
        esac
    fi
    printf '  %-10s %8s %7s  %s\n' "$lane" "$secs" "$verdict" "$LOGDIR/$lane.log"
done

if [ "$ran" -ne "${#lane_names[@]}" ]; then
    echo "check-lanes: $ran of ${#lane_names[@]} lanes reported an exit code"
fi

echo
if [ ${#fail[@]} -gt 0 ]; then
    joined=$(printf '%s; ' "${fail[@]}")
    echo "check-lanes: FAILED in ${wall}s — ${joined%; }"
    echo "check-lanes: the lane log above names the step, and its own output is beside it in $LOGDIR"
    exit 1
fi
echo "check-lanes: ${#lane_names[@]} lanes green in ${wall}s (${#steps[@]} gates)"
echo ""
echo "make check: all gates green"
