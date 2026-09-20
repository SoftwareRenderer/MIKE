# ADR-0001 — Key decisions are recorded

**Status:** accepted

**Rules that follow:** `AGENTS.md` (its first paragraph and the Docs Index), `docs/adr/README.md`

## Abstract

Every key technical decision gets its own numbered file in `docs/adr/`, written at the moment the choice is made. The rule stays short in `AGENTS.md` and the record holds why the choice was made, what it costs, and what would reopen it. Records are found through the index in `docs/adr/README.md`, not by being linked from the rules that rest on them, and none is written afterwards for a choice that has already shipped.

## Context

Rules, the reason behind each rule, and background history were written down in the same places: most rules in `AGENTS.md` had a measurement or a rejected idea underneath them, several files under `docs/` had paragraphs defending a choice, and `docs/` also held a one-off report of the numbers behind the code-shape checks. Three things went wrong. The rule files only got longer. Nobody could say which file a new sentence belonged in. Numbers stayed on the page after the code that produced them had moved on.

## Decision

Write each key decision in its own numbered file under `docs/adr/`, at the time the choice is made. Each file says what was chosen, what it replaces, what it costs, and which command or test shows the result. `AGENTS.md` keeps the short rule and the subsystem file under `docs/` keeps how the code works — neither carries the argument, and neither has to link the record, because `docs/adr/README.md` lists what exists.

Two things follow. We do not write these files for decisions taken earlier: nobody remembers what was considered at the time, so a file written now would be a defence rather than a record, and an older rule that needs explaining states its current reason in its subsystem doc instead. And a file is not rewritten when we change our mind: the new decision gets a new number and the old file's status becomes `superseded by NNNN`.

## Consequences

Every sentence has one obvious home: a rule in `AGENTS.md`, how it works in `docs/<name>.md`, why it was decided this way in `docs/adr/`. `scripts/docs-check.py` checks the parts a machine can check — links and section names that still exist, every doc listed in an index, no `path.go:NN` references, documented environment variables matching the code, no time estimates outside a decision file, the file format from `docs/adr/README.md`, and a per-file limit on length and on past-tense sentences so the rule files cannot grow back into essays.
Past tense, renamed paths and out-of-date figures are allowed only under `docs/adr/`, which is what keeps the other two kinds of file short. Deciding which home a sentence belongs in stays a judgement call for whoever makes the edit; the script does not try. Re-check with `make docs-check` and `make test-docs-check`.
