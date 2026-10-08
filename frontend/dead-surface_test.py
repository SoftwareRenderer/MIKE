#!/usr/bin/env python3
"""Self-test for dead-surface.py — a gate that counts seams and copied CSS is worth nothing if it can
quietly stop counting either, so every case plants a small tree and asks the gate the question it asks
the repository.

Usage (from frontend/):   python3 dead-surface_test.py

Each rule is pinned in both directions: the seam that must be reported and the lookalike that must not
(an import from another production file, a template-literal hole, a barrel), the copy that must raise
the CSS count and the two rules that merely resemble each other (`var(--a)` against `var(--b)`). The
last cases prove copied CSS is counted and gates nothing, and that an untagged seam fails anyway.
"""
import contextlib
import io
import os
import sys
import tempfile
import importlib.util

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location('dead_surface', os.path.join(HERE, 'dead-surface.py'))
assert _spec is not None and _spec.loader is not None, 'dead-surface.py must sit beside this test'
ds = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ds)

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


def seam_case(desc, expected, files):
    """Expect exactly `expected` (a list of symbol names) to be reported as untagged test-only."""
    global cases_run
    cases_run += 1
    untagged, seams, unnamed = ds.measure_seams(tree(files))
    got = sorted(d.name for d in untagged)
    if got != sorted(expected):
        failures.append(f'{desc}: reported {got}, expected {sorted(expected)}'
                        + (f' (tagged: {[d.name for d in seams]}, unnamed: {[d.name for d in unnamed]})'))


def dupe_case(desc, expected, files):
    """Expect `expected` duplicated declaration lines from this tree."""
    global cases_run
    cases_run += 1
    got, _ = ds.measure_css(tree(files))
    if got != expected:
        failures.append(f'{desc}: counted {got} duplicated declaration lines, expected {expected}')


def pair(module_body, unit_body='const probe = SEAM;', module='src/lib/a.ts',
         unit_path='src/lib/__tests__/a.unit.ts', extra=None):
    """One production module, one unit file that names `SEAM`, plus anything else the case needs."""
    files = {module: module_body, unit_path: unit_body}
    files.update(extra or {})
    return files


# --- an untagged seam is reported, whatever it declares -----------------------
for kind, decl in [
    ('const', 'export const SEAM = 4;'),
    ('let', 'export let SEAM = 4;'),
    ('function', 'export function SEAM() { return 4; }'),
    ('async function', 'export async function SEAM() { return 4; }'),
    ('class', 'export class SEAM {}'),
    ('interface', 'export interface SEAM { x: number }'),
    ('type', 'export type SEAM = { x: number };'),
    ('enum', 'export enum SEAM { A, B }'),
]:
    seam_case(f'an untagged {kind} seam', ['SEAM'], pair(decl))

seam_case('an untagged seam in a .svelte <script>', ['SEAM'],
          pair('<script>\n\texport function SEAM() { return 4; }\n</script>\n<p>{SEAM()}</p>',
               module='src/lib/Comp.svelte'))
# The template beside it is markup, not a module body: text that quotes a declaration is not one.
seam_case('markup outside <script> declares nothing', [], {
    'src/lib/Comp.svelte': '<p>export const SEAM = 1;</p>',
    'src/lib/__tests__/a.unit.ts': 'const probe = SEAM;',
})

# --- the tag makes it a retained seam, in every shape the tree uses -----------
seam_case('a one-line block tag', [],
          pair('/** @knipignore Its test reads this. */\nexport const SEAM = 4;'))
seam_case('a multi-line block tag', [],
          pair('/**\n * A window the tests measure.\n *\n * @knipignore Its test measures this window.\n */\n'
               'export const SEAM = 4;'))
seam_case('a line-comment tag above', [],
          pair('// @knipignore Its test reads this.\nexport const SEAM = 4;'))
seam_case('a tag at the end of the export line', [],
          pair('export const SEAM = 4; // @knipignore its test reads this'))
seam_case('a blank line between the tag and the export', [],
          pair('/** @knipignore Its test reads this. */\n\nexport const SEAM = 4;'))
seam_case('a tag in a .svelte <script>', [],
          pair('<script>\n\t/** @knipignore Its test reads this. */\n\texport function SEAM() {}\n</script>\n',
               module='src/lib/Comp.svelte'))
seam_case('a tag on a neighbour does not license this one', ['SECOND'],
          pair('/** @knipignore Its test reads this. */\nexport const FIRST = 1;\n'
               'export const SECOND = 2;', unit_body='const probe = SECOND;'))
seam_case('a tag two blank lines above is not the declaration\'s own', ['SEAM'],
          pair('/** @knipignore Its test reads this. */\n\n\nexport const SEAM = 4;'))
seam_case('a file-level doc comment is not a declaration tag', ['SEAM'],
          pair('/**\n * The module.\n * @knipignore nothing here is a seam\n */\n\n\n'
               'export const SEAM = 4;'))

# --- a symbol another production file names is never a seam ------------------
seam_case('another production file imports and uses it', [],
          pair('export const SEAM = 4;', extra={
              'src/lib/b.ts': "import { SEAM } from './a';\nexport const b = SEAM;\n"}))
seam_case('another production file names it in a Svelte mustache', [],
          pair('export function SEAM() { return 4; }', extra={
              'src/lib/B.svelte': '<script>\n\timport { SEAM } from \'./a\';\n</script>\n<p>{SEAM()}</p>'}))
seam_case('a template-literal hole is a use', [],
          pair('export const SEAM = \'x\';', extra={
              'src/lib/b.ts': "import { SEAM } from './a';\nexport const k = `${SEAM}:1`;\n"}))
seam_case('a barrel re-export is a use', [],
          pair('export const SEAM = 4;', extra={
              'src/lib/index.ts': "export { SEAM } from './a';\n"}))

# --- what is NOT a use ------------------------------------------------------
seam_case('a comment in another production file is not a use', ['SEAM'],
          pair('export const SEAM = 4;', extra={
              'src/lib/b.ts': '// SEAM used to live here\nexport const b = 1;\n'}))
seam_case('a quoted string in another production file is not a use', ['SEAM'],
          pair('export const SEAM = 4;', extra={
              'src/lib/b.ts': "export const needle = 'SEAM';\n"}))
seam_case('a <style> block in another production file is not a use', ['SEAM'],
          pair('export const SEAM = 4;', extra={
              'src/lib/B.svelte': '<p>x</p>\n<style>.SEAM { color: red }</style>'}))

# --- which files are the unit side ------------------------------------------
seam_case('a .unit.svelte.ts file is the unit side', ['SEAM'],
          pair('export const SEAM = 4;', unit_path='src/lib/__tests__/a.unit.svelte.ts'))
seam_case('any file under __tests__/ is the unit side', ['SEAM'],
          pair('export const SEAM = 4;', unit_path='src/lib/utils/__tests__/helper.ts'))
seam_case('a .svelte unit file is the unit side', ['SEAM'],
          pair('export const SEAM = 4;', unit_path='src/lib/__tests__/probe.unit.svelte.ts'))

# --- what the gate leaves to the other gates --------------------------------
seam_case('a symbol no unit file names is check:dead\'s, not this gate\'s', [],
          pair('export const SEAM = 4;', unit_body='export const other = 1;'))
untagged, seams, unnamed = ds.measure_seams(tree(pair('export const SEAM = 4;',
                                                      unit_body='export const other = 1;')))
check('the un-named export is counted where a reader can see it', [d.name for d in unnamed] == ['SEAM'])
seam_case('an index.ts barrel is never measured', [],
          pair('export const SEAM = 4;', module='src/lib/stores/index.ts'))
seam_case('the English catalog is never measured', [],
          pair("export const SEAM = 'text';", module='src/lib/i18n/en.ts'))
seam_case('a type re-export declares no name', [],
          pair("export type { SEAM } from './b';", extra={'src/lib/b.ts': 'export type SEAM = number;'}))
seam_case('a value re-export declares no name', [],
          pair("export { SEAM } from './b';", extra={'src/lib/b.ts': 'export const SEAM = 1;'}))
seam_case('export default declares no local name', [],
          pair('const SEAM = 4;\nexport default SEAM;'))
seam_case('a declaration inside a function body is not an export', [], {
    'src/lib/a.ts': 'export function outer() {\n  const SEAM = 1;\n  return SEAM;\n}\n',
    'src/lib/__tests__/a.unit.ts': 'const probe = SEAM;',
})

# --- the finding names the site ---------------------------------------------
site = ds.measure_seams(tree({
    'src/lib/a.ts': '/** doc */\nexport const FIRST = 1;\n\nexport const SEAM = 4;\n',
    'src/lib/__tests__/a.unit.ts': 'const probe = SEAM;',
}))[0]
check('the finding carries the file and the line of the export',
      [(d.path, d.line) for d in site] == [('src/lib/a.ts', 4)])

# --- measurement 2: the CSS counter -----------------------------------------
def sheets(a, b='', ca='src/lib/styles/a.css', cb='src/lib/styles/b.css'):
    """Two stylesheets — the smallest tree in which a cross-file duplicate can exist."""
    return {ca: a, cb: b}


dupe_case('a declaration copied into a second stylesheet is one duplicated line', 1,
          sheets('.a { color: red; }', '.b { color: red; }'))
dupe_case('two copies in one block are not cross-file duplication', 0,
          sheets('.a { color: red; }\n.b { color: red; }'))
dupe_case('three blocks holding one declaration contribute two lines', 2,
          {'src/lib/styles/a.css': '.a { color: red; }',
           'src/lib/styles/b.css': '.b { color: red; }',
           'src/lib/styles/c.css': '.c { color: red; }'})
dupe_case('a <style> block copying a stylesheet counts', 1,
          {'src/lib/styles/a.css': '.a { color: red; }',
           'src/lib/Comp.svelte': '<p>x</p>\n<style>.c { color: red }</style>'})
dupe_case('two components repeating one rule count', 1,
          {'src/lib/A.svelte': '<p>x</p>\n<style>.a { display: flex }</style>',
           'src/lib/B.svelte': '<p>y</p>\n<style>.b { display: flex }</style>'})
dupe_case('whitespace and comments do not hide a copy', 1,
          sheets('.a {\n  color:\n     red;\n}\n', '/* the same rule */\n.b { color: red }\n'))
dupe_case('a string literal\'s content is not the identity of a copy', 1,
          sheets('.a::before { content: "one"; }', '.b::before { content: "two"; }'))
dupe_case('a hex colour is a literal, so two spellings of it are one declaration', 1,
          sheets('.a { color: #fff }', '.b { color: #ffffff }'))
dupe_case('a declaration wrapped over three lines is one run', 1,
          sheets('.a {\n  transition:\n    opacity\n    200ms;\n}', '.b { transition: opacity 200ms; }'))
dupe_case('a custom property definition copied twice counts', 1,
          sheets(':root { --row-height: 24px; }', '.x { --row-height: 24px; }'))

dupe_case('two different tokens are two rules, not a copy', 0,
          sheets('.a { color: var(--color-border) }', '.b { color: var(--color-text) }'))
dupe_case('two different lengths are two rules, not a copy', 0,
          sheets('.a { padding: 7px }', '.b { padding: 8px }'))
dupe_case('two different properties are two rules, not a copy', 0,
          sheets('.a { color: red }', '.b { background: red }'))
dupe_case('a selector or an at-rule prelude is not a declaration', 0,
          sheets('.a:hover { }', '@media (min-width: 600px) { .a:hover { } }'))
dupe_case('a declaration inside @media still counts as a run', 1,
          sheets('@media (min-width: 600px) { .a { color: red } }', '.b { color: red }'))
dupe_case('a test probe\'s style is not the product\'s CSS', 0,
          {'src/lib/styles/a.css': '.a { color: red; }',
           'src/lib/__tests__/Probe.svelte': '<p>x</p>\n<style>.a { color: red }</style>'})

runs = ds.declarations_in('.a {\n  color: red; padding: 1px;\n  top:\n    2px;\n}\n')
check('two runs on one line are two runs, and a wrapped run keeps its first line',
      [(l, d) for l, d in runs] == [(2, 'color:red'), (2, 'padding:1px'), (3, 'top:2px')])

# --- copied CSS is counted and gates nothing; the seam still fails -----------
planted = sheets('.a { color: red; }', '.b { color: red; }')
measured, _ = ds.measure_css(tree(planted))
buffer = io.StringIO()
with contextlib.redirect_stdout(buffer):
    dupes_exit = ds.main([], root=tree(planted))
check('a tree of copied declarations passes: the count is reported, not gated', dupes_exit == 0)
check('the run prints the number it measured and says it gates nothing',
      f'{measured:5d}' in buffer.getvalue() and 'not gated' in buffer.getvalue())

with_seam = dict(planted, **{'src/lib/a.ts': 'export const SEAM = 1;',
                             'src/lib/__tests__/a.unit.ts': 'const probe = SEAM;'})
buffer = io.StringIO()
with contextlib.redirect_stdout(buffer):
    seam_exit = ds.main([], root=tree(with_seam))
check('an untagged seam fails the run beside a tree full of copies', seam_exit == 1)
check('the seam finding prints file:line and the two ways out',
      'src/lib/a.ts:1  SEAM' in buffer.getvalue() and '@knipignore' in buffer.getvalue())

if failures:
    print(f'dead-surface_test: {len(failures)} FAILURE(S) of {cases_run} cases')
    for f in failures:
        print(f'  {f}')
    print('A case that reported too FEW means the gate has gone blind; one that reported too many means')
    print('a lookalike is not excluded by context. Both are the gate lying about the tree.')
    sys.exit(1)
print(f'dead-surface_test: ok — {cases_run} cases, both directions hold')
