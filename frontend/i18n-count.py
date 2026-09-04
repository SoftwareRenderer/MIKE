#!/usr/bin/env python3
"""Candidate-string counter for the app-shell/shared-primitives i18n batch.

Usage (from frontend/):   python3 i18n-count.py [-v]

Counts, per owned file:
  1. template text nodes      - text between tags in a .svelte template (any letters)
  2. quoted literals in <script> / .ts modules
  3. quoted literals inside a template mustache/block expression (`{ok ? 'Yes' : 'No'}`)

Excluded, by CONTEXT (never by string identity, so a real bug still measures):
  - literal aria-label / aria-labelledby / aria-describedby / title / placeholder /
    alt values - template attributes AND script-level `ariaLabel:` / `title=` keys
    (project convention; see frontend/src/lib/i18n/README.md)
  - comments, HTML comments, <style> blocks
  - console.* / logger.* arguments (log lines are never translated)
  - literals in property-KEY position (`{ 'X-CSRF-Token': v }`) and values of
    technical keys (`component:`, `type:`, `event:`, ...)
  - key-comparison context (`e.key === 'ArrowDown'`, `.has(`, `case `, `.get(`)
  - arguments of technical APIs (addEventListener, querySelector<T>(, CustomEvent,
    IndexedDB cache-key helpers, ...)
  - technical tokens: whitespace-free identifiers, paths, selectors, dotted message
    keys, ALL_CAPS and PascalCaseMultiWord tokens, HTML entities (`&times;`), and
    template literals with no prose words of their own (`modal ${cls}`)
"""
import re
import os
import sys

FILES = [
    'src/lib/components/App.svelte',
    'src/lib/components/App/Sidebar.svelte',
    'src/lib/components/App/TopBar.svelte',
    'src/lib/components/LoginModal.svelte',
    'src/lib/components/CreateTaskModal.svelte',
    'src/lib/components/Toast.svelte',
    'src/lib/components/BaseModal.svelte',
    'src/lib/components/ConfirmationDialog.svelte',
    'src/lib/stores/dialogs.svelte.ts',
    'src/lib/a11y.ts',
    'src/lib/constants.ts',
    'src/lib/constants/modes.ts',
    'src/lib/utils/keyboard.ts',
    'src/lib/utils/explore-mode.ts',
    'src/lib/utils/modal-state.ts',
    'src/lib/stores/ui.svelte.ts',
    'src/lib/components/ToggleSwitch.svelte',
    'src/lib/components/Icon.svelte',
    'src/lib/components/ResizeHandle.svelte',
    'src/lib/components/VirtualList.svelte',
    'src/lib/components/FileMentionDropdown.svelte',
    'src/lib/utils/file-mention.ts',
    'src/lib/composables/use-keyboard-shortcuts.svelte.ts',
]

A11Y_KEY = (r'(?:aria-label|aria-labelledby|aria-describedby|ariaLabel|'
            r'ariaDescribedby|title|placeholder|alt)')
A11Y_KEY_RE = re.compile(A11Y_KEY + r'\s*[:=]\s*$', re.IGNORECASE)
TECH_KEY_VALUE_RE = re.compile(
    r'(?:component|type|kind|event|action|slot|channel|scope|role|format|variant|'
    r'mimeType|op|origin|source|target|view|mode|status|state|tab|provider|lang|'
    r'id|key|name|url|path|version|hash|axis|behavior)\s*:\s*$', re.IGNORECASE)
LOG_RE = re.compile(r'(?:console\.\w+|\blogger?\.\w+)\s*\(\s*$', re.IGNORECASE)
IMPORT_RE = re.compile(r'(?:import|require|from)\s*\(?\s*$', re.IGNORECASE)
COMPARE_RE = re.compile(r'(?:===|!==|==|!=|\bcase|\.has|\.get|\.set|\.delete|\bin)\s*\(?\s*[\[{(]?\s*$')
GENERIC = r'(?:\s*<[^<>]*(?:<[^<>]*>)?[^<>]*>)?'
API_RE = re.compile(
    r'(?:add\s*EventListener|remove\s*EventListener|dispatch\s*Event|CustomEvent|'
    r'querySelector' + GENERIC + r'|querySelectorAll' + GENERIC + r'|'
    r'getElementById|getElementsBy\w+|closest|matches|'
    r'get\w*Attribute|set\w*Attribute|hasAttribute|'
    r'getBoardCache|setBoardCache|removeBoardCache|boardCacheKey|doneStatsCacheKey|'
    r'localStorage|sessionStorage|matchMedia|setProperty|getPropertyValue)\s*\(\s*$',
    re.IGNORECASE)
KEY_POS_PRE_RE = re.compile(r'(?:[{,[])\s*$')
KEY_POS_POST_RE = re.compile(r'^\s*:')
SINGLE_CAPIT = re.compile(r"^[A-Z][a-z]+(?:[.!?…:]{1,3})?$")
PASCAL = re.compile(r'^[A-Z][a-z0-9]+(?:[A-Z][a-z0-9]+)+$')
IDENT = re.compile(r'^[^A-Za-z]{0,2}[a-z][A-Za-z0-9._\-/:#&%+]*$|^[A-Z0-9][A-Z0-9_-]*$')
ENTITY_ONLY = re.compile(r'^(?:&[a-zA-Z]+;|#\d+;|&#x[0-9a-fA-F]+;|\s|[^\w\s])+$')
LIT_RE = re.compile(r"'((?:[^'\\\n]|\\.)*)'|\"((?:[^\"\\\n]|\\.)*)\"|`((?:[^`\\]|\\.)*)`", re.DOTALL)


def line_of(src, pos):
    return src.count('\n', 0, pos) + 1


def strip_comments(text):
    """Blank out // and /* */ comments, tracking string state so a `//` inside a
    literal survives."""
    out = []
    i, n = 0, len(text)
    quote = ''
    while i < n:
        c = text[i]
        if quote:
            out.append(c)
            if c == '\\':
                if i + 1 < n:
                    out.append(text[i + 1])
                    i += 2
                    continue
            elif c == quote:
                quote = ''
            i += 1
            continue
        if c in '"\'`':
            quote = c
            out.append(c)
            i += 1
            continue
        if c == '/' and i + 1 < n and text[i + 1] == '/':
            while i < n and text[i] != '\n':
                i += 1
            continue
        if c == '/' and i + 1 < n and text[i + 1] == '*':
            end = text.find('*/', i + 2)
            end = n if end == -1 else end + 2
            out.append(''.join('\n' if ch == '\n' else ' ' for ch in text[i:end]))
            i = end
            continue
        out.append(c)
        i += 1
    return ''.join(out)


def template_literal_is_token(val):
    """`modal ${cls}`, `/api/projects/${id}/git/branches` and `?project=${id}` are
    class fragments and addresses; `task ${n} for review` is copy."""
    rest = re.sub(r'\$\{.*?\}', ' ', val)
    if re.match(r'^\s*(?:/|\w+://|[?&])', rest) or '=' in rest:
        return True
    if re.search(r'[A-Za-z]{2,}\s+[A-Za-z]{2,}', rest):
        return False
    if re.match(r'^\s*[A-Z][a-z]{2,}', rest):
        return False
    if re.search(r'[A-Za-z]{2,}[^.]*[.:!?…]', rest):
        return False
    return True


def is_prose(val):
    """Does this literal look like user-visible copy rather than a token?"""
    if not re.search(r'[A-Za-z]', val):
        return False
    v = val.strip()
    if ENTITY_ONLY.match(v):
        return False
    if PASCAL.match(v):
        return False
    if '`' in v:
        return False
    if IDENT.match(v) and '${' not in v:
        return False
    if '${' in v:
        return not template_literal_is_token(v)
    if re.search(r'\S\s+\S', v):
        return True
    return bool(SINGLE_CAPIT.match(v))


def script_literal_items(src, start, end):
    chunk = strip_comments(src[start:end])
    items = []
    for m in LIT_RE.finditer(chunk):
        val = next(g for g in m.groups() if g is not None)
        pre = chunk[:m.start()].rstrip()
        post = chunk[m.end():]
        if not is_prose(val):
            continue
        if A11Y_KEY_RE.search(pre) or TECH_KEY_VALUE_RE.search(pre):
            continue
        if LOG_RE.search(pre) or IMPORT_RE.search(pre):
            continue
        if COMPARE_RE.search(pre) or API_RE.search(pre):
            continue
        if KEY_POS_PRE_RE.search(pre) and KEY_POS_POST_RE.match(post):
            continue
        items.append((line_of(src, start + m.start()), ' '.join(val.split())))
    return items


def tag_len(text, i):
    """Length of the markup tag starting at text[i] == '<'. Honours quotes and
    `{...}` attribute expressions (whose `=>` and `Record<string, x>` must not
    end the tag)."""
    n = len(text)
    j = i + 1
    if j >= n or text[j] not in 'abcdefghijklmnopqrstuvwxyz/!ABCDEFGHIJKLMNOPQRSTUVWXYZ':
        return 0
    depth = 0
    quote = ''
    while j < n:
        c = text[j]
        if quote:
            if c == '\\':
                j += 2
                continue
            if c == quote:
                quote = ''
        elif c in '"\'`':
            quote = c
        elif c == '{':
            depth += 1
        elif c == '}':
            depth -= 1
        elif depth == 0 and c == '>':
            return j - i + 1
        j += 1
    return 0


def mustache_len(text, i):
    """Length of the `{...}` expression starting at text[i] == '{'."""
    n = len(text)
    depth = 0
    quote = ''
    j = i
    while j < n:
        c = text[j]
        if quote:
            if c == '\\':
                j += 2
                continue
            if c == quote:
                quote = ''
        elif c in '"\'`':
            quote = c
        elif c == '{':
            depth += 1
        elif c == '}':
            depth -= 1
            if depth == 0:
                return j - i + 1
        j += 1
    return n - i


def template_items(src, start, end, scan_mustaches=True):
    text = re.sub(r'<!--.*?-->', lambda m: re.sub(r'[^\n]', ' ', m.group(0)),
                  src[start:end], flags=re.DOTALL)
    items = []
    i, n = 0, len(text)
    buf, buf_start = [], None

    def flush():
        if buf and any(c.strip() for c in buf):
            node = ''.join(buf)
            if re.search(r'[A-Za-z]', node) and not ENTITY_ONLY.match(node.strip()):
                items.append((line_of(src, start + buf_start), ' '.join(node.split())))

    while i < n:
        ch = text[i]
        if ch == '<':
            ln = tag_len(text, i)
            if ln:
                flush()
                buf, buf_start = [], None
                i += ln
                continue
        if ch == '{':
            flush()
            buf, buf_start = [], None
            ln = mustache_len(text, i)
            if scan_mustaches:
                items += script_literal_items(src, start + i, start + i + ln)
            i += max(ln, 1)
            continue
        if buf_start is None:
            buf_start = i
        buf.append(ch)
        i += 1
    flush()
    return items


def count_file(path, verbose=False):
    src = open(path, encoding='utf-8').read()
    items = []
    if path.endswith('.svelte'):
        pos = 0
        for m in re.finditer(r'<(script|style)\b[^>]*>.*?</\1>', src, flags=re.DOTALL | re.IGNORECASE):
            items += template_items(src, pos, m.start())
            if m.group(1).lower() == 'script':
                open_m = re.match(r'<script[^>]*>', m.group(0), flags=re.DOTALL)
                close_m = re.search(r'</script>\s*$', m.group(0), flags=re.DOTALL)
                if open_m and close_m:
                    items += script_literal_items(src, m.start() + open_m.end(), m.start() + close_m.start())
            pos = m.end()
        items += template_items(src, pos, len(src))
    else:
        items += script_literal_items(src, 0, len(src))
    items.sort()
    if verbose:
        for ln, val in items:
            print(f'      {path}:{ln}  {val!r}')
    return len(items)


def main():
    verbose = '-v' in sys.argv
    grand = 0
    for f in FILES:
        if not os.path.exists(f):
            print(f'  MISSING  {f}')
            continue
        n = count_file(f, verbose=verbose)
        grand += n
        print(f'{n:4d}  {f}')
    print('----')
    print(f'{grand:4d}  TOTAL candidate strings')


if __name__ == '__main__':
    main()
