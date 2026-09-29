#!/usr/bin/env python3
"""Measure what the e2e lane costs before and after its own first test.

    cd frontend && npx playwright test            # the run
    python3 scripts/e2e-time.py [-- <playwright args>]   # the same run, measured

    --show-specs=N   per-lane spec files to print (default 6; 0 prints none)
    --json=PATH      keep the JSON report at PATH instead of a temp file

The suite's fixed cost is invisible in Playwright's own summary: the reporter prints one number for
the tests, and global setup (fixture repos, the lane servers) and global teardown (stopping them,
deleting the data dirs) happen outside it. This runs `npx playwright test` with the `list` reporter
passed through and the `json` reporter writing to a file, then splits the wall clock at lines the
harness already prints and adds the per-spec durations from that JSON. Nothing in
`frontend/tests/global-setup.ts` changes to be measurable.

The per-spec durations are reported **per lane**, because a lane's own wall total is what a lane change
moves: the run body is the longest lane, and a project that finishes early leaves its workers to the
others.

The split is taken from:

    [global-setup] …            → setup, until Playwright prints
    Running N tests using W workers
    … (the tests)               → the run body, until
    [teardown] ENTRY            → teardown, until the runner exits

`[teardown] ENTRY` is matched at its LAST occurrence, because a run that recovers a stale lock calls
teardown once inside setup and once for real.

Exit code is Playwright's.
"""
import datetime
import json
import os
import re
import subprocess
import sys
import tempfile
import time

FRONTEND = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), 'frontend')
RUNNING_RE = re.compile(r'^Running \d+ tests? using ')
TEARDOWN_RE = re.compile(r'^\[teardown\] ENTRY')
SUMMARY_RE = re.compile(r'^\s*\d+ (passed|failed|interrupted|flaky|skipped)\b')


def dist_is_stale() -> bool:
    """True when a file under frontend/src is newer than the built document.

    The lane servers serve `frontend/dist` from disk, so a run proves what the last `npm run build`
    contained (docs/testing.md → E2E conventions). Warned about, never enforced: the run stays
    someone's deliberate `make test-time`.
    """
    index = os.path.join(FRONTEND, 'dist', 'index.html')
    try:
        built = os.path.getmtime(index)
    except OSError:
        return True
    for root, dirs, files in os.walk(os.path.join(FRONTEND, 'src')):
        dirs[:] = [d for d in dirs if d != 'node_modules']
        for name in files:
            if name.endswith(('.ts', '.svelte', '.css', '.js')):
                try:
                    if os.path.getmtime(os.path.join(root, name)) > built:
                        return True
                except OSError:
                    continue
    return False


def spec_durations(report: dict) -> dict:
    """{lane: (tests, [(file, seconds)], lane_wall_seconds)} for every lane the report holds.

    A file's time is the sum of its tests' durations: Playwright reports per-test numbers and the worker
    overhead between them belongs to the lane, not to the file. A retried test contributes every attempt,
    so a red run's lane total is an upper bound.

    Keyed by project ("lane") because the projects share one report and one wall clock, and a lane's own
    wall clock — first test start to last test end — is what a lane split moves: projects are queued in
    order, so the run body is the longest lane plus whatever of the others overlaps it.
    """
    totals, files, spans = {}, {}, {}

    def walk(suite):
        for spec in suite.get('specs') or []:
            name = spec.get('file') or spec.get('title') or '?'
            for test in spec.get('tests') or []:
                lane = test.get('projectName') or '?'
                results = test.get('results') or []
                secs = sum((r.get('duration') or 0) for r in results) / 1000.0
                totals[lane] = totals.get(lane, 0) + 1
                per_file = files.setdefault(lane, {})
                per_file[name] = per_file.get(name, 0.0) + secs
                for r in results:
                    stamp = parse_time(r.get('startTime'))
                    if stamp is None:
                        continue
                    first, last = spans.get(lane, (stamp, stamp))
                    spans[lane] = (min(first, stamp), max(last, stamp + (r.get('duration') or 0) / 1000.0))
        for child in suite.get('suites') or []:
            walk(child)

    for suite in report.get('suites') or []:
        walk(suite)
    return {
        lane: (totals[lane], sorted(per_file.items(), key=lambda kv: -kv[1]),
               (spans[lane][1] - spans[lane][0]) if lane in spans else None)
        for lane, per_file in files.items()
    }


def parse_time(stamp):
    """The JSON reporter's ISO-8601 `startTime` as seconds, or None when it is absent."""
    if not stamp:
        return None
    try:
        return datetime.datetime.fromisoformat(stamp.replace('Z', '+00:00')).timestamp()
    except ValueError:
        return None


def print_lanes(lanes: dict, show_specs: int) -> None:
    """One line per lane, then its slowest files. The longest lane is the run body's critical path."""
    for lane, (tests, specs, wall) in sorted(lanes.items(), key=lambda kv: -(kv[1][2] or 0)):
        secs = sum(s for _, s in specs)
        wall_txt = f'{wall:.2f}s wall clock' if wall is not None else 'wall clock n/a'
        print(f'[test-time] lane {lane}: {wall_txt} · {tests} tests in {len(specs)} files · '
              f'{secs:.2f}s summed test time')
        for file, spec_secs in specs[:show_specs]:
            print(f'[test-time]   {spec_secs:6.2f}s  {file}')


def main(argv) -> int:
    show_specs, json_arg, keep_json = 6, None, False
    while argv and argv[0].startswith('--') and argv[0] != '--':
        arg = argv.pop(0)
        if arg == '--show-specs' or arg.startswith('--show-specs='):
            value = arg.split('=', 1)[1] if '=' in arg else argv.pop(0)
            show_specs = int(value)
        elif arg == '--json' or arg.startswith('--json='):
            json_arg = arg.split('=', 1)[1] if '=' in arg else argv.pop(0)
            keep_json = True
        else:
            raise SystemExit(f'e2e-time.py: unknown option {arg}')
    if argv[:1] == ['--']:
        argv = argv[1:]
    if dist_is_stale():
        print('[test-time] WARNING frontend/dist is older than frontend/src — this run proves the '
              'last `npm run build`, not the working tree. Run `cd frontend && npm run build` first.',
              flush=True)

    if json_arg:
        json_path = json_arg
    else:
        fd, json_path = tempfile.mkstemp(prefix='pw-e2e-time-', suffix='.json')
        os.close(fd)
    env = dict(os.environ, PLAYWRIGHT_JSON_OUTPUT_NAME=json_path)
    t0 = time.monotonic()
    workers_at = tests_at = summary_at = None
    proc = subprocess.Popen(
        ['npx', 'playwright', 'test', '--reporter=list,json', *argv],
        cwd=FRONTEND, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        text=True, bufsize=1,
    )
    for line in proc.stdout:
        if workers_at is None and RUNNING_RE.match(line):
            workers_at = time.monotonic()
        elif TEARDOWN_RE.match(line):
            tests_at = time.monotonic()
        elif SUMMARY_RE.match(line):
            summary_at = time.monotonic()
        sys.stdout.write(line)
        sys.stdout.flush()
    rc = proc.wait()
    t_end = time.monotonic()

    setup = (workers_at - t0) if workers_at else None
    body = (tests_at - workers_at) if (tests_at and workers_at) else None
    teardown = (t_end - tests_at) if tests_at else None

    def fmt(v):
        return f'{v:.2f}s' if v is not None else 'n/a'

    counts = ''
    body_ms = None
    try:
        with open(json_path) as fh:
            report = json.load(fh)
        stats = report.get('stats') or {}
        body_ms = stats.get('duration')
        counts = ' · '.join(
            f'{stats.get(k, 0)} {k}' for k in ('expected', 'unexpected', 'flaky', 'skipped') if stats.get(k)
        )
        lanes = spec_durations(report)
    except (OSError, ValueError) as e:
        print(f'[test-time] could not read the JSON report ({json_path}): {e}')
        lanes = {}
    if show_specs:
        print_lanes(lanes, show_specs)

    print()
    print(f'[test-time] setup={fmt(setup)}  tests={fmt(body)}  teardown={fmt(teardown)}  '
          f'total={fmt(t_end - t0)}'
          + (f'  (json run body {body_ms / 1000:.2f}s)' if body_ms else '')
          + (f'  [{counts}]' if counts else ''))
    if summary_at and tests_at:
        print(f'[test-time] reporter summary landed {summary_at - tests_at:.2f}s after teardown '
              '(printed last; it is not part of the split)')
    if not keep_json:
        os.unlink(json_path)
    elif json_arg:
        print(f'[test-time] JSON report kept at {json_arg}')
    return rc


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
