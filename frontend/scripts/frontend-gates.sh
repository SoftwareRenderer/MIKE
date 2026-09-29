#!/usr/bin/env bash
# Every `make test-frontend` gate, side by side.
#
# The gates are independent reads over the same tree: one 1338-file svelte-check compile, one vitest run, one
# knip pass, and six scan slots that cost under two seconds each on an idle machine. Run in series their wall
# clock is the sum; run here it is the slowest slot. `docs/testing.md → Measured gate cost` prices both shapes.
#
# Each slot is a chain of commands that must stay in order — a gate's own self-test runs before the gate,
# because "the counter can fail" is the claim the counter rests on — and each slot keeps its own log under
# node_modules/.cache/frontend-gates. Nothing here may read green that did not run: every slot line is
# launched, every slot's exit code is read back from its own file, and a slot that left no exit file, exited
# non-zero, or whose command does not exist fails the run by name. `make test-frontend` runs
# `npm run check:gates-test` in front of this script — a self-test that ran inside the runner could say
# nothing about a runner that failed to start — and that test also insists the table below still names every
# `check:*` script in package.json except the ones exempted there with a reason.
#
# A green slot's stdout is printed, because these gates print numbers on purpose (the dead-surface ceiling and
# the i18n surface count make their claim on stdout, not in the exit code). The one exception is vitest, whose
# per-file lines are hundreds: its summary lines are printed and the log is named.
#
# vitest is a slot like any other, and that is measured rather than assumed: beside the compile it cost
# 203.26 s against 202.14 s alone on 8 CPUs, so the pair is cheaper than the two in series.
#
# Env:
#   MIKE_GATE_LOGS   where the per-slot logs go   (default node_modules/.cache/frontend-gates)
#   MIKE_GATE_SLOTS  a slot table to run instead of the default one (test hook: name/print/command per line)
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

LOGDIR=${MIKE_GATE_LOGS:-node_modules/.cache/frontend-gates}

# name <TAB> green-output <TAB> command chain. `full` prints the log on green, `vitest-summary` prints the
# reporter's own summary lines. Order is only the printing order; every slot starts at the same moment.
default_slots() {
    cat <<'SLOTS'
types-test	full	npm run check:typecheck-test
types	full	npm run check:unused
vitest	vitest-summary	npm test
dead	full	npm run check:dead
i18n	full	npm run check:i18n && npm run check:i18n-surface-test && npm run check:i18n-surface
sinks	full	npm run check:sinks-test && npm run check:sinks
surface	full	npm run check:dead-surface-test && npm run check:dead-surface
sleeps	full	npm run check:sleeps-test && npm run check:sleeps
fetch	full	npm run check:fetch
colors	full	npm run check:colors
SLOTS
}

slot_table() {
    if [ -n "${MIKE_GATE_SLOTS:-}" ] && [ -f "$MIKE_GATE_SLOTS" ]; then
        cat "$MIKE_GATE_SLOTS"
    else
        default_slots
    fi
}

# --slots prints the table and stops: the self-test reads it, and a person may want to see what runs.
if [ "${1:-}" = --slots ]; then
    slot_table
    exit 0
fi

rm -rf "$LOGDIR" && mkdir -p "$LOGDIR" || exit 1

names=()
prints=()
cmds=()
while IFS=$'\t' read -r name print cmd; do
    [ -z "${name// }" ] && continue
    case "$name" in '#'*) continue ;; esac
    names+=("$name")
    prints+=("$print")
    cmds+=("$cmd")
done < <(slot_table)

if [ ${#names[@]} -eq 0 ]; then
    echo "frontend-gates: the slot table is empty — nothing would be checked" >&2
    exit 1
fi

# Two slots with one name would share one log and one exit file, so one gate's red could be read as the
# other's green.
dups=$(printf '%s\n' "${names[@]}" | LC_ALL=C sort | uniq -d)
if [ -n "$dups" ]; then
    echo "frontend-gates: slot name used twice — $(tr '\n' ' ' <<<"$dups")" >&2
    exit 1
fi

echo "==> frontend gates: ${#names[@]} slots in parallel ($(nproc) CPUs), logs in $LOGDIR"

start=$(date +%s.%N)
pids=()
for i in "${!names[@]}"; do
    log=$LOGDIR/${names[$i]}.log
    # The command runs through bash -c so a slot may chain its self-test in front of its gate. `time` is not
    # portable here, so the slot stamps its own start and stop.
    (
        date +%s.%N >"$LOGDIR/${names[$i]}.t0"
        bash -c "${cmds[$i]}" >"$log" 2>&1
        printf '%s\n' "$?" >"$LOGDIR/${names[$i]}.exit"
        date +%s.%N >"$LOGDIR/${names[$i]}.t1"
    ) &
    pids+=("$!")
done
for pid in "${pids[@]}"; do
    wait "$pid"
done
end=$(date +%s.%N)

elapsed=$(awk -v a="$start" -v b="$end" 'BEGIN { printf "%.2f", b - a }')

# Read every slot back. A missing exit file means the slot never finished its command — killed, or never
# started — and is a failure, not a blank cell.
fail=()
ran=0
printf '\n==> slot results (%ss wall)\n' "$elapsed"
printf '  %-8s %8s %5s  %s\n' slot seconds exit log
for i in "${!names[@]}"; do
    name=${names[$i]}
    exitfile=$LOGDIR/$name.exit
    if [ -f "$exitfile" ]; then
        code=$(cat "$exitfile")
        ran=$((ran + 1))
    else
        code=missing
        fail+=("$name (no exit code — the slot did not finish)")
    fi
    secs=$(awk -v a="$(cat "$LOGDIR/$name.t0" 2>/dev/null || echo "$start")" \
        -v b="$(cat "$LOGDIR/$name.t1" 2>/dev/null || echo "$end")" 'BEGIN { printf "%.2f", b - a }')
    printf '  %-8s %8s %5s  %s\n' "$name" "$secs" "$code" "$LOGDIR/$name.log"
    if [ "$code" != 0 ]; then
        case "$code" in
        127) fail+=("$name — command not found (${cmds[$i]})") ;;
        missing) ;;
        *) fail+=("$name (exit $code): ${cmds[$i]}") ;;
        esac
    fi
done

if [ "$ran" -ne "${#names[@]}" ]; then
    echo "frontend-gates: $ran of ${#names[@]} slots reported an exit code" >&2
fi

# The logs: what each gate measured stays on stdout, in slot order.
for i in "${!names[@]}"; do
    name=${names[$i]}
    log=$LOGDIR/$name.log
    code=$(cat "$LOGDIR/$name.exit" 2>/dev/null || echo missing)
    echo
    echo "==> $name: ${cmds[$i]}  (exit $code)"
    if [ ! -f "$log" ]; then
        echo "    no log file — the slot did not run"
        continue
    fi
    if [ "$code" = 0 ] && [ "${prints[$i]}" = vitest-summary ]; then
        # The reporter colours its own summary, so the escapes come off before the summary lines are picked.
        sed 's/\x1b\[[0-9;]*m//g' "$log" | grep -E 'Test Files|Tests +[0-9]|Duration|Snapshots' \
            || tail -n 5 "$log"
        echo "    full log: $log"
    else
        cat "$log"
    fi
done

echo
if [ ${#fail[@]} -gt 0 ] || [ "$ran" -ne "${#names[@]}" ]; then
    echo "frontend-gates: FAILED"
    for f in "${fail[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
echo "frontend-gates: ${#names[@]} slots green in ${elapsed}s"
