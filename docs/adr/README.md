# Architectural Decision Records

A record of a decision: what was chosen, what it replaced, what it costs, and the command or test that re-measures it. Why this directory exists, and why a record is written when the choice is made, is ADR-0001.

`AGENTS.md` holds the rule, `docs/<subsystem>.md` holds the rule and the invariants that follow from
it, and this directory holds **why the rule is this shape**. A subsystem doc that catches itself
arguing — a measurement, a rejected alternative, a sentence in the past tense — is describing a decision
that was never written down: write the record now, while the alternatives are still known.

## What belongs here, and what does not

| Belongs | Does not belong |
|---|---|
| A choice between options someone would plausibly choose differently (a port vs. stdio, a hash vs. a position, `memory.max` vs. `memory.high`) | A rule with no plausible alternative ("never log the master password") — that is `AGENTS.md` |
| The measurement the decision rests on, with the command that re-runs it | The current list of variables, routes or files — that is a table of record in the subsystem doc, or generated |
| What the decision gives up, and the case that would reopen it | A work plan or a backlog: open one as `**Status:** proposed` |
| The history of the decision itself: what it replaced, and what broke | History in a rule document, where it can only go stale |
| — | A decision taken before this practice existed. Nobody remembers what was weighed then, so give the current reason in the subsystem doc instead (ADR-0001) |

## The shape

`NNNN-kebab-case.md`, numbered once and never reused. Six parts, no more:

```markdown
# ADR-NNNN — The bridge gets no port inside the container

**Status:** accepted

**Rules that follow:** `docs/sandbox-bridge.md → Why the bridge never gets a port`

## Abstract
Two or three sentences: what was decided and what it changes. Enough that a reader can stop here.

## Context
What the tree looked like and what forced a choice. Two or three sentences.

## Decision
One paragraph, in the present tense, naming the mechanism.

## Consequences
What this makes cheap, what it makes impossible, and what would reopen it.
```

- **Status** is `accepted`, `proposed` (a decision not yet taken — this is where a plan lives),
  `superseded by NNNN`, or `deprecated`. A record is never edited after the fact: a new decision that
  reverses an old one gets a new number, and the old one's status is changed to point at it. That is
  the only history an ADR directory keeps on purpose.
- **Abstract** is the whole decision in two or three sentences, for the reader who needs the answer and
  not the argument. It states the choice and its effect; the reasoning belongs below it.
- **Evidence** is inline where it is short — a figure, a host, the date it was measured — and always
  carries the command or the test that re-measures it, so a reader on a different machine knows which
  half to distrust.
- A record names the rule or subsystem doc it grounds (`**Rules that follow:**`), and that is the usual
  extent of the linking: the doc states the rule and does not carry the argument or link the record. A
  reader who wants the reasoning comes to the index here. `scripts/docs-check.py` refuses a record that
  names no rule, and a superseded pointer that names nothing.
- Past tense, superseded paths and figures that a newer measurement will beat are welcome **here** and
  refused elsewhere; that is what the gate's limits on length and past-tense sentences enforce.

## Index

| # | Decision | Status |
|---|---|---|
| [0001](0001-key-decisions-are-recorded.md) | Key decisions get a numbered file, written when the choice is made and never backfilled | accepted |
| [0002](0002-chat-jump-floors-its-page.md) | A chat jump floors its page at the prompt it named instead of walking the conversation five answers at a time | accepted |
