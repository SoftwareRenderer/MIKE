#!/usr/bin/env python3
"""The "core names no language" gate.

`docs/i18n.md` and `src/lib/i18n/README.md` both promise that the app's core holds zero
translated strings and zero language names: the active language is whichever `i18n` pack is
enabled, a locale tag is carried through verbatim, and nothing in the tree knows what a
language is called. A promise you have to eyeball is a promise that rots, so this script is
the claim.

    cd frontend && python3 i18n-gate.py        # exit 0 = clean, 1 = violations listed

Scope is **core source**: everything under `src/` except a test's own fixtures — a pack test
cannot prove a pack applied without bytes that differ from English, so `__tests__/` dirs and
`*.unit.*` files are out of scope by definition, not by exception.

Four rules, each one a sentence in the docs:

  1. no locale tag          — no `zh`/`fr`/`ja`/`ko` (or any region/script-tagged form of
                              them) anywhere in core. ONE documented exception: the CJK
                              typography block in `src/app.css`, where a tag may appear ONLY
                              inside a `:lang(…)` selector — AGENTS.md requires that block,
                              and a selector targets a language without naming one to a
                              reader. (`src/lib/types.ts`'s `i18n.locale` field and the
                              runtime's `LocaleTag` are the *shape* of a tag, never a tag.)
  2. no translated text     — no CJK/hangul/kana codepoint in core. That is how "no endonym
                              in a comment" and "no translated sample value" are checked
                              without listing every language on earth.
  3. no invented name       — no `Intl.DisplayNames` in core: a locale's display name comes
                              from the pack's own `display_name`, so the name a user sees is
                              the one the pack's author chose.
  4. no per-locale branch   — no comparison or `switch` keyed on a locale-ish identifier
                              against a tag-shaped literal, and no `switch (locale)`. A
                              table of names or a branch on a language is exactly the
                              knowledge rule 1 forbids, expressed as control flow. The
                              runtime's own fallback reads `DEFAULT_LOCALE` (imported from
                              `en.ts`), so even the fallback's *spelling* stays out of core.
"""

import re
import sys
from pathlib import Path

SRC = Path(__file__).parent / "src"

# Rule 1's tripwire. It is deliberately a short list — the tags the packs this product ships
# and their region forms — standing in for the general rule ("core knows no locale") that the
# per-file review and rule 4's structural check carry. New pack, new tag: add it here.
TAG_TRIPWIRE = ("zh", "fr", "ja", "ko")
TAG_RE = re.compile(
    r"(?<![A-Za-z0-9])(?:%s)(?:-[A-Za-z0-9]{1,8})*(?![A-Za-z0-9])" % "|".join(TAG_TRIPWIRE)
)
# A CSS selector line: the `:lang(` comes before any `{`, so the tag is targeting, not saying.
LANG_SELECTOR_RE = re.compile(r"^[^{};]*:lang\(")

# Rule 2. Han ideographs, kana, hangul, CJK punctuation. (Non-ASCII Latin-script punctuation
# — — … é — is not in scope: the rule is about languages, not about ASCII.)
NON_LATIN_RE = re.compile(
    "[\u3000-\u303f\u3040-\u30ff\u3130-\u318f\u3400-\u4dbf\u4e00-\u9fff"
    "\uac00-\ud7af\uf900-\ufaff\ufe30-\ufe4f\uff61-\uff9f]"
)

# Rule 4. Control flow keyed on a language: `locale === 'xx'`, `tag !== 'xx-YY'`,
# `switch (locale)`. The identifier has to look like a locale (a `typeof x === 'string'
#` guard is not one), and the literal has to look like a tag (a 6-letter word is not one).
LOCALE_COMPARE_RE = re.compile(
    r"\b(?:locale|localeTag|languageTag|language|lang|tag)\w*\s*[!=]==?\s*['\"][A-Za-z]{2,3}(?:-[A-Za-z]{2,8})?['\"]"
)
LOCALE_SWITCH_RE = re.compile(
    r"\bswitch\s*\(\s*(?:locale|localeTag|languageTag|language|lang|tag)\b"
)

SUFFIXES = {".ts", ".svelte", ".css", ".js", ".html"}


def is_test(path: Path) -> bool:
    return "__tests__" in path.parts or ".unit." in path.name


def core_files():
    for path in sorted(SRC.rglob("*")):
        if not path.is_file() or path.suffix not in SUFFIXES or is_test(path.relative_to(SRC)):
            continue
        yield path


def main() -> int:
    failures: dict[tuple[str, str, int], str] = {}
    scanned = 0

    def fail(rule: str, rel: str, n: int, text: str) -> None:
        # One row per (rule, file, line): a line with three tags broke one rule, not three.
        failures.setdefault((rule, rel, n), text)

    for path in core_files():
        scanned += 1
        rel = path.relative_to(SRC.parent).as_posix()
        try:
            lines = path.read_text(encoding="utf-8").splitlines()
        except UnicodeDecodeError:  # a binary that slipped into src/
            continue
        for n, line in enumerate(lines, 1):
            if TAG_RE.search(line):
                if not (rel == "src/app.css" and LANG_SELECTOR_RE.match(line)):
                    fail("1 locale tag", rel, n, line)
            if NON_LATIN_RE.search(line):
                fail("2 non-Latin text", rel, n, line)
            if "Intl.DisplayNames" in line:
                fail("3 invented name", rel, n, line)
            if LOCALE_COMPARE_RE.search(line) or LOCALE_SWITCH_RE.search(line):
                fail("4 per-locale branch", rel, n, line)

    # Report per rule so a new failure names the rule it broke, not just the line.
    for rule in ("1 locale tag", "2 non-Latin text", "3 invented name", "4 per-locale branch"):
        rows = [(k, v) for k, v in failures.items() if k[0] == rule]
        status = "ok  " if not rows else "FAIL"
        print(f"{status} {rule:<19} {len(rows):>3} violation(s)")
        for (_rule, rel, n), text in sorted(rows, key=lambda r: (r[0][1], r[0][2]))[:12]:
            print(f"       {rel}:{n}: {text.strip()[:110]}")
        if len(rows) > 12:
            print(f"       … {len(rows) - 12} more")

    print(f"\nscanned {scanned} core files under src/ "
          f"(test fixtures excluded: any __tests__/ dir or *.unit.* file)")
    if failures:
        print(f"GATE FAILED: {len(failures)} violation(s) — see the rule numbers above.")
        return 1
    print("GATE PASSED: core names no language.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
