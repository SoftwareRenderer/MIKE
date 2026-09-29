#!/usr/bin/env python3
"""Self-test for sleep-gate.py — a counter that can quietly stop counting is a ceiling nobody reads, so
every case plants a small tree and asks the gate the question it asks the repository.

Usage (from frontend/):   python3 sleep-gate_test.py

Each rule is pinned in both directions: the sleep that must be reported and the lookalikes that must not
(a call named in prose, one inside a string, one inside a block comment), the comment long enough to be
a reason and the one that is only a label. The last cases prove the ceiling bites and that
`--update-ceiling` rewrites the constant it names.
"""
import contextlib
import io
import os
import re
import sys
import tempfile
import importlib.util

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location('sleep_gate', os.path.join(HERE, 'sleep-gate.py'))
assert _spec is not None and _spec.loader is not None, 'sleep-gate.py must sit beside this test'
sg = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sg)

cases_run = 0
failures = []


def tree(files):
    """Write `{path: body}` under a fresh root, so one case is one repository."""
    root = tempfile.mkdtemp()
    for rel, body in files.items():
        full = os.path.join(root, rel)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, 'w', encoding='utf-8') as fh:
            fh.write(body)
    return root


def check(desc, ok):
    global cases_run
    cases_run += 1
    if not ok:
        failures.append(desc)


def site_case(desc, expected, files):
    """Expect exactly `expected` unreasoned sites from this tree."""
    global cases_run
    cases_run += 1
    got = sg.sites(tree(files))
    unreasoned = [s for s in got if not s[2]]
    if len(unreasoned) != expected or len(got) < expected:
        failures.append(f'{desc}: {len(unreasoned)} unreasoned of {len(got)} sites, '
                        f'expected {expected} unreasoned')
    return got


def run_case(desc, expected_rc, files, ceiling):
    """Run `main` over a planted tree with the ceiling pinned to `ceiling`."""
    global cases_run
    cases_run += 1
    original = sg.SLEEP_CEILING
    sg.SLEEP_CEILING = ceiling
    try:
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            rc = sg.main([], root=tree(files), gate_path=os.devnull)
    finally:
        sg.SLEEP_CEILING = original
    if rc != expected_rc:
        failures.append(f'{desc}: exit {rc}, expected {expected_rc}\n{out.getvalue()}')
    return out.getvalue()


SPEC = 'tests/e2e/one.spec.ts'

# A site in code is a site, with no reason to excuse it.
sites = site_case('a bare sleep', 1, {
    SPEC: "test('x', async ({ page }) => {\n  await page.waitForTimeout(300);\n});\n"})
check('the site is located on its own line', sites == [(SPEC, 2, None)])

# The same duration with its reason beside it is kept, and keeps measurement 1 at zero.
sites = site_case('a reasoned sleep', 0, {
    SPEC: "  await page.waitForTimeout(1100); // moved_at has second precision, so the order needs seconds\n"})
check('the reason travels with the site', bool(sites) and bool(sites[0][2]))

# A reason on the line above explains the code around the wait, not the wait, and does not exempt it.
site_case('a reason on the line above the sleep', 1, {
    SPEC: "  // drag settle time: headless is slow and the thumb must be grabbed before it moves\n"
          "  await page.waitForTimeout(150);\n"})

# A label is not a reason.
site_case('a comment too short to be a reason', 1, {SPEC: "  await page.waitForTimeout(300); // settle\n"})

# Lookalikes that must not be counted at all.
site_case('a call named in prose', 0, {
    SPEC: "  // this spec used to waitForTimeout(5000) before every assertion\n"})
site_case('a call inside a string', 0, {
    SPEC: "  const src = 'await page.waitForTimeout(300);';\n"})
site_case('a call inside a block comment', 0, {
    SPEC: "  /* await page.waitForTimeout(300);\n   */\n  const ok = 1;\n"})
site_case('a call in a file the gate does not read', 0, {'tests/mock-llm.js': 'x.waitForTimeout(1);\n'})

# The ceiling is a ceiling: at it the run is green, one site past it is not.
two = {SPEC: "  await page.waitForTimeout(1); // the reason is long enough to be one\n"
             "  await page.waitForTimeout(2); // and so is this reason, just as long\n"}
out = run_case('a tree at its ceiling', 0, two, 2)
check('the number is printed even when green', '2  waitForTimeout calls' in out)
out = run_case('one sleep past the ceiling', 1, two, 1)
check('the ceiling names its own rise', 'grew from 1 to 2' in out)
out = run_case('an unreasoned sleep fails at zero ceiling', 1, {
    SPEC: "  await page.waitForTimeout(400);\n"}, 99)
check('the finding names the fix', 'wait for a condition' in out)

# --update-ceiling rewrites the constant it prints, in a copy of the gate.
root = tree({SPEC: "  await page.waitForTimeout(1); // the reason is long enough to be one\n"})
gate_copy = os.path.join(root, 'gate.py')
with open(os.path.join(HERE, 'sleep-gate.py'), encoding='utf-8') as fh:
    source = fh.read()
with open(gate_copy, 'w', encoding='utf-8') as fh:
    fh.write(source)
with contextlib.redirect_stdout(io.StringIO()):
    sg.main(['--update-ceiling'], root=root, gate_path=gate_copy)
with open(gate_copy, encoding='utf-8') as fh:
    rewritten = fh.read()
check('--update-ceiling writes the measured number',
      re.search(r'(?m)^SLEEP_CEILING = 1$', rewritten) is not None)

# The gate really reads this repository: a walk that finds no spec file measures nothing.
check('the real tree is scanned', len(sg.spec_files(HERE)) > 50)

print(f'{cases_run} cases, {len(failures)} failures')
for f in failures:
    print(f'  FAIL  {f}')
sys.exit(1 if failures else 0)
