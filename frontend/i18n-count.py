#!/usr/bin/env python3
"""Candidate-string counter — the whole-tree i18n surface gate.

Usage (from frontend/):
    python3 i18n-count.py            # every core file; exit 1 if any finding remains
    python3 i18n-count.py -v         # + the offending literals with file:line
    python3 i18n-count.py src/lib/components/DiffViewer.svelte ...   # only these files

This is a gate, not a report: the tree is at zero, so **any** candidate or text-attribute
literal is a regression and a non-zero exit. `npm run check:i18n-surface` is it, and
`make test-frontend` runs it beside the unit tests and `i18n-gate.py`.

Passing file **and** directory arguments measures a subset — how a change to one area checks
itself before the whole gate is run.

Counts, per file:
  1. template text nodes      - text between tags in a .svelte template (any letters)
  2. quoted literals in <script> / .ts modules
  3. quoted literals inside a template mustache/block expression (`{ok ? 'Yes' : 'No'}`)
  4. literals written into a **text attribute** - `aria-label`, `alt`, `placeholder`,
     `title` on a tag. Reported with its own `file:line` and the attribute, because the
     accessible name and the tooltip are the only copy a screen-reader user, or a
     hovered icon, is given: `docs/i18n.md -> Attribute text is a catalog key`.
     A site that is genuinely not copy (a `placeholder` that is a format example) sits
     in TEXT_ATTR_ALLOW, one entry per site with its reason, and an entry whose site has
     gone is itself a finding - so the list only ever shrinks.

Excluded, by CONTEXT (never by string identity, so a real bug still measures):
  - script-level `ariaLabel:` / `ariaDescribedby:` / `title=` keys and an imperative
    `el.title =` - a name built off the render path, which the module that builds it owns
    (project convention; see frontend/src/lib/i18n/README.md)
  - attributes that carry no text: an IDREF (`aria-labelledby`, `aria-describedby`) or a
    flag (`aria-hidden`, `aria-expanded`, `role`) - the rule is keyed on the attribute
    NAME, so `aria-labelledby="confirm-title"` is an id, and stays
  - comments, HTML comments, <style> blocks
  - arguments of console.* / logger.* calls (log lines are never translated) — the
    whole enclosing call, so a message continued on its own line is still a log
  - literals in property-KEY position (`{ 'X-CSRF-Token': v }`) and values of
    technical keys (`component:`, `type:`, `event:`, ...), SVG shape data (`d:`,
    `points:`) included
  - key-comparison context (`e.key === 'ArrowDown'`, `.has(`, `case `, `.get(`)
  - arguments of technical APIs (addEventListener, querySelector<T>(, CustomEvent,
    IndexedDB cache-key helpers, ...)
  - members of a TypeScript string-union type (`kind: 'Program' | 'Script'`)
  - CSS: a declaration value whose key is a CSS property and whose value is CSS
    tokens, and an inline `style`/`cssText` declaration block (`padding:7px 16px;…`)
  - embedded source markup — a literal whose body is code (`` `x.replace(…)` ``,
    `<path d="M28,0 …"/>`), which is a program shipped inside a string, not copy
  - technical tokens: whitespace-free identifiers, paths, selectors, dotted message
    keys, ALL_CAPS and PascalCaseMultiWord tokens, HTML entities (`&times;`), and
    template literals with no prose words of their own (`modal ${cls}`,
    `board:${projectId}`)

Skipped outright: any `__tests__/` dir, any `*.unit.*` file, and `src/lib/i18n/` —
that directory holds the English catalog of record, which is English on purpose.
"""
import re
import os
import sys

A11Y_KEY = (r'(?:aria-label|aria-labelledby|aria-describedby|ariaLabel|'
            r'ariaDescribedby|placeholder|alt)')
A11Y_KEY_RE = re.compile(A11Y_KEY + r'\s*[:=]\s*$', re.IGNORECASE)
# Labelling names in a *script* position — an `ariaLabel:` object key, an imperative
# `el.title =` — hold a name built off the render path, which the module that builds it owns.
# A tag's own `aria-label`/`alt`/`placeholder`/`title` is not exempt: `template_items` steps
# over a whole tag, so these three regexes never see it and the text-attribute rule below
# reports it.
# `title` is the one labelling name that is *ambiguous*: as a script object property it is
# usually visible copy — `showConfirm({ title })` renders an `<h2>` (see
# src/lib/i18n/README.md). It counts as copy unless the object it belongs to carries a
# sibling `labelKey:`, which is the shape of "this title is the English fallback of a label
# that already has a key".
TITLE_KEY_RE = re.compile(r'\btitle\s*:\s*$', re.IGNORECASE)
# …while an assignment is the tooltip itself and not copy at all: `el.title = 'Stage'`.
TITLE_ATTR_RE = re.compile(r'\btitle\s*=\s*$', re.IGNORECASE)
KEYED_LABEL_RE = re.compile(r'\blabelKey\s*:', re.IGNORECASE)


def title_is_a_tooltip(chunk, start):
    """Is the `title:` owning this literal the fallback of a keyed label?"""
    depth, i = 0, start - 1
    floor = max(0, start - 4000)
    while i >= floor:
        c = chunk[i]
        if c in ')}]':
            depth += 1
        elif c == '{':
            if depth == 0:
                end = chunk.find('}', start)
                return bool(KEYED_LABEL_RE.search(chunk[i:end if end != -1 else len(chunk)]))
            depth -= 1
        elif c == ';' and depth == 0:
            return False
        i -= 1
    return False
TECH_KEY_VALUE_RE = re.compile(
    r'(?:component|type|kind|event|action|slot|channel|scope|role|format|variant|'
    r'mimeType|op|origin|source|target|view|mode|status|state|tab|provider|lang|'
    r'id|key|name|url|path|version|hash|axis|behavior)\s*:\s*$', re.IGNORECASE)
# SVG shape data is geometry, like the `<path d="…"/>` markup the embedded-source rule
# already excludes. Word-boundary anchored so `grid:` cannot borrow the single-letter `d`.
SVG_GEOMETRY_KEY_RE = re.compile(r'\b(?:d|points|viewBox|cx|cy|rx|ry)\s*:\s*$')
LOG_RE = re.compile(r'(?:console\.\w+|\blogger?\.\w+)\s*(?:\(\s*)?$', re.IGNORECASE)
IMPORT_RE = re.compile(r'(?:import|require|from)\s*\(?\s*$', re.IGNORECASE)
COMPARE_RE = re.compile(r'(?:===|!==|==|!=|\bcase|\.has|\.get|\.set|\.delete|\bin)\s*\(?\s*[\[{(]?\s*$')
GENERIC = r'(?:\s*<[^<>]*(?:<[^<>]*>)?[^<>]*>)?'
# Every one of these takes a *needle* — a selector, an event name, a cache key — never a
# sentence a reader reads. The tail (optional `?.`, then `(`) applies to the whole list:
# when it once applied only to the last alternative, every other name below matched on bare
# text anywhere in the line and the rule quietly excluded far more than it should.
API_METHODS = (
    r'add\s*EventListener|remove\s*EventListener|dispatch\s*Event|CustomEvent|'
    r'querySelector' + GENERIC + r'|querySelectorAll' + GENERIC + r'|'
    r'getElementById|getElementsBy\w+|closest|matches|'
    r'get\w*Attribute|set\w*Attribute|hasAttribute|'
    r'getBoardCache|setBoardCache|removeBoardCache|boardCacheKey|doneStatsCacheKey|'
    r'localStorage|sessionStorage|matchMedia|setProperty|getPropertyValue|'
    r'startsWith|endsWith|includes|localeCompare|index\s*Of|lastIndex\s*Of|'
    r'slice|substr(?:ing)?|charAt|charCodeAt|codePointAt|'
    r'\.(?:replace\s*All|replace|match\s*All|match|search)'
)
API_RE = re.compile(r'(?:' + API_METHODS + r')\s*(?:\?\.)?\(\s*$', re.IGNORECASE)
KEY_POS_PRE_RE = re.compile(r'(?:[{,[])\s*$')
# `]:` so a computed key (`[${SELECTOR}]: { margin: 0 }`) is read as a key too.
KEY_POS_POST_RE = re.compile(r'^\s*\]?\s*:')
# A whole literal that is one parenthesised declaration is a CSS media query —
# `matchMedia('(hover: none)')` — not a sentence in parentheses.
MEDIA_QUERY_RE = re.compile(r'^\([^():]+:[^()]*\)$')
SINGLE_CAPIT = re.compile(r"^[A-Z][a-z]+(?:[.!?…:]{1,3})?$")
PASCAL = re.compile(r'^[A-Z][a-z0-9]+(?:[A-Z][a-z0-9]+)+$')
IDENT = re.compile(r'^[^A-Za-z]{0,2}[a-z][A-Za-z0-9._\-/:#&%+]*$|^[A-Z0-9][A-Z0-9_-]*$')
ENTITY_ONLY = re.compile(r'^(?:&[a-zA-Z]+;|#\d+;|&#x[0-9a-fA-F]+;|\s|[^\w\s])+$')
LIT_RE = re.compile(r"'((?:[^'\\\n]|\\.)*)'|\"((?:[^\"\\\n]|\\.)*)\"|`((?:[^`\\]|\\.)*)`", re.DOTALL)

# Containers whose members are identifiers the platform looks up, never prose:
# `new Set(['Fingerprint', …])` is a keyword table, `['Save']` rendered is copy.
IDENT_TABLE_RE = re.compile(r'\b(?:new\s+)?(?:Set|Map|WeakSet|WeakMap)\s*\(?\s*[\[{(]?\s*$')
# A message continued onto its own line is still a log argument.
LOG_CALL_RE = re.compile(r'(?:console\.\w+|\blogger?\.\w+)\s*\(?\s*$')
# DOM class lists and element lookups: `classList.add('ext-footer-btn footer-toggle')`
# is a selector, however many words it has.
CLASS_LIST_RE = re.compile(
    r'(?:classList\.(?:add|remove|toggle|contains|replace)|className|\$?classes?)\s*[:=(]?\s*$')
# The extension host channel. Every rejection a host handler raises is caught at the
# single dispatch point and posted back to the extension frame as
# `{type:'response', ok:false, error: e.message}` (see `dispatch` in channel.ts), so an
# Error raised on that side of the channel is a protocol reply, not a rendered label.
# The rendered half of that surface is `renderStatus(kind, message)`, whose message an
# extension supplies — and a literal core renders itself (`'Loading…'`, 'Extension
# error', `Shown by ${name}`) is not an Error argument and is still measured.
ERROR_ARG_RE = re.compile(r'\bnew\s+Error\s*\(?\s*$|^\s*throw\s*$|^\s*reject\s*\(?\s*$')
# …the same for an object handed straight to the channel's `post`:
# `this.post({ type: 'response', ok: false, error: 'rate limit exceeded' })`.
CHANNEL_POST_RE = re.compile(r'\b(?:this\.)?post\s*\(\s*$')
# …and for `validateArgs`, the arg-schema check whose reason is interpolated into the
# `invalid args: ${err}` reply above. Naming the function rather than its strings keeps
# the rule safe in the right direction: rename it and the strings start counting again.
CHANNEL_PROTOCOL_FNS = {'validateArgs'}
# `'Program' | 'Script' | 'Document'` — a union type is a type, not a label. The bars
# must not be doubled: `message || 'Loading…'` is a fallback expression whose copy is
# exactly what this counter exists to catch.
UNION_PRE_RE = re.compile(r'[^|]\|\s*$')
UNION_POST_RE = re.compile(r'^\s*\|[^|]')

# --- CSS ---------------------------------------------------------------------
# A declaration value is CSS when BOTH halves say so: the key is a CSS property
# name, and every token of the value is a CSS token. `label: 'Low'` stays copy
# because `label` is not a property; `padding: '7px 16px'` is not.
CSS_PROPS = set("""
align-content align-items align-self animation animation-delay animation-duration
animation-fill-mode animation-iteration-count animation-name animation-timing-function
appearance backdrop-filter backface-visibility background background-attachment
background-blend-mode background-clip background-color background-image background-origin
background-position background-repeat background-size block-size border border-block
border-bottom border-bottom-color border-bottom-left-radius border-bottom-right-radius
border-bottom-style border-bottom-width border-collapse border-color border-image
border-left border-left-color border-left-style border-left-width border-radius
border-right border-right-color border-right-style border-right-width border-spacing
border-style border-top border-top-color border-top-left-radius border-top-right-radius
border-top-style border-top-width border-width bottom box-shadow box-sizing break-inside
caption-side caret-color clear clip clip-path color column-count column-gap column-rule
columns contain content counter-increment counter-reset cursor direction display
empty-cells filter flex flex-basis flex-direction flex-flow flex-grow flex-shrink
flex-wrap float font font-family font-feature-settings font-kerning font-size
font-size-adjust font-stretch font-style font-synthesis font-variant font-variant-caps
font-variant-ligatures font-weight gap grid grid-area grid-auto-columns grid-auto-flow
grid-auto-rows grid-column grid-row grid-template grid-template-areas grid-template-columns
grid-template-rows height hyphens image-rendering inline-size inset isolation justify-content
justify-items justify-items left letter-spacing line-break line-height list-style
list-style-image list-style-position list-style-type margin margin-bottom margin-left
margin-right margin-top mask max-height max-inline-size max-width min-block-size
min-height min-width mix-blend-mode object-fit object-position opacity order outline
outline-color outline-offset outline-style outline-width overflow overflow-anchor
overflow-wrap overflow-x overflow-y padding padding-bottom padding-left padding-right
padding-top page-break perspective pointer-events position quotes resize right rotate
row-gap scroll-behavior scroll-margin scroll-padding shape-outside tab-size table-layout
text-align text-align-last text-decoration text-decoration-color text-decoration-line
text-decoration-style text-emphasis text-indent text-justify text-overflow text-rendering
text-shadow text-transform text-underline-offset top touch-action transform transform-origin
transition transition-delay transition-duration transition-property transition-timing-function
unicode-bidi user-select vertical-align visibility white-space widows width will-change
word-break word-spacing word-wrap writing-mode z-index zoom
flex-flow place-items place-content accent-color color-scheme all
""".split())
CSS_VALUE_RE = re.compile(
    r"""^(?:
        [+-]?(?:\d+(?:\.\d+)?|\.\d+)(?:px|em|rem|ex|ch|%|vh|vw|vmin|vmax|s|ms|deg|fr|pt|dpi)?
      | \#[0-9a-fA-F]{3,8}
      | \"[^\"]*\"
      | [a-zA-Z-]+\([^()]*\)
      | -{0,2}[a-zA-Z][a-zA-Z0-9-]*
      | /
    )$""", re.VERBOSE)
# `position:fixed;z-index:10000;width:28px;…` — an inline declaration block.
CSS_BLOCK_RE = re.compile(r'(?:^|[;{\s])[a-z-]+\s*:\s*[^;{}]+\s*;', re.IGNORECASE)
# `@keyframes ext-spin{from{transform:rotate(0deg)}to{…}}` and any `{ prop: value }`
# pair — a stylesheet shipped as a string.
CSS_RULE_RE = re.compile(r'@[a-z-]+\s|\{[^{}]*[a-z-]+\s*:\s*[^{}]+\}', re.IGNORECASE)
CSS_STYLE_KEY_RE = re.compile(r'(?:style|cssText|setAttribute)\s*(?:[:=(]\s*[\'"]?)?$', re.IGNORECASE)


def css_value_is_css(value):
    """Every token is a length, color, var()/rgba() call, quoted family name or keyword.
    Commas separate shorthands outside brackets and arguments inside them, and a quoted
    family name is one token even though it contains spaces — so only separators at depth 0
    and outside quotes split. `rgba(30, 30, 46, 0.95)` and `"Noto Sans Mono"` each stay one."""
    tokens = css_tokens(value)
    if not tokens or any(t.count('"') % 2 or t.count("'") % 2 for t in tokens):
        return False
    return all(CSS_VALUE_RE.match(t) for t in tokens)


def css_tokens(value):
    """Split a CSS value on depth-0 whitespace/commas, keeping brackets and quotes whole."""
    tokens, buf, depth, quote = [], '', 0, ''
    for ch in value.strip():
        if quote:
            if ch == quote:
                quote = ''
        elif ch in '"\'':
            quote = ch
        elif ch == '(':
            depth += 1
        elif ch == ')':
            depth -= 1
        if not quote and depth <= 0 and ch in ' \t,':
            if buf:
                tokens.append(buf)
                buf = ''
            continue
        buf += ch
    if buf:
        tokens.append(buf)
    return tokens


def css_prop_key(name):
    """`border-right` and `borderRight` are the same property spelled twice."""
    return name.lower().replace('-', '')


CSS_PROP_KEYS = {css_prop_key(p) for p in CSS_PROPS}


def is_css_declaration(pre, value):
    """`borderRight: '1px solid var(--color-border)'` / `padding: '0 6px'`, and the
    whole-literal form a concatenated `cssText` is built from
    (`'clip-path:polygon(0 0, 0 100%, 100% 100%)'`, no trailing semicolon).

    Both halves must say CSS — a property name *and* a value made of CSS tokens — which
    is why `'Note: check the diff before committing'` is still copy: `note` is not a
    property, and the rest is not a value."""
    in_pre = re.search(r'([A-Za-z][A-Za-z0-9-]*)\s*:\s*$', pre)
    if in_pre:
        key, body = in_pre.group(1), value
    else:
        whole = re.match(r'([A-Za-z][A-Za-z0-9-]*)\s*:\s*(.*)$', value, re.DOTALL)
        if not whole:
            return False
        key, body = whole.group(1), whole.group(2)
    if css_prop_key(key) not in CSS_PROP_KEYS:
        return False
    return css_value_is_css(body)


# --- embedded source ---------------------------------------------------------
# A literal can hold a whole program: a code string handed to `new Function`, an
# SVG path, a stylesheet injected as text. It is markup the string evaluates, not
# copy the reader reads.
CODE_MARKERS = re.compile(
    r'=>|\bfunction\b|\bconst \w|\blet \w|\bvar \w|\breturn \b|\bif\s*\(|\bfor\s*\(|'
    r'\bwhile\s*\(|[;{}]\s*$|\}\)\(|\.(?:replace|split|join|forEach|map|filter|push|exec|test)\b|'
    r'</[a-zA-Z]|<[a-zA-Z]+ [a-zA-Z-]+=|d="[Mm]|style=\\?"|/\*|\*/|//[^\n]|'
    r'\$\{[^}]*\}\s*[:=]|:\s*[a-z]+\s*:\s*[a-z]', re.IGNORECASE)
# A literal that BEGINS like a program is a program: `'function variableName'` is a
# CodeMirror token name, not a label. Kept to keywords no English sentence opens with.
CODE_BODY_RE = re.compile(
    r'^(?:function|const|let|var|return|typeof|instanceof|await|async|export|import|'
    r'class|interface|enum|void|new|if|for|while|switch|try|catch)\b'
    r'|=>|\bnew\s+[A-Z]|\bconsole\.\w|\brequire\(|\bimport\(')


# A value built only of functions, lengths, colors and var() — `translateY(2px)
# rotate(-90deg)` — is a CSS fragment even with no `property:` before it, because a
# sentence always contains a bare word and this contains none.
CSS_VALUE_ONLY_RE = re.compile(r'^[\s,()/]*$')


def is_css_value_alone(value):
    return bool(value.strip()) and css_value_is_css(value) and all(
        '(' in t or '#' in t or t.startswith('"') or CSS_VALUE_ONLY_RE.match(t)
        or re.match(r'^[+-]?(\d+(\.\d+)?|\.\d+)', t)
        for t in css_tokens(value))


def looks_like_code(value):
    """The body of this literal is source/markup rather than something to translate."""
    if CODE_BODY_RE.match(value.strip()):
        return True
    if CSS_BLOCK_RE.search(value) or CSS_RULE_RE.search(value):
        return True
    if is_css_value_alone(value):
        return True
    if re.search(r'^<[a-zA-Z]', value.strip()):   # a literal that IS markup
        return True
    if re.search(r'</[a-zA-Z]|\bd="[Mm]', value):   # HTML/SVG shipped as a string
        return True
    return len(CODE_MARKERS.findall(value)) >= 2


def is_union_member(pre, post, value):
    """`type: 'Program' | 'Script'` — a type literal with no spaces is not copy."""
    if ' ' in value.strip():
        return False
    return bool(UNION_PRE_RE.search(pre) or UNION_POST_RE.match(post))


def enclosing_call_is(chunk, start, pattern):
    """Is this literal an argument of a call matching `pattern`?

    Walks back over balanced brackets, so a message continued onto its own line
    (`console.warn(prefix,\\n  '…')`) is still recognised as a log argument. `pattern`
    is matched against the text before the opening bracket, so it must be `$`-anchored.
    """
    depth = 0
    i = start - 1
    floor = max(0, start - 4000)
    while i >= floor:
        c = chunk[i]
        if c in ')]}':
            depth += 1
        elif c in '([{':
            if depth == 0:
                return bool(re.search(pattern, chunk[floor:i]))
            depth -= 1
        elif c == ';' and depth == 0:
            return False
        i -= 1
    return False


def enclosing_call_matches_any(chunk, start, pattern, levels=3):
    """Like `enclosing_call_is`, but tests each of the `levels` brackets this literal
    sits inside — `Promise.reject(new Error(\n 'x'))` is nested twice, and so is the
    second argument of `setAttribute('d', …)` (the bracket is included in the window so
    a pattern that ends in `(` still matches it)."""
    depth = 0
    i = start - 1
    floor = max(0, start - 4000)
    checked = 0
    while i >= floor and checked < levels:
        c = chunk[i]
        if c in ')]}':
            depth += 1
        elif c in '([{':
            if depth == 0:
                checked += 1
                if re.search(pattern, chunk[floor:i + 1]) or re.search(pattern, chunk[floor:i]):
                    return True
            else:
                depth -= 1
        elif c == ';' and depth == 0:
            return False
        i -= 1
    return False


def enclosing_function(chunk, start):
    """Name of the innermost function whose declaration precedes `start`.

    Approximate on purpose — it takes the last declaration keyword before the offset.
    Every caller treats "no match" as *count the string*, so a wrong answer here costs
    a finding, never a silent pass.
    """
    window = chunk[max(0, start - 6000):start]
    best, at = None, -1
    for m in re.finditer(r'\bfunction\s+([A-Za-z0-9_$]+)|\b(?:const|let)\s+([A-Za-z0-9_$]+)\s*=\s*(?:async\s*)?(?:\([^)]*\)|[A-Za-z0-9_$]+)\s*=>', window):
        best = m.group(1) or m.group(2)
        at = m.end()
    return best if best and len(window) - at < 1200 else None


def line_of(src, pos):
    return src.count('\n', 0, pos) + 1


def regex_starts_here(text, i):
    """Is the `/` at i the start of a regex literal rather than a division?

    The usual heuristic, biased toward "division": a `/` preceded by the end of an
    operand divides. Getting this wrong only means a comment leaks back into the scan
    exactly as it did before regexes were tracked — it can never hide copy, because a
    detected regex is blanked, and copy never lives in one.
    """
    j = i - 1
    while j >= 0 and text[j] in ' \t':
        j -= 1
    prev = text[j] if j >= 0 else ''
    if prev == '':
        return True
    if re.match(r'[A-Za-z0-9_$)\]}' '\'"`]', prev):
        return False
    return True


def regex_len(text, i):
    """Length of the regex literal starting at text[i] == '/', or 0 if unterminated
    on this line. Tracks `[…]` classes and escapes; a regex cannot span a newline."""
    n = len(text)
    j = i + 1
    while j < n:
        c = text[j]
        if c == '\\':
            j += 2
            continue
        if c == '\n':
            return 0
        if c == '[':
            j += 1
            while j < n and text[j] != ']':
                j += 2 if text[j] == '\\' else 1
            j += 1
            continue
        if c == '/':
            j += 1
            while j < n and text[j] in 'gimsuy':
                j += 1
            return j - i
        j += 1
    return 0


def strip_comments(text):
    """Blank out // comments, /* */ comments and regex literals, tracking string state
    so a `//` inside a literal survives. Quotes inside a comment are inert (an apostrophe
    in prose must not swallow the rest of the file), and a quote inside a *regex* is inert
    for the same reason — without that, `/'/ ` opened a fake string that ate the following
    `//` and left the comment's own prose to be counted as copy. Blanks keep the length so
    line numbers reported from the stripped text still point at the right line."""
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
        if c == '/' and i + 1 < n and text[i + 1] == '/':
            while i < n and text[i] != '\n':
                out.append(' ')
                i += 1
            continue
        if c == '/' and i + 1 < n and text[i + 1] == '*':
            end = text.find('*/', i + 2)
            end = n if end == -1 else end + 2
            out.append(''.join('\n' if ch == '\n' else ' ' for ch in text[i:end]))
            i = end
            continue
        if c == '/' and regex_starts_here(text, i):
            ln = regex_len(text, i)
            if ln:
                out.append(' ' * ln)
                i += ln
                continue
        if c in '"\'`':
            quote = c
            out.append(c)
            i += 1
            continue
        out.append(c)
        i += 1
    return ''.join(out)


def template_literal_is_token(val):
    """`modal ${cls}`, `/api/projects/${id}/git/branches` and `board:${projectId}` are
    class fragments, addresses and cache keys; `task ${n} for review` is copy."""
    rest = re.sub(r'\$\{.*?\}', ' ', val)
    if re.match(r'^\s*(?:/|\w+://|[?&])', rest) or '=' in rest:
        return True
    if re.search(r'[A-Za-z]{2,}\s+[A-Za-z]{2,}', rest):
        return False
    # A literal that OPENS with an interpolation is a sentence whose subject was slotted
    # in — `${n} results`, `${name} failed` — not a key with a suffix. Keys and paths open
    # with their own identifier, which is the whole difference. (Checked after the address
    # test above, or every `${API_BASE}/projects/${id}` URL in api/ reads as prose.)
    if val.lstrip().startswith('${') and re.search(r'[A-Za-z]{4}', rest):
        return False
    if re.match(r'^\s*[A-Z][a-z]{2,}', rest):
        return False
    # A sentence may end in "…:" ("Copy to clipboard:"), a key may not. The test is
    # the space, not the word: `board:${id}` is one token glued to a colon, which is
    # how an IndexedDB key is spelled, while prose keeps its space before the mark.
    if re.search(r'\S\s[^.]*[.:!?…]$', rest):
        return False
    if re.search(r'[A-Za-z]{2,}[^.]*[.:!?…]', rest) and re.search(r'\s\S+\s', rest):
        return False
    return True


def is_prose(val):
    """Does this literal look like user-visible copy rather than a token?"""
    if not re.search(r'[A-Za-z]', val):
        return False
    v = val.strip()
    if ENTITY_ONLY.match(v):
        return False
    if MEDIA_QUERY_RE.match(v):
        return False
    if PASCAL.match(v):
        return False
    if '`' in v:
        return False
    if IDENT.match(v) and '${' not in v:
        return False
    if looks_like_code(v):
        return False
    if '${' in v:
        return not template_literal_is_token(v)
    if re.search(r'\S\s+\S', v):
        return True
    return bool(SINGLE_CAPIT.match(v))


def script_literal_items(src, start, end, channel_errors=False):
    chunk = strip_comments(src[start:end])
    items = []
    for m in LIT_RE.finditer(chunk):
        val = next(g for g in m.groups() if g is not None)
        pre = chunk[:m.start()].rstrip()
        post = chunk[m.end():]
        if not is_prose(val):
            continue
        if A11Y_KEY_RE.search(pre) or TECH_KEY_VALUE_RE.search(pre) or TITLE_ATTR_RE.search(pre):
            continue
        if SVG_GEOMETRY_KEY_RE.search(pre):
            continue
        if TITLE_KEY_RE.search(pre) and title_is_a_tooltip(chunk, m.start()):
            continue
        if LOG_RE.search(pre) or IMPORT_RE.search(pre):
            continue
        if COMPARE_RE.search(pre) or API_RE.search(pre) or CLASS_LIST_RE.search(pre):
            continue
        if enclosing_call_matches_any(chunk, m.start(), API_RE.pattern) or \
           enclosing_call_matches_any(chunk, m.start(), CLASS_LIST_RE.pattern):
            continue
        if KEY_POS_PRE_RE.search(pre) and KEY_POS_POST_RE.match(post):
            continue
        if is_css_declaration(pre, val) or CSS_STYLE_KEY_RE.search(pre):
            continue
        if is_union_member(pre, post, val):
            continue
        if enclosing_call_matches_any(chunk, m.start(), LOG_CALL_RE.pattern):
            continue
        if enclosing_call_matches_any(chunk, m.start(), IDENT_TABLE_RE.pattern):
            continue
        if channel_errors and enclosing_call_matches_any(chunk, m.start(), ERROR_ARG_RE.pattern):
            continue
        if channel_errors and enclosing_call_matches_any(chunk, m.start(), CHANNEL_POST_RE.pattern):
            continue
        if channel_errors and enclosing_function(chunk, m.start()) in CHANNEL_PROTOCOL_FNS:
            continue
        items.append((line_of(src, start + m.start()), ' '.join(val.split())))
    return items


def tag_len(text, i):
    """Length of the markup tag starting at text[i] == '<'. Honours quotes, regex literals
    and `{...}` attribute expressions (whose `=>` and `Record<string, x>` must not end the
    tag). A quote inside a regex — `title={v.replace(/'/, '')}` — is inert for the same
    reason it is inert in a comment: without it the quote pair leaves the tag and every
    attribute after it goes unseen."""
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
        elif c == '/' and depth > 0:
            # A regex inside an attribute expression. Only inside one: at depth 0 a `/` is
            # a closing tag or a self-closing slash, and the heuristic would not know.
            ln = regex_len(text, j) if regex_starts_here(text, j) else 0
            if ln:
                j += ln
                continue
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
    """Length of the `{...}` expression starting at text[i] == '{'. Honours quotes and regex
    literals, so an apostrophe inside a regex does not open a string that outlives the
    expression."""
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
        elif c == '/' and depth > 0 and regex_starts_here(text, j):
            ln = regex_len(text, j)
            if ln:
                j += ln
                continue
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


def template_items(src, start, end, scan_mustaches=True, channel_errors=False):
    text = re.sub(r'<!--.*?-->', lambda m: re.sub(r'[^\n]', ' ', m.group(0)),
                  src[start:end], flags=re.DOTALL)
    items = []
    i, n = 0, len(text)
    buf, buf_start = [], None
    svg_depth = 0
    code_depth = 0

    def flush():
        if buf and any(c.strip() for c in buf):
            node = ''.join(buf)
            word = node.strip()
            # Glyphs drawn inside an icon (`<svg><text>A</text></svg>`, the A→Z sort mark,
            # the `Aa` case toggle) are part of the picture; the button's own
            # `title`/`aria-label` carries the meaning, and that text is measured where it
            # is written. A whole `<svg>` subtree is therefore not a copy surface.
            if svg_depth:
                return
            # Inside `<code>` / `<kbd>` the bytes are a literal the reader copies — a git
            # URL, a flag, a protocol token — and translating one breaks the thing it spells.
            if code_depth:
                return
            # A single token with no space is a keycap, a ref name or a letter badge —
            # `esc`, `pgup`, `origin` — the same class as the `/` and `-` keys beside them.
            # Multi-word copy is untouched, which is what makes this safe to apply to text
            # nodes at all.
            if IDENT.match(word) and ' ' not in word:
                return
            if re.search(r'[A-Za-z]', node) and not ENTITY_ONLY.match(word):
                items.append((line_of(src, start + buf_start), ' '.join(node.split())))

    while i < n:
        ch = text[i]
        if ch == '<':
            ln = tag_len(text, i)
            if ln:
                flush()
                tag = text[i:i + ln].lower()
                if tag.startswith('<svg'):
                    svg_depth += 1
                elif tag.startswith('</svg'):
                    svg_depth = max(0, svg_depth - 1)
                elif re.match(r'<(?:code|kbd|samp)\b', tag):
                    code_depth += 1
                elif re.match(r'</(?:code|kbd|samp)\b', tag):
                    code_depth = max(0, code_depth - 1)
                buf, buf_start = [], None
                i += ln
                continue
        if ch == '{':
            flush()
            buf, buf_start = [], None
            ln = mustache_len(text, i)
            if scan_mustaches:
                items += script_literal_items(src, start + i, start + i + ln, channel_errors)
            i += max(ln, 1)
            continue
        if buf_start is None:
            buf_start = i
        buf.append(ch)
        i += 1
    flush()
    return items


# The extension-host channel tree, matched at the start of a path and inside one, so the
# counter reports the same way whether it is handed `src/lib/...` or an absolute path.
CHANNEL_TREE_RE = re.compile(r'(?:^|/)src/lib/extensions/')


# --- text attributes ---------------------------------------------------------
# `aria-label`, `alt`, `placeholder` and `title` are the attributes whose value the app
# renders as text, so a literal written into one is copy core owns. The rule is keyed on
# the attribute NAME: `aria-labelledby`/`aria-describedby` hold an element id and
# `aria-hidden`/`aria-expanded` a flag, so they are not text and are not in this list.
TEXT_ATTR_RE = re.compile(r'^(?:aria-label|alt|placeholder|title)$', re.IGNORECASE)
# The accessor a catalog key arrives through: `t(key, params)` from `$lib/i18n`. Matched
# against the text before an opening bracket, so `t(cond ? 'a.key' : 'b.key')` — a key
# chosen per frame — is a key too, and no copy.
CATALOG_CALL_RE = re.compile(r'\bt\s*$')
# A literal is what the attribute renders when the token before it opens the expression or
# chooses between rendered values. Anything else — `(`, `,`, `[`, an operator — is a key or
# an operand, and a literal there is not the attribute's text.
RENDERED_BEFORE_RE = re.compile(r'(?:\?|:|\|\||\?\?|&&|\+)\s*$')
# `label: 'Low'` is a property of an object someone looks up; `'a' : b` is a ternary
# branch. The glued colon is the difference, and an ambiguous `x : 'y'` reads as a branch
# — the direction that produces a finding rather than a silence.
PROPERTY_COLON_RE = re.compile(r"""[A-Za-z0-9_$\]}'"`]:\s*$""")
# …and a literal that continues into a comparison, or is followed by a glued colon, is a
# key rather than the value: `mode === 'rw'`, `{'staged': rows}`.
NOT_RENDERED_AFTER_RE = re.compile(r"^(?:\s*(?:===|!==|==|!=|<=|>=)|:)")
INTERP_RE = re.compile(r'\$\{[^{}]*\}')
LETTER_RE = re.compile(r'[A-Za-z]')

# Every literal still allowed in a text attribute, one entry per site, each with the
# reason it is not copy. A `placeholder` that is a format example is the answer's shape,
# not a sentence to translate (`frontend/src/lib/i18n/README.md`).
TEXT_ATTR_ALLOW: dict[tuple[str, str, str], str] = {
    ('src/lib/components/ConfigModal/ExtensionConfigFields.svelte', 'placeholder', '/host/path'):
        'a path shape to type, not a sentence to translate',
    ('src/lib/components/ConfigModal/ExtensionConfigFields.svelte', 'placeholder', '/container/path'):
        'a path shape to type, not a sentence to translate',
    ('src/lib/components/ConfigModal/ExtensionEditModal.svelte', 'placeholder', '/path/to/dependency/directory'):
        'a path shape to type, not a sentence to translate',
    ('src/lib/components/ConfigModal/ExtensionEditModal.svelte', 'placeholder', '/path/to/the/agent/folder'):
        'the fallback beside an extension\'s own suggestion: a path shape, not copy',
    ('src/lib/components/ConfigModal/ProviderCard.svelte', 'placeholder', 'http://localhost:8080/v1'):
        'an example URL — translating it teaches the wrong shape',
    ('src/lib/components/FileBrowser.svelte', 'placeholder', 'https://github.com/org/repo.git'):
        'an example URL, paired with the `git@…` example beside the field',
    ('src/lib/components/SandboxConfigModal/ImageSection.svelte', 'placeholder', 'docker.io/ubuntu:latest'):
        'an example image reference, identical in every locale',
    ('src/lib/components/SandboxConfigModal/MountsSection.svelte', 'placeholder', '/host/path'):
        'a path shape to type, not a sentence to translate',
    ('src/lib/components/SandboxConfigModal/MountsSection.svelte', 'placeholder', '/container/path'):
        'a path shape to type, not a sentence to translate',
}


def stale_allow_keys(seen):
    """TEXT_ATTR_ALLOW entries no scanned file matched. Only a whole-tree run has seen
    every site, so only that run may call an entry stale."""
    return sorted(set(TEXT_ATTR_ALLOW) - seen)


def allow_key(path, attr, value):
    """The TEXT_ATTR_ALLOW entry permitting this site, or None. Matched by path suffix so
    a file handed to `count_file` from any working directory is still itself."""
    norm = path.replace(os.sep, '/')
    for key in TEXT_ATTR_ALLOW:
        if key[1] == attr and key[2] == value and norm.endswith(key[0]):
            return key
    return None


def is_rendered_literal(chunk, start, end):
    """Is this literal one the attribute renders as text?

    `title={'Staged changes'}` and `title={open ? 'Collapse' : 'Expand'}` are. A key —
    `t('k')`, or `t(open ? 'a' : 'b')` — is not, nor is a comparison's operand
    (`mode === 'rw'`), an object key, or an argument of another function, which is a lookup
    key as often as it is copy. The copy belongs at the attribute itself."""
    if enclosing_call_is(chunk, start, CATALOG_CALL_RE.pattern):
        return False
    if NOT_RENDERED_AFTER_RE.match(chunk[end:]):
        return False
    pre = chunk[:start].rstrip()
    if not pre:
        return True
    if RENDERED_BEFORE_RE.search(pre):
        return not PROPERTY_COLON_RE.search(pre)
    return False


def rendered_literals(chunk):
    """(offset, value) for each literal `chunk` (an attribute's `{…}` expression, comments
    already blanked) renders as text. `''` is how a tooltip says "none this frame", and a
    literal with no letter outside its `${…}` holes (`' · '`) is glue, not copy."""
    for m in LIT_RE.finditer(chunk):
        val = next(g for g in m.groups() if g is not None)
        if not val.strip() or not LETTER_RE.search(INTERP_RE.sub(' ', val)):
            continue
        if is_rendered_literal(chunk, m.start(), m.end()):
            yield m.start(), val


def tag_attributes(tag):
    """Yield (name, kind, value, value_offset) for each attribute of `tag`, a tag string
    starting at its `<`. `kind` is 'static' (`="…"`) or 'expr' (`={…}`); a `{…spread}`, a
    bare flag and a bare word value are not text attributes and are skipped. The tag's own
    name is skipped by position, so `<titleish>` is never read as `title`."""
    n = len(tag)
    i = 1
    while i < n and not tag[i].isspace():
        i += 1
    while i < n:
        while i < n and tag[i] in ' \t\n\r':
            i += 1
        if i >= n or tag[i] in '>/':
            return
        if tag[i] == '{':
            i += max(mustache_len(tag, i), 1)
            continue
        j = i
        while j < n and tag[j] not in '= \t\n\r>/':
            j += 1
        name = tag[i:j]
        if j >= n or tag[j] != '=':
            i = j if j > i else i + 1
            continue
        k = j + 1
        while k < n and tag[k] in ' \t\n\r':
            k += 1
        if k < n and tag[k] in '"\'':
            quote, e = tag[k], k + 1
            while e < n and tag[e] != quote:
                e += 2 if tag[e] == '\\' else 1
            yield name, 'static', tag[k + 1:e], k + 1
            i = e + 1
            continue
        if k < n and tag[k] == '{':
            ln = mustache_len(tag, k)
            yield name, 'expr', tag[k + 1:k + ln - 1], k + 1
            i += max(ln, 1)
            continue
        e = k
        while e < n and tag[e] not in ' \t\n\r>':
            e += 1
        i = e


def text_attr_items(src, start, end, path):
    """(line, attribute, literal, allow-key-or-None) for every literal written into a text
    attribute in the template region `src[start:end]` of `path`."""
    # An HTML comment is blanked, not skipped: `tag_len` reads `<!--` as the start of a tag
    # and runs to the first `>`, so a comment that quotes one would swallow the markup after
    # it — as would a comment a prose `>` ends early. Blanking keeps every offset and line.
    text = re.sub(r'<!--.*?-->', lambda m: re.sub(r'[^\n]', ' ', m.group(0)),
                  src[start:end], flags=re.DOTALL)
    items = []
    i, n = 0, len(text)
    while i < n:
        ch = text[i]
        if ch == '{':
            i += max(mustache_len(text, i), 1)     # outside a tag, a mustache holds no attribute
            continue
        if ch != '<':
            i += 1
            continue
        ln = tag_len(text, i)
        if not ln:
            i += 1
            continue
        tag = text[i:i + ln]
        for name, kind, value, voff in tag_attributes(tag):
            if not TEXT_ATTR_RE.match(name):
                continue
            at = i + voff
            if kind == 'static':
                found = [(0, value)] if value.strip() and LETTER_RE.search(value) else []
            else:
                chunk = strip_comments(value)
                found = list(rendered_literals(chunk))
            for off, literal in found:
                items.append((line_of(src, start + at + off), name.strip().lower(), literal,
                              allow_key(path, name.strip().lower(), literal)))
        i += ln
    return items


def analyze_file(path):
    """(candidate literals, text-attribute literals) for one file. A text-attribute row is
    (line, attribute, literal, allow-key-or-None); the None rows are the findings."""
    src = open(path, encoding='utf-8').read()
    channel_errors = bool(CHANNEL_TREE_RE.search(path.replace(os.sep, '/')))
    items = []
    attrs = []
    if path.endswith('.svelte'):
        pos = 0
        for m in re.finditer(r'<(script|style)\b[^>]*>.*?</\1>', src, flags=re.DOTALL | re.IGNORECASE):
            items += template_items(src, pos, m.start())
            attrs += text_attr_items(src, pos, m.start(), path)
            if m.group(1).lower() == 'script':
                open_m = re.match(r'<script[^>]*>', m.group(0), flags=re.DOTALL)
                close_m = re.search(r'</script>\s*$', m.group(0), flags=re.DOTALL)
                if open_m and close_m:
                    items += script_literal_items(src, m.start() + open_m.end(),
                                                  m.start() + close_m.start(), channel_errors)
            pos = m.end()
        items += template_items(src, pos, len(src), channel_errors=channel_errors)
        attrs += text_attr_items(src, pos, len(src), path)
    else:
        items += script_literal_items(src, 0, len(src), channel_errors)
    items.sort()
    attrs.sort()
    return items, attrs


def count_file(path, verbose=False):
    """What one file owes: its candidate strings plus the text-attribute literals no
    TEXT_ATTR_ALLOW entry covers."""
    items, attrs = analyze_file(path)
    bad = [a for a in attrs if a[3] is None]
    if verbose:
        for ln, val in items:
            print(f'      {path}:{ln}  {val!r}')
        for ln, attr, val, _ in bad:
            print(f'      {path}:{ln}  {attr}={val!r}')
    return len(items) + len(bad)


SKIP_DIRS = {'__tests__', 'node_modules', '.svelte-kit', 'dist'}
# The catalog of record is English by design, and the runtime beside it speaks only
# to the console. Neither is an unconverted surface.
SKIP_PREFIXES = (os.path.join('src', 'lib', 'i18n') + os.sep,)


def owned_files():
    found = []
    for root, dirs, fs in os.walk('src'):
        dirs[:] = sorted(d for d in dirs if d not in SKIP_DIRS)
        for f in sorted(fs):
            if not f.endswith(('.svelte', '.ts')) or '.unit.' in f:
                continue
            p = os.path.join(root, f)
            if p.startswith(SKIP_PREFIXES):
                continue
            found.append(p)
    return found


def expand(paths):
    """Accept files and/or directories on the command line, in the order given."""
    out = []
    for p in paths:
        if os.path.isdir(p):
            out.extend(f for f in owned_files() if f.startswith(p.rstrip(os.sep) + os.sep))
        else:
            out.append(p)
    return out


def main():
    verbose = '-v' in sys.argv
    listed = expand([a for a in sys.argv[1:] if not a.startswith('-')])
    files = listed or owned_files()
    grand = 0
    attrs = 0
    nonzero = 0
    rows = []
    used = set()
    for f in files:
        if not os.path.exists(f):
            print(f'  MISSING  {f}')
            grand += 1  # a file a batch claims to own but does not have is a finding
            continue
        items, attr_items = analyze_file(f)
        bad = [a for a in attr_items if a[3] is None]
        used.update(a[3] for a in attr_items if a[3])
        n = len(items) + len(bad)
        grand += len(items)
        attrs += len(bad)
        nonzero += 1 if n else 0
        if verbose:
            for ln, val in items:
                print(f'      {f}:{ln}  {val!r}')
        for ln, attr, val, _ in bad:
            rows.append(f'{f}:{ln}  {attr}={val!r}')
        if n or listed:
            print(f'{n:4d}  {f}')
    for row in rows:
        print(f'  ATTR  {row}')
    # A site that moved or converted leaves its permission behind; the allowlist shrinks
    # with the tree, so a stale row is as much a finding as a new one. Only a whole-tree
    # run has seen every site, so a file-argument run checks none.
    stale = [] if listed else stale_allow_keys(used)
    for rel, attr, val in stale:
        print(f'  STALE  {rel}: {attr}={val!r} is allowed, but nothing there reads like '
              f'that any more — delete the entry')
    print('----')
    verb = 'is' if grand == 1 else 'are'
    print(f'{grand:4d}  candidate strings in {len(files)} files '
          f'({nonzero} file{"" if nonzero == 1 else "s"} unconverted)')
    print(f'{attrs:4d}  literal text attribute(s) outside the allowlist '
          f'({len(TEXT_ATTR_ALLOW)} allowed, each with its reason)')
    if grand or attrs or stale:
        print('Every one of these is user-facing text core still owns. Move it to a key '
              'in src/lib/i18n/en.ts and render it with t(); see frontend/src/lib/i18n/README.md.')
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
