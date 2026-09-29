#!/usr/bin/env bash
# What the test gates cost, measured.
#
# Every timing in this repo was a Makefile comment until this script existed. It measures the wall clock of
# each `make check` sub-gate, of every Go package and test, of every vitest file, and of every Playwright
# spec and test, prints the tables slowest-first, and keeps the raw numbers in $TIMING_DIR/<utc>.json so the
# next change is compared against a run instead of against a memory. `make test-time` runs it; the recorded
# baseline lives in `docs/testing.md → Measured gate cost`.
#
# Lanes are positional arguments (or LANES=...): gates, check, go, vitest, e2e. One lane re-runs cheaply,
# which is the point — a task measuring one package or one spec should not pay for the suite.
#
#   scripts/test-timings.sh                          # every lane
#   scripts/test-timings.sh go                       # one lane
#   GO_PKGS=./backend/git/ scripts/test-timings.sh   # one package (narrows to the go lane)
#   E2E_GREP=chat-core scripts/test-timings.sh       # one spec file (narrows to the e2e lane)
#
# Env:
#   LANES          lanes to run                 (default: gates check go vitest e2e)
#   GO_PKGS        go test scope                (default: ./backend/... ./pkg/... .)
#   GO_PARALLEL    go test -parallel            (default: 4, the value make test-go uses)
#   VITEST_FILTER  vitest file patterns
#   E2E_GREP       playwright positional filter — a spec path pattern, e.g. chat-core
#   E2E_TITLE      playwright --grep — a test-title pattern
#   E2E_ARGS       extra playwright arguments
#   E2E_WORKERS    playwright -j for the measured run
#   E2E_PROBE=0    skip the harness-fixed-cost probe run
#   E2E_RESTART=0  do not restart a dev server this lane had to stop
#   TIMING_DIR     output directory             (default: data/test-timings)
#
# The e2e lane kills ports 3000/3001/9999 (`frontend/tests/global-setup.ts` owns them) and so stops a running
# dev server; it restarts one afterwards if it stopped one, and prints the leftover check either way.
#
# Raw reporter output stays beside the JSON in $TIMING_DIR/<utc>.d/ — go's -json stream, the vitest and
# Playwright reports, and one log per timed unit. Nothing here is a gate: a red unit is timed and printed
# like a green one, because a failure that costs 40 s is still a number. The script exits non-zero when a
# unit exited other than expected, so `make test-time` cannot read as a clean measurement of a red tree.
set -uo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT" || exit 1

TIMING_DIR=${TIMING_DIR:-data/test-timings}
GO_SCOPE_DEFAULT='./backend/... ./pkg/... .'
GO_PARALLEL=${GO_PARALLEL:-4}
STAMP=$(date -u +%Y-%m-%dT%H%M%SZ)
WORK="$TIMING_DIR/$STAMP.d"
RUN_JSON="$TIMING_DIR/$STAMP.json"
UNITS="$WORK/units.tsv"
TOTAL_T0=$(date +%s.%N)

ALL_LANES='gates check go vitest e2e'
# A filter with no LANES names the lane it belongs to, so measuring one package does not also buy a suite.
if [[ -z "${LANES:-}" ]]; then
    if [[ $# -gt 0 ]]; then LANES="$*"
    elif [[ -n "${GO_PKGS:-}" ]]; then LANES=go
    elif [[ -n "${VITEST_FILTER:-}" ]]; then LANES=vitest
    elif [[ -n "${E2E_GREP:-}" || -n "${E2E_TITLE:-}" || -n "${E2E_ARGS:-}" || -n "${E2E_WORKERS:-}" ]]; then LANES=e2e
    else LANES=$ALL_LANES
    fi
fi

mkdir -p "$TIMING_DIR" "$WORK"
: >"$UNITS"

lane_wanted() { [[ " $LANES " == *" $1 "* ]]; }

elapsed() { awk -v a="$1" -v b="$2" 'BEGIN { printf "%.2f", b - a }'; }

# record <lane> <name> <seconds> <exit> <cmd> — one row of the table every table is built from.
record() { printf '%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" "$5" >>"$UNITS"; }

# run_unit <lane> <name> <dir> <cmd...> — time one command, keep its output in a log, print one line.
# A non-zero exit is recorded and the log's tail is echoed; measurement continues.
run_unit() {
    local lane=$1 name=$2 dir=$3
    shift 3
    local log="$WORK/$(printf '%s--%s' "$lane" "$name" | tr -c 'A-Za-z0-9._-' '_').log"
    local t0 t1 rc dur
    t0=$(date +%s.%N)
    if [[ -n "$dir" ]]; then
        ( cd "$dir" && "$@" ) >"$log" 2>&1
    else
        ( "$@" ) >"$log" 2>&1
    fi
    rc=$?
    t1=$(date +%s.%N)
    dur=$(elapsed "$t0" "$t1")
    printf '  %-32s %8s s  exit %s\n' "$name" "$dur" "$rc"
    record "$lane" "$name" "$dur" "$rc" "(cd $dir && $*)"
    if [[ $rc -ne 0 ]]; then
        printf '     %s exited %s; last lines of %s:\n' "$name" "$rc" "${log#"$TIMING_DIR"/}"
        tail -n 12 "$log" | sed 's/^/        /'
    fi
}

# run_stream <lane> <name> <dir> <logfile> <cmd...> — same, but the command's output is stamped with its
# elapsed time into <logfile> and echoed back, for a run whose own log lines are part of the measurement.
run_stream() {
    local lane=$1 name=$2 dir=$3 log=$4
    shift 4
    local t0 t1 rc dur
    t0=$(date +%s.%N)
    if [[ -n "$dir" ]]; then
        ( cd "$dir" && "$@" 2>&1 ) | stamp_stream | tee "$log" | tail -n 25
    else
        ( "$@" 2>&1 ) | stamp_stream | tee "$log" | tail -n 25
    fi
    rc=${PIPESTATUS[0]}
    t1=$(date +%s.%N)
    dur=$(elapsed "$t0" "$t1")
    printf '  %-32s %8s s  exit %s\n' "$name" "$dur" "$rc"
    record "$lane" "$name" "$dur" "$rc" "(cd $dir && $*)"
    return "$rc"
}

# expect_last <code> — the row just recorded was supposed to exit with this code, so a non-zero exit there
# is the measurement working rather than a red tree.
expect_last() {
    sed -i '$ s/$/\t'"$1"'/' "$UNITS"
}

# stamp_stream prefixes each line with its elapsed seconds, so globalSetup and globalTeardown can be read
# off the log: the runner prints "Running N tests using M workers" once globalSetup has returned, and
# global-setup's own "[teardown] ENTRY" line marks where the teardown starts.
stamp_stream() {
    python3 -u -c '
import sys, time
t0 = time.monotonic()
for line in sys.stdin:
    sys.stdout.write("[%7.2f] %s" % (time.monotonic() - t0, line))
    sys.stdout.flush()
'
}

# ---------------------------------------------------------------------------
# gates: every make check sub-gate on its own
# ---------------------------------------------------------------------------

lane_gates() {
    echo
    echo "==> lane gates: each make check sub-gate on its own (warm cache)"
    # The names and the order are the ones `check` runs. `build` is the bare `go build` inside check, not
    # `make build`, which also rebuilds the SPA.
    run_unit gates lint            '' make lint
    run_unit gates archgate        '' make archgate
    run_unit gates test-archgate   '' make test-archgate
    run_unit gates docs-check      '' make docs-check
    run_unit gates test-docs-check '' make test-docs-check
    run_unit gates bridge-check    '' make bridge-check
    run_unit gates build           '' go build -trimpath -o mike .
    run_unit gates check-release   '' make check-release
    run_unit gates test-go         '' make test-go
    run_unit gates svelte-check    frontend npm run check
    # The same compile's other verdict. It is its own row because that is what it costs when nothing earlier
    # in the lane has compiled the tree — which is exactly what the row after it reads as when it has.
    run_unit gates check-unused    frontend npm run check:unused
    run_unit gates test-frontend   '' make test-frontend
    render gates
}

# ---------------------------------------------------------------------------
# check: one whole make check, its steps serial and none skipped
# ---------------------------------------------------------------------------

lane_check() {
    echo
    echo "==> lane check: one whole make check (-i, so a red step does not cut the measurement short)"
    # `check` is one target whose recipe is every step, and make abandons a recipe at its first failing line
    # (-k continues *other targets*, not the rest of this one). So on a tree that is red anywhere, plain
    # `make check` is a lower bound and not a cost. -i runs every line, and its own closing echo then prints
    # "all gates green" whatever happened — the grep below is what turns that back into a verdict.
    run_unit check 'make check (whole, -i)' '' make -i --no-print-directory check
    # -i swallows the failures, so say which steps failed inside the pass rather than leave a bare exit 0.
    local failed
    failed=$(grep -hE '^(FAIL|make(\[[0-9]+\])?: \*\*\*)' "$WORK"/check--make_check*.log 2>/dev/null | head -6)
    if [[ -n "$failed" ]]; then
        echo '     steps that failed inside the pass (the exit column cannot show them):'
        printf '%s\n' "$failed" | sed 's/^/        /'
    fi
    render check
}

# ---------------------------------------------------------------------------
# go: per package and per test, from the JSON event stream
# ---------------------------------------------------------------------------

lane_go() {
    local pkgs=${GO_PKGS:-$GO_SCOPE_DEFAULT}
    local -a pkg_args=()
    read -r -a pkg_args <<<"$pkgs"
    echo
    echo "==> lane go: go test -json -count=1 -parallel $GO_PARALLEL ${pkg_args[*]}"
    local t0 t1 rc
    t0=$(date +%s.%N)
    go test -json -count=1 -parallel "$GO_PARALLEL" "${pkg_args[@]}" \
        >"$WORK/go.jsonl" 2>"$WORK/go.err"
    rc=$?
    t1=$(date +%s.%N)
    printf '  %-32s %8s s  exit %s\n' 'go test (whole lane)' "$(elapsed "$t0" "$t1")" "$rc"
    record go 'go test (whole lane)' "$(elapsed "$t0" "$t1")" "$rc" \
        "go test -json -count=1 -parallel $GO_PARALLEL $pkgs"
    if [[ $rc -ne 0 ]]; then
        printf '     go test exited %s; last lines of %s/go.err:\n' "$rc" "$WORK"
        tail -n 12 "$WORK/go.err" | sed 's/^/        /'
    fi
    printf '%s\n' "$pkgs" >"$WORK/go.scope"
    render go
}

# ---------------------------------------------------------------------------
# vitest: per file
# ---------------------------------------------------------------------------

lane_vitest() {
    local -a filter=()
    [[ -n "${VITEST_FILTER:-}" ]] && read -r -a filter <<<"$VITEST_FILTER"
    echo
    echo "==> lane vitest: vitest run ${filter[*]:-<every file>}"
    run_unit vitest 'vitest run (total)' frontend \
        npx vitest run --reporter=default --reporter=json \
        "--outputFile.json=$ROOT/$WORK/vitest.json" ${filter[@]+"${filter[@]}"}
    render vitest
}

# ---------------------------------------------------------------------------
# e2e: per spec and per test, by project, plus the harness fixed cost
# ---------------------------------------------------------------------------

# Leftovers the harness owes nothing on. Ports and /tmp state are what docs/testing.md names; podman is
# asked directly, because a run that started a sandbox and failed leaves it behind.
e2e_leftovers() {
    local procs ports data containers probe
    procs=$(pgrep -xc mike 2>/dev/null); procs=${procs:-0}
    ports=$(lsof -ti:3000,3001,9999 2>/dev/null | tr '\n' ' ')
    data=$(ls -d /tmp/kanban-test-data* 2>/dev/null | tr '\n' ' ')
    containers=$(timeout 30 podman ps -aq 2>/dev/null | grep -c .); containers=${containers:-0}
    probe=$(timeout 30 podman info >/dev/null 2>&1 && printf 'no' || printf 'yes')
    printf '  leftover pgrep -xc mike     : %s%s\n' "$procs" \
        "$( [[ $procs -eq 0 ]] && printf ' (PASS)' || printf ' (FAIL — make stop)')"
    printf '  leftover 3000/3001/9999     : %s\n' \
        "$( [[ -z "$ports" ]] && printf 'free (PASS)' || printf '%s (FAIL)' "$ports")"
    printf '  leftover /tmp/kanban-test-* : %s\n' \
        "$( [[ -z "$data" ]] && printf 'none (PASS)' || printf '%s (FAIL)' "$data")"
    printf '  leftover podman containers  : %s%s (podman unusable here: %s — docs/testing.md → A machine that cannot start a container)\n' \
        "$containers" "$( [[ $containers -eq 0 ]] && printf ' (PASS)')" "$probe"
}

lane_e2e() {
    echo
    echo "==> lane e2e: Playwright per project, plus globalSetup/globalTeardown"
    # global-setup spawns the two test servers with this shell's environment, and startup refuses to boot
    # without an encryption key. The databases are wiped every run, so a throwaway key is enough.
    if [[ -z "${MIKE_ENCRYPTION_KEY:-}" ]]; then
        export MIKE_ENCRYPTION_KEY='test-timings-throwaway-key'
        echo '  MIKE_ENCRYPTION_KEY was unset: exported a throwaway value for the test servers'
    fi
    # The harness serves frontend/dist from disk and never builds it, so an unbuilt tree proves nothing.
    run_unit e2e 'npm run build (vite)' frontend npm run build

    local had_server=0
    local running
    running=$(pgrep -xc mike 2>/dev/null); running=${running:-0}
    [[ $running -gt 0 ]] && had_server=1
    if [[ $had_server -eq 1 ]]; then
        echo '  stopping the dev server: global-setup owns ports 3000/3001/9999'
        pkill -x mike 2>/dev/null
        sleep 1
    fi

    local -a args=()
    [[ -n "${E2E_TITLE:-}" ]] && args+=(--grep "$E2E_TITLE")
    [[ -n "${E2E_WORKERS:-}" ]] && args+=(-j "$E2E_WORKERS")
    [[ -n "${E2E_ARGS:-}" ]] && read -r -a args <<<"${args[*]:-} $E2E_ARGS"
    [[ -n "${E2E_GREP:-}" ]] && read -r -a args <<<"${args[*]:-} $E2E_GREP"

    run_stream e2e 'playwright test (whole lane)' frontend "$WORK/e2e.log" \
        env PLAYWRIGHT_JSON_OUTPUT_FILE="$ROOT/$WORK/playwright.json" \
        npx playwright test --reporter=list,json ${args[@]+"${args[@]}"}

    # The harness fixed cost on its own: globalSetup boots two servers and seeds both databases and
    # globalTeardown tears that down, with no test in the middle.
    if [[ "${E2E_PROBE:-1}" != "0" ]]; then
        run_stream e2e 'harness fixed cost (no tests)' frontend "$WORK/e2e-probe.log" \
            npx playwright test --reporter=list --grep __timings_probe_no_such_test__ -j 1
        # Zero matching tests is the probe's success condition, and Playwright answers it with
        # "Error: No tests found" and exit 1.
        expect_last 1
    fi

    e2e_leftovers
    if [[ $had_server -eq 1 && "${E2E_RESTART:-1}" != "0" ]]; then
        if [[ -x ./mike ]]; then
            echo '  restarting the dev server this lane stopped: make run'
            ( make run >/dev/null 2>&1 & )
            sleep 3
            local up
            up=$(pgrep -xc mike 2>/dev/null); up=${up:-0}
            printf '  dev server after make run: pgrep -xc mike = %s\n' "$up"
        else
            echo '  dev server not restarted: ./mike is not built — run make build && make run'
        fi
    fi
    render e2e
}

# ---------------------------------------------------------------------------
# Human tables. Every number comes from units.tsv or a reporter's own JSON.
# ---------------------------------------------------------------------------

render() {
    python3 - "$WORK" "$1" <<'PY'
import csv, json, os, re, sys

work, section = sys.argv[1], sys.argv[2]
ROWS = []
try:
    with open(os.path.join(work, 'units.tsv')) as fh:
        for rec in csv.reader(fh, delimiter='\t'):
            if len(rec) >= 5:
                ROWS.append({'lane': rec[0], 'name': rec[1], 'seconds': float(rec[2]),
                             'exit': int(rec[3]), 'cmd': rec[4],
                             'expect': int(rec[5]) if len(rec) > 5 else 0})
except OSError:
    pass
ROOT = os.getcwd()


def exit_cell(u):
    return f"{u['exit']} (expected)" if u['expect'] else str(u['exit'])


def units(lane=None):
    return [u for u in ROWS if lane is None or u['lane'] == lane]


def pick(lane, name):
    return next((u for u in units(lane) if u['name'] == name), None)


def table(title, headers, rows, note=None):
    print('\n' + title)
    if note:
        print(note)
    if not rows:
        print('  (nothing measured)')
        return
    cells = [[str(c) for c in r] for r in rows]
    widths = [max(len(headers[i]), max(len(r[i]) for r in cells)) for i in range(len(headers))]
    print('  ' + '  '.join(h.ljust(widths[i]) for i, h in enumerate(headers)))
    print('  ' + '  '.join('-' * widths[i] for i in range(len(headers))))
    for r in cells:
        print('  ' + '  '.join(r[i].ljust(widths[i]) for i in range(len(headers))))


def short(path):
    """A reporter's absolute path, relative to this tree."""
    p = path.strip()
    return p[len(ROOT) + 1:] if p.startswith(ROOT + '/') else p


def unitlog(lane, name):
    """The log run_unit kept for one unit — the reporter's own summary lines live only there."""
    path = os.path.join(work, re.sub(r'[^A-Za-z0-9._-]', '_', f'{lane}--{name}') + '.log')
    return open(path, errors='replace').read() if os.path.exists(path) else ''


ANSI = re.compile(r'\x1b\[[0-9;]*m')


def strip_ansi(text):
    return ANSI.sub('', text)


# --- gates ----------------------------------------------------------------------

if section in ('gates', 'check'):
    whole = pick('check', 'make check (whole, -i)')
    sub = [u for u in units('gates') if u['name'] != 'make check (whole)']
    table('gates: make check sub-gates, slowest first',
          ['seconds', 'exit', 'sub-gate'],
          [[f"{u['seconds']:.2f}", exit_cell(u), u['name']]
           for u in sorted(sub, key=lambda u: -u['seconds'])])
    msg = f"sum of the {len(sub)} sub-gates {sum(u['seconds'] for u in sub):.2f} s"
    if whole:
        msg += (f"; one whole make check {whole['seconds']:.2f} s — the same steps serially inside one make "
                '(-i, so no step is skipped). The gap is make\'s own overhead plus whatever each step found '
                'cached from the earlier pass; that pass also carries the verdict, because -i ignores a '
                'failing step and its exit code says nothing about the tree.')
    print('\n' + msg)

# --- go -------------------------------------------------------------------------

elif section == 'go':
    scope_file = os.path.join(work, 'go.scope')
    scope = open(scope_file).read().strip() if os.path.exists(scope_file) else ''
    pkgs, tests, odd = {}, {}, []
    stream = os.path.join(work, 'go.jsonl')
    if os.path.exists(stream):
        with open(stream, errors='replace') as fh:
            for line in fh:
                line = line.strip()
                if not line.startswith('{'):
                    if line:
                        odd.append(line)
                    continue
                try:
                    ev = json.loads(line)
                except ValueError:
                    continue
                # `go test -json` names its fields Action / Package / Test / Elapsed (seconds).
                act = ev.get('Action')
                if act not in ('pass', 'fail', 'skip'):
                    continue
                el = ev.get('Elapsed', 0.0)
                if ev.get('Test'):
                    tests[(ev.get('Package', ''), ev['Test'])] = (el, act)
                else:
                    pkgs[ev.get('Package', '')] = (el, act)
        errfile = os.path.join(work, 'go.err')
        if os.path.exists(errfile):
            err = open(errfile, errors='replace').read().strip()
            if err:
                odd.append(err[:800])
    else:
        print('\ngo: no -json stream at ' + stream)
    table('go: per-package duration, slowest first', ['seconds', 'result', 'package'],
          [[f'{v[0]:.2f}', v[1], k] for k, v in sorted(pkgs.items(), key=lambda kv: -kv[1][0])])
    table('go: top 30 slowest tests (a subtest is a test)', ['seconds', 'result', 'test', 'package'],
          [[f'{v[0]:.2f}', v[1], k[1], k[0]]
           for k, v in sorted(tests.items(), key=lambda kv: -kv[1][0])[:30]])
    for u in units('go'):
        print(f"\n{u['name']}: {u['seconds']:.2f} s (exit {u['exit']}) — scope {scope}")
    print(f"packages {len(pkgs)}; tests {len(tests)} "
          f"({sum(1 for v in tests.values() if v[1] == 'pass')} pass, "
          f"{sum(1 for v in tests.values() if v[1] == 'fail')} fail, "
          f"{sum(1 for v in tests.values() if v[1] == 'skip')} skip)")
    if odd:
        print(f"\nevents outside the grammar — {len(odd)} line(s), usually a build or scope failure:")
        for o in odd[:6]:
            print('  ' + o.replace('\n', '\n  ')[:400])

# --- vitest ---------------------------------------------------------------------

elif section == 'vitest':
    path = os.path.join(work, 'vitest.json')
    files, cases = [], []
    if os.path.exists(path):
        for f in json.load(open(path)).get('testResults', []):
            name = short(f.get('name', ''))
            dur = (f.get('endTime', 0) - f.get('startTime', 0)) / 1000.0
            asserts = f.get('assertionResults', [])
            files.append([dur, f.get('status', '?'), name, len(asserts),
                          sum(1 for a in asserts if a.get('status') == 'failed')])
            for a in asserts:
                cases.append([(a.get('duration') or 0) / 1000.0, a.get('status', '?'),
                              a.get('fullName') or a.get('title', ''), name])
        files.sort(key=lambda r: -r[0])
        cases.sort(key=lambda r: -r[0])
    else:
        print('\nvitest: no report at ' + path)
    table('vitest: per-file duration, slowest first', ['seconds', 'status', 'file', 'tests', 'failed'],
          [[f'{r[0]:.2f}', r[1], r[2], r[3], r[4]] for r in files],
          note="  a file's seconds are vitest's own (collection plus its tests) and overlap across workers, so\n"
               '  the total row below is the wall clock, not the sum of this column')
    table('vitest: top 20 slowest tests', ['seconds', 'status', 'test', 'file'],
          [[f'{r[0]:.3f}', r[1], r[2][:70], r[3]] for r in cases[:20]])
    for u in units('vitest'):
        print(f"\n{u['name']}: {u['seconds']:.2f} s (exit {u['exit']})")
    # vitest's own totals name the cost the per-file column cannot: a file's duration excludes the worker's
    # transform, setup and jsdom environment, which is where most of this run's wall clock went.
    summary = [ln.strip() for ln in strip_ansi(unitlog('vitest', 'vitest run (total)')).splitlines()
               if re.match(r'^\s*(Test Files|Tests|Duration)\s', ln)]
    for ln in summary:
        print('  ' + ln)
    print(f"files {len(files)}; tests {len(cases)}; failed {sum(1 for c in cases if c[1] == 'failed')}")

# --- e2e ------------------------------------------------------------------------

elif section == 'e2e':
    path = os.path.join(work, 'playwright.json')
    specs, cases, projects = set(), [], {}

    # Playwright's JSON report carries no project on a test. The two lanes are disjoint by filename and
    # ISOLATED_SPECS in frontend/playwright.config.ts is what makes them so (the chromium testMatch is
    # derived from it), so the config decides which lane a spec belongs to.
    cfg = os.path.join(ROOT, 'frontend', 'playwright.config.ts')
    isolated = set()
    if os.path.exists(cfg):
        block = re.search(r'const ISOLATED_SPECS = \[(.*?)\];', open(cfg).read(), re.S)
        isolated = set(re.findall(r"'([^']+)'", block.group(1))) if block else set()

    def project_of(spec_file, reported):
        if reported:
            return reported
        stem = os.path.basename(spec_file).replace('.spec.ts', '')
        return 'isolated' if stem in isolated else 'chromium'

    if os.path.exists(path):
        report = json.load(open(path))

        def walk(node, file_title):
            for spec in node.get('specs', []):
                fname = spec.get('file') or file_title or '?'
                specs.add(fname)
                for t in spec.get('tests', []):
                    proj = project_of(fname, t.get('projectName'))
                    dur = sum(r.get('duration', 0) for r in t.get('results', [])) / 1000.0
                    status = t.get('status', '?')
                    cases.append([dur, status, spec.get('title', ''), fname, proj])
                    p = projects.setdefault(proj, [0.0, 0, 0, {}])
                    p[0] += dur
                    p[1] += 1
                    p[2] += 1 if status not in ('expected', 'skipped') else 0
                    sp = p[3].setdefault(fname, [0.0, 0])
                    sp[0] += dur
                    sp[1] += 1
            for child in node.get('suites', []):
                walk(child, file_title or node.get('title'))

        for suite in report.get('suites', []):
            walk(suite, suite.get('title'))
    else:
        print('\ne2e: no report at ' + path)
    for proj, (dur, n, bad, per_spec) in sorted(projects.items(), key=lambda kv: -kv[1][0]):
        table(f'e2e: project {proj} — {n} tests, {dur:.1f} s of test time, {bad} not passing',
              ['seconds', 'tests', 'spec'],
              [[f'{v[0]:.2f}', v[1], s] for s, v in sorted(per_spec.items(), key=lambda kv: -kv[1][0])])
    table('e2e: top 30 slowest tests', ['seconds', 'status', 'test', 'spec', 'project'],
          [[f'{r[0]:.2f}', r[1], r[2][:58], r[3], r[4]] for r in sorted(cases, key=lambda r: -r[0])[:30]])

    # globalSetup and globalTeardown are not in Playwright's report. They are read off the stamped log: the
    # runner prints "Running N tests using M workers" once globalSetup has returned, and global-setup's own
    # first teardown line marks where the teardown starts (the reporter's summary comes after it).
    log = os.path.join(work, 'e2e.log')
    logtext = open(log, errors='replace').read() if os.path.exists(log) else ''

    def markers(text):
        """(tests start, teardown start) as elapsed seconds of a stamped log."""
        start = end = None
        for line in strip_ansi(text).splitlines():
            m = re.match(r'\[\s*([\d.]+)\]', line)
            if not m:
                continue
            ts, rest = float(m.group(1)), line[m.end():]
            if start is None and (re.search(r'Running \d+ tests? (using|across)', rest)
                                  or 'Error: No tests found' in rest):
                start = ts
            if '[teardown] ENTRY' in rest:
                end = ts
        return start, end

    setup_ts, teardown_ts = markers(logtext)
    lane, probe = pick('e2e', 'playwright test (whole lane)'), pick('e2e', 'harness fixed cost (no tests)')
    print('\ne2e: the harness around the tests')
    reported = [ln[ln.find(']') + 1:].strip() for ln in strip_ansi(logtext).splitlines()
                if ']' in ln and re.match(r'\s*\d+ (passed|failed|flaked|interrupted|skipped|did not run)\b',
                                          ln[ln.find(']') + 1:])]
    if reported:
        print('  reporter summary: ' + ', '.join(reported))
    print('  globalSetup    ' + (f'{setup_ts:.2f} s  (process start → "Running N tests using M workers": '
                                 'both servers, both databases, the seed projects, the fixture repos and '
                                 'worker startup)'
                                 if setup_ts is not None else
                                 'not measured — no "Running N tests" line, so no test ran'))
    if teardown_ts is not None and lane:
        print(f'  globalTeardown {lane["seconds"] - teardown_ts:.2f} s  ([teardown] ENTRY → process exit: '
              'stopping both servers by identity and deleting the data directories)')
    if lane:
        print(f'  whole lane     {lane["seconds"]:.2f} s (exit {lane["exit"]}) — setup, teardown, worker '
              'startup and the test rows above')
    if probe:
        probe_log = os.path.join(work, 'e2e-probe.log')
        p_text = open(probe_log, errors='replace').read() if os.path.exists(probe_log) else ''
        p_start, p_end = markers(p_text)
        print(f'  harness fixed cost {probe["seconds"]:.2f} s (exit {exit_cell(probe)}) — globalSetup + '
              'globalTeardown with zero tests (--grep that matches nothing), this process end to end')
        if p_start is not None and p_end is not None:
            harness = p_start + probe['seconds'] - p_end
            print(f'    of which globalSetup {p_start:.2f} s + globalTeardown '
                  f'{probe["seconds"] - p_end:.2f} s = {harness:.2f} s of harness, and '
                  f'{probe["seconds"] - harness:.2f} s of runner boot')
    build = pick('e2e', 'npm run build (vite)')
    if build:
        print(f'  npm run build  {build["seconds"]:.2f} s (exit {build["exit"]}) — global-setup serves '
              'frontend/dist and never builds it')
    print(f"specs {len(specs)}; tests {len(cases)}; "
          f"not passing {sum(1 for c in cases if c[1] not in ('expected', 'skipped'))}")

# --- every unit ------------------------------------------------------------------

else:
    table('every measured unit, slowest first', ['seconds', 'exit', 'lane', 'unit'],
          [[f"{u['seconds']:.2f}", exit_cell(u), u['lane'], u['name']]
           for u in sorted(ROWS, key=lambda u: -u['seconds'])])
    bad = [u for u in units() if u['exit'] != u['expect']]
    if bad:
        print('\nunexpected exits (timed anyway): '
              + ', '.join(f"{u['lane']}/{u['name']}" for u in bad))
PY
}

# The one JSON file this run produces: the host it ran on, every timed unit, and every reporter report
# verbatim, so a later comparison needs this file and nothing else.
merge() {
    python3 - "$WORK" "$RUN_JSON" "$STAMP" "${TIMING_TOTAL_SECONDS:-}" <<'PY'
import csv, json, os, platform, sys

work, outjson, stamp, total = sys.argv[1:5]
rows = []
try:
    with open(os.path.join(work, 'units.tsv')) as fh:
        for rec in csv.reader(fh, delimiter='\t'):
            if len(rec) >= 5:
                rows.append({'lane': rec[0], 'name': rec[1], 'seconds': float(rec[2]),
                             'exit': int(rec[3]), 'cmd': rec[4],
                             'expect': int(rec[5]) if len(rec) > 5 else 0})
except OSError:
    pass


def sh(cmd):
    try:
        return ' '.join(os.popen(cmd + ' 2>/dev/null').read().split())
    except OSError:
        return ''


def tail(name, limit):
    path = os.path.join(work, name)
    if not os.path.exists(path):
        return None
    return ''.join(open(path, errors='replace').readlines()[-limit:])


out = {
    'schema': 1,
    'generated_utc': stamp,
    'host': {
        'hostname': platform.node(),
        'kernel': platform.release(),
        'cpu_count': os.cpu_count(),
        'go': sh('go version'),
        'node': sh('node --version'),
        'playwright': sh('cd frontend && npx playwright --version'),
        'podman': sh('timeout 30 podman info >/dev/null 2>&1 && echo usable')
                  or 'unusable (docs/testing.md → A machine that cannot start a container)',
    },
    'seconds_total': float(total) if total else None,
    'lanes': sorted({u['lane'] for u in rows}),
    'units': rows,
    'artifacts': {name: os.path.abspath(os.path.join(work, name))
                  for name in sorted(os.listdir(work))} if os.path.isdir(work) else {},
}

scope = os.path.join(work, 'go.scope')
if os.path.exists(os.path.join(work, 'go.jsonl')):
    out['go'] = {'scope': open(scope).read().strip() if os.path.exists(scope) else None,
                 'jsonl': os.path.abspath(os.path.join(work, 'go.jsonl')),
                 'stderr': tail('go.err', 200)}
for name, key in (('vitest.json', 'vitest'), ('playwright.json', 'e2e')):
    path = os.path.join(work, name)
    if os.path.exists(path):
        out.setdefault(key, {})['report'] = json.load(open(path))
for name, key, limit in (('e2e.log', 'stamped_log_tail', 300), ('e2e-probe.log', 'probe_log_tail', 40)):
    body = tail(name, limit)
    if body is not None:
        out.setdefault('e2e', {})[key] = body

json.dump(out, open(outjson, 'w'), indent=1)
print(f'wrote {outjson} ({os.path.getsize(outjson)} bytes)')
print(f'raw artifacts kept in {work}/')
PY
}

usage() { sed -n '2,35p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

case "${1:-}" in
    -h|--help|help) usage; exit 0 ;;
esac

echo "test-timings: lanes     : $LANES"
echo "test-timings: json      : $RUN_JSON"
echo "test-timings: artifacts : $WORK/"

lane_wanted gates  && lane_gates
lane_wanted check  && lane_check
lane_wanted go     && lane_go
lane_wanted vitest && lane_vitest
lane_wanted e2e    && lane_e2e

render units
export TIMING_TOTAL_SECONDS=$(elapsed "$TOTAL_T0" "$(date +%s.%N)")
merge
if [[ " $LANES " == *' e2e '* ]]; then
    echo 'note: the e2e lane stops the dev server. make run if it stopped one and did not restart it.'
fi

# A red unit is measured, not hidden — and it still fails the command, so a measurement of a red tree
# cannot be handed on as a green one. A unit whose expected exit is non-zero is not red.
if [[ $(awk -F'\t' '{ want = ($6 == "") ? 0 : $6 } $4 != want { n++ } END { print n + 0 }' "$UNITS") -gt 0 ]]; then
    echo 'test-timings: at least one unit exited unexpectedly — see the exit column above'
    exit 1
fi
