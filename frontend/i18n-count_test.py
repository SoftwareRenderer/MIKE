#!/usr/bin/env python3
"""Self-test for i18n-count.py — the gate that says "no user-facing text is left unconverted"
is only worth anything if it cannot quietly stop seeing real copy.

Usage (from frontend/):   python3 i18n-count_test.py

Every case pins one decision the counter makes, in both directions: real copy in a
lookalike position must still measure, and the lookalike must not. Run it after any change
to the exclusion rules.
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
    extension-host rules key off where the file lives."""
    with tempfile.TemporaryDirectory() as d:
        full = os.path.join(d, path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, 'w', encoding='utf-8') as fh:
            fh.write(body)
        return counter.count_file(full)


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

# --- the lookalikes that must not -------------------------------------------
case('literal aria-label', 0, '<button aria-label="Save">{icon}</button>')
case('title attribute', 0, '<button title="Save changes">{icon}</button>')
case('title attribute built in an expression', 0, "<button title={ok ? 'Stage changes' : 'Unstage changes'}>{icon}</button>")
case('title assigned to an element', 0, "<script>el.title = 'Stage changes';</script>")
case('title: beside its labelKey is the fallback, not a hole', 0,
     "<script>const cols = [{ id: 'done', title: 'Done', labelKey: 'board.column.done' }];</script>")
case('placeholder attribute', 0, '<input placeholder="Search files" />')
case('script-level ariaLabel key', 0, "<script>const b = { ariaLabel: 'Close dialog' };</script>")
case('comment text', 0, '<script>// TODO: rename the cancel button\nconst x = 1;</script>')
case('block comment with an apostrophe', 0, "<script>/* don't translate this prose */\nconst x = 1;</script>")
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
case('glyph drawn inside an icon', 0, '<button title="Sort alphabetically"><svg><text x="0" y="10">Aa</text></svg></button>')
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

if failures:
    print(f'i18n-count_test: {len(failures)} FAILURE(S) of {cases_run} cases')
    for f in failures:
        print(f'  {f}')
    print('A case that counted too FEW means the gate has gone blind to real copy;')
    print('a case that counted too many means a lookalike is not excluded by context.')
    sys.exit(1)
print(f'i18n-count_test: ok — {cases_run} cases, both directions hold')
