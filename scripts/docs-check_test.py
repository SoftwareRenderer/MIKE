#!/usr/bin/env python3
"""`scripts/docs-check.py` in both directions.

A gate that never fails is indistinguishable from a gate that cannot fail, so every rule is run twice:
once against a fixture it must pass, once against the same fixture with exactly that rule's violation
injected. `test-archgate` holds the same bargain for the structural gate.

    make test-docs-check
"""

import contextlib
import importlib.util
import io
import json
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent

AGENTS = """# AGENTS.md

Rules live here, mechanism in `docs/`, decisions in `docs/adr/`.

## Docs Index

| File | Subsystem |
|---|---|
| `docs/sub.md` | the subsystem this fixture is about |
| `docs/configuration.md` | the variables this fixture reads |
"""

SUB = """# Sub

The rules are in `AGENTS.md → Docs Index`.

## The rule

One rule, naming `backend/thing.go` and MIKE_FOO.
"""

CONFIG = """# Configuration

MIKE_FOO — the only variable this fixture reads.
"""

ADR_README = """# Architectural Decision Records

| # | Decision | Status |
|---|---|---|
| [0001](0001-the-decision.md) | The fixture's one decision | accepted |
"""

ADR_0001 = """# ADR-0001 — The fixture's one decision

**Status:** accepted

**Rules that follow:** `docs/sub.md → The rule`

## Abstract

One choice, and what it changes.

## Context

Why a choice was needed at all.

## Decision

One mechanism, named, in the present tense.

## Consequences

What this makes impossible, and what would reopen it.
"""

STARTUP = """package startup

const EnvFoo = "MIKE_FOO"
"""

THING = "package thing // reads MIKE_FOO\n"

failures = []


def check(name, ok, detail=""):
    print(f"{'OK  ' if ok else 'FAIL'} {name}{'' if ok else '  — ' + detail}")
    if not ok:
        failures.append(name)


def load_gate():
    spec = importlib.util.spec_from_file_location("docs_check", HERE / "docs-check.py")
    gate = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(gate)
    return gate


def fixture(root: Path, readme: bool = False):
    """A tree the gate must pass: one rule, one subsystem doc, one variable, one ADR."""
    (root / "docs" / "adr").mkdir(parents=True)
    (root / "backend" / "startup").mkdir(parents=True)
    (root / "scripts" / "docscheck").mkdir(parents=True)
    (root / "AGENTS.md").write_text(AGENTS)
    (root / "docs" / "sub.md").write_text(SUB)
    (root / "docs" / "configuration.md").write_text(CONFIG)
    (root / "docs" / "adr" / "README.md").write_text(ADR_README)
    (root / "docs" / "adr" / "0001-the-decision.md").write_text(ADR_0001)
    (root / "backend" / "startup" / "config.go").write_text(STARTUP)
    (root / "backend" / "thing.go").write_text(THING)
    if readme:
        (root / "README.md").write_text("# Mike\n\n## Running it\n\n`make run`.\n")
    return root


def prepare(root: Path, *, freeze: bool = True):
    """Point a fresh gate module at `root`, and freeze its register at the fixture's counts."""
    gate = load_gate()
    gate.ROOT = root
    gate.DOCS = root / "docs"
    gate.ADR_DIR = root / "docs" / "adr"
    gate.AGENTS = root / "AGENTS.md"
    gate.CONFIG_DOC = root / "docs" / "configuration.md"
    gate.BASELINE = root / "scripts" / "docscheck" / "baseline.json"
    gate.CODE_SEARCH = ["backend", "scripts", "Makefile"]
    if freeze:
        run(gate, ["--update"])
    return gate


def run(gate, argv):
    out = io.StringIO()
    argv_old = sys.argv
    sys.argv = ["docs-check.py", *argv]
    try:
        with contextlib.redirect_stdout(out):
            code = gate.main()
    finally:
        sys.argv = argv_old
    return code, out.getvalue()


def appends(rel, text):
    def mutate(root):
        p = root / rel
        p.write_text(p.read_text() + text)
    return mutate


def removes(rel, needle):
    def mutate(root):
        p = root / rel
        p.write_text(p.read_text().replace(needle, ""))
    return mutate


def writes(rel, text):
    def mutate(root):
        p = root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)
    return mutate


def replaces(rel, old, new):
    def mutate(root):
        p = root / rel
        p.write_text(p.read_text().replace(old, new))
    return mutate


# (name, rule label expected in the report, mutation, text expected in the report)
CASES = [
    ("1 pointer to a heading that is not there", "1 pointers",
     appends("docs/sub.md", "\nSee `docs/configuration.md → Nonexistent`.\n"), "names no heading"),
    ("1 pointer to a file that is not there", "1 pointers",
     appends("docs/sub.md", "\nSee `docs/gone.md → Anywhere`.\n"), "a file that is not in the tree"),
    ("1 pointer naming the middle of a heading", "1 pointers",
     appends("docs/sub.md", "\nSee `docs/sub.md → rule`.\n"), "names no heading"),
    ("1 nested pointer whose second step names no heading", "1 pointers",
     appends("docs/sub.md", "\nSee `AGENTS.md → Docs Index → Test environment`.\n"), "names no heading"),
    ("2 a doc no index lists", "2 indexes",
     writes("docs/orphan.md", "# Orphan\n\nA doc nobody points at.\n"), "is not listed in AGENTS.md"),
    ("2 an ADR the ADR index omits", "2 indexes",
     writes("docs/adr/0002-unindexed.md", ADR_0001.replace("ADR-0001", "ADR-0002")),
     "is not listed in docs/adr/README.md"),
    ("3 a `file.go:NN` reference", "3 line numbers",
     appends("docs/sub.md", "\nSee `backend/thing.go:42`.\n"), "line-number reference"),
    ("3 a `file.go:NN` reference inside an ADR (only three rules are exempt there)", "3 line numbers",
     appends("docs/adr/0001-the-decision.md", "\nSee `backend/thing.go:42`.\n"), "line-number reference"),
    ("4 a variable nothing reads", "4 environment",
     appends("docs/sub.md", "\n`MIKE_NOT_A_SWITCH` selects the permissive branch.\n"),
     "read by nothing in this tree"),
    ("4 a startup variable documented nowhere", "4 environment",
     replaces("backend/startup/config.go", 'const EnvFoo = "MIKE_FOO"',
              'const EnvFoo = "MIKE_FOO"\n\nconst EnvBar = "MIKE_UNDOCUMENTED"'),
     "documented nowhere"),
    ("5 a work estimate in a rules document", "5 estimates",
     appends("docs/sub.md", "\nThis is roughly 3 days of work.\n"), "work estimate"),
    ("6 an ADR with no abstract", "6 ADR shape",
     replaces("docs/adr/0001-the-decision.md", "## Abstract\n\nOne choice, and what it changes.\n\n", ""),
     "missing ## Abstract"),
    ("6 an ADR missing its consequences", "6 ADR shape",
     replaces("docs/adr/0001-the-decision.md", "## Consequences\n\nWhat this makes impossible, and what "
              "would reopen it.\n", ""), "missing ## Consequences"),
    ("6 an ADR with no status", "6 ADR shape",
     replaces("docs/adr/0001-the-decision.md", "**Status:** accepted\n", ""),
     "missing a `**Status:**"),
    ("6 a superseded pointer that names nothing", "6 ADR shape",
     replaces("docs/adr/0001-the-decision.md", "**Status:** accepted", "**Status:** superseded by 9999"),
     "which does not exist"),
    ("6 a record that names no rule", "6 ADR shape",
     replaces("docs/adr/0001-the-decision.md",
              "**Rules that follow:** `docs/sub.md → The rule`\n\n", ""),
     "name the rule it grounds"),
    ("7 a backticked path that resolves to nothing", "7 paths",
     appends("docs/sub.md", "\nSee `backend/gone.go`.\n"), "resolves to nothing"),
    ("8 a file not yet in the register", "8 the ratchet", None, "not in scripts/docscheck/baseline.json"),
    ("8 a sentence over the length limit", "8 the ratchet",
     appends("docs/sub.md", "\n" + " ".join(["word"] * 46) + ".\n"), "longsent grew"),
]


def main() -> int:
    # The fixture passes, and an unfrozen register is itself a finding.
    for label, extra in (("clean fixture", None), ("clean fixture with a root README", {"readme": True})):
        with tempfile.TemporaryDirectory() as tmp:
            root = fixture(Path(tmp), **(extra or {}))
            if label.endswith("root README"):
                (root / "AGENTS.md").write_text(
                    AGENTS + "\nThe packaged README owns the run story: `README.md → Running it`.\n")
            gate = prepare(root, freeze=False)
            code, out = run(gate, [])
            check(f"{label}: refuses an unfrozen register", code == 1 and "8 the ratchet" in out, out[-200:])
            code, out = run(gate, ["--update"])
            code, out = run(gate, [])
            check(f"{label}: passes once frozen", code == 0 and "GATE PASSED" in out, out[-400:])

    for name, label, mutate, needle in CASES:
        with tempfile.TemporaryDirectory() as tmp:
            root = fixture(Path(tmp))
            gate = prepare(root, freeze=name != "8 a file not yet in the register")
            if mutate:
                mutate(root)
            code, out = run(gate, [])
            ok = code == 1 and f"FAIL {label}" in out and needle in out
            check(name, ok, f"exit={code} needle={needle!r}\n{out}")

    # Rule 8 is a ratchet, so it needs both directions of its own: growth and a stale entry.
    with tempfile.TemporaryDirectory() as tmp:
        root = fixture(Path(tmp))
        gate = prepare(root)
        appends("docs/sub.md", "\nA second line, and a third.\n")(root)
        code, out = run(gate, [])
        check("8 words grow past the ceiling", code == 1 and "words grew" in out, out)

        appends("docs/sub.md", " This used to be different.\n")(root)
        code, out = run(gate, [])
        check("8 past-tense sentences grow", code == 1 and "history grew" in out, out)

        (root / "docs" / "sub.md").unlink()
        code, out = run(gate, [])
        check("8 a register entry with no file", code == 1 and "no longer exists" in out, out)

        (root / "docs" / "sub.md").write_text(SUB)
        register = json.loads(gate.BASELINE.read_text())
        register["docs/sub.md"] = {"words": len(SUB.split()) + 50, "history": 0,
                                   "longsent": register["docs/sub.md"]["longsent"]}
        gate.BASELINE.write_text(json.dumps(register))
        code, out = run(gate, [])
        check("8 shrinking is allowed", code == 0, out)

    # The gate's own exemptions: an ADR is where history, a retired path and a figure live.
    with tempfile.TemporaryDirectory() as tmp:
        root = fixture(Path(tmp))
        gate = prepare(root)
        appends("docs/adr/0001-the-decision.md",
                "\nThis used to be `backend/gone.go`, and was roughly 3 days of work.\n")(root)
        run(gate, ["--update"])  # the record grew, which is allowed; refreeze and ask about the rest
        code, out = run(gate, [])
        check("docs/adr keeps history, retired paths and estimates", code == 0, out)

    print(f"\n{len(CASES) + 9} checks, {len(failures)} failure(s).")
    if failures:
        print("GATE SELF-TEST FAILED: " + "; ".join(failures))
        return 1
    print("GATE SELF-TEST PASSED: every rule fires on its own violation and passes without one.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
