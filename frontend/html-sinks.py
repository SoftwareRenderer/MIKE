#!/usr/bin/env python3
"""Every place core turns a string into HTML, or hands the browser a URL or a style
declaration, listed with the mechanism that keeps it safe.

Seven rules: a sink must be listed, must not render a translation, must still hold the
sink it was listed for, and must declare a method that is still in force; every URL
binding, dynamic tag name and style interpolation must be one this app wrote.
`docs/markup-sanitizing.md` owns what each rule does, the mechanism vocabulary the
tables use, and what the scan cannot see.

    cd frontend && python3 html-sinks.py     # exit 0 = clean, 1 = violations listed
"""

import re
import sys
from pathlib import Path

FRONTEND = Path(__file__).parent
SRC = FRONTEND / "src"

SINK_RE = re.compile(r"\.innerHTML\b|\.outerHTML\b|insertAdjacentHTML\s*\(|\{@html\b")
TRANSLATION_RE = re.compile(r"(?<![A-Za-z0-9_$.])(?:t|packText)\s*\(|i18n\s*\.\s*t\s*\(")

# Loaded by the browser but living outside src/, so the scan names its real scope.
OUTSIDE_SRC = ("index.html", "public/host-client.js")

# Every file in core that turns a string into elements, with the reason it is safe.
ALLOW: dict[str, str] = {
    "src/lib/components/Chat/ChatMessage.svelte": "escapeHtml on the user's own text (assistant markdown renders in Chat/MarkdownBody)",
    "src/lib/components/Chat/MarkdownBody.svelte": "utils/sanitize over marked (assistant markdown; a top-level fence renders as Chat/CodeFence or Chat/DiagramFence instead)",
    "src/lib/components/Chat/ToolCall.svelte": "utils/sanitize over marked (question, summary, tool result)",
    "src/lib/components/ConfigModal/AgentSection.svelte": "utils/sanitize over marked (manifest description/setup)",
    "src/lib/utils/lsp-hover-actions.ts": "utils/sanitize with the hr tag forbidden, over the LSP plugin's docToHTML",
    "src/lib/components/DiffViewer.svelte": "highlighter output — text escaped, classes from a fixed list",
    "src/lib/components/CommitDiffOverlay.svelte": "highlighter output — text escaped, classes from a fixed list",
    "src/lib/components/FileSearchPanel.svelte": "escapeHtml on the line and on every match",
    "src/lib/components/Icon.svelte": "singleSvgMarkup — a lone sanitized <svg> root, from an *.svg?raw import",
    "src/lib/components/ConfigModal.svelte": "{@html mikeLogo} — an *.svg?raw import",
    "src/lib/components/TerminalPanel.svelte": "two literal SVG path strings on created <svg> elements",
    "src/lib/utils/sticky-scroll.ts": "clones CodeMirror's own line DOM; the fallback is escapeHtml",
    "src/lib/extensions/channel.ts": "innerHTML = '' — a clear, not an injection",
    "src/lib/utils/sanitize.ts": "the sanitizer every `sanitizer` entry above points at; its own outerHTML read is off DOMPurify's sanitized DOM",
}

# How each file above keeps untrusted text from becoming markup. See rule 4.
METHOD: dict[str, str] = {
    "src/lib/components/Chat/ChatMessage.svelte": "escaped-by:lib/utils/format",
    "src/lib/components/Chat/MarkdownBody.svelte": "sanitizer",
    "src/lib/components/Chat/ToolCall.svelte": "sanitizer",
    "src/lib/components/ConfigModal/AgentSection.svelte": "sanitizer",
    "src/lib/utils/lsp-hover-actions.ts": "sanitizer",
    "src/lib/components/DiffViewer.svelte": "escaped-by:lib/highlighter",
    "src/lib/components/CommitDiffOverlay.svelte": "escaped-by:lib/highlighter",
    "src/lib/components/FileSearchPanel.svelte": "escaped-by:lib/utils/format",
    "src/lib/components/Icon.svelte": "guard:singleSvgMarkup",
    "src/lib/components/ConfigModal.svelte": "literal",
    "src/lib/components/TerminalPanel.svelte": "literal",
    "src/lib/utils/sticky-scroll.ts": "escaped-by:lib/utils/format",
    "src/lib/extensions/channel.ts": "literal",
    "src/lib/utils/sanitize.ts": "guard:sanitizeHTML",
}

# The one module a `sanitizer` method points at; rule 4 requires the exact import.
SANITIZER_MODULE = "$lib/utils/sanitize"

# --- rules 5 to 7 ---------------------------------------------------------------

URL_ATTR = r"href|src|srcdoc|action|formaction|poster|xlink:href"
URL_BIND_RE = re.compile(r"(?<![\w$-])(?:" + URL_ATTR + r")\s*=\s*\{")
URL_SET_RE = re.compile(r"""setAttribute\s*\(\s*['"](""" + URL_ATTR + r""")['"]\s*,""")
# An asset import is a file the build chose, so it cannot be a URL someone else wrote.
ASSET_IMPORT_RE = re.compile(
    r"import\s+([A-Za-z_$][\w$]*)\s+from\s*['\"][^'\"]*(?:\?raw|\?url|/assets/)[^'\"]*['\"]"
)
# The guards rule 5 accepts, each with the module that declares it and a marker present
# only while the guard still checks the scheme.
URL_GUARDS: dict[str, tuple[str, str]] = {
    "safeUrl": ("lib/utils/safe-url.ts", "u.protocol"),
    "imageSrc": ("lib/utils/attachments.ts", "DATA_URL_SCHEME"),
}
# What `encoded-by` accepts as proof that a builder escapes what it interpolates.
ENCODING_MARKERS = ("encodeURIComponent", "URLSearchParams")

# Every URL binding neither an asset import nor a guard explains, keyed by (file, expression),
# each declared as `mechanism — reason`. `literal` is the one form no script can confirm.
URL_SINKS: dict[tuple[str, str], str] = {
    ("src/lib/components/App/Sidebar.svelte", "p.cloning ? undefined : projectHref(p)"):
        "encoded-by:lib/utils/app-link — project and task params are percent-encoded",
    ("src/lib/components/Card.svelte", "cardHref"):
        "contains:encodeURIComponent — built in this file from encoded params",
    ("src/lib/components/App/TopBar.svelte", "explorerHref"):
        "contains:encodeURIComponent — this file builds the query",
    ("src/lib/components/App/TopBar.svelte", "terminalHref"):
        "encoded-by:lib/utils/app-link — projectTerminalHref",
    ("src/lib/components/CardDetail/TaskHeader.svelte", "explorerHref"):
        "contains:encodeURIComponent — this file builds the query",
    ("src/lib/components/CardDetail/TaskHeader.svelte", "terminalHref"):
        "encoded-by:lib/utils/app-link — taskTerminalHref",
    ("src/lib/components/ExplorerHeader.svelte", "terminalHref"):
        "literal — a prop; ExplorerView passes projectTerminalHref (app-link encodes)",
    ("src/lib/components/Chat/ToolCall.svelte", "iconInfo.src"):
        "literal — the tool-icon table maps tool names to ?raw imports, and Icon.svelte "
        "refuses anything that is not a lone sanitized <svg> root",
    ("src/lib/components/EmptyState.svelte", "icon"):
        "literal — a prop; every caller passes an *.svg?raw import, and the value reaches the "
        "browser through Icon.svelte, which refuses anything that is not a lone sanitized <svg> root",
    ("src/lib/components/ImageDiffModal.svelte", "url"):
        "encoded-by:lib/api/fs — rawFileURL builds the query with URLSearchParams",
    ("src/lib/components/Chat/DiagramFence.svelte", "diagram.imageSrc"):
        "literal — lib/utils/diagram.ts prefixes the markup that svgImageMarkup accepted, so it is never "
        "text a message wrote. __tests__/diagram.unit.ts pins that the bytes it decodes to are that markup",
    ("src/lib/services/ui-injector.svelte.ts", "setAttribute(src)"):
        "encoded-by:lib/services/ui-injector.svelte — extBaseURL re-encodes every segment of "
        "the served path and refuses one that leaves /extensions/",
}

DYNAMIC_TAG_RE = re.compile(r"<svelte:element\s+this\s*=\s*\{")
STYLE_ATTR_RE = re.compile(r"(?<![\w$-])style\s*=\s*([\"'])")
STYLE_DIRECTIVE_RE = re.compile(r"(?<![\w$-])style:[A-Za-z-]+\s*=\s*\{")
CSS_TEXT_RE = re.compile(r"\.style\.(?:cssText|setProperty)[^;\n]*\$\{")
# A unit right after the `}` leaves the number in front of it as the only possible value.
CSS_UNITS = r"(?:px|%|em|rem|vh|vw|vmin|vmax|deg|grad|turn|fr|ch|ex|pt|pc)"
CSS_UNIT_RE = re.compile(rf"^(?:{CSS_UNITS})(?![\w-])")
# In a `style:prop={…}` value the unit travels with the value, so it is matched where only a
# unit can be. An unquoted `em` inside a word is an identifier.
CSS_UNIT_ANY_RE = re.compile(rf"(?<=[\d.}}\)]){CSS_UNITS}(?![\w])|['\"]{CSS_UNITS}['\"]")

# Every style interpolation the unit rule does not explain, keyed by (file, expression).
STYLE_SINKS: dict[tuple[str, str], str] = {
    ("src/lib/components/Icon.svelte", "safeColor"):
        "contains:safeCSSValue — the color prop passes safeCSSValue, which refuses a value "
        "carrying a `;` or a url()",
    ("src/lib/components/Column.svelte", "color"):
        "literal — Board's four column colors, each a var(--color-column-…)",
    ("src/lib/components/BaseModal.svelte", "zIndex"):
        "contains:constants/z-layers — the overlay's depth is a tier constant, this file's default "
        "being the modal tier and every caller in core passing one",
    ("src/lib/components/Chat/ModelSelector.svelte", "Z.overlay"):
        "contains:constants/z-layers — a tier constant this file imports, not text a caller supplies",
    ("src/lib/components/FileMentionDropdown.svelte", "Z.overlay"):
        "contains:constants/z-layers — a tier constant this file imports, not text a caller supplies",
    ("src/lib/components/Card.svelte", "subtaskProgress.pct * 100"):
        "literal — a ratio this file computes",
    ("src/lib/components/Card.svelte", "modelBadge.color"):
        "contains:getModelColor — a hash into MODEL_COLORS, or a literal in this file",
    ("src/lib/components/Card.svelte", "statusBadge.color"):
        "contains:--color-status — statusBadge returns tokens or literals from this file",
    ("src/lib/components/Card.svelte",
     "nextStatus === 'done' && hasChanges ? '#EAB308' : statusColors[nextStatus]"):
        "contains:statusColors — a literal or a lookup in this file's fixed map",
    ("src/lib/components/CardDetail/BoardOverview.svelte", "getColumnColor(section.id)"):
        "contains:var(-- — the column colors are tokens declared in this file",
    ("src/lib/components/CardDetail/TaskHeader.svelte", "badge.color"):
        "literal — a prop; the badge color is one of the status literals",
    ("src/lib/components/Chat/ContextDonut.svelte", "STROKE"):
        "contains:# — STROKE is a literal in this file",
    ("src/lib/components/Chat/ToolCall.svelte",
     "tc.isPending ? 'var(--color-accent)' : tc.interrupted ? 'var(--color-status-yellow)' "
     ": (tc.success ? 'var(--color-status-green)' : 'var(--color-status-red)')"):
        "literal — four tokens written in the template itself",
    ("src/lib/components/BranchSymbols.svelte", "getSymbolColor(item.symbol.kind)"):
        "contains:var(-- — getSymbolColor maps a fixed LSP kind onto a token",
    ("src/lib/components/BranchSymbols.svelte", "getSymbolColor(symbol.kind)"):
        "contains:var(-- — getSymbolColor maps a fixed LSP kind onto a token",
    ("src/lib/components/GitStatusPanel.svelte", "getStatusBadge(row).color"):
        "hex-colors:self — getStatusBadge in this file returns a hex color per status",
    ("src/lib/components/DiffViewer.svelte", "badge.color"):
        "hex-colors:lib/utils/diff-parser — getStatusBadge there returns a hex color per status",
    ("src/lib/components/BaseModal.svelte", "width"):
        "contains:'640px' — a CSS width a caller chooses; this file's default is a literal, "
        "and every caller in core passes one",
    ("src/lib/components/CardDetail.svelte", "boardWidth"):
        "contains:.toFixed(1)}% — a percentage string this file builds",
    ("src/lib/components/CardDetail.svelte", "chatWidth"):
        "contains:.toFixed(1)}% — a percentage string this file builds",
    ("src/lib/components/CardDetail.svelte", "diffWidth"):
        "contains:.toFixed(1)}% — a percentage string this file builds",
}


def core_files(src: Path):
    """Every core file as (report path, file); report paths are relative to `frontend/`."""
    root = src.parent
    for path in sorted(src.rglob("*")):
        if not path.is_file() or path.suffix not in (".svelte", ".ts", ".js"):
            continue
        rel = path.relative_to(src).as_posix()
        if "__tests__/" in f"/{rel}" or ".unit." in path.name:
            continue
        yield f"src/{rel}", path
    for name in OUTSIDE_SRC:
        path = root / name
        if path.is_file():
            yield name, path


def _read(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):  # a binary that slipped into src/
        return ""


def _imports(body: str, module_last_segment: str) -> bool:
    """Whether the file imports from a module whose last path segment matches."""
    return re.search(
        r"from\s*['\"][^'\"]*/" + re.escape(module_last_segment) + r"['\"]", body
    ) is not None


def _line_of(body: str, index: int) -> int:
    return body.count("\n", 0, index) + 1


def _braced(body: str, brace_at: int) -> tuple[str, int]:
    """The `{…}` expression starting at `brace_at`, and the index just past it.

    An unbalanced brace returns ("", brace_at): the caller cannot read the expression and
    must declare the site.
    """
    depth = 0
    for i in range(brace_at, len(body)):
        c = body[i]
        if c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0:
                return body[brace_at + 1:i], i + 1
    return "", brace_at


def _sink_statement(body: str, at: int) -> str:
    """The statement a sink match belongs to — what rule 2 reads instead of one line.

    A template may spread a `{@html …}` expression over lines; a JS assignment ends at the
    newline.
    """
    if body.startswith("{@html", at):
        expr, _ = _braced(body, at)
        return expr
    end = body.find("\n", at)
    return body[at:end if end != -1 else len(body)]


def _source_line(body: str, index: int) -> str:
    start = body.rfind("\n", 0, index) + 1
    end = body.find("\n", index)
    return body[start:end if end != -1 else len(body)]


def check_method(rel: str, method: str, body: str, src: Path) -> str | None:
    """Why this file's declared method is not actually in force, or None."""
    if method == "literal":
        return None
    if method == "sanitizer":
        # The import statement, not the bare word: prose about the sanitizer is not a claim.
        if re.search(r"from\s*['\"]" + re.escape(SANITIZER_MODULE) + r"['\"]", body):
            # Importing DOMPurify beside it means a second, looser configuration is in use.
            if re.search(r"from\s*['\"]dompurify['\"]", body):
                return "imports DOMPurify directly beside the shared sanitizer — a second, looser configuration"
            return None
        return f"claims `sanitizer` but does not import {SANITIZER_MODULE}"
    if method.startswith("guard:"):
        ident = method[len("guard:"):]
        if re.search(r"\b" + re.escape(ident) + r"\b", body):
            return None
        return f"claims `guard:{ident}` but that check is no longer in the file"
    if method.startswith("escaped-by:"):
        module = method[len("escaped-by:"):]
        if not _imports(body, module.rsplit("/", 1)[-1]):
            return f"claims `escaped-by:{module}` but no longer imports it"
        why = _module_marker(module, "escapeHtml", src)
        if why:
            return f"claims `escaped-by:{module}` but {why}"
        return None
    return f"unknown method {method!r} — rule 4 lists sanitizer/guard:/escaped-by:/literal"


def _module_text(module: str, src: Path) -> str | None:
    """The body of a module under src/, or None when there is no such module. The declared
    path may name the extension or leave it off."""
    for name in (module, *(module + s for s in (".ts", ".js", ".svelte"))):
        helper = src / name
        if helper.is_file():
            return _read(helper)
    return None


def _module_marker(module: str, marker: str, src: Path) -> str | None:
    """Why `module` under src/ does not contain `marker`, or None."""
    for suffix in (".ts", ".js", ".svelte"):
        helper = src / (module + suffix)
        if helper.exists():
            if marker in _read(helper):
                return None
            return f"{module}{suffix} no longer contains {marker!r}"
    return f"{module} does not exist under src/"


def _decl(spec: str) -> tuple[str, str]:
    """A declaration is written `mechanism — reason`; this is where the two are split."""
    mech, sep, reason = spec.partition(" — ")
    return mech.strip(), (reason.strip() if sep else "")


def _in_comment(body: str, index: int) -> bool:
    """Whether a match sits in a comment — prose about a sink is not a sink."""
    start = body.rfind("\n", 0, index) + 1
    return body[start:index].lstrip().startswith(("//", "*", "/*", "<!--"))


def check_declared(mech: str, body: str, src: Path) -> str | None:
    """Why a rule 5/7 declaration's mechanism no longer holds, or None.

    Forms: `literal`, `contains:<string>`, `encoded-by:<module>`, `escaped-by:<module>`,
    `guard:<Ident>`, `hex-colors:<module|self>` — each one checkable from the tree.
    """
    kind, _, arg = mech.partition(":")
    if kind == "literal":
        return None
    if kind == "contains":
        if arg in body:
            return None
        return f"the declaration's marker {arg!r} is no longer in the file"
    if kind == "encoded-by":
        for marker in ENCODING_MARKERS:
            if _module_marker(arg, marker, src) is None:
                return None
        return f"{arg} no longer encodes (no encodeURIComponent or URLSearchParams in it)"
    if kind == "escaped-by":
        return _module_marker(arg, "escapeHtml", src)
    if kind == "hex-colors":
        text = body if arg == "self" else _module_text(arg, src)
        if text is None:
            return f"src/{arg} does not exist"
        colors = re.findall(r"color:\s*['\"]([^'\"]*)['\"]", text)
        bad = [c for c in colors if not re.fullmatch(r"#[0-9a-fA-F]{3,8}", c)]
        if bad:
            return f"{arg} now returns a color that is not a hex literal: {bad[0]!r}"
        if not colors:
            return f"{arg} declares no color literal at all any more — nothing vouches for it"
        return None
    if kind == "guard":
        return None if re.search(r"\b" + re.escape(arg) + r"\b", body) else \
            f"the declared guard {arg!r} is no longer in the file"
    return (f"unknown mechanism {kind!r} — rules 5 and 7 use "
            "literal/contains:/encoded-by:/escaped-by:/guard:/hex-colors:")


def _calls_guard(expr: str, guards: dict[str, tuple[str, str]]) -> bool:
    return any(re.search(r"(?<![\w$.])" + re.escape(name) + r"\s*\(", expr)
               for name in guards)


def _guard_explains(expr: str, guards: dict[str, tuple[str, str]], src: Path) -> str | None:
    """Why a guard in `expr` is not one any more, or None while every guard it calls is.

    A guard is only a guard while its own module still does the thing that makes it one, so
    the marker is read out of that module.
    """
    for name, (module, marker) in guards.items():
        if not re.search(r"(?<![\w$.])" + re.escape(name) + r"\s*\(", expr):
            continue
        text = _module_text(module, src)
        if text is None or marker not in text:
            return f"{name}() is called but src/{module} no longer contains {marker!r} — it is not a guard"
    return None


def _asset_bindings(body: str) -> set[str]:
    return set(ASSET_IMPORT_RE.findall(body))


def check_urls(rel: str, body: str, src: Path, url_sinks: dict,
               guards: dict[str, tuple[str, str]]) -> tuple[list[tuple[int, str, str]], set[str]]:
    """Rule 5: every URL the browser is told to follow, and why it is not accounted for.

    The set is every site the rule saw, safe or not, so a stale declaration is caught.
    """
    problems: list[tuple[int, str, str]] = []
    sites: set[str] = set()
    assets = _asset_bindings(body)
    for m in URL_BIND_RE.finditer(body):
        if _in_comment(body, m.start()):
            continue
        expr, _end = _braced(body, m.end() - 1)
        where = _line_of(body, m.start())
        found = expr.strip()
        sites.add(found)
        why = _url_problem(rel, found, assets, body, src, url_sinks=url_sinks, guards=guards)
        if why:
            problems.append((where, found or expr.strip(), why))
    for m in URL_SET_RE.finditer(body):
        if _in_comment(body, m.start()):
            continue
        # Keyed by the attribute it sets: the value is an argument, not a binding.
        key = f"setAttribute({m.group(1)})"
        sites.add(key)
        why = _url_problem(rel, key, assets, body, src, url_sinks=url_sinks,
                           declared_only=True)
        if why:
            line_end = body.find("\n", m.start())
            stmt = body[m.start():line_end if line_end != -1 else len(body)].strip().rstrip(";")
            problems.append((_line_of(body, m.start()), f"{key} — {stmt}", why))
    return problems, sites


def _url_problem(rel: str, expr: str, assets: set[str], body: str, src: Path, *,
                 url_sinks: dict, guards: dict[str, tuple[str, str]] | None = None,
                 declared_only: bool = False) -> str | None:
    """Why this URL expression is not acceptable, or None when it is.

    `declared_only` is the `setAttribute` form: its value is an argument, so it is keyed by
    the attribute it sets and has to be declared.
    """
    if not expr:
        return "a URL binding this gate cannot read — declare it in URL_SINKS"
    if not declared_only:
        guard = _guard_explains(expr, guards or URL_GUARDS, src)
        if guard is not None:
            return guard
        if _calls_guard(expr, guards or URL_GUARDS):
            return None
        # `shieldIcon` and `shieldIcon.src` are both the build's own file.
        root = re.split(r"[^$\w]", expr)[0].rsplit(".", 1)[-1]
        if root in assets and expr[:1] not in ("`", "'", '"'):
            return None
    decl = url_sinks.get((rel, expr))
    if decl is None:
        return ("a URL the browser follows, from neither an asset import nor a guard — "
                "add it to URL_SINKS with the mechanism that keeps it http(s)/data-inert")
    mech, reason = _decl(decl)
    why = check_declared(mech, body, src)
    return None if why is None else f"{why} (declared `{mech}` — {reason})"


def check_dynamic_tags(rel: str, body: str) -> list[tuple[int, str, str]]:
    """Rule 6: `<svelte:element this={X}>` may only name an element this app wrote."""
    problems = []
    for m in DYNAMIC_TAG_RE.finditer(body):
        if _in_comment(body, m.start()):
            continue
        expr, _ = _braced(body, m.end() - 1)
        found = expr.strip()
        if re.fullmatch(r'"[^"]*"', found):
            continue
        problems.append((_line_of(body, m.start()), found,
                         "a tag name that is not a string literal is an element this app "
                         "did not write"))
    return problems


def check_styles(rel: str, body: str, src: Path, style_sinks: dict) -> tuple[list[tuple[int, str, str]], set[str]]:
    """Rule 7: every interpolation the browser reads as a CSS declaration, and why one is not
    a number the attribute completes. The set is every site, safe or not (see rule 5)."""
    problems: list[tuple[int, str, str]] = []
    sites: set[str] = set()
    for m in STYLE_ATTR_RE.finditer(body):
        if _in_comment(body, m.start()):
            continue
        quote = m.end() - 1
        close = body.find(m.group(1), quote + 1)
        if close == -1:
            continue
        value = body[quote + 1:close]
        at = value.find("{")
        while at != -1:
            expr, end = _braced(value, at)
            found = expr.strip()
            after = value[end:]
            sites.add(found)
            if not CSS_UNIT_RE.match(after.lstrip()) and after.lstrip()[:1] != ")":
                why = _style_problem(rel, found, body, src, style_sinks)
                if why:
                    problems.append((_line_of(body, quote + at), found, why))
            at = value.find("{", at + 1)
    for m in STYLE_DIRECTIVE_RE.finditer(body):
        if _in_comment(body, m.start()):
            continue
        expr, _ = _braced(body, m.end() - 1)
        found = expr.strip()
        # A `style:` value is the whole declaration, so the unit has to travel inside it.
        sites.add(found)
        if CSS_UNIT_ANY_RE.search(found):
            continue
        why = _style_problem(rel, found, body, src, style_sinks)
        if why:
            problems.append((_line_of(body, m.start()), found, why))
    for m in CSS_TEXT_RE.finditer(body):
        if _in_comment(body, m.start()):
            continue
        line_end = body.find("\n", m.start())
        found = body[m.start():line_end if line_end != -1 else len(body)].strip()
        sites.add(found)
        why = _style_problem(rel, found, body, src, style_sinks)
        if why:
            problems.append((_line_of(body, m.start()), found, why))
    return problems, sites


def _style_problem(rel: str, expr: str, body: str, src: Path, style_sinks: dict) -> str | None:
    """Why this style interpolation is not acceptable, or None when it is."""
    if not expr:
        return "a style interpolation this gate cannot read — declare it in STYLE_SINKS"
    if expr.startswith(("`", "'", '"')) and CSS_UNIT_ANY_RE.search(expr):
        return None  # a literal declaration the template itself completes
    decl = style_sinks.get((rel, expr))
    if decl is None:
        return ("text reaching a style attribute — the value can carry its own `;`, which is "
                "the layout power the sanitizer refuses for untrusted markup; declare it in "
                "STYLE_SINKS or give the attribute the unit")
    mech, reason = _decl(decl)
    why = check_declared(mech, body, src)
    return None if why is None else f"{why} (declared `{mech}` — {reason})"


def analyze(src: Path, allow: dict[str, str], methods: dict[str, str],
            url_sinks: dict | None = None, style_sinks: dict | None = None) -> dict:
    """Every violation, as data. `main()` renders it; the self-test asserts on it."""
    url_sinks = URL_SINKS if url_sinks is None else url_sinks
    style_sinks = STYLE_SINKS if style_sinks is None else style_sinks
    found: dict[str, list[tuple[int, str]]] = {}
    translated: list[tuple[str, int, str]] = []
    scanned = 0
    bodies: dict[str, str] = {}
    urls: list[tuple[str, int, str, str]] = []
    tags: list[tuple[str, int, str, str]] = []
    styles: list[tuple[str, int, str, str]] = []
    seen_urls: set[tuple[str, str]] = set()
    seen_styles: set[tuple[str, str]] = set()

    for rel, path in core_files(src):
        scanned += 1
        body = _read(path)
        bodies[rel] = body
        for m in SINK_RE.finditer(body):
            n = _line_of(body, m.start())
            found.setdefault(rel, []).append((n, _source_line(body, m.start())))
            if TRANSLATION_RE.search(_sink_statement(body, m.start())):
                translated.append((rel, n, _source_line(body, m.start())))
        url_problems, url_sites = check_urls(rel, body, src, url_sinks, URL_GUARDS)
        style_problems, style_sites = check_styles(rel, body, src, style_sinks)
        urls += [(rel, *p) for p in url_problems]
        tags += [(rel, *p) for p in check_dynamic_tags(rel, body)]
        styles += [(rel, *p) for p in style_problems]
        seen_urls.update((rel, e) for e in url_sites)
        seen_styles.update((rel, e) for e in style_sites)

    unlisted = {rel: rows for rel, rows in found.items() if rel not in allow}
    stale = {rel: reason for rel, reason in allow.items() if rel not in found}

    undeclared = sorted(rel for rel in found if rel not in methods)
    extra_methods = sorted(rel for rel in methods if rel not in allow)
    broken: list[tuple[str, str]] = []
    for rel in sorted(found):
        method = methods.get(rel)
        if method is None:
            continue
        why = check_method(rel, method, bodies[rel], src)
        if why:
            broken.append((rel, f"`{method}` — {why}"))

    declared_urls = sorted(set(url_sinks) - seen_urls)
    declared_styles = sorted(set(style_sinks) - seen_styles)

    return {
        "scanned": scanned,
        "unlisted": unlisted,
        "translated": translated,
        "stale": stale,
        "undeclared": undeclared,
        "extra_methods": extra_methods,
        "broken": broken,
        "urls": urls,
        "tags": tags,
        "styles": styles,
        "stale_urls": declared_urls,
        "stale_styles": declared_styles,
    }


def clean(report: dict) -> bool:
    return not (
        report["unlisted"]
        or report["translated"]
        or report["stale"]
        or report["undeclared"]
        or report["extra_methods"]
        or report["broken"]
        or report["urls"]
        or report["tags"]
        or report["styles"]
        or report["stale_urls"]
        or report["stale_styles"]
    )


def render(report: dict) -> list[str]:
    """The gate's answer, one line per rule plus the offenders. Returns rows so the
    self-test can assert on the status words rather than on a printed string."""
    out: list[str] = []

    def line(ok: bool, label: str, count: int, unit: str) -> None:
        out.append(f"{'ok  ' if ok else 'FAIL'} {label} {count:>3} {unit}")

    line(not report["unlisted"], "1 unlisted sink  ", len(report["unlisted"]), "file(s)")
    for rel in sorted(report["unlisted"]):
        for n, ln in report["unlisted"][rel][:3]:
            out.append(f"       {rel}:{n}: {ln.strip()[:100]}")

    line(not report["translated"], "2 translated sink  ", len(report["translated"]), "line(s)")
    for rel, n, ln in sorted(report["translated"])[:12]:
        out.append(f"       {rel}:{n}: {ln.strip()[:100]}")

    line(not report["stale"], "3 stale allowlist  ", len(report["stale"]), "file(s)")
    for rel in sorted(report["stale"]):
        out.append(f"       {rel} (listed for: {report['stale'][rel]})")

    bad4 = report["undeclared"] or report["extra_methods"] or report["broken"]
    line(not bad4, "4 declared method  ", len(report["broken"]) + len(report["undeclared"]), "problem(s)")
    for rel in report["undeclared"]:
        out.append(f"       {rel}: a sink with no METHOD entry — say how it stays safe")
    for rel in report["extra_methods"]:
        out.append(f"       {rel}: a METHOD entry for a file that is not in ALLOW")
    for rel, why in report["broken"]:
        out.append(f"       {rel}: {why}")

    for label, key, stale_key in (
        ("5 unaccounted URL  ", "urls", "stale_urls"),
        ("6 dynamic tag      ", "tags", None),
        ("7 style injection  ", "styles", "stale_styles"),
    ):
        rows = report[key]
        line(not rows and not report.get(stale_key), label, len(rows), "site(s)")
        for rel, n, expr, why in rows[:12]:
            out.append(f"       {rel}:{n}: {expr[:70]} — {why}")
        for rel, expr in (report.get(stale_key) or [])[:12]:
            out.append(f"       {rel}: {expr[:70]} — declared, but no such site is there any more")
    return out


def main() -> int:
    report = analyze(SRC, ALLOW, METHOD)
    for row in render(report):
        print(row)
    print(f"\nscanned {report['scanned']} core files (src/, plus {', '.join(OUTSIDE_SRC)}); "
          f"{len(ALLOW)} sink file(s) allowed.")
    if clean(report):
        print("GATE PASSED: every HTML sink in core is listed, none of them renders a "
              "translation, each declares how it stays safe, and every URL and style "
              "declaration is one this app wrote.")
        return 0
    print("GATE FAILED: a sink, a URL or a style declaration is unaccounted for — "
          "see the rule numbers above.")
    return 1


if __name__ == "__main__":
    sys.exit(main())
