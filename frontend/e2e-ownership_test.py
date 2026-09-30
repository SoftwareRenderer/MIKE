#!/usr/bin/env python3
"""Self-test for e2e-ownership.py — the gate counts specs that start a turn without owning a project,
so a case plants a small `tests/e2e` and asks the gate the question it asks the repository.

Usage (from frontend/):   python3 e2e-ownership_test.py

Every rule is pinned in both directions: the spec that must be reported and the lookalikes that must
not be (one that owns its project, one that names the endpoint in a comment only, one that navigates a
task somebody else started). The register is pinned too: an entry whose spec has been converted is a
finding, and so is a gate whose spec directory is gone — a scan that reads nothing is not a clean run.
"""
import contextlib
import importlib.util
import io
import os
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location('e2e_ownership', os.path.join(HERE, 'e2e-ownership.py'))
assert _spec is not None and _spec.loader is not None, 'e2e-ownership.py must sit beside this test'
gate = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(gate)

cases_run = 0
failures = []


def tree(specs):
    """Write `{name: body}` as a `tests/e2e` directory and return its path."""
    root = os.path.join(tempfile.mkdtemp(), 'e2e')
    os.makedirs(root, exist_ok=True)
    for name, body in specs.items():
        with open(os.path.join(root, name), 'w', encoding='utf-8') as handle:
            handle.write(body)
    return root


def check(desc, ok):
    global cases_run
    cases_run += 1
    if not ok:
        failures.append(desc)


def offenders(specs):
    found, _ = gate.scan(tree(specs))
    return found or []


def run_main(spec_dir, baseline):
    """Run main() against a planted tree with `baseline` as the register."""
    original = gate.BASELINE
    gate.BASELINE = baseline
    env = os.environ.get('E2E_OWNERSHIP_DIR')
    os.environ['E2E_OWNERSHIP_DIR'] = spec_dir
    try:
        with contextlib.redirect_stdout(io.StringIO()) as out:
            code = gate.main([])
        return code, out.getvalue()
    finally:
        gate.BASELINE = original
        if env is None:
            os.environ.pop('E2E_OWNERSHIP_DIR', None)
        else:
            os.environ['E2E_OWNERSHIP_DIR'] = env


STARTS = "test('a', async ({ page }) => { await createTaskViaApi(page, p, 'x'); });\n"
SENDS = "test('a', async ({ page }) => { await csrfFetch(page, `${path}/send`); });\n"
STARTS_ROUTE = "test('a', async ({ page }) => { await csrfFetch(page, `${path}/start\"`); });\n"
USES_UI_SEND = "test('a', async ({ page }) => { await sendMessage(page, 'hi'); });\n"
OWNS = "isolatedProject();\n" + STARTS
READS_ONLY = "test('a', async ({ page }) => { await navigateToTask(page, taskId); });\n"
ONLY_IN_COMMENT = "// this spec never calls /send; it watches a turn another spec started\n" + READS_ONLY
POSTS_OWN = ("test('a', async ({ page }) => {\n"
             "  await csrfFetch(page, '/api/projects', { method: 'POST', body: { path: OWN_REPO } });\n"
             "  await createTaskViaApi(page, own, 'x');\n});\n")
POSTS_OWN_SPLIT = ("test('a', async ({ page }) => {\n"
                   "  await csrfFetch(page, {\n    url: '/api/projects',\n    method: 'POST',\n  });\n"
                   "  await createTaskViaApi(page, own, 'x');\n});\n")
POSTS_AND_SHARES = POSTS_OWN.replace("});", "  const p = await getProjectId(page);\n});")

check('a spec that POSTs a project of its own owns a project', offenders({'a.spec.ts': POSTS_OWN}) == [])
check('the POST is still found when the call is split over lines', offenders({'a.spec.ts': POSTS_OWN_SPLIT}) == [])
check('a spec that POSTs a project and resolves the shared one is reported',
      offenders({'a.spec.ts': POSTS_AND_SHARES}) == ['a.spec.ts'])

check('an API-created task is a turn', offenders({'a.spec.ts': STARTS}) == ['a.spec.ts'])
check('the send endpoint is a turn', offenders({'a.spec.ts': SENDS}) == ['a.spec.ts'])
check('the start endpoint is a turn', offenders({'a.spec.ts': STARTS_ROUTE}) == ['a.spec.ts'])
check("the chat's own send helper is a turn", offenders({'a.spec.ts': USES_UI_SEND}) == ['a.spec.ts'])
check('a spec that owns its project is clean', offenders({'a.spec.ts': OWNS}) == [])
check('a spec that only opens a task is clean', offenders({'a.spec.ts': READS_ONLY}) == [])
check('a turn named only in a comment is not a turn', offenders({'a.spec.ts': ONLY_IN_COMMENT}) == [])
check('the count is specs, not call sites',
      offenders({'a.spec.ts': STARTS + STARTS + SENDS}) == ['a.spec.ts'])
check('a helper file is not a spec', offenders({'helpers.ts': STARTS}) == [])
check('a tree with no specs scans zero', offenders({}) == [])

# The register: a new offender fails, a converted one fails too, and a clean tree passes.
missing_dir = os.path.join(tempfile.mkdtemp(), 'nowhere')
code, out = run_main(missing_dir, {})
check('a missing spec directory fails the run', code == 1 and 'FAIL' in out)

planted = tree({'brand-new.spec.ts': STARTS, 'owned.spec.ts': OWNS})
code, out = run_main(planted, {})
check('an unregistered offender fails the run', code == 1 and 'brand-new.spec.ts' in out and 'NEW' in out)
check('a spec that owns its project is never reported', 'owned.spec.ts' not in out)

code, out = run_main(planted, {'brand-new.spec.ts': 'not converted: queues on the shared project'})
check('a registered offender passes the run', code == 0 and 'NEW' not in out)

code, out = run_main(tree({'owned.spec.ts': OWNS}), {'gone.spec.ts': 'not converted'})
check('a register entry with no spec behind it fails the run', code == 1 and 'STALE' in out and 'gone.spec.ts' in out)

# The register shipped with the gate must describe this repository, or the gate is decoration.
here, _ = gate.scan(gate.SPEC_DIR)
unregistered = [name for name in (here or []) if name not in gate.BASELINE]
check('the shipped register covers the real specs', not unregistered)
orphaned = [name for name in gate.BASELINE if here is not None and name not in here]
check('the shipped register holds no converted spec', not orphaned)

if failures:
    print(f'e2e-ownership_test: {len(failures)} FAILURE(S) of {cases_run} cases')
    for f in failures:
        print(f'  {f}')
    print('A case that reported too FEW means the gate has gone blind; one that reported too many means a')
    print('lookalike is not excluded by context. Both are the gate lying about the tree.')
    sys.exit(1)
print(f'e2e-ownership_test: ok — {cases_run} cases, both directions hold')
