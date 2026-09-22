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

SUB_CHECK = """
## Checking this

`python3 scripts/check.py` catches a lie in the rule above. From its own directory it is
`cd scripts && python3 check.py`.
"""

SUB = """# Sub

The rules are in `AGENTS.md → Docs Index`.

## The rule

One rule, naming `backend/thing.go` and MIKE_FOO.
""" + SUB_CHECK

CONFIG = """# Configuration

MIKE_FOO — the only variable this fixture reads.

## Checking this

`python3 scripts/check.py` prints what the server resolved for MIKE_FOO.
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
ran = 0  # counted as they run: a printed total derived from len(CASES) went stale the first time a check was added outside CASES


def check(name, ok, detail=""):
    global ran
    ran += 1
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
    (root / "scripts" / "check.py").write_text("#!/usr/bin/env python3\n")
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


def long_prose(count: int):
    """`count` sentences over the length limit, as one block of text. Each one starts with a capital
    because the gate ends a sentence at a full stop only when a capital follows it — a run of lowercase
    lines is one sentence, however many full stops it holds."""
    return "\n" + ("Word " + "word " * 45 + ".\n") * count


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
    ("9 a doc that names no check", "9 the guideline",
     replaces("docs/sub.md", SUB_CHECK, ""), "nothing here names a check"),
    ("9 a command that does not exist", "9 the guideline",
     replaces("docs/sub.md", "scripts/check.py", "scripts/gone.py"), "does not exist"),
    ("9 a cd prefix that names no directory", "9 the guideline",
     replaces("docs/sub.md", "cd scripts &&", "cd nowhere &&"), "does not exist"),
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
        appends("docs/sub.md", " This used to be different.\n")(root)
        code, out = run(gate, [])
        check("8 past-tense sentences grow", code == 1 and "history grew" in out, out)

        (root / "docs" / "sub.md").unlink()
        code, out = run(gate, [])
        check("8 a register entry with no file", code == 1 and "no longer exists" in out, out)

        (root / "docs" / "sub.md").write_text(SUB)
        register = json.loads(gate.BASELINE.read_text())
        register["docs/sub.md"] = {"history": 0,
                                   "longsent": register["docs/sub.md"]["longsent"]}
        gate.BASELINE.write_text(json.dumps(register))
        code, out = run(gate, [])
        check("8 shrinking is allowed", code == 0, out)

        # The budget is a second, whole-tree cap: it refuses a doc that stayed inside its own ceiling,
        # because paying for a long sentence somewhere else is the point of having one.
        appends("docs/sub.md", long_prose(2))(root)
        run(gate, ["--update"])
        total = sum(len(gate.long_sentences(t)) for p, t in gate.doc_texts().items() if not gate.is_adr(p))
        register = json.loads(gate.BASELINE.read_text())
        register[gate.GLOBAL_KEY] = {"longsent": total - 1}
        gate.BASELINE.write_text(json.dumps(register))
        code, out = run(gate, [])
        check("8 the whole-tree budget refuses what no file exceeded",
              code == 1 and "whole-tree budget" in out, out)

        register[gate.GLOBAL_KEY] = {"longsent": total}
        gate.BASELINE.write_text(json.dumps(register))
        code, out = run(gate, [])
        check("8 a budget the docs sit exactly inside passes", code == 0, out)
        check("8 the budget entry is not read as a missing file", "_global is in the register" not in out, out)

    # The budget ratchets at the freeze, not at the check, so `--update` is the act that moves the docs:
    # the freeze that creates it records the truth, every later one shaves, and growth cannot be laundered.
    with tempfile.TemporaryDirectory() as tmp:
        root = fixture(Path(tmp))
        gate = prepare(root, freeze=False)
        appends("docs/sub.md", long_prose(30))(root)
        total = sum(len(gate.long_sentences(t)) for p, t in gate.doc_texts().items() if not gate.is_adr(p))
        check("8 the fixture holds enough long sentences to measure against", total > gate.LONG_SENTENCE_TARGET,
              str(total))
        run(gate, ["--update"])
        first = json.loads(gate.BASELINE.read_text())[gate.GLOBAL_KEY]["longsent"]
        check("8 the freeze that creates the budget records the truth", first == total, f"{first} vs {total}")

        run(gate, ["--update"])
        second = json.loads(gate.BASELINE.read_text())[gate.GLOBAL_KEY]["longsent"]
        check("8 a later freeze shaves the budget", 0 < second < first, f"{second} vs {first}")

        appends("docs/sub.md", long_prose(4))(root)
        run(gate, ["--update"])
        third = json.loads(gate.BASELINE.read_text())[gate.GLOBAL_KEY]["longsent"]
        check("8 a freeze cannot raise the budget after the docs grew", third <= second, f"{third} vs {second}")
        code, out = run(gate, [])
        check("8 so the growth is still reported", code == 1 and "whole-tree budget" in out, out)

        # A freeze cannot walk the number down by itself, and cannot push it under the target: the only way
        # to the target is the edit that removes a long sentence.
        settled = third
        for _ in range(6):
            run(gate, ["--update"])
            after = json.loads(gate.BASELINE.read_text())[gate.GLOBAL_KEY]["longsent"]
            check("8 repeated freezes move nothing", after == settled, f"{after} vs {settled}")
            settled = after
        check("8 the target is a floor, never a quota to freeze to",
              gate.freeze_budget(30, 26) == 26 and gate.freeze_budget(10, 10) == 10,
              f"{gate.freeze_budget(30, 26)} {gate.freeze_budget(10, 10)}")

    # The gate's own exemptions: an ADR is where history, a retired path and a figure live.
    with tempfile.TemporaryDirectory() as tmp:
        root = fixture(Path(tmp))
        gate = prepare(root)
        appends("docs/adr/0001-the-decision.md",
                "\nThis used to be `backend/gone.go`, and was roughly 3 days of work.\n")(root)
        run(gate, ["--update"])  # the record grew, which is allowed; refreeze and ask about the rest
        code, out = run(gate, [])
        check("docs/adr keeps history, retired paths and estimates", code == 0, out)

    print(f"\n{ran} checks, {len(failures)} failure(s).")
    if failures:
        print("GATE SELF-TEST FAILED: " + "; ".join(failures))
        return 1
    print("GATE SELF-TEST PASSED: every rule fires on its own violation and passes without one.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
