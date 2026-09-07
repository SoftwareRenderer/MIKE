#!/usr/bin/env python3
"""Every place core turns a string into HTML is listed here, with its reason.

`{@html}` and `innerHTML` are the two ways a string becomes an element, and almost
everything that reaches them is written by someone else: LLM output, tool results,
file and diff content from whatever repository is open, language-server hover text,
extension manifests, and the active translation pack's catalog — which is validated
for shape only, asks for no capability at install time, and is one click away in the
first-run language offer.

`Card.svelte` used to build its menu row by concatenating an icon and a `t()` label
into `innerHTML`. CSP stops the payload running, but `frame-src 'self'` plus
same-origin `/extensions/` assets is a way back into the page, so "blocked by CSP"
is not a fix. The row is built with DOM APIs now, and this gate keeps the surface
from growing back:

  1. known sink     — every file with a sink is in ALLOW below with the reason it is
                      safe. A new one is a decision, not an oversight.
  2. no translations — `t(`, `packText(` or `i18n.t(` inside a sink statement fails,
                      in a listed file or not.
  3. no stale list  — a listed file with no sink left in it fails, so the list cannot
                      drift into a record of things that used to be true.
  4. declared method — each listed file also says in METHOD *how* it stays safe, and
                      the ones that claim a mechanism are checked for it:
                        `sanitizer`               imports `$lib/utils/sanitize` and does NOT
                                                  also import DOMPurify — one configuration
                                                  is the whole point (it drops `style`, which
                                                  DOMPurify's defaults keep — see that
                                                  module for why that matters).
                        `guard:<Ident>`           the named check is still in the file.
                        `escaped-by:<module>`     the file imports that module and the
                                                  module still escapes.
                        `literal`                 nothing untrusted reaches the sink;
                                                  nothing to check but the reason above.
                      A file with no method, a method for a file that has no sink, an
                      unknown method name and a vanished mechanism all fail.

What it cannot see: a value carried into a sink through a variable, and a builder
like `highlighter.ts` whose output a sink later renders. Those need the per-component
test, and every sink listed here has one. `html-sinks_test.py` holds each rule of 4
in both directions, because a sink check that stopped matching would report the
same clean line as a sink that is genuinely safe.

    cd frontend && python3 html-sinks.py     # exit 0 = clean, 1 = violations listed
"""

import re
import sys
from pathlib import Path

SRC = Path(__file__).parent / "src"

SINK_RE = re.compile(r"\.innerHTML\b|\.outerHTML\b|insertAdjacentHTML\s*\(|\{@html\b")
TRANSLATION_RE = re.compile(r"(?<![A-Za-z0-9_$.])(?:t|packText)\s*\(|i18n\s*\.\s*t\s*\(")

# Every file in core that turns a string into elements, with the reason it is safe.
# Keep the reason true — it names what does the escaping, so a future edit is judged against it.
ALLOW: dict[str, str] = {
    "src/lib/components/Chat/ChatMessage.svelte": "utils/sanitize over marked (assistant/system markdown)",
    "src/lib/components/Chat/ToolCall.svelte": "utils/sanitize over marked (question, summary, tool result)",
    "src/lib/components/ConfigModal/AgentSection.svelte": "utils/sanitize over marked (manifest description/setup)",
    "src/lib/utils/lsp-hover-actions.ts": "utils/sanitize with the hr tag forbidden, over the LSP plugin's docToHTML",
    "src/lib/components/DiffViewer.svelte": "highlighter output — text escaped, classes from a fixed list",
    "src/lib/components/CommitDiffOverlay.svelte": "highlighter output — text escaped, classes from a fixed list",
    "src/lib/components/FileSearchPanel.svelte": "escapeHtml on the line and on every match",
    "src/lib/components/Icon.svelte": "singleSvgMarkup — a lone sanitized <svg> root, from an *.svg?raw import",
    "src/lib/components/ConfigModal.svelte": "{@html pwaIcon} — an *.svg?raw import",
    "src/lib/components/ModeSelect.svelte": "renderIcon returns literal SVG per known id; anything else is ''",
    "src/lib/components/TerminalPanel.svelte": "two literal SVG path strings on created <svg> elements",
    "src/lib/utils/sticky-scroll.ts": "clones CodeMirror's own line DOM; the fallback is escapeHtml",
    "src/lib/extensions/channel.ts": "innerHTML = '' — a clear, not an injection",
    "src/lib/utils/sanitize.ts": "the sanitizer every `sanitizer` entry above points at; its own outerHTML read is off DOMPurify's sanitized DOM",
}

# How each file above keeps untrusted text from becoming markup. See rule 4.
METHOD: dict[str, str] = {
    "src/lib/components/Chat/ChatMessage.svelte": "sanitizer",
    "src/lib/components/Chat/ToolCall.svelte": "sanitizer",
    "src/lib/components/ConfigModal/AgentSection.svelte": "sanitizer",
    "src/lib/utils/lsp-hover-actions.ts": "sanitizer",
    "src/lib/components/DiffViewer.svelte": "escaped-by:lib/highlighter",
    "src/lib/components/CommitDiffOverlay.svelte": "escaped-by:lib/highlighter",
    "src/lib/components/FileSearchPanel.svelte": "escaped-by:lib/utils/format",
    "src/lib/components/Icon.svelte": "guard:singleSvgMarkup",
    "src/lib/components/ConfigModal.svelte": "literal",
    "src/lib/components/ModeSelect.svelte": "literal",
    "src/lib/components/TerminalPanel.svelte": "literal",
    "src/lib/utils/sticky-scroll.ts": "escaped-by:lib/utils/format",
    "src/lib/extensions/channel.ts": "literal",
    "src/lib/utils/sanitize.ts": "guard:sanitizeHTML",
}

# The one module a `sanitizer` method points at; rule 4 requires the exact import.
SANITIZER_MODULE = "$lib/utils/sanitize"
# `literal` is the one method with nothing mechanical to check; everything else is
# dispatched by `check_method` (see the module docstring for the vocabulary).


def core_files(src: Path):
    for path in sorted(src.rglob("*")):
        if not path.is_file() or path.suffix not in (".svelte", ".ts", ".js"):
            continue
        rel = path.relative_to(src).as_posix()
        if "__tests__/" in f"/{rel}" or ".unit." in path.name:
            continue
        yield path


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


def check_method(rel: str, method: str, body: str, src: Path) -> str | None:
    """Why this file's declared method is not actually in force, or None."""
    if method == "literal":
        return None
    if method == "sanitizer":
        # The exact module path inside an `import ... from`, not the bare word:
        # a comment mentioning the sanitizer must not satisfy the claim.
        if re.search(r"from\s*['\"]" + re.escape(SANITIZER_MODULE) + r"['\"]", body):
            # The one configuration is the point. A file that imports the sanitizer
            # AND DOMPurify has started keeping a second, looser one for itself.
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
        last = module.rsplit("/", 1)[-1]
        if not _imports(body, last):
            return f"claims `escaped-by:{module}` but no longer imports it"
        for suffix in (".ts", ".js", ".svelte"):
            helper = src / (module + suffix)
            if helper.exists():
                if "escapeHtml" in _read(helper):
                    return None
                return f"{module}{suffix} no longer escapes (no escapeHtml in it)"
        return f"{module} does not exist under src/"
    return f"unknown method {method!r} — rule 4 lists sanitizer/guard:/escaped-by:/literal"


def analyze(src: Path, allow: dict[str, str], methods: dict[str, str]) -> dict:
    """Every violation, as data. `main()` renders it; the self-test asserts on it."""
    found: dict[str, list[tuple[int, str]]] = {}
    translated: list[tuple[str, int, str]] = []
    scanned = 0
    bodies: dict[str, str] = {}

    for path in core_files(src):
        scanned += 1
        rel = f"src/{path.relative_to(src).as_posix()}"
        body = _read(path)
        bodies[rel] = body
        for n, line in enumerate(body.splitlines(), 1):
            if not SINK_RE.search(line):
                continue
            found.setdefault(rel, []).append((n, line))
            if TRANSLATION_RE.search(line):
                translated.append((rel, n, line))

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

    return {
        "scanned": scanned,
        "unlisted": unlisted,
        "translated": translated,
        "stale": stale,
        "undeclared": undeclared,
        "extra_methods": extra_methods,
        "broken": broken,
    }


def clean(report: dict) -> bool:
    return not (
        report["unlisted"]
        or report["translated"]
        or report["stale"]
        or report["undeclared"]
        or report["extra_methods"]
        or report["broken"]
    )


def main() -> int:
    report = analyze(SRC, ALLOW, METHOD)

    def line(status: str, label: str, count: int, unit: str) -> None:
        print(f"{'ok  ' if status else 'FAIL'} {label} {count:>3} {unit}")

    status = "ok" if not report["unlisted"] else "FAIL"
    line(status, "1 unlisted sink  ", len(report["unlisted"]), "file(s)")
    for rel in sorted(report["unlisted"]):
        for n, ln in report["unlisted"][rel][:3]:
            print(f"       {rel}:{n}: {ln.strip()[:100]}")

    status = "ok" if not report["translated"] else "FAIL"
    line(status, "2 translated sink  ", len(report["translated"]), "line(s)")
    for rel, n, ln in sorted(report["translated"])[:12]:
        print(f"       {rel}:{n}: {ln.strip()[:100]}")

    status = "ok" if not report["stale"] else "FAIL"
    line(status, "3 stale allowlist  ", len(report["stale"]), "file(s)")
    for rel in sorted(report["stale"]):
        print(f"       {rel} (listed for: {report['stale'][rel]})")

    bad4 = report["undeclared"] or report["extra_methods"] or report["broken"]
    print(f"{'ok  ' if not bad4 else 'FAIL'} 4 declared method  {len(report['broken']) + len(report['undeclared']):>3} problem(s)")
    for rel in report["undeclared"]:
        print(f"       {rel}: a sink with no METHOD entry — say how it stays safe")
    for rel in report["extra_methods"]:
        print(f"       {rel}: a METHOD entry for a file that is not in ALLOW")
    for rel, why in report["broken"]:
        print(f"       {rel}: {why}")

    print(f"\nscanned {report['scanned']} core files under src/; {len(ALLOW)} sink file(s) allowed.")
    if bad4 or report["unlisted"] or report["translated"] or report["stale"]:
        print("GATE FAILED: an HTML sink is unaccounted for — see the rule numbers above.")
        return 1
    print("GATE PASSED: every HTML sink in core is listed, none of them renders a "
          "translation, and each declares how it stays safe.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
