#!/usr/bin/env python3
"""A spec that starts a turn must own the project it starts it on.

    cd frontend && python3 e2e-ownership.py                  # the measurement, exit 1 on a new offender
    cd frontend && python3 e2e-ownership.py -v               # + every baseline entry and its reason
    cd frontend && python3 e2e-ownership.py --update-baseline # re-freeze after converting specs

`npm run check:e2e-ownership` runs this beside its self-test `e2e-ownership_test.py`, and
`make test-frontend` runs both. `docs/testing.md → Concurrency is part of the environment` owns the
reason: one server answers every worker and queues executor turns **per project**, so a spec that starts
a task on a project it does not own waits behind every other worker's turn. The symptom is `.btn-send`
never becoming visible, or a turn-bound poll burning its whole 60-120 s budget, in whichever spec lost
the race that run — and 17 of the 24 failures in one measured run were exactly that class.

The rule (AGENTS.md → Testing): a spec file that names a turn-starting call must own the project it
starts it on — either by calling `isolatedProject()`, which gives the worker its own copy of the fixture
repo, or by POSTing a project of its own and never naming `getProjectId(`, the helper that resolves the
shared one.

    const taskId = await createTaskViaApi(page, projectId, `probe ${Date.now()}`);  // starts a turn
    isolatedProject();                                                             // so own the project

It is a frozen register, not a zero, because 21 files predate the gate and two of them cannot convert:
a spec that asserts the **global** view (the sidebar's project list, the add-project flow) must not
claim a project of its own. A new offender fails the run; a baseline entry that has been converted
fails too, so the register cannot rot in either direction.
"""
import os
import re
import sys

SPEC_DIR = 'tests/e2e'
# What "starts a turn" means in this suite: an API task creation (a created task auto-starts), the
# chat's send endpoint, the executor's start endpoint, or the UI's own send helper.
TURN_START_RE = re.compile(r'/send\b|/start"|\bcreateTaskViaApi\(|\bsendMessage\(')
OWNS_PROJECT_RE = re.compile(r'\bisolatedProject\(')
# A spec that POSTs its own project is not queueing on the shared project either — provided it never
# names `getProjectId(`, the helper that resolves global-setup's shared one.
CREATES_PROJECT_RE = re.compile(r"['\"]/api/projects['\"][\s\S]{0,160}?method:\s*'POST'")
USES_SHARED_PROJECT_RE = re.compile(r'\bgetProjectId\(')

# Measured with `python3 e2e-ownership.py --update-baseline`. Each entry names why it is not converted;
# converting one means deleting its line here, and the gate fails if the line stays.
BASELINE = {
    # The isolated lane runs one worker against its own server (playwright.config.ts), so its turns
    # queue behind nothing. It still pays for the rule's absence if that lane ever parallelises.
    'html-preview.spec.ts': 'isolated lane: one worker, its own server',
    'mode-select.spec.ts': 'isolated lane: one worker, its own server',
    'preferences-hide-model-badge.spec.ts': 'isolated lane: one worker, its own server',
    'preferences-keyboard.spec.ts': 'isolated lane: one worker, its own server',
    'provider-task-concurrency.spec.ts': 'isolated lane: one worker, its own server',
    'settings-provider.spec.ts': 'isolated lane: one worker, its own server',
    'link-like-rows.spec.ts': 'asserts the global sidebar view, which must not gain a project',
}


def scan(spec_dir):
    """(offenders, files scanned). An offender starts a turn and never claims a project."""
    offenders, scanned = [], 0
    if not os.path.isdir(spec_dir):
        return None, 0
    for name in sorted(os.listdir(spec_dir)):
        if not name.endswith('.spec.ts'):
            continue
        path = os.path.join(spec_dir, name)
        if not os.path.isfile(path):
            continue
        scanned += 1
        with open(path, encoding='utf-8') as handle:
            source = handle.read()
        # The spec's own comments may name the rule it breaks; strip line comments before matching.
        body = re.sub(r'^\s*//.*$', '', source, flags=re.MULTILINE)
        if not TURN_START_RE.search(body):
            continue
        owns = OWNS_PROJECT_RE.search(body) or (
            CREATES_PROJECT_RE.search(body) and not USES_SHARED_PROJECT_RE.search(body))
        if not owns:
            offenders.append(name)
    return offenders, scanned


def render_baseline(offenders):
    """The BASELINE block, rebuilt from what the tree actually does."""
    lines = ['BASELINE = {']
    for name in sorted(offenders):
        lines.append(f"    {name!r}: 'not converted: queues on the shared project',")
    lines.append('}')
    return '\n'.join(lines)


def update_baseline(path, offenders):
    with open(path, encoding='utf-8') as handle:
        source = handle.read()
    start = source.index('BASELINE = {')
    end = source.index('\n}', start) + len('\n}')
    with open(path, 'w', encoding='utf-8') as handle:
        handle.write(source[:start] + render_baseline(offenders) + source[end:])


def main(argv):
    verbose = '-v' in argv or '--verbose' in argv
    recount = '--update-baseline' in argv
    spec_dir = os.environ.get('E2E_OWNERSHIP_DIR', SPEC_DIR)

    offenders, scanned = scan(spec_dir)
    if offenders is None:
        print(f'e2e-ownership: FAIL no spec directory at {os.path.abspath(spec_dir)} — a gate that '
              f'scans nothing is not a clean run')
        return 1

    new = [name for name in offenders if name not in BASELINE]
    stale = [name for name in BASELINE if name not in offenders]
    print(f'e2e-ownership: {scanned} specs scanned, {len(offenders)} start a turn without owning a '
          f'project (register holds {len(BASELINE)})')
    if verbose:
        for name in offenders:
            print(f'    {"new  " if name in new else "held "} {name}  '
                  f'{BASELINE.get(name, "not in the register")}')

    if recount:
        update_baseline(os.path.abspath(__file__), offenders)
        print(f'register re-frozen at {len(offenders)} entries')
        return 1 if new else 0

    failed = False
    for name in new:
        print(f'  NEW   {name}: it starts a turn (createTaskViaApi / /send / /start / sendMessage) and '
              f'never calls isolatedProject(), so its turn queues behind every other worker\'s on the '
              f'shared project. Call isolatedProject() at file top level and open it with gotoProject().')
        failed = True
    for name in stale:
        print(f'  STALE {name}: it no longer starts a turn without owning a project, so drop it from '
              f'BASELINE in e2e-ownership.py — a register entry with nothing behind it hides the next one.')
        failed = True
    return 1 if failed else 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
