#!/usr/bin/env python3
"""Self-test for html-sinks.py.

A sink gate is only worth what it can still see. Rule 4 of this one checks that a
file claiming a mechanism really has it — and a check that stopped matching would
print the same `ok` line as a sink that is genuinely safe. So every rule is pinned in
both directions: the unsafe shape must be reported, and the safe lookalike must not.

Usage (from frontend/):   python3 html-sinks_test.py
"""
import importlib.util
import os
import pathlib
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location('sinks', os.path.join(HERE, 'html-sinks.py'))
assert _spec is not None and _spec.loader is not None, 'html-sinks.py must sit beside this test'
sinks = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sinks)

cases_run = 0
failures = []

SINK = '<div>{@html value}</div>\n'
SANITIZING = ("<script>import { sanitizeHTML } from '$lib/utils/sanitize';</script>"
              "<div>{@html sanitizeHTML(value)}</div>\n")
FORMAT_MODULE = "export function escapeHtml(text: string) { return text; }\n"
THING = {'src/lib/Thing.svelte': 'reason'}


def analyze(files, allow, methods):
    """Run the gate's rules over `files` (a path→body map) as if it were src/."""
    with tempfile.TemporaryDirectory() as d:
        root = os.path.join(d, 'src')
        for rel, body in files.items():
            full = os.path.join(root, rel)
            os.makedirs(os.path.dirname(full), exist_ok=True)
            with open(full, 'w', encoding='utf-8') as fh:
                fh.write(body)
        return sinks.analyze(pathlib.Path(root), allow, methods)


def case(desc, field, files, allow, methods, reported):
    """Assert `field` of the report is non-empty (`reported`) or empty (not)."""
    global cases_run
    cases_run += 1
    got = bool(analyze(files, allow, methods)[field])
    if got != reported:
        failures.append(f'{desc}: expected {field} {"reported" if reported else "clean"}, '
                        f'got {analyze(files, allow, methods)[field]}')


def meta(desc, ok):
    global cases_run
    cases_run += 1
    if not ok:
        failures.append(desc)


# --- rule 1: a sink must be listed -------------------------------------------
case('an unlisted sink is reported', 'unlisted', {'lib/Thing.svelte': SINK}, {}, {}, True)
case('a listed sink is not', 'unlisted', {'lib/Thing.svelte': SINK}, THING,
     {'src/lib/Thing.svelte': 'literal'}, False)
case('a component test file is excluded', 'unlisted', {'lib/__tests__/t.unit.svelte.ts': 'el.innerHTML = x;'},
     {}, {}, False)
case('.innerHTML counts as a sink', 'unlisted', {'lib/Thing.ts': 'el.innerHTML = value;'}, {}, {}, True)
case('insertAdjacentHTML counts as a sink', 'unlisted',
     {'lib/Thing.ts': 'el.insertAdjacentHTML("beforeend", value);'}, {}, {}, True)

# --- rule 2: a sink must never render a translation --------------------------
for label, expr in [('t()', "t('chat.send')"), ('packText()', "packText('chat.send')"),
                    ('i18n.t()', "i18n.t('chat.send')")]:
    case(f'{label} inside a sink is reported even in a listed file', 'translated',
         {'lib/Thing.svelte': '<div>{@html %s}</div>' % expr}, THING,
         {'src/lib/Thing.svelte': 'literal'}, True)
case('a sink with no translation call is not', 'translated', {'lib/Thing.svelte': SINK}, THING,
     {'src/lib/Thing.svelte': 'literal'}, False)
case('a translation outside a sink is not', 'translated',
     {'lib/Thing.svelte': "<p>{t('chat.send')}</p>"}, {}, {}, False)
case('another function in a sink is not a translation', 'translated',
     {'lib/Thing.svelte': '<div>{@html format(value)}</div>'}, THING,
     {'src/lib/Thing.svelte': 'literal'}, False)
case('an identifier ending in t is not a translation call', 'translated',
     {'lib/Thing.svelte': '<div>{@html slot(value)}</div>'}, THING,
     {'src/lib/Thing.svelte': 'literal'}, False)

# --- rule 3: the list cannot drift into history ------------------------------
case('a listed file with no sink left is stale', 'stale', {'lib/Thing.svelte': '<p>plain</p>'},
     THING, {}, True)
case('a listed file that still has its sink is not', 'stale', {'lib/Thing.svelte': SINK}, THING,
     {'src/lib/Thing.svelte': 'literal'}, False)

# --- rule 4: each listed file declares how it stays safe ---------------------
case('a sink with no declared method is undeclared', 'undeclared', {'lib/Thing.svelte': SINK},
     THING, {}, True)
case('a declared method clears it', 'undeclared', {'lib/Thing.svelte': SINK}, THING,
     {'src/lib/Thing.svelte': 'literal'}, False)
case('a method for a file that is not allowed is reported', 'extra_methods',
     {'lib/Thing.svelte': SINK}, {}, {'src/lib/Thing.svelte': 'literal'}, True)
case('an unknown method name is a broken claim', 'broken', {'lib/Thing.svelte': SINK}, THING,
     {'src/lib/Thing.svelte': 'trust-me'}, True)

# `sanitizer`
case('sanitizer with the import is satisfied', 'broken', {'lib/Thing.svelte': SANITIZING}, THING,
     {'src/lib/Thing.svelte': 'sanitizer'}, False)
case('sanitizer without the import is broken', 'broken', {'lib/Thing.svelte': SINK}, THING,
     {'src/lib/Thing.svelte': 'sanitizer'}, True)
case('sanitizer pointing at some other module is broken', 'broken',
     {'lib/Thing.svelte': "<script>import { s } from '$lib/sanitize';</script>" + SINK}, THING,
     {'src/lib/Thing.svelte': 'sanitizer'}, True)
case('sanitizer alongside a direct DOMPurify import is broken', 'broken',
     {'lib/Thing.svelte': "<script>import { sanitizeHTML } from '$lib/utils/sanitize';"
                          "import DOMPurify from 'dompurify';</script>" + SINK}, THING,
     {'src/lib/Thing.svelte': 'sanitizer'}, True)
case('sanitizer named only in a comment is broken', 'broken',
     {'lib/Thing.svelte': '<!-- everything here goes through $lib/utils/sanitize -->' + SINK}, THING,
     {'src/lib/Thing.svelte': 'sanitizer'}, True)

# `guard:<Ident>`
case('guard finds the named check', 'broken',
     {'lib/Thing.svelte': 'const ok = singleSvgMarkup(src); {@html ok}'}, THING,
     {'src/lib/Thing.svelte': 'guard:singleSvgMarkup'}, False)
case('guard fails when the check is renamed away', 'broken',
     {'lib/Thing.svelte': 'const ok = someOtherCheck(src); {@html ok}'}, THING,
     {'src/lib/Thing.svelte': 'guard:singleSvgMarkup'}, True)

# `escaped-by:<module>`
IMPORTS_FORMAT = "import { escapeHtml } from '$lib/utils/format';\n" + SINK
case('escaped-by is satisfied when the helper still escapes', 'broken',
     {'lib/Thing.svelte': IMPORTS_FORMAT, 'lib/utils/format.ts': FORMAT_MODULE}, THING,
     {'src/lib/Thing.svelte': 'escaped-by:lib/utils/format'}, False)
case('escaped-by fails when the import goes away', 'broken',
     {'lib/Thing.svelte': SINK, 'lib/utils/format.ts': FORMAT_MODULE}, THING,
     {'src/lib/Thing.svelte': 'escaped-by:lib/utils/format'}, True)
case('escaped-by fails when the helper stops escaping', 'broken',
     {'lib/Thing.svelte': IMPORTS_FORMAT, 'lib/utils/format.ts': 'export function shout(t) { return t; }\n'},
     THING, {'src/lib/Thing.svelte': 'escaped-by:lib/utils/format'}, True)
case('escaped-by fails when the helper does not exist', 'broken',
     {'lib/Thing.svelte': IMPORTS_FORMAT}, THING,
     {'src/lib/Thing.svelte': 'escaped-by:lib/utils/format'}, True)

# --- the shipped lists themselves --------------------------------------------
def real_body(rel):
    return (sinks.SRC.parent / rel).read_text(encoding='utf-8')


meta('every allowed sink declares a method', set(sinks.ALLOW) == set(sinks.METHOD))
meta('every declared method is a form rule 4 knows',
     all(m in ('literal', 'sanitizer') or m.startswith(('guard:', 'escaped-by:'))
         for m in sinks.METHOD.values()))
meta('every listed file still contains the sink form it was listed for',
     all(sinks.SINK_RE.search(real_body(rel)) for rel in sinks.ALLOW))
meta('every sanitizer sink really imports the sanitizer',
     all(sinks.SANITIZER_MODULE in real_body(rel)
         for rel, m in sinks.METHOD.items() if m == 'sanitizer'))
meta('no sink reaches for the sanitizer without being listed',
     {rel for rel in sinks.ALLOW if sinks.SANITIZER_MODULE in real_body(rel)}
     >= {rel for rel, m in sinks.METHOD.items() if m == 'sanitizer'})
meta('the real src/ is clean', sinks.clean(sinks.analyze(sinks.SRC, sinks.ALLOW, sinks.METHOD)))

print(f'html-sinks_test: {cases_run} cases, {len(failures)} failure(s)')
for f in failures:
    print('  FAIL ' + f)
if failures:
    print('SELF-TEST FAILED: the sink gate cannot see what it claims to see.')
    sys.exit(1)
print('SELF-TEST PASSED: all four rules hold in both directions.')
sys.exit(0)
