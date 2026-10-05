#!/usr/bin/env python3
"""The two surfaces no other frontend gate measures: an export kept alive only by its own unit
test, and a CSS declaration copied into a second stylesheet.

    cd frontend && python3 dead-surface.py                   # both measurements, exit 1 on a finding
    cd frontend && python3 dead-surface.py -v                # + every retained seam and the biggest groups
    cd frontend && python3 dead-surface.py --update-ceiling  # re-freeze measurement 2 after consolidating

`npm run check:dead-surface` runs this, and `make test-frontend` runs it beside its self-test,
`dead-surface_test.py`. `docs/testing.md → The dead-surface gate` owns the rules; what follows is the
shortest version of each.

Measurement 1 — an `export`ed declaration in a production module that no other production file names
and at least one unit file names exists only so a test can reach an internal. That is a seam, and a
retained seam says so on its declaration:

    /** @knipignore The coalescing tests measure this window; nothing in the app reads it. */
    export const MIRROR_COALESCE_MS = 1500;

The tag sits on the declaration, so deleting the symbol deletes the permission and there is no
allowlist file to forget. knip cannot see the seam: an ignored test file still counts as a user of
what it imports. Naming is textual — an identifier anywhere outside a comment, a quoted string or a
`<style>` block — so a name that only appears in prose or a fixture string is not a user.

Measurement 2 — how many CSS declaration lines are written in more than one stylesheet or `<style>`
block, counting every copy past the first, with comments, whitespace, string literals and colours
normalized away and a custom property keeping its name. Scoped blocks repeat tokens legitimately, so
this number is frozen as `CSS_CEILING` and only rising above it fails.
"""
import os
import re
import sys

SRC = 'src'
SKIP_DIRS = {'node_modules', '.svelte-kit', 'dist'}
SCRIPT_SUFFIXES = ('.ts', '.svelte')
# A barrel's exports are re-exports by design; the catalog of record exports the whole catalog.
BARREL_RE = re.compile(r'(?:^|/)index\.ts$')
CATALOG = 'src/lib/i18n/en.ts'
UNIT_RE = re.compile(r'\.unit\.[^/]*$')
TEST_DIR = '/__tests__/'

# Measured with `python3 dead-surface.py --update-ceiling`. It only ever goes down; raising it is a
# decision a person makes in a commit message, so the constant is the register.
CSS_CEILING = 8062

DECL_RE = re.compile(
    r'^[ \t]*export[ \t]+(?:async[ \t]+)?'
    r'(?P<kind>function|const|let|class|type|interface|enum)\b(?P<rest>[^\n]*)',
    re.MULTILINE)
NAME_AFTER_KIND = re.compile(r'^\s*\*?\s*(?P<name>[A-Za-z_$][\w$]*)')
TOKEN_RE = re.compile(r'[A-Za-z_$][\w$]*')
SCRIPT_BLOCK_RE = re.compile(r'<script\b[^>]*>(.*?)</script>', re.DOTALL | re.IGNORECASE)
STYLE_BLOCK_RE = re.compile(r'<style\b[^>]*>(.*?)</style>', re.DOTALL | re.IGNORECASE)
CSS_STRING_RE = re.compile(r'"(?:[^"\\]|\\.)*"|\'(?:[^\'\\]|\\.)*\'')
CSS_HEX_RE = re.compile(r'#[0-9a-fA-F]{3,8}\b')
PROPERTY_HEAD_RE = re.compile(r'^(?:--[A-Za-z0-9_-]+|[A-Za-z][A-Za-z0-9-]*)$')
# The single-word CSS properties, for the head test below: a hyphen or `--` is enough on its own, and
# every property the tree writes is either of those or one of these.
SINGLE_WORD_PROPERTY = frozenset("""
all animation appearance background border bottom clear clip color columns container content cursor
direction display filter flex float font gap grid height inset isolation left margin mask opacity
order outline overflow padding perspective position resize right rotate scale top transform
transition translate visibility width widows zoom
""".split())
TAG = '@knipignore'


def read(path):
    with open(path, encoding='utf-8') as fh:
        return fh.read()


# --- reading a source file ---------------------------------------------------
def regex_starts_here(text, i):
    """Is the `/` at `i` a regex literal rather than a division? The usual heuristic, biased toward
    division. Wrong only in the direction that leaves a comment visible to the scan."""
    j = i - 1
    while j >= 0 and text[j] in ' \t':
        j -= 1
    prev = text[j] if j >= 0 else ''
    if prev == '':
        return True
    if prev == '{':
        return False          # `{/if}` — a Svelte block closer, not a regex
    return not re.match(r"[A-Za-z0-9_$)\]}'\"`]", prev)


def regex_len(text, i):
    """Length of the regex literal at `text[i] == '/'`, or 0 when it does not close on its line."""
    n, j = len(text), i + 1
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


def blank_comments(text):
    """Blank out `//`, `/* */` and `<!-- -->`, keeping length and line numbers.

    Tracks string state so a `//` inside a literal survives, and blanks regex literals so a quote
    inside one (`/'/`) cannot open a fake string that swallows the rest of the file.
    """
    out = []
    i, n, quote = 0, len(text), ''
    while i < n:
        c = text[i]
        if quote:
            out.append(c)
            if c == '\\':
                out.append(text[i + 1] if i + 1 < n else '')
                i += 2
                continue
            if c == quote:
                quote = ''
            i += 1
            continue
        if c == '/' and text.startswith('//', i):
            while i < n and text[i] != '\n':
                out.append(' ')
                i += 1
            continue
        if c == '/' and text.startswith('/*', i):
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
        if c == '<' and text.startswith('<!--', i):
            end = text.find('-->', i + 4)
            end = n if end == -1 else end + 3
            out.append(''.join('\n' if ch == '\n' else ' ' for ch in text[i:end]))
            i = end
            continue
        if c in '"\'':
            quote = c
        out.append(c)
        i += 1
    return ''.join(out)


def blank_quoted(text):
    """Blank the body of `'…'` and `"…"` literals. A backtick literal is left whole: its `${…}` holes
    hold real uses, and telling a hole from prose is more machinery than the answer is worth."""
    out = []
    i, n, quote = 0, len(text), ''
    while i < n:
        c = text[i]
        if quote:
            if c == '\\':
                out.append('  ')
                i += 2
                continue
            if c == quote:
                quote = ''
                out.append(c)
            else:
                out.append('\n' if c == '\n' else ' ')
            i += 1
            continue
        out.append(c)
        if c in '"\'':
            quote = c
        i += 1
    return ''.join(out)


def code_view(rel, src):
    """What a *use* of a name looks like: comments and quoted strings blanked, and for a `.svelte`
    file every `<style>` block blanked too."""
    text = blank_quoted(blank_comments(src))
    if rel.endswith('.svelte'):
        text = STYLE_BLOCK_RE.sub(lambda m: '\n' * m.group(1).count('\n'), text)
    return text


def script_view(src):
    """The `<script>` bodies of a `.svelte` file at their real offsets, everything else blanked, so a
    declaration is read only where one can be written and its line number stays the file's."""
    padded = ['\n' if ch == '\n' else ' ' for ch in src]
    for m in SCRIPT_BLOCK_RE.finditer(src):
        for k, ch in enumerate(m.group(1)):
            pos = m.start(1) + k
            if pos < len(padded):
                padded[pos] = ch
    return ''.join(padded)


def is_test_file(rel):
    """Is this the unit side of the seam rule — a `*.unit.*` module, or anything under `__tests__/`?"""
    return bool(UNIT_RE.search(rel)) or TEST_DIR in rel


def classify(root):
    """(measured production files, every production file, unit files) under `root/src`.

    The middle list is a superset of the first: a barrel and the English catalog are never measured —
    a re-export and a catalog entry are the file's job — but they *name* what they carry, so a symbol
    the app reaches through a barrel is not a test-only seam.
    """
    measured, namers, unit = [], [], []
    for dirpath, dirs, files in os.walk(os.path.join(root, SRC)):
        dirs[:] = sorted(d for d in dirs if d not in SKIP_DIRS)
        for f in sorted(files):
            if not f.endswith(SCRIPT_SUFFIXES):
                continue
            rel = os.path.relpath(os.path.join(dirpath, f), root).replace(os.sep, '/')
            if is_test_file(rel):
                unit.append(rel)
                continue
            namers.append(rel)
            if not BARREL_RE.search(rel) and rel != CATALOG:
                measured.append(rel)
    return measured, namers, unit


class Decl:
    """One exported declaration: the site its finding prints, and whether the comment attached to the
    declaration carries the tag."""

    def __init__(self, path, name, line, tagged):
        self.path, self.name, self.line, self.tagged = path, name, line, tagged

    def __str__(self):
        return f'{self.path}:{self.line}  {self.name}'


def attached_comment(src, export_offset):
    """The comment attached to an `export`: the block comment ending on the line above it, any `//`
    lines above that, and the rest of the `export`'s own line. One blank line may sit between, which is
    what keeps a file's own doc comment from licensing the first declaration under it."""
    head = src[:export_offset]
    own = src[export_offset:]
    own = own[:own.find('\n')] if '\n' in own else own
    before = head[:head.rfind('\n') + 1].rstrip()
    gap = head[len(before):].count('\n')
    region, lines = own, before.split('\n')
    if before.endswith('*/') and gap <= 2:
        start = before.rfind('/*')
        if start != -1:
            region = before[start:] + '\n' + region
            lines = before[:start].split('\n')
    blanks = 0
    for line in reversed(lines):
        stripped = line.strip()
        if not stripped:
            blanks += 1
            if blanks > 1:
                break
            continue
        if stripped.startswith('//'):
            region = stripped + '\n' + region
            blanks = 0
        else:
            break
    return region


def declarations(rel, src):
    """Every measured `export`ed declaration in `rel`, with its line and whether its attached comment
    carries `@knipignore`."""
    body = script_view(src) if rel.endswith('.svelte') else src
    found = []
    for m in DECL_RE.finditer(body):
        named = NAME_AFTER_KIND.match(m.group('rest'))
        if not named:
            continue                       # `export type { X } from '…'`, `export const { a } = …`
        name = named.group('name')
        line = src.count('\n', 0, m.start()) + 1
        found.append(Decl(rel, name, line, TAG in attached_comment(src, m.start())))
    return found


def names_used(files, sources):
    """Per file, the identifiers it uses — read from `code_view`, so a comment or a quoted string
    never counts."""
    return {rel: set(TOKEN_RE.findall(code_view(rel, sources[rel]))) for rel in files}


def measure_seams(root):
    """(untagged test-only exports, tagged seams, exports nothing names) for the tree at `root`.

    An export is test-only when no *other* production file names it and at least one unit file does.
    """
    measured, namers, unit = classify(root)
    sources = {rel: read(os.path.join(root, rel)) for rel in namers + unit}
    prod_names = names_used(namers, sources)
    unit_names = names_used(unit, sources)
    untagged, seams, unnamed = [], [], []
    for rel in measured:
        for decl in declarations(rel, sources[rel]):
            if any(decl.name in seen for other, seen in prod_names.items() if other != rel):
                continue
            if not any(decl.name in seen for seen in unit_names.values()):
                unnamed.append(decl)
            elif decl.tagged:
                seams.append(decl)
            else:
                untagged.append(decl)
    return untagged, seams, unnamed


# --- measurement 2: duplicated CSS declarations ------------------------------
def normalize_decl(run):
    """One declaration run to the text two copies of it share: whitespace and the trailing `;` gone,
    lowercased, a string literal `S`, a hex colour `I`.

    A custom property keeps its name — two different tokens are two different rules."""
    body = re.sub(r'\s+', ' ', run).strip().rstrip(';').strip()
    body = CSS_STRING_RE.sub('S', body)
    body = CSS_HEX_RE.sub('I', body)
    return re.sub(r'\s*:\s*', ':', body).lower()


def is_declaration(run):
    """Is this run a declaration rather than a selector or an at-rule prelude?

    A `;` guarantees a declaration; a run ending at a brace has no such promise, so its head must be a
    property name. `.a:hover` and `button:focus` end at `{`, and reading either as a declaration would
    count two selectors that merely match the same elements as one copied rule.
    """
    head, sep, value = run.partition(':')
    if not sep or not value.strip():
        return False
    head = head.strip()
    if not PROPERTY_HEAD_RE.match(head):
        return False
    return '-' in head or head.lower() in SINGLE_WORD_PROPERTY


def declarations_in(text):
    """(line, normalized declaration) for every declaration run inside a rule in `text`.

    A run is the text up to a `;`, or up to the brace that closes it when the author left the last one
    unsemicolon'd, so a declaration wrapped over three lines is one run. A custom-property definition
    (`--row-height: 24px;`) is a run, because two copies of it are two copies of a token.
    """
    out = []
    depth = parens = 0
    buf, buf_line = '', None
    line, quote = 1, ''
    i, n = 0, len(text)
    while i < n:
        c = text[i]
        i += 1
        if quote:
            buf += c
            if c == '\\':
                nxt = text[i] if i < n else ''
                buf += nxt
                i += 1
                if nxt == '\n':
                    line += 1
            elif c == quote:
                quote = ''
            elif c == '\n':
                line += 1
            continue
        if c == '\n':
            line += 1
            buf += ' '
            continue
        if c in '"\'':
            quote = c
        elif c == '(':
            parens += 1
        elif c == ')' and parens:
            parens -= 1
        elif c == '{':
            if depth > 0 and is_declaration(buf):
                out.append((buf_line or line, normalize_decl(buf)))
            depth += 1
            buf, buf_line = '', None
            continue
        elif c == '}':
            if depth > 0 and is_declaration(buf):
                out.append((buf_line or line, normalize_decl(buf)))
            depth = max(0, depth - 1)
            buf, buf_line = '', None
            continue
        elif c == ';' and not parens:
            if depth > 0 and is_declaration(buf):
                out.append((buf_line or line, normalize_decl(buf)))
            buf, buf_line = '', None
            continue
        if buf_line is None and c not in ' \t':
            buf_line = line
        buf += c
    return out


def css_blocks(root):
    """(block label, line offset, text) for every stylesheet and every `<style>` block under `root/src`.

    A `.svelte` file's style block is its own block: two components repeating one rule is exactly the
    duplication being measured. A test probe's style is not the product's CSS, so it is not measured."""
    blocks = []
    for dirpath, dirs, files in os.walk(os.path.join(root, SRC)):
        dirs[:] = sorted(d for d in dirs if d not in SKIP_DIRS)
        for f in sorted(files):
            rel = os.path.relpath(os.path.join(dirpath, f), root).replace(os.sep, '/')
            if is_test_file(rel):
                continue
            if f.endswith('.css'):
                blocks.append((rel, 0, blank_comments(read(os.path.join(root, rel)))))
            elif f.endswith('.svelte'):
                src = read(os.path.join(root, rel))
                for m in STYLE_BLOCK_RE.finditer(src):
                    blocks.append((rel, src.count('\n', 0, m.start(1)),
                                   blank_comments(m.group(1))))
    return blocks


def measure_css(root):
    """(duplicated declaration lines, groups) for the tree at `root`.

    A group is (normalized declaration, occurrences, distinct blocks, first two `file:line`), largest
    first."""
    sites = {}
    for label, offset, text in css_blocks(root):
        for line, decl in declarations_in(text):
            sites.setdefault(decl, []).append((label, offset + line))
    total, groups = 0, []
    for decl, where in sites.items():
        blocks = {label for label, _ in where}
        if len(blocks) < 2:
            continue
        total += len(where) - 1
        groups.append((decl, len(where), len(blocks), where[:2]))
    groups.sort(key=lambda g: (-g[1], g[0]))
    return total, groups


def update_ceiling(path, value):
    """Rewrite the `CSS_CEILING` constant in the script at `path` to `value`."""
    src = read(path)
    new, count = re.subn(r'(?m)^CSS_CEILING = \d+$', f'CSS_CEILING = {value}', src, count=1)
    if not count:
        raise SystemExit(f'dead-surface: no `CSS_CEILING = <n>` line in {path} to rewrite')
    with open(path, 'w', encoding='utf-8') as fh:
        fh.write(new)


def main(argv, root='.'):
    verbose = '-v' in argv
    recount = '--update-ceiling' in argv
    untagged, seams, unnamed = measure_seams(root)
    dupes, groups = measure_css(root)
    blocks = len(css_blocks(root))

    for d in untagged:
        print(f'  SEAM  {d}  — no other production file names it and a unit file does: either '
              f'un-export it, or tag the declaration with @knipignore and its one-line reason')
    if verbose:
        for d in seams:
            print(f'   seam  {d}')
    print('----')
    print(f'{len(untagged):5d}  untagged test-only exports (must be 0)')
    print(f'{len(seams):5d}  tagged seams, each with its reason')
    print(f'{len(unnamed):5d}  exports nothing in src/ names at all — they reach a consumer through '
          f'a signature, which is check:dead\'s question')
    print(f'{dupes:5d}  duplicated CSS declaration lines across {blocks} stylesheets and <style> '
          f'blocks (ceiling {CSS_CEILING})')
    if verbose:
        for decl, count, block_count, where in groups[:15]:
            print(f'    dup   {count:4d}x in {block_count:3d} blocks  {decl}   '
                  + ', '.join(f'{f}:{l}' for f, l in where))
        if len(groups) > 15:
            print(f'    … {len(groups) - 15} more groups')

    if recount:
        update_ceiling(os.path.abspath(__file__), dupes)
        print(f'ceiling re-frozen at {dupes}')
        return 1 if untagged else 0

    failed = bool(untagged)
    if dupes > CSS_CEILING:
        print(f'  DUP   the duplicated-declaration count rose from {CSS_CEILING} to {dupes}. A scoped '
              f'<style> block repeating a token set is legal, so this is a ceiling and not a zero — '
              f'but a rise is a new copy. Point the rule at a shared class, or re-freeze with '
              f'`python3 dead-surface.py --update-ceiling` and say why in the commit.')
        failed = True
    return 1 if failed else 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
