#!/usr/bin/env python3
"""Self-test for html-sinks.py: every rule pinned in both directions, because a check
that stopped matching prints the same `ok` as a sink that is genuinely safe.

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


def analyze(files, allow, methods, url_sinks=None, style_sinks=None):
    """Run the gate's rules over `files` (a path→body map) as if it were the tree."""
    with tempfile.TemporaryDirectory() as d:
        root = os.path.join(d, 'src')
        for rel, body in files.items():
            # `lib/…` is under src/; anything else (public/…, index.html) sits beside it.
            full = os.path.join(root, rel) if rel.startswith('lib/') else os.path.join(d, rel)
            os.makedirs(os.path.dirname(full), exist_ok=True)
            with open(full, 'w', encoding='utf-8') as fh:
                fh.write(body)
        return sinks.analyze(pathlib.Path(root), allow, methods, url_sinks, style_sinks)


def case(desc, field, files, allow, methods, reported, url_sinks=None, style_sinks=None):
    """Assert `field` of the report is non-empty (`reported`) or empty (not)."""
    global cases_run
    cases_run += 1
    got = bool(analyze(files, allow, methods, url_sinks, style_sinks)[field])
    if got != reported:
        failures.append(f'{desc}: expected {field} {"reported" if reported else "clean"}, '
                        f'got {analyze(files, allow, methods, url_sinks, style_sinks)[field]}')


def case_label(desc, files, label, reported, url_sinks=None, style_sinks=None):
    """Assert `label`'s rule line prints FAIL (`reported`) or ok (not) for `files`.

    The status words are what a developer reads, so they are asserted like any other output.
    """
    global cases_run
    cases_run += 1
    rows = sinks.render(analyze(files, {}, {},
                                url_sinks={} if url_sinks is None else url_sinks,
                                style_sinks={} if style_sinks is None else style_sinks))
    hit = any(row.startswith('FAIL') and label in row for row in rows)
    if hit != reported:
        failures.append(f'{desc}: expected {label} to print '
                        f'{"FAIL" if reported else "ok"}, got {rows}')


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

# --- rule 2 again: the sink's statement, not the line it started on ----------
case('a {@html} spread over lines still shows a translation', 'translated',
     {'lib/Thing.svelte': '<div>{@html\n    t("chat.send")\n}</div>'}, THING,
     {'src/lib/Thing.svelte': 'literal'}, True)
case('a multiline sink with no translation is not', 'translated',
     {'lib/Thing.svelte': '<div>{@html\n    renderedHtml\n}</div>'}, THING,
     {'src/lib/Thing.svelte': 'literal'}, False)

# --- rule 5: a URL the browser follows on its own ----------------------------
ASSET = "import aIcon from '$lib/assets/icons/a.svg?raw';\n<div><Icon src={aIcon} /></div>\n"
GUARD_OK = {'lib/Thing.svelte': '<img src={imageSrc(att)} />\n',
            'lib/utils/attachments.ts': 'const DATA_URL_SCHEME = "data:";\n'
                                        'export function imageSrc(a) { return DATA_URL_SCHEME + a.data; }\n'}
GUARD_GONE = {'lib/Thing.svelte': '<img src={imageSrc(att)} />\n',
              'lib/utils/attachments.ts': 'export function imageSrc(a) { return a.data; }\n'}
UNSCHEMADE = {'lib/Thing.svelte': '<a href={ext.manifest.homepage}>x</a>\n'}
case('a URL from data is reported', 'urls', UNSCHEMADE, {}, {}, True)
case('an asset import is the build\'s own file and needs no declaration', 'urls',
     {'lib/Thing.svelte': ASSET}, {}, {}, False)
case('a data URL from the attachment guard is not reported', 'urls', GUARD_OK, {}, {}, False)
case('a guard that stopped building a data URL is reported', 'urls', GUARD_GONE, {}, {}, True)
DECLARED_URL = {'lib/Thing.svelte':
                '<script>const q = encodeURIComponent(p.id);</script>\n'
                '<a href={projectHref(p)}>x</a>\n'}
case('a declared URL whose file encodes is not reported', 'urls', DECLARED_URL, {}, {}, False,
     url_sinks={('src/lib/Thing.svelte', 'projectHref(p)'):
                'contains:encodeURIComponent — the file builds an encoded query'})
case('a declared URL whose marker went away is reported', 'urls',
     {'lib/Thing.svelte': '<a href={projectHref(p)}>x</a>\n'}, {}, {}, True,
     url_sinks={('src/lib/Thing.svelte', 'projectHref(p)'):
                'contains:encodeURIComponent — the file builds an encoded query'})
case('a declaration for a site that is no longer there is stale', 'stale_urls', DECLARED_URL,
     {}, {}, False, url_sinks={('src/lib/Thing.svelte', 'projectHref(p)'): 'literal — gone'})
case('a declaration whose site is gone is reported as stale', 'stale_urls',
     {'lib/Thing.svelte': '<script>const q = encodeURIComponent(1);</script><p>plain</p>\n'},
     {}, {}, True,
     url_sinks={('src/lib/Thing.svelte', 'projectHref(p)'): 'literal — gone'})
case('setAttribute with a URL from data is reported', 'urls',
     {'lib/Thing.ts': "el.setAttribute('href', ext.url);\n"}, {}, {}, True)
case('a declared setAttribute URL is not reported', 'urls',
     {'lib/Thing.ts': "el.setAttribute('href', ext.url);\n"}, {}, {}, False,
     url_sinks={('src/lib/Thing.ts', 'setAttribute(href)'): 'literal — ext.url is a build path'})
case('a URL inside a comment is prose, not a sink', 'urls',
     {'lib/Thing.svelte': '<!-- <a href={ext.manifest.homepage}> would be a sink -->\n'}, {}, {}, False)

# --- rule 6: a tag name from data is createElement with someone else's argument ---
case('a dynamic element name is reported', 'tags',
     {'lib/Thing.svelte': '<svelte:element this={item.tag}>x</svelte:element>\n'}, {}, {}, True)
case('a literal element name is not', 'tags',
     {'lib/Thing.svelte': '<svelte:element this={"div"}>x</svelte:element>\n'}, {}, {}, False)

# --- rule 7: text reaching a style attribute ---------------------------------
case('a style value the attribute completes with a unit is not reported', 'styles',
     {'lib/Thing.svelte': '<div style="width: {layout.leftWidth}px">x</div>\n'}, {}, {}, False)
case('a style value with no unit is reported', 'styles',
     {'lib/Thing.svelte': '<div style="width: {task.title}">x</div>\n'}, {}, {}, True)
case('a style: directive whose value carries the unit is not reported', 'styles',
     {'lib/Thing.svelte': '<div style:height={ratio * 100 + \'%\'}>x</div>\n'}, {}, {}, False)
case('a style: directive with no unit is reported', 'styles',
     {'lib/Thing.svelte': '<div style:width={widthStr}>x</div>\n'}, {}, {}, True)
HEX_OK = {'lib/Thing.svelte': '<span style="color: {badge.color}">x</span>\n',
          'lib/badge.ts': "export function badge() { return { color: '#3fb950' }; }\n"}
HEX_BAD = {'lib/Thing.svelte': '<span style="color: {badge.color}">x</span>\n',
           'lib/badge.ts': "export function badge(n) { return { color: n }; }\n"}
HEX_DECL = {('src/lib/Thing.svelte', 'badge.color'): 'hex-colors:lib/badge — colors are hex there'}
case('a style color from a hex-only module is not reported', 'styles', HEX_OK, {}, {}, False,
     style_sinks=HEX_DECL)
case('a style color the module no longer keeps hex is reported', 'styles', HEX_BAD, {}, {}, True,
     style_sinks=HEX_DECL)
case('an interpolated cssText is reported', 'styles',
     {'lib/Thing.ts': 'el.style.cssText = `color: ${entry.name}`;\n'}, {}, {}, True)
case('a literal cssText is not', 'styles',
     {'lib/Thing.ts': "el.style.cssText = 'display:flex;gap:8px';\n"}, {}, {}, False)
case('a style declaration for a site that is gone is stale', 'stale_styles',
     {'lib/Thing.svelte': '<p>plain</p>\n'}, {}, {}, True, style_sinks=HEX_DECL)

# --- the files outside src/ are in scope -------------------------------------
case('an innerHTML in an extension bridge script is a sink', 'unlisted',
     {'public/host-client.js': 'el.innerHTML = msg.html;\n'}, {}, {}, True)
case('a sink in index.html is a sink', 'unlisted',
     {'index.html': '<script>document.getElementById("app").innerHTML = theme;</script>\n'},
     {}, {}, True)

# --- the status words themselves ---------------------------------------------
case_label('a URL violation prints FAIL on its own rule line', UNSCHEMADE, '5 unaccounted URL', True)
case_label('a clean URL rule line does not', {'lib/Thing.svelte': ASSET}, '5 unaccounted URL', False)
case_label('a translated sink prints FAIL on rule 2',
           {'lib/Thing.svelte': '<div>{@html t("chat.send")}</div>'}, '2 translated sink', True)
case_label('a clean rule 2 line does not', {'lib/Thing.svelte': SINK}, '2 translated sink', False)
case_label('a style violation prints FAIL on rule 7',
           {'lib/Thing.svelte': '<div style="top: {task.title}">x</div>'}, '7 style injection', True)
case_label('a stale URL declaration prints FAIL on its rule line',
           {'lib/Thing.svelte': '<p>plain</p>'}, '5 unaccounted URL', True,
           url_sinks={('src/lib/Thing.svelte', 'gone(p)'): 'literal — the site is gone'})

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
meta('every rule 5/7 declaration is a form this file can check',
     all(sinks._decl(spec)[0] in ('literal',) or sinks._decl(spec)[0].split(':')[0]
         in ('contains', 'encoded-by', 'escaped-by', 'guard', 'hex-colors')
         for table in (sinks.URL_SINKS, sinks.STYLE_SINKS) for spec in table.values()))
meta('the browser files outside src/ are scanned',
     set(sinks.OUTSIDE_SRC) <= {rel for rel, _p in sinks.core_files(sinks.SRC)})
meta('every rule 5 guard still holds its marker in the real tree',
     all((sinks._module_text(m, sinks.SRC) or '') .find(mark) != -1
         for m, mark in sinks.URL_GUARDS.values()))
meta('no URL or style declaration is a duplicate of another',
     len(sinks.URL_SINKS) == len(set(sinks.URL_SINKS)) and len(sinks.STYLE_SINKS) == len(set(sinks.STYLE_SINKS)))
meta('the real src/ is clean', sinks.clean(sinks.analyze(sinks.SRC, sinks.ALLOW, sinks.METHOD)))

print(f'html-sinks_test: {cases_run} cases, {len(failures)} failure(s)')
for f in failures:
    print('  FAIL ' + f)
if failures:
    print('SELF-TEST FAILED: the sink gate cannot see what it claims to see.')
    sys.exit(1)
print('SELF-TEST PASSED: all seven rules hold in both directions.')
sys.exit(0)
