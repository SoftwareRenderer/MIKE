#!/usr/bin/env python3
"""Self-test for css-surface.py: each judgement pinned in both directions, because a gate that
stopped matching prints the same `ok` as a stylesheet with no adoptable rule in it.

Usage (from frontend/):   python3 css-surface_test.py
"""
import importlib.util
import os
import pathlib
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location('surface', os.path.join(HERE, 'css-surface.py'))
assert _spec is not None and _spec.loader is not None, 'css-surface.py must sit beside this test'
surface = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(surface)

cases_run = 0
failures = []

IMPORTER = ("<script></script><div class=\"explorer-view\"></div>\n"
            "<style>\n@import '$lib/styles/shared.css';\n</style>\n")


def analyze(files):
    """Run the gate's rules over `files` (a path→body map) as if it were the tree."""
    with tempfile.TemporaryDirectory() as d:
        root = os.path.join(d, 'src')
        for rel, body in files.items():
            full = os.path.join(root, rel)
            os.makedirs(os.path.dirname(full), exist_ok=True)
            with open(full, 'w', encoding='utf-8') as fh:
                fh.write(body)
        return surface.analyze(pathlib.Path(root))


def case(desc, files, reported):
    """Assert the gate reports an offender (`reported`) or stays clean (not)."""
    global cases_run
    cases_run += 1
    got = analyze(files)['offenders']
    if bool(got) != reported:
        failures.append(f'{desc}: expected {"an offender" if reported else "clean"}, got {got}')


def label_case(desc, files, reported):
    """Assert the status word a developer reads: FAIL when reported, ok when not."""
    global cases_run
    cases_run += 1
    rows = surface.render(analyze(files))
    failed = rows[0].startswith('FAIL')
    if failed != reported:
        failures.append(f'{desc}: expected {"FAIL" if reported else "ok"}, got {rows[0]!r}')


# A rule written through :global() in a component's own <style> is unscoped: adoptable.
case(':global() in a component style', {
    'lib/Thing.svelte': '<style>:global(.a.b){position:fixed;inset:0}</style>\n'}, True)

# The same escape in a file the component imports, because Svelte scopes only what it is given.
case(':global() in an imported sheet', {
    'lib/Thing.svelte': IMPORTER,
    'lib/styles/shared.css': ':global(.a.b){position:fixed}\n'}, True)

# The same rule written bare in an imported sheet is scoped by Svelte, so a fragment cannot name
# the element it lands on.
case('bare rule in an imported sheet', {
    'lib/Thing.svelte': IMPORTER,
    'lib/styles/shared.css': '.a.b{position:fixed}\n'}, False)

# A sheet no component imports is never scoped: every subject is adoptable.
case('bare rule in a global sheet', {
    'app.css': '.a.b{position:fixed}\n'}, True)

# Only the subject matters: scoping an ancestor does not hide a global subject, because the
# fragment sits inside the app's own ancestor already.
case('global subject under a scoped ancestor', {
    'lib/Thing.svelte': IMPORTER,
    'lib/styles/shared.css': ':global(.a .b){position:fixed}\n'}, True)
case('scoped subject under a global ancestor', {
    'lib/Thing.svelte': IMPORTER,
    'lib/styles/shared.css': ':global(.a) .b{position:fixed}\n'}, False)

# Something only the app can write in the subject: a scope hash, an id, an attribute.
case('subject with a scope hash', {
    'lib/Thing.svelte': IMPORTER,
    'lib/styles/shared.css': '.a.b.svelte-1a2b3c{position:fixed}\n'}, False)
case('subject with an attribute', {
    'lib/Thing.svelte': IMPORTER,
    'lib/styles/shared.css': ':global(.a[data-fs]){position:fixed}\n'}, False)
case('subject with an id', {
    'lib/Thing.svelte': IMPORTER,
    'lib/styles/shared.css': ':global(#app-overlay){position:fixed}\n'}, False)
case('document subject', {
    'app.css': 'body{position:fixed}\n'}, False)

# The positions that keep an ancestor box: they cover a region, not the application.
case('absolute is bounded', {
    'app.css': '.a{position:absolute;inset:0;z-index:99}\n'}, False)
case('sticky is bounded', {
    'app.css': '.a{position:sticky;top:0}\n'}, False)

label_case('the label prints FAIL', {'app.css': '.a{position:fixed}\n'}, True)
label_case('the label prints ok', {'app.css': '.a{position:absolute}\n'}, False)

print(f'{cases_run - len(failures)}/{cases_run} css-surface cases passed')
for f in failures:
    print('  FAIL ' + f)
sys.exit(1 if failures else 0)
