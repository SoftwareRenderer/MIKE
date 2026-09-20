#!/usr/bin/env python3
"""The documentation gate: `AGENTS.md` holds rules, `docs/<subsystem>.md` holds the rules and
invariants for one subsystem, `docs/adr/` holds the decisions with their evidence.

Eight rules, all mechanical, plus one ratchet:

1. a `file.md → Section` pointer names a heading that is there, in a file that is there;
2. every doc is listed in an index (`AGENTS.md` for subsystem docs, `docs/adr/README.md` for ADRs);
3. no `path.go:NN` reference — a line number is a claim about a revision, not about the tree;
4. every `MIKE_*` named in a doc is one something reads, and every variable `backend/startup`
   declares is documented;
5. no work estimate outside an ADR;
6. an ADR is shaped like an ADR (numbered filename, Status, Abstract, Context, Decision, Consequences)
   and names the rule or doc it grounds;
7. a backticked path is a path: a tree-relative one that resolves to nothing is a rename that
   happened after the doc was written;
8. a file's word count and its count of past-tense sentences only shrink — the register in
   `scripts/docscheck/baseline.json` is the ceiling, and `--update` re-freezes it. Words, not lines:
   breaking one 300-word paragraph into a list of reasons is the edit this file asks for, and it adds lines.
9. and so does its count of sentences over 45 words, which is where a doc stopped being a list of short
   statements and became an argument the reader has to hold in their head at once.

Past tense, estimates and superseded paths are welcome in `docs/adr/`, which is what they are for;
rules and mechanism stay present tense, so those checks skip that directory — as they skip the long-sentence
count, because a record is allowed to be an argument.

    python3 scripts/docs-check.py            # exit 0 = clean, 1 = violations listed
    python3 scripts/docs-check.py --update   # re-freeze the register at today's counts
"""

import argparse
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DOCS = ROOT / "docs"
ADR_DIR = DOCS / "adr"
BASELINE = ROOT / "scripts" / "docscheck" / "baseline.json"
AGENTS = ROOT / "AGENTS.md"
CONFIG_DOC = DOCS / "configuration.md"

# A pointer's section title stops at a backtick or the line end, so two pointers on one line and a
# sentence carrying on past the pointer both parse. A heading whose title holds a backtick is pointed
# at without it; matching ignores punctuation anyway.
POINTER_RE = re.compile(r"([A-Za-z0-9_./-]+\.md)`?\s*(?:→|->)\s*([^`\n]*)")
LINE_REF_RE = re.compile(r"[A-Za-z0-9_./{}-]+\.(?:go|ts|tsx|js|svelte|py|css|json|tmpl|sql|yaml|yml|mjs|sh):\d+")
BARE_LINE_REF_RE = re.compile(r"`:[0-9]{2,}`")
ENV_RE = re.compile(r"\bMIKE_[A-Z0-9_]+\b")
STARTUP_ENV_RE = re.compile(r'"(MIKE_[A-Z0-9_]+)"')
ESTIMATE_RE = re.compile(r"\b(?:\d+\s*[–-]\s*\d+|roughly\s+\d+|about\s+\d+)\s*(?:days?|hours?|weeks?|man-days?)\b", re.I)
HISTORY_RE = re.compile(
    r"\b(?:used to|use[dd]? to\b|previously|historically|originally|no longer|retired|"
    r"do not reintroduce|it was removed|were removed|was removed|inferred, not proven)\b", re.I)

# A backticked path is a claim that the path is there. Only source paths are checked — build
# output, `/tmp`, `/api` routes and Go import paths are not this tree's files — and a shorthand
# that resolves anywhere in the tree passes, so only a renamed or deleted package fails.
PATH_PREFIXES = ("backend/", "pkg/", "frontend/", "scripts/", "docs/", "sandbox/", "prompts/", "src/")
BUILD_OUTPUT = ("frontend/dist",)  # gitignored; only its .gitkeep placeholder is tracked
SOURCE_SUFFIXES = (".go", ".ts", ".svelte", ".js", ".mjs", ".py", ".css", ".json", ".tmpl", ".sh",
                   ".yaml", ".yml", ".html", ".wasm", ".sql", ".svg")
PATH_TOKEN_RE = re.compile(r"`([A-Za-z0-9_./*{},+-]+(?:/[A-Za-z0-9_./*{},+-]+)+/?)(?::\d+)?`")

# Files that live outside the docs tree but are named by docs as the source of truth.
CODE_SEARCH = ["backend", "pkg", "main.go", "scripts", "frontend", "compose.yaml", ".env.example",
               "Dockerfile", "Makefile"]

ADR_REQUIRED_SECTIONS = ("## Abstract", "## Context", "## Decision", "## Consequences")
ADR_STATUS_RE = re.compile(r"^\*\*Status:\*\*\s*(accepted|superseded by \d{4}|proposed|deprecated)", re.M)
ADR_NAME_RE = re.compile(r"^(\d{4})-[a-z0-9][a-z0-9-]*\.md$")
# The record points at the rule, not the other way round: a rule states itself and stays short, and the
# reader who asks "why this shape?" arrives through docs/adr/README.md and follows the record's own pointer.
ADR_RULES_LINE_RE = re.compile(r"^\*\*Rules that follow:\*\*", re.M)


def md_files():
    out = [AGENTS]
    out += sorted(DOCS.rglob("*.md"))
    return [p for p in out if p.exists()]


def is_adr(path: Path) -> bool:
    try:
        return ADR_DIR in path.resolve().parents or path.resolve().parent == ADR_DIR
    except OSError:
        return False


def headings_of(path: Path):
    return [re.sub(r"^#+\s*", "", l).strip() for l in path.read_text().splitlines() if re.match(r"^#{1,6} ", l)]


def norm(s: str) -> str:
    return re.sub(r"[^a-z0-9]", "", s.lower())


def resolve_doc(ref: str, from_path: Path):
    """Docs are addressed by name from anywhere in the tree: `docs/x.md`, `adr/NNNN-x.md`, `AGENTS.md`.
    A bare name means the referrer's own directory first, or a stale `README.md` at the root resolves
    against whichever README happens to exist."""
    name = ref.split("/")[-1]
    for cand in (from_path.parent / name, ROOT / name, DOCS / name, ADR_DIR / name):
        if cand.is_file():
            return cand
    return None


def doc_texts():
    return {p: p.read_text() for p in md_files()}


# --- rule 1: pointers -----------------------------------------------------------------------

def check_pointers(texts):
    bad = []
    heads = {p: [norm(h) for h in headings_of(p)] for p in texts}
    for path, text in texts.items():
        for n, line in enumerate(text.splitlines(), 1):
            for m in POINTER_RE.finditer(line):
                target, section = m.group(1), m.group(2).strip().strip("`").strip()
                section = re.split(r"[.;:]", section)[0].strip(" `”\"'")
                found = resolve_doc(target, path)
                if found is None:
                    bad.append((path, n, f"`{target} → {section}` points at a file that is not in the tree"))
                    continue
                # A pointer may leave the docs tree — a packaged README, a doc beside the code it
                # describes — so the heading list is read wherever the pointer landed.
                heads.setdefault(found, [norm(h) for h in headings_of(found)])
                # Each `→` step names a heading from its first word, in either direction (a pointer may
                # stop early, or carry on past a colon the heading cannot hold). Naming the middle of a
                # heading is how a renamed section survives as a dead pointer, so it is refused.
                steps = [norm(s) for s in re.split(r"→|->", section) if norm(s)]
                if any(not any(h == w or h.startswith(w) or w.startswith(h) for h in heads[found])
                       for w in steps):
                    bad.append((path, n, f"`{target} → {section}` names no heading in {rel(found)}"))
    return bad


# --- rule 2: the indexes --------------------------------------------------------------------

def check_indexes(texts):
    bad = []
    agent_text = texts[AGENTS]
    adr_index = (ADR_DIR / "README.md")
    listed_agents = set(re.findall(r"docs/[a-z0-9-]+/[a-z0-9._-]+\.md|docs/[a-z0-9._-]+\.md", agent_text))
    listed_adr = set(re.findall(r"\d{4}-[a-z0-9._-]+\.md", adr_index.read_text())) if adr_index.is_file() else set()
    for path in texts:
        if path == AGENTS or path.name == "README.md":
            continue
        here = rel(path)
        if is_adr(path):
            if not adr_index.is_file():
                bad.append((path, 1, "no docs/adr/README.md to index this ADR"))
            elif path.name not in listed_adr:
                bad.append((path, 1, f"{here} is not listed in docs/adr/README.md"))
        elif here not in listed_agents:
            bad.append((path, 1, f"{here} is not listed in AGENTS.md → Docs Index"))
    return bad


# --- rule 3: line numbers -------------------------------------------------------------------

def check_line_refs(texts):
    bad = []
    for path, text in texts.items():
        for n, line in enumerate(text.splitlines(), 1):
            for m in LINE_REF_RE.finditer(line):
                bad.append((path, n, f"line-number reference {m.group(0)!r} — name the symbol instead"))
            for m in BARE_LINE_REF_RE.finditer(line):
                bad.append((path, n, f"line-number reference {m.group(0)} — name the symbol instead"))
    return bad


# --- rule 4: the environment table ----------------------------------------------------------

def env_declared_in_code():
    names = set()
    for entry in CODE_SEARCH:
        p = ROOT / entry
        files = [p] if p.is_file() else [f for f in p.rglob("*") if f.is_file()] if p.is_dir() else []
        for f in files:
            if any(part in {"node_modules", "dist", ".svelte-kit", "__pycache__"} for part in f.parts):
                continue
            if f.suffix not in {".go", ".py", ".sh", ".ts", ".svelte", ".yaml", ".yml", ".json", ".mjs"} \
                    and f.name not in {"Makefile", "Dockerfile", ".env.example"}:
                continue
            try:
                names.update(ENV_RE.findall(f.read_text(errors="ignore")))
            except OSError:
                continue
    return names


def startup_declared_env():
    cfg = ROOT / "backend" / "startup" / "config.go"
    return set(STARTUP_ENV_RE.findall(cfg.read_text())) if cfg.is_file() else set()


def check_env(texts):
    bad = []
    code = env_declared_in_code()
    config_text = texts.get(CONFIG_DOC, "")
    for path, text in texts.items():
        if is_adr(path):
            continue  # a record names the spelling it retired; that is the record's job
        for n, line in enumerate(text.splitlines(), 1):
            # `$MIKE_TOKEN` in a curl example is a shell variable, not one the server reads.
            for name in re.findall(r"(?<![A-Za-z0-9_$])(MIKE_[A-Z0-9_]+)\b", line):
                if name not in code:
                    bad.append((path, n, f"{name} is named here and read by nothing in this tree"))
    for name in sorted(startup_declared_env()):
        if name not in config_text:
            bad.append((CONFIG_DOC, 1, f"{name} is declared by backend/startup and documented nowhere"))
    return bad


# --- rule 5: estimates ----------------------------------------------------------------------

def check_estimates(texts):
    bad = []
    for path, text in texts.items():
        if is_adr(path):
            continue
        for n, line in enumerate(text.splitlines(), 1):
            if ESTIMATE_RE.search(line):
                bad.append((path, n, "work estimate in a rules document — it belongs in the ADR it argues for"))
    return bad


# --- rule 6: the shape of an ADR ------------------------------------------------------------

def check_adrs():
    bad = []
    if not ADR_DIR.is_dir():
        return [(DOCS, 1, "docs/adr/ does not exist, so no decision has a home")]
    seen = {}
    for path in sorted(ADR_DIR.glob("*.md")):
        if path.name == "README.md":
            continue
        m = ADR_NAME_RE.match(path.name)
        if not m:
            bad.append((path, 1, "filename must be NNNN-kebab-case.md"))
            continue
        num = m.group(1)
        if num in seen:
            bad.append((path, 1, f"ADR-{num} is used by {rel(seen[num])} too"))
        seen[num] = path
        text = path.read_text()
        for section in ADR_REQUIRED_SECTIONS:
            if section not in text:
                bad.append((path, 1, f"missing {section}"))
        status = ADR_STATUS_RE.search(text)
        if not status:
            bad.append((path, 1, "missing a `**Status:** accepted|superseded by NNNN|proposed|deprecated` line"))
        elif (sup := re.search(r"superseded by (\d{4})", status.group(1))):
            if sup.group(1) not in seen and not list(ADR_DIR.glob(f"{sup.group(1)}-*.md")):
                bad.append((path, 1, f"superseded by ADR-{sup.group(1)}, which does not exist"))
        if not ADR_RULES_LINE_RE.search(text):
            bad.append((path, 1, "no `**Rules that follow:**` line — the record must name the rule it grounds"))
    return bad


# --- rule 7: a backticked path is a path ----------------------------------------------------

def tree_paths():
    names, paths = set(), set()
    for entry in ("backend", "pkg", "frontend", "scripts", "docs", "sandbox", "prompts"):
        base = ROOT / entry
        if not base.is_dir():
            continue
        for f in base.rglob("*"):
            if f.is_file() and not any(p in {"node_modules", "dist", ".svelte-kit", "__pycache__"} for p in f.parts):
                paths.add(str(f.relative_to(ROOT)))
                names.add(f.name)
    return paths, names


def check_paths(texts):
    paths, names = tree_paths()
    bad = []
    for path, text in texts.items():
        if is_adr(path):
            continue
        for n, line in enumerate(text.splitlines(), 1):
            for m in PATH_TOKEN_RE.finditer(line):
                token = m.group(1)
                if not token.startswith(PATH_PREFIXES) or any(c in token for c in "*{}<>…"):
                    continue
                if token in BUILD_OUTPUT or any(token.startswith(b + "/") for b in BUILD_OUTPUT):
                    continue
                if "..." in token or token.endswith("/"):
                    continue
                leaf = token.split("/")[-1]
                if "." in leaf and not leaf.endswith(SOURCE_SUFFIXES):
                    continue  # `pkg/sandboxruntime.GetAgentBinaryPath` is a symbol, not a file
                if (ROOT / token).exists() or (ROOT / "frontend" / token).exists() \
                        or token in paths or leaf in names:
                    continue
                bad.append((path, n, f"`{token}` resolves to nothing in this tree"))
    return bad


SENTENCE_RE = re.compile(r"(?<=[.!?:])\s+(?=[A-Z\"(X])")
LONG_SENTENCE_WORDS = 45


def long_sentences(text: str):
    """The sentences over the limit, with code and identifiers masked out first: a list of file paths is long,
    but it is not a sentence, and neither is a fenced block."""
    masked = re.sub(r"```.*?```", " CODE ", text, flags=re.S)
    masked = re.sub(r"`[^`\n]*`", " X ", masked)
    units, buf = [], []
    for line in masked.splitlines():
        s = line.strip()
        if not s or s.startswith("|"):
            if buf:
                units.append(" ".join(buf))
                buf = []
            continue
        if buf and re.match(r"([-*+] |\d+\. |#{1,6} )", s):
            units.append(" ".join(buf))
            buf = [s]
            continue
        buf.append(s)
    if buf:
        units.append(" ".join(buf))
    return [len(s.split()) for u in units for s in SENTENCE_RE.split(u) if len(s.split()) > LONG_SENTENCE_WORDS]


# --- rule 8: the ratchet --------------------------------------------------------------------

def counts_for(path, text):
    # Words, not lines: a line count measures how the file is wrapped, so it punished the bulleted list that a
    # wall of prose should become. The count is still a ceiling that only moves down.
    prose = re.sub(r"```.*?```", "", text, flags=re.S)
    return {"words": len(prose.split()),
            "history": len(HISTORY_RE.findall(text)) if not is_adr(path) else 0,
            "longsent": len(long_sentences(text)) if not is_adr(path) else 0}


def check_ratchet(texts, baseline, update):
    if update:
        return []
    bad = []
    for path, text in sorted(texts.items()):
        rel_path = rel(path)
        got = counts_for(path, text)
        allowed = baseline.get(rel_path)
        if allowed is None:
            bad.append((path, 1, f"not in scripts/docscheck/baseline.json ({got['words']} words, "
                                 f"{got['history']} past-tense, {got['longsent']} long sentences) — "
                                 f"run `make docs-check UPDATE=1`"))
            continue
        for key, ceiling in allowed.items():
            if got[key] > ceiling:
                bad.append((path, 1, f"{key} grew: {got[key]} against a ceiling of {ceiling} — "
                                     f"the register only shrinks"))
    for rel_path in baseline:
        if rel_path not in {rel(p) for p in texts}:
            bad.append((ROOT, 1, f"{rel_path} is in the register and no longer exists — drop the entry"))
    return bad


def rel(path: Path) -> str:
    try:
        return str(path.relative_to(ROOT))
    except ValueError:
        return str(path)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--update", action="store_true", help="re-freeze the register at today's counts")
    args = ap.parse_args()

    texts = doc_texts()
    baseline = {}
    if BASELINE.is_file():
        baseline = json.loads(BASELINE.read_text())

    if args.update:
        BASELINE.parent.mkdir(parents=True, exist_ok=True)
        data = {rel(p): counts_for(p, t) for p, t in sorted(texts.items())}
        BASELINE.write_text(json.dumps(data, indent=1, sort_keys=True) + "\n")
        print(f"register frozen: {len(data)} file(s) -> {rel(BASELINE)}")
        return 0

    sections = [
        ("1 pointers    ", check_pointers(texts)),
        ("2 indexes     ", check_indexes(texts)),
        ("3 line numbers", check_line_refs(texts)),
        ("4 environment ", check_env(texts)),
        ("5 estimates   ", check_estimates(texts)),
        ("6 ADR shape   ", check_adrs()),
        ("7 paths       ", check_paths(texts)),
        ("8 the ratchet ", check_ratchet(texts, baseline, update=False)),
    ]

    failed = 0
    for label, rows in sections:
        print(f"{'OK  ' if not rows else 'FAIL'} {label}  {len(rows)} violation(s)")
        for path, n, why in rows[:20]:
            print(f"       {rel(path)}:{n}: {why}")
            if len(rows) > 20:
                print(f"       … and {len(rows) - 20} more")
                break
        failed += len(rows)

    print(f"\nscanned {len(texts)} markdown file(s); register holds {len(baseline)} entries.")
    long_total = sum(len(long_sentences(t)) for p, t in texts.items() if not is_adr(p))
    print(f"{long_total} sentence(s) over {LONG_SENTENCE_WORDS} words outside docs/adr/ "
          f"(the register caps that number per file).")
    if failed:
        print(f"GATE FAILED: {failed} documentation violation(s) above.")
        return 1
    print("GATE PASSED: pointers resolve, every doc is indexed, no line numbers, every path is "
          "there, the environment table agrees with the code, and no register entry grew.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
