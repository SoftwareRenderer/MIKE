#!/usr/bin/env python3
"""List every stylesheet rule a foreign fragment could adopt to cover the page, with the file
that wrote it.

`sanitize.ts` keeps `class`, so a rule whose subject markup can name is layout power handed over
by another route: `KEPT_CLASS` refuses the token, this gate keeps the stylesheet's half of that
claim true. What counts as adoptable, and why only `position: fixed` is counted —
`docs/markup-sanitizing.md → Class Adoption and the Stylesheet`.

    cd frontend && python3 css-surface.py     # exit 0 = clean, 1 = offenders listed
"""

import re
import sys
from pathlib import Path

FRONTEND = Path(__file__).parent
SRC = FRONTEND / "src"

STYLE_BLOCK_RE = re.compile(r"<style\b[^>]*>(.*?)</style>", re.DOTALL | re.IGNORECASE)
CSS_IMPORT_RE = re.compile(r"@import\s+(?:url\()?\s*['\"]([^'\"]+)['\"]")
RULE_RE = re.compile(r"([^{}]+)\{([^{}]*)\}")
GLOBAL_OPEN_RE = re.compile(r":global\(")
APP_ONLY_RE = re.compile(r"\.svelte-[0-9a-z]+|[#\[]")
# The document itself: markup a fragment hands to a sink cannot become these.
DOCUMENT_SUBJECTS = {"html", "body", "head"}
COVERS_RE = re.compile(r"position\s*:\s*fixed", re.IGNORECASE)


def _norm(path: str) -> str:
    parts: list[str] = []
    for part in path.split("/"):
        if part in ("", "."):
            continue
        if part == "..":
            if parts:
                parts.pop()
            continue
        parts.append(part)
    return "/".join(parts)


def strip_comments(text: str) -> str:
    """CSS comments out, offsets and line count preserved so a reported line stays true."""
    out, i = [], 0
    while True:
        start = text.find("/*", i)
        if start == -1:
            out.append(text[i:])
            return "".join(out)
        end = text.find("*/", start + 2)
        end = len(text) if end == -1 else end + 2
        out.append(text[i:start])
        out.append("".join(c if c == "\n" else " " for c in text[start:end]))
        i = end


def expand_globals(selector: str) -> tuple[str, list[bool]]:
    """`selector` with every `:global()` unwrapped, and one flag per character: unscoped or not."""
    text: list[str] = []
    flags: list[bool] = []
    opened: list[int] = []
    i = 0
    while i < len(selector):
        m = GLOBAL_OPEN_RE.match(selector, i)
        if m:
            opened.append(len(text))
            i = m.end()
            continue
        c = selector[i]
        if c == ")" and opened:
            for k in range(opened.pop(), len(text)):
                flags[k] = True
            i += 1
            continue
        text.append(c)
        flags.append(bool(opened))
        i += 1
    return "".join(text), flags


def subject_of(selector: str) -> tuple[str, bool]:
    """The subject compound of `selector`, and whether `:global()` left it unscoped."""
    expanded, flags = expand_globals(selector)
    parts = [p for p in re.split(r"[\s>+~]+", expanded) if p]
    if not parts:
        return "", False
    subject = parts[-1]
    at = expanded.rfind(subject)
    return subject, all(flags[at:at + len(subject)])


def is_adoptable(selector: str, file_is_global: bool) -> bool:
    """Whether markup a model wrote could match this selector."""
    subject, unscoped = subject_of(selector)
    if not subject or APP_ONLY_RE.search(subject) or subject in DOCUMENT_SUBJECTS:
        return False
    return unscoped or file_is_global


def scoped_css(bodies: dict[str, str]) -> set[str]:
    """Every `.css` a component `<style>` imports — Svelte scopes those; the rest are global."""
    found: set[str] = set()
    for rel, body in bodies.items():
        if not rel.endswith(".svelte"):
            continue
        for block in STYLE_BLOCK_RE.finditer(body):
            for ref in CSS_IMPORT_RE.findall(block.group(1)):
                if ref.startswith("$lib/"):
                    target = "src/lib/" + ref[len("$lib/"):]
                elif ref.startswith("/"):
                    continue  # no root in this tree for a stylesheet to sit under
                else:
                    target = (Path(rel).parent / ref).as_posix()
                found.add(_norm(target))
    return found


def style_blocks(rel: str, body: str):
    """(offset, css) for each stylesheet this file contributes."""
    if rel.endswith(".svelte"):
        return [(m.start(), m.group(1)) for m in STYLE_BLOCK_RE.finditer(body)]
    return [(0, body)]


def core_files(src: Path):
    for path in sorted(src.rglob("*")):
        if not path.is_file() or path.suffix not in (".svelte", ".css"):
            continue
        rel = path.relative_to(src).as_posix()
        if "__tests__/" in f"/{rel}" or ".unit." in path.name:
            continue
        yield f"src/{rel}", path


def _read(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return ""


def analyze(src: Path) -> dict:
    """Every adoptable rule that covers the page, as data: `render()` prints it, the test asserts."""
    bodies = {rel: _read(path) for rel, path in core_files(src)}
    scoped = scoped_css(bodies)

    offenders: list[tuple[str, int, str, str]] = []
    for rel, body in bodies.items():
        file_is_global = rel.endswith(".css") and rel not in scoped
        for base, css in style_blocks(rel, body):
            css = strip_comments(css)
            for m in RULE_RE.finditer(css):
                decls = m.group(2)
                hit = COVERS_RE.search(decls)
                if not hit:
                    continue
                for selector in m.group(1).split(","):
                    if not is_adoptable(selector, file_is_global):
                        continue
                    line = body.count("\n", 0, base + m.start()) + 1
                    offenders.append((rel, line, " ".join(selector.split()),
                                      " ".join(hit.group().split())))

    return {"scanned": len(bodies), "offenders": offenders}


def clean(report: dict) -> bool:
    return not report["offenders"]


def render(report: dict) -> list[str]:
    n = len(report["offenders"])
    out = [f"{'ok  ' if not n else 'FAIL'} 1 overlay-capable rule {n:>3} rule(s)"]
    for rel, line, sel, decl in report["offenders"][:20]:
        out.append(f"       {rel}:{line}: {sel[:70]} — {decl}")
    return out


def main() -> int:
    report = analyze(SRC)
    for row in render(report):
        print(row)
    print(f"\nscanned {report['scanned']} stylesheet(s): src/**.css and every <style> block.")
    if clean(report):
        print("GATE PASSED: no rule a foreign fragment can name pins an element to the viewport.")
        return 0
    print("GATE FAILED: markup carrying these class names could cover the page. Scope the rule's "
          "subject so only an element the app renders matches it.")
    return 1


if __name__ == "__main__":
    sys.exit(main())
