#!/usr/bin/env python3
"""Self-test for i18n-count.py — the gate that says "no user-facing text is left unconverted"
is only worth anything if it cannot quietly stop seeing real copy.

Usage (from frontend/):   python3 i18n-count_test.py

Every case pins one decision the counter makes, in both directions: real copy in a
lookalike position must still measure, and the lookalike must not. Run it after any change
to the exclusion rules — including the text-attribute rule, whose two sides are the literal
that must be reported and the `t()` call, IDREF, flag, lookup key and `''` that must not be.
"""
import os
import sys
import tempfile
import importlib.util

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location('counter', os.path.join(HERE, 'i18n-count.py'))
assert _spec is not None and _spec.loader is not None, 'i18n-count.py must sit beside this test'
counter = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(counter)

cases_run = 0
failures = []


def measure(body, path):
    """Count `body` as a file at `path` — the path is part of the case, because the
    extension-host rules and the text-attribute allowlist key off where the file lives."""
    with tempfile.TemporaryDirectory() as d:
        full = os.path.join(d, path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, 'w', encoding='utf-8') as fh:
            fh.write(body)
        return counter.count_file(full)


def check(desc, ok):
    """A case the counter's one number cannot carry — the allowlist ratchet, a line number."""
    global cases_run
    cases_run += 1
    if not ok:
        failures.append(desc)


def write(body, path):
    """Write `body` where `path` says, and hand back the real path."""
    d = tempfile.mkdtemp()
    full = os.path.join(d, path)
    os.makedirs(os.path.dirname(full), exist_ok=True)
    with open(full, 'w', encoding='utf-8') as fh:
        fh.write(body)
    return full


def case(desc, expected, body, path='src/lib/components/Thing.svelte'):
    global cases_run
    cases_run += 1
    got = measure(body, path)
    if got != expected:
        failures.append(f'{desc}: expected {expected}, counted {got}')


# --- real copy must measure --------------------------------------------------
case('template text node', 1, '<p>Save changes</p>')
case('a dialog title:/message: pair is two rendered strings', 2,
     "<script>dialog.showConfirm({ title: 'Repository has changed', message: 'Refresh before committing?' });</script>")
case('both arms of a ternary in a mustache', 2, '<p>{ok ? "Yes" : "No"}</p>')
case('script literal', 1, '<script>const label = "Run task";</script><p>{label}</p>')
case('labelled option', 1, '<script>const opts = [{ value: "low", label: "Low" }];</script>')
case('fallback after ||', 1, '<script>txt.textContent = message || "Loading…";</script>')
case('copy ending in a colon', 1, '<script>const t = "Copy to clipboard:";</script>')
case('a sentence that looks like a declaration', 1,
     "<script>const note = 'Note: check the diff before committing';</script>")
case('format() is not a technical API', 1, "<script>const s = format('Saved the file');</script>")
case('copy containing a URL', 1, '<script>const t = "See https://example.com for details";</script>')
case('plural-ish sentence', 1, '<script>const t = `Show all changes`;</script>')
case('a single capitalised word is still copy', 1, '<button>Save</button>')
case('copy after an icon', 1, '<svg width="12" height="12"></svg><p>Save changes</p>')
case('error shown to the user', 1, '<script>throw new Error("Failed to save");</script>')
case('a key that merely ends in d is not SVG geometry', 1,
     "<script>const m = { thread: 'Open the newest thread' };</script>")
case('path data quoted as copy is still copy', 1,
     "<script>const tip = 'M8 3v10M3 8h10 draws a plus sign';</script>")

# --- a literal written into a text attribute is copy -------------------------
# The accessible name and the tooltip are the only words a screen-reader user, or a
# hovered icon, is given, so each of these is a finding whatever its shape.
case('a literal aria-label', 1, '<button aria-label="Save the file">{icon}</button>')
case('a literal title', 1, '<button title="Save changes">{icon}</button>')
case('a literal placeholder', 1, '<input placeholder="Search files" />')
case('a literal alt', 1, '<img src={icon} alt="Diagram of the board" />')
case('a lowercase accessible name is still copy', 1, '<button aria-label="close">{icon}</button>')
case('a quoted value in an expression', 1, "<button title={'Stage changes'}>{icon}</button>")
case('both arms of a ternary in a title', 2,
     "<button title={ok ? 'Stage changes' : 'Unstage changes'}>{icon}</button>")
case('a literal glued onto a key', 1, "<button title={t('diff.collapse') + ' (draft)'}>{icon}</button>")
case('a component prop that renders as text', 1, '<BaseModal title="Name the project" />')

# --- the lookalikes that must not -------------------------------------------
case('a catalog key on an attribute', 0, "<button aria-label={t('task.save')}>{icon}</button>")
case('a catalog key chosen per frame inside t()', 0,
     "<button title={t(open ? 'task.collapse' : 'task.expand')}>{icon}</button>")
case('a title built from a variable', 0, '<span title={fullPath}>{fullPath}</span>')
case('an aria-label chosen between two keys', 0,
     "<button aria-label={collapsed ? t('a.b') : t('a.c')}>{icon}</button>")
case('a script-level ariaLabel key', 0, "<script>const b = { ariaLabel: 'Close dialog' };</script>")
case('title assigned to an element', 0, "<script>el.title = 'Stage changes';</script>")
case('title: beside its labelKey is the fallback, not a hole', 0,
     "<script>const cols = [{ id: 'done', title: 'Done', labelKey: 'board.column.done' }];</script>")
case('aria-hidden carries a flag', 0, '<span aria-hidden="true">{icon}</span>')
case('aria-expanded carries a flag', 0, '<button aria-expanded={open}>{icon}</button>')
case('aria-labelledby carries an id', 0,
     '<div role="dialog" aria-labelledby="confirm-title">{body}</div>')
case('aria-describedby carries an id even in an expression', 0,
     "<div aria-describedby={tip ? 'card-tooltip' : undefined}>{body}</div>")
case('aria-controls and role carry no text', 0,
     '<button role="button" aria-controls="sidebar" tabindex="0">{icon}</button>')
case('alt="" says decorative', 0, '<img src={icon} alt="" />')
case('a comparison key inside a title', 0,
     "<button title={mode === 'rw' ? t('sandbox.readWrite') : t('sandbox.readOnly')}>{icon}</button>")
case('a lookup key inside a title', 0,
     "<button title={isCollapsed('staged') ? t('diff.expand') : t('diff.collapse')}>{icon}</button>")
case('a separator between rendered sentences', 0, '<span title={sentences.join(" · ")}>{text}</span>')
case('a template literal of variables only', 0, '<span title={`${label} · ${lang}`}>{label}</span>')
case('no tooltip this frame', 0, "<span title={stamp ? absolute(stamp) : ''}>{stamp}</span>")
case('a quote inside a regex in an attribute', 0,
     "<button title={value.replace(/'/, '')}>{icon}</button>")
case('a block closer before inline markup', 0,
     '{#if a}<span class="badge-provider">{p}</span><span class="sep"> · </span>{/if}<span class="badge-model">{m}</span>\n'
     '<div class="subtask-group is-waited">{q}</div>')
case('copy after a block closer is still measured', 1,
     '{#if a}<span class="badge-provider">{p}</span>{/if}<span class="badge-model">{m}</span>\n'
     '<button title="Open the folder">{icon}</button>')
case('an allowed format example, at its own site', 0,
     '<input placeholder="docker.io/ubuntu:latest" aria-label={t("sandbox.imageNameAria")} />',
     path='src/lib/components/SandboxConfigModal/ImageSection.svelte')
case('the same example in another file is not allowed', 1,
     '<input placeholder="docker.io/ubuntu:latest" />', path='src/lib/components/Other.svelte')
case('a comment before an attribute does not hide it', 1,
     '<!-- the placeholder is a shape, not copy -->\n<input placeholder="Search files" />')
case('a comment that quotes a tag does not swallow the markup after it', 1,
     '<!-- <input title="x"> -->\n<button aria-label="Save the file">{icon}</button>')
case('comment text', 0, '<script>// TODO: rename the cancel button\nconst x = 1;</script>')
case('block comment with an apostrophe', 0, "<script>/* don't translate this prose */\nconst x = 1;</script>")
# A comment inside `{...}` — an attribute expression or a mustache — is inert too: an
# apostrophe left in its prose opens a string that runs past the tag, the tag is never
# found, and the markup inside it is counted as copy while the copy after it goes unseen.
COMMENTED_ATTR = ("<input\n    oninput={(e) => {\n"
                  "        // Written from the store's copy, not the caret\n"
                  "        send(e.value);\n    }}\n")
case('line comment with an apostrophe in an attribute expression', 0,
     COMMENTED_ATTR + "    placeholder={t('editor.codePlaceholder')} />")
case('an attribute literal in a tag like that is still seen', 1,
     COMMENTED_ATTR + '    placeholder="Search files" />')
case('block comment with an apostrophe in a tag attribute', 0,
     "<input\n    title={label /* don't stretch it */ ? label : t('a.none')}\n"
     "    aria-label={t('a.aria')} />")
case('a quoted attribute in a tag like that is still seen', 1,
     "<input\n    title={label /* don't stretch it */ ? label : t('a.none')}\n"
     '    aria-label="Save the file" />')
case('block comment with an apostrophe in a mustache', 0,
     "<span>{value /* don't translate this prose */ ? t('a.ok') : t('a.no')}</span>")
case('copy after a mustache like that is still measured', 1,
     "<span>{value /* don't translate this prose */ ? t('a.ok') : t('a.no')}</span>\n"
     '<button>Save now</button>')
case('log line', 0, '<script>console.warn("Failed to arm the channel");</script>')
case('log line continued on its own line', 0,
     '<script>console.warn(prefix,\n    `channel disarmed until it re-handshakes.`);</script>')
case('quote inside a regex', 0, "<script>const re = /'/; // and a comment mentioning 'quoted' text here</script>")
case('CSS declaration value', 0, "<script>const t = { padding: '0 6px', borderRight: '1px solid var(--color-border)' };</script>")
case('SVG shape data built with DOM APIs', 0,
     "<script>const icon = { tag: 'path', attrs: { d: 'M8 3v10M3 8h10', stroke: 'currentColor' } };</script>")
case('SVG points and viewBox', 0,
     "<script>const s = { points: '3.5,1.5 3.5,8.5 8.5,5', viewBox: '0 0 16 16' };</script>")
case('inline style block', 0, "<script>el.cssText = 'position:fixed;z-index:10000;width:28px;';</script>")
case('keyframes text', 0, "<script>s.textContent = '@keyframes spin{from{transform:rotate(0deg)}}';</script>")
case('class list', 0, "<script>btn.className = 'ext-footer-btn footer-toggle';</script>")
case('diff parse marker', 0, "<script>if (line.startsWith('diff --git ')) parse();</script>")
case('slicing a parse marker', 0, "<script>const p = line.slice('diff --git '.length);</script>")
case('cssText fragment with no trailing semicolon', 0,
     "<script>el.style.cssText = 'position:absolute;' + 'clip-path:polygon(0 0, 0 100%, 100% 100%)';</script>")
case('classList.add', 0, "<script>btn.classList.add('is-active primary');</script>")
case('string union type', 0, "<script>const k: Kind = 'Program' | 'Script' | 'Document';</script>")
case('Go template token name', 0, "<script>return 'function variableName';</script>")
case('svg markup in a string', 0, '<script>el.innerHTML = `<path d="M28,0 L14,0" fill="#0084FF"/>`;</script>')
case('glyph drawn inside an icon', 0,
     "<button title={t('explorer.symbols.sortAlphabetically')}><svg><text x=\"0\" y=\"10\">Aa</text></svg></button>")
case('keycap label', 0, '<button onclick="go()">esc</button>')
case('remote name', 0, '<span class="git-badge-label">origin</span>')
case('svg path data as an attribute argument', 0,
     "<script>path.setAttribute('d', 'M4 6l4 4 4-4');</script>")
case('a CSS value with no property before it', 0,
     "<script>const tr = open ? 'translateY(2px)' : 'translateY(2px) rotate(-90deg)';</script>")
case('a font stack', 0,
     "<script>const o = { fontFamily: '\"Noto Sans Mono\", ui-monospace, monospace' };</script>")
case('keyword table in a Set', 0, "<script>const s = new Set(['Fingerprint', 'Permalink', 'Sections']);</script>")
case('a sentence opening with an interpolation', 1, '<script>return `${n} results`;</script>')
case('an interpolated URL is an address, not copy', 0,
     "<script>return `${API_BASE}/projects/${projectId}/tasks/${taskId}`;</script>")
case('a unit suffix after an interpolation', 0, '<script>return `${kwh.toFixed(3)} kWh`;</script>')
case('a protocol needle stripped from a status line', 0,
     "<script>const n = data.activity.replace('Pulling ', '');</script>")
case('an optional-chained closest()', 0,
     "<script>if (e.target?.closest?.('.key-chip, .shortcut-action')) return;</script>")
case('a computed CSS selector key', 0,
     '<script>const t = { [`${SEL} blockquote`]: { margin: 0 } };</script>')
case('copy that merely shares a line with an API name', 1,
     "<script>const fn = matches; const msg = 'Save the file now';</script>")
case('a media query', 0, "<script>const TOUCH = '(hover: none)';</script>")
case('icon markup held in a string', 0,
     "<script>items.push({ icon: '<polygon points=\"3.5,1.5\" fill=\"currentColor\"/>' });</script>")
case('IndexedDB cache key', 0, '<script>return `board:${projectId}`;</script>')
case('diff cache key with two parts', 0, '<script>return `diff:${projectId}:${taskId}`;</script>')
case('dotted message key', 0, "<script>t('editor.save');</script>")
case('technical key value', 0, "<script>fire({ type: 'task_completed', payload: 1 });</script>")
case('key comparison', 0, "<script>if (e.key === 'ArrowDown') go();</script>")
case('DOM API argument', 0, "<script>el.addEventListener('pointerdown', go);</script>")
case('all-caps constant', 0, "<script>const K = 'NO_PROXY_HOST';</script>")
case('single PascalCase word', 0, "<script>const C = 'CommitDiffOverlay';</script>")

# --- the extension-host channel is protocol, everywhere else is UI ----------
case('channel error reply', 0,
     "<script>this.post({ type: 'response', id, ok: false, error: 'rate limit exceeded' });</script>",
     path='src/lib/extensions/channel.ts')
case('channel handler rejection', 0,
     "<script>if (!root) throw new Error('editor not ready');</script>",
     path='src/lib/extensions/handlers/editor.ts')
case('channel arg-schema reason', 0,
     "<script>function validateArgs(a, s) {\n  return `'${key}' must be a string`;\n}</script>",
     path='src/lib/extensions/channel.ts')
case('but a label the host renders is still copy', 1,
     "<script>by.textContent = `Shown by ${extensionName}`;</script>",
     path='src/lib/extensions/modal.ts')
case('and the same throw outside the channel is copy', 1,
     "<script>if (!root) throw new Error('editor not ready');</script>",
     path='src/lib/components/FileEditor.svelte')

# --- the report names the site, and the allowlist only shrinks ---------------
rows = counter.analyze_file(write('<div\n     class="row"\n     aria-label="Save now"\n/>',
                                 'src/lib/components/Thing.svelte'))[1]
check('the row names the attribute line, the attribute and the text',
      [(r[0], r[1], r[2]) for r in rows] == [(3, 'aria-label', 'Save now')])
check('a finding in a file that names no allow entry carries none',
      bool(rows) and rows[0][3] is None)
mount = 'src/lib/components/SandboxConfigModal/MountsSection.svelte'
allowed = counter.analyze_file(write('<input placeholder="/host/path" />', mount))[1]
check('the allowed site reports no finding',
      [r[3] for r in allowed] == [(mount, 'placeholder', '/host/path')])
check('an entry nothing matched is stale, so the allowlist only shrinks',
      counter.stale_allow_keys(set()) == sorted(counter.TEXT_ATTR_ALLOW))
check('an entry every site matched is not stale',
      counter.stale_allow_keys(set(counter.TEXT_ATTR_ALLOW)) == [])

if failures:
    print(f'i18n-count_test: {len(failures)} FAILURE(S) of {cases_run} cases')
    for f in failures:
        print(f'  {f}')
    print('A case that counted too FEW means the gate has gone blind to real copy;')
    print('a case that counted too many means a lookalike is not excluded by context.')
    sys.exit(1)
print(f'i18n-count_test: ok — {cases_run} cases, both directions hold')
