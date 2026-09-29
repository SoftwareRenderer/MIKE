#!/usr/bin/env python3
"""Self-test for scripts/frontend-gates.sh — the runner that decides `make test-frontend`, so a runner that
reports green for a gate that never ran is the whole gate gone.

Usage (from frontend/):   python3 scripts/frontend-gates_test.py

Cases run the real script against fixture slot tables in a throwaway log directory: an all-green set, a
command that fails, a command that does not exist, a slot killed before it can record its exit code, an empty
table. Two more hold the runner to its promises: a green gate's numbers must still reach stdout, and two
sleeping slots must finish in the time of one. The last cases read the table and package.json, so a `check:*`
gate cannot be added or renamed without the runner hearing about it.
"""
import json
import os
import re
import subprocess
import sys
import tempfile
import time

HERE = os.path.dirname(os.path.abspath(__file__))
RUNNER = os.path.join(HERE, 'frontend-gates.sh')
FRONTEND = os.path.dirname(HERE)
PACKAGE = json.load(open(os.path.join(FRONTEND, 'package.json'), encoding='utf-8'))

# Gates the table need not run, each with the reason. A name added here must still exist in package.json,
# which the last case checks, so an exemption cannot outlive the script it excuses.
EXEMPT = {
    'check:relation': 'two compiles and a scratch file inside src/, so it runs alone and by hand',
    'check:gates': 'the runner itself; running it inside itself recurses',
    'check:gates-test': 'runs in front of the runner in `make test-frontend`: a self-test inside the runner '
                        'says nothing about a runner that never started',
}

cases_run = 0
failures = []


def check(desc, ok):
    global cases_run
    cases_run += 1
    if not ok:
        failures.append(desc)


def slots(*rows):
    """Write a fixture slot table (`name<TAB>green-output<TAB>command`) and return its path."""
    fd, path = tempfile.mkstemp(suffix='.tsv')
    with os.fdopen(fd, 'w') as fh:
        for row in rows:
            fh.write('\t'.join(row) + '\n')
    return path


def runner(table=None):
    """Run the runner with a fixture table in its own log directory. Returns (proc, seconds, log dir)."""
    logs = tempfile.mkdtemp()
    env = dict(os.environ, MIKE_GATE_LOGS=logs)
    if table is not None:
        env['MIKE_GATE_SLOTS'] = table
    started = time.monotonic()
    proc = subprocess.run(['bash', RUNNER], cwd=FRONTEND, env=env, capture_output=True, text=True)
    return proc, time.monotonic() - started, logs


# --- a green run, and the ways a run must not read green ------------------------------------------------

def script(*lines):
    """A fixture command as `bash <file>`, so its output reaches stdout only through the log: the echoed
    command line would otherwise satisfy an assertion about the output itself."""
    fd, path = tempfile.mkstemp(suffix='.sh')
    with os.fdopen(fd, 'w') as fh:
        fh.write('\n'.join(lines) + '\n')
    return 'bash ' + path


proc, _, logs = runner(slots(('one', 'full', script("printf 'SENTINEL 42\\n'")), ('two', 'full', 'true')))
check('an all-green slot table exits 0', proc.returncode == 0)
check('...and says how many slots it ran', '2 slots green' in proc.stdout)
check('a green gate prints its numbers on stdout', 'SENTINEL 42' in proc.stdout)
check('every slot leaves a log beside its exit code',
      all(os.path.exists(os.path.join(logs, n + suffix))
          for n in ('one', 'two') for suffix in ('.log', '.exit')))
check('a slot log path is printed where its numbers are', 'one.log' in proc.stdout)

proc, _, _ = runner(slots(('ok', 'full', 'true'),
                          ('bad', 'full', script('printf "why\\n"', 'exit 3')),
                          ('also-ok', 'full', 'true')))
check('one failing slot fails the run', proc.returncode == 1)
check('the failing slot is named with its exit code', re.search(r'bad \(exit 3\)', proc.stdout) is not None)
check('the failing slot log is printed', 'why' in proc.stdout)
check('the healthy neighbours still report', 'ok' in proc.stdout and 'also-ok' in proc.stdout)

proc, _, _ = runner(slots(('gone', 'full', 'this-command-does-not-exist-41f2')))
check('a command that does not exist fails the run', proc.returncode == 1)
check('and is named as such', 'command not found' in proc.stdout)

proc, _, _ = runner(slots(('killed', 'full', 'kill -9 $PPID'), ('after', 'full', 'true')))
check('a slot killed before recording an exit code fails the run', proc.returncode == 1)
check('and is named as never finishing', 'did not finish' in proc.stdout)
check('...and the run counts what reported at all', 'reported an exit code' in proc.stdout + proc.stderr)

proc, _, _ = runner(slots(('same', 'full', 'true'), ('SAME', 'full', 'true'), ('same', 'full', 'false')))
check('two slots sharing a name are refused, not merged into one log', proc.returncode == 1)
check('...and the duplicate is named', 'used twice' in proc.stdout + proc.stderr)

fd, empty = tempfile.mkstemp(suffix='.tsv')
os.close(fd)
proc, _, _ = runner(empty)
check('an empty slot table refuses to pass', proc.returncode == 1)
check('and says so', 'slot table is empty' in proc.stderr)

# --- the two promises the parallel shape makes ----------------------------------------------------------

proc, secs, _ = runner(slots(('slow-a', 'full', 'sleep 3'), ('slow-b', 'full', 'sleep 3')))
check('two slots run at once (%.1fs for two 3s slots)' % secs, secs < 5.0)

# The reporter's own lines must reach stdout from the log, not from the echoed command, so the fixture writes
# them to a file the slot cats.
reporter = script("printf '%s\\n' ' X src/a.unit.ts 1ms' ' Test Files  2 passed (2)' "
                  "'      Tests  7 passed (7)' '   Duration  9.00s'")
proc, _, _ = runner(slots(('vitest', 'vitest-summary', reporter)))
check('the vitest slot prints its summary lines', 'Test Files  2 passed' in proc.stdout
      and 'Duration  9.00s' in proc.stdout)
check('...without the per-file wall', 'src/a.unit.ts' not in proc.stdout)

# --- the table still covers the gates -------------------------------------------------------------------

table = subprocess.run(['bash', RUNNER, '--slots'], cwd=FRONTEND, capture_output=True, text=True)
check('the runner prints its own slot table', table.returncode == 0 and table.stdout.strip() != '')
rows = [r.split('\t') for r in table.stdout.splitlines() if r.strip()]
check('the table has one name, print mode and command per row', bool(rows) and
      all(len(r) == 3 for r in rows))
commands = '\n'.join(r[2] for r in rows)


def runs(name):
    """Whether some slot names `npm run name` — as a whole script name, not as another name's prefix."""
    return re.search(r'npm run ' + re.escape(name) + r'(?![A-Za-z0-9_-])', commands) is not None


uncovered = [n for n in PACKAGE['scripts'] if n.startswith('check:') and n not in EXEMPT and not runs(n)]
check('every check:* gate runs in a slot: %s' % ', '.join(uncovered), not uncovered)
stale = [n for n in EXEMPT if n not in PACKAGE['scripts']]
check('every exemption is still a real script: %s' % ', '.join(stale), not stale)
unknown = sorted({m for m in re.findall(r'npm run ([A-Za-z0-9:_-]+)', commands)
                  if m not in PACKAGE['scripts']})
check('no slot names an npm script that no longer exists: %s' % ', '.join(unknown), not unknown)
check('vitest runs in a slot', 'npm test' in commands)
check('and so does the one typecheck compile', 'npm run check:unused' in commands)
for chain, what in [('npm run check:i18n-surface-test && npm run check:i18n-surface', 'the i18n counter'),
                    ('npm run check:sinks-test && npm run check:sinks', 'the sink audit'),
                    ('npm run check:dead-surface-test && npm run check:dead-surface', 'the dead-surface count'),
                    ('npm run check:typecheck-test', 'the typecheck derivation')]:
    check('a gate runs after its own self-test: %s' % what, chain in commands)

if failures:
    print('frontend-gates_test: %d FAILURE(S) of %d cases' % (len(failures), cases_run))
    for f in failures:
        print('  %s' % f)
    print('The runner is the gate: a slot it reports green without running one is a check never made.')
    sys.exit(1)
print('frontend-gates_test: ok — %d cases, the runner fails red, prints green, and covers the table'
      % cases_run)
