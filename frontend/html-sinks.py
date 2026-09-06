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

What it cannot see: a value carried into a sink through a variable, and a builder
like `highlighter.ts` whose output a sink later renders. Those need the per-component
test, and every sink listed here has one.

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
    "src/lib/components/Chat/ChatMessage.svelte": "DOMPurify over marked; the user path escapes < and >",
    "src/lib/components/Chat/ToolCall.svelte": "DOMPurify over marked (question, summary, tool result)",
    "src/lib/components/ConfigModal/AgentSection.svelte": "DOMPurify over marked (manifest description/setup)",
    "src/lib/utils/lsp-hover-actions.ts": "DOMPurify with FORBID_TAGS over the LSP plugin's docToHTML",
    "src/lib/components/DiffViewer.svelte": "highlighter output — text escaped, classes from a fixed list",
    "src/lib/components/CommitDiffOverlay.svelte": "highlighter output — text escaped, classes from a fixed list",
    "src/lib/components/FileSearchPanel.svelte": "escapeHtml on the line and on every match",
    "src/lib/components/Icon.svelte": "{@html src} of an *.svg?raw import — a build-time file",
    "src/lib/components/ConfigModal.svelte": "{@html pwaIcon} — an *.svg?raw import",
    "src/lib/components/ModeSelect.svelte": "renderIcon returns literal SVG per known id; anything else is ''",
    "src/lib/components/TerminalPanel.svelte": "two literal SVG path strings on created <svg> elements",
    "src/lib/utils/sticky-scroll.ts": "clones CodeMirror's own line DOM; the fallback is escapeHtml",
    "src/lib/extensions/channel.ts": "innerHTML = '' — a clear, not an injection",
}


def core_files():
    for path in sorted(SRC.rglob("*")):
        if not path.is_file() or path.suffix not in (".svelte", ".ts", ".js"):
            continue
        rel = path.relative_to(SRC).as_posix()
        if "__tests__/" in f"/{rel}" or ".unit." in path.name:
            continue
        yield path


def main() -> int:
    found: dict[str, list[tuple[int, str]]] = {}
    translated: list[tuple[str, int, str]] = []
    scanned = 0

    for path in core_files():
        scanned += 1
        rel = f"src/{path.relative_to(SRC).as_posix()}"
        try:
            lines = path.read_text(encoding="utf-8").splitlines()
        except UnicodeDecodeError:  # a binary that slipped into src/
            continue
        for n, line in enumerate(lines, 1):
            if not SINK_RE.search(line):
                continue
            found.setdefault(rel, []).append((n, line))
            if TRANSLATION_RE.search(line):
                translated.append((rel, n, line))

    unlisted = {rel: rows for rel, rows in found.items() if rel not in ALLOW}
    stale = {rel: reason for rel, reason in ALLOW.items() if rel not in found}

    # Rule 1: an unlisted sink.
    status = "ok  " if not unlisted else "FAIL"
    print(f"{status} 1 unlisted sink   {len(unlisted):>3} file(s)")
    for rel in sorted(unlisted):
        for n, line in unlisted[rel][:3]:
            print(f"       {rel}:{n}: {line.strip()[:100]}")

    # Rule 2: a translated string reaching an HTML parser.
    status = "ok  " if not translated else "FAIL"
    print(f"{status} 2 translated sink   {len(translated):>3} line(s)")
    for rel, n, line in sorted(translated)[:12]:
        print(f"       {rel}:{n}: {line.strip()[:100]}")

    # Rule 3: a listed file with no sink left in it.
    status = "ok  " if not stale else "FAIL"
    print(f"{status} 3 stale allowlist   {len(stale):>3} file(s)")
    for rel in sorted(stale):
        print(f"       {rel} (listed for: {stale[rel]})")

    print(f"\nscanned {scanned} core files under src/; {len(ALLOW)} sink file(s) allowed.")
    if unlisted or translated or stale:
        print("GATE FAILED: an HTML sink is unaccounted for — see the rule numbers above.")
        return 1
    print("GATE PASSED: every HTML sink in core is listed and none of them renders a translation.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
