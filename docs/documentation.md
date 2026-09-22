# Documentation is a guideline, not a record

A doc exists so the next person can change that subsystem without learning it from source first. That is
the test for every sentence here: does it tell someone what to do, or what to avoid?

## The three homes

- `AGENTS.md` — the rule, in the imperative, one line where one line does it. It is read on every task, so
  it holds the decision and nothing else.
- `docs/<subsystem>.md` — the guideline: what the subsystem owns, the invariants that must stay true, the
  mechanism that holds them, and the command that would catch a lie.
- `docs/adr/` — the decision, with its alternatives and its evidence. Proof lives here and nowhere else.

A rule file that has started arguing is a rule file with a record missing. The same rule holds inside
source: `AGENTS.md → Comments: one home, plain words, only what the code cannot say`.

## What a doc owes the reader

Every indexed doc answers four questions, in this order, so a reader who stops after any one of them is
still safe:

1. **What does this own?** One paragraph naming the package or the surface, and what it deliberately does
   not cover.
2. **What must stay true?** The invariants, each one a sentence you can check by reading the code.
3. **How is it held?** The mechanism, named by the types and functions that implement it — the names a
   reader meets in the source, not a paraphrase of them.
4. **How do I check it?** A `## Checking this` section naming one command that fails when the doc stops
   being true: a package's tests, a gate, a script.

Write for the person who changes this in six months and will not read the whole file. The load-bearing
rule goes first, not the background. A doc has no fixed length: it is as long as its subsystem needs, and
`scripts/docs-check.py` has no opinion about word counts.

## Long sentences

The gate counts sentences over 45 words outside `docs/adr/`, and prints the total, the whole-tree budget
and the target of 25.

- The budget only shrinks. `make docs-check UPDATE=1` shaves at least 5% off it on every run and can never
  raise it; 25 is where it stops.
- Each file keeps its own ceiling too, so a doc cannot grow a long sentence by paying for it in another.
- A red that names the budget is not a fault in your edit. Split the long sentence in the file you are
  already standing in — a statement, then the reason on the next line — and the number moves.
- `docs/adr/` is exempt. A record is allowed to be an argument.

## The gate

`make docs-check` runs nine rules; `make test-docs-check` proves each one can fail.

1. a pointer names a heading that is there, in a file that is there;
2. every doc is listed in an index;
3. no `path.go:NN` reference — a line number is a claim about a revision;
4. every `MIKE_*` named in a doc is one something reads, and every declared variable is documented;
5. no work estimate outside an ADR;
6. an ADR is shaped like an ADR and names the rule it grounds;
7. a backticked path resolves in this tree;
8. past-tense counts and long-sentence counts only shrink, per file and across the tree;
9. every indexed doc names a check, and that check exists.

Rules 3, 4, 5 and 7 catch a doc that has drifted from the code. Rules 8 and 9 catch a doc that stopped
being something a contributor can act on.

## Adding a doc

1. Write it against the four questions above.
2. Give it a row in `AGENTS.md → Docs Index`. An unindexed doc is invisible, and rule 2 says so.
3. Name the check that would actually fail. A command nobody wrote is worse than none: rule 9 looks it up.
4. Freeze the register with `make docs-check UPDATE=1`. The gate refuses a file with no entry.
5. Delete the sentence you wrote to explain the doc. The index row says where it belongs.

## Checking this

`python3 scripts/docs-check.py` measures the docs and `make test-docs-check` proves the measurement can
fail; both run inside `make check`. Every rule quoted above is asserted in both directions by
`scripts/docs-check_test.py`, so a gate that stops failing is caught here rather than trusted.
