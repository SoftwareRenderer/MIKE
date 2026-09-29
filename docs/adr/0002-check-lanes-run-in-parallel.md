# ADR-0002 — `make check` runs its two halves as lanes

**Status:** accepted

**Rules that follow:** `AGENTS.md → Testing`, `docs/testing.md → Measured gate cost`

## Abstract

`make check` runs its eleven sub-gates as two concurrent lanes — a Go lane, and a docs-plus-frontend lane —
started by `scripts/check-lanes.sh`, one log per lane and one table of lane, seconds and verdict at the end.
The target's name, its invocation, its eleven steps and its closing line are unchanged. Its wall clock is the
slower lane instead of the sum of the eleven, and a failing step stops its own lane, so both halves of the
gate report on every run instead of only the first half to go red.

## Context

`check` was one recipe of eleven lines, each a `$(MAKE)` call, so the gate cost what all eleven cost in a row.
`docs/testing.md → Measured gate cost` prices that shape at 347.73 s for one whole pass on the 8-CPU dev host
and names the two rows that dominate it: `test-go` at 147.47 s and `test-frontend` at 194.66 s. Neither waits
on the other.

The two halves share no file. The Go lane writes `mike` and the Go build cache; the frontend lane writes
`frontend/node_modules/.cache`; the two docs gates read `docs/`, `AGENTS.md`, `Makefile` and the source tree,
and `test-docs-check` writes its fixtures under `tempfile.TemporaryDirectory()`, never in the tree. `go:embed`
in `assets.go` does read `frontend/dist`, which is why the lane table's build step is the bare
`go build -trimpath -o mike .` and never `make build`: only `make frontend` and `npm run build` write that
directory, and neither is in the gate. Measured, not assumed — list every file newer than the pass's start
around one two-lane run:

```bash
touch /tmp/check-marker && make check; find . -newer /tmp/check-marker \
  -not -path './.git*' -not -path './node_modules*' -not -path './frontend/node_modules*' \
  -not -path './extensions/*'
```

That prints `mike` and the runner's own `data/check-lanes/`, and nothing under `frontend/dist`, `backend/`,
`pkg/`, `scripts/` or `frontend/src/`.

The serial shape had a second cost no timing table showed. make abandons a recipe at its first failing line —
`-k` continues *other targets*, not the rest of that recipe — so a red `test-go` meant the three gates after
it never ran, and anyone on a host where the container suites are red
(`docs/testing.md → A machine that cannot start a container`) read the Go half's output and never learned
whether the frontend half was green. `scripts/test-timings.sh` works around this by timing `make -i check`,
an instrument-only mode whose exit code means nothing.

## Decision

`scripts/check-lanes.sh` holds the gate's step table — `lane`, `step`, `what it is for`, `command`, one line
per sub-gate — and starts one background loop per lane. `make check` runs the runner's own test first, then
the runner, and the runner prints the gate's closing line when every lane came back green.

Each lane's steps run in table order into `data/check-lanes/<lane>.log`, and a lane's verdict is the exit code
of its first failing step, written to `<lane>.exit` when the lane ends. The parent reads those files back
instead of a wait status, so a lane that was killed or never started is a named failure and not a green row,
and a step a stopped lane never reached prints `skip` rather than green. A failing step stops its own lane and
no other. The parent prints a heartbeat every 30 s naming where each lane has got to, then each lane's log,
then the table.

The lanes are `go` — `lint`, `archgate`, `test-archgate`, `bridge-check`, `build`, `check-release`, `test-go` —
and `frontend` — `docs-check`, `test-docs-check`, the one typecheck compile, `test-frontend`. The docs gates
ride with the frontend because they read `frontend/src/` and `docs/` and nothing the Go lane writes. `test-go`
keeps the `-parallel 4` the `Makefile` already passes, so the split adds one running lane to the machine
rather than one per gate.

**Not `make -j`.** `-j2` over `check: check-go check-frontend` would overlap the same two halves, and it costs
three things this runner does not carry: jobserver bookkeeping, one interleaved stdout for both halves, and no
way to say "stop this half at its first failure, run the other half to its end" — `-k` is global, so it either
stops both halves at the first red or runs both through every red. A `-j` log needs output ordering plus a
wrapper that captures each target's text before anyone can say which line was which lane's, and the verdict of
a killed job arrives as make's own exit code rather than as a file naming the lane. None of it is testable the
way `scripts/check-lanes_test.sh` tests this runner: a forced lane failure, a lane killed mid-step, a lane
that left no verdict, a lane and step named twice, and a child `make` that inherits `-i` from its parent. That
last hazard is live here — `scripts/test-timings.sh` *does* run `make -i check` — so the runner clears
`MAKEFLAGS` before starting any lane, because a runner whose child makes answer 0 for a failing gate prints a
green table over a red gate.

## Consequences

Measured the day the split landed, same host, both shapes running the same eleven commands on a warm tree with
staticcheck and deadcode installed (`scripts/lint-tools.sh` prints either install command, and `lint` stops
the Go lane without them):

| wall (s) | the shape | how to re-run it |
|---|---|---|
| 339.00 | eleven gates in one lane, run 1 | `scripts/check-lanes.sh` with `MIKE_CHECK_LANES` holding all eleven steps in one lane |
| 345.58 | eleven gates in one lane, run 2 | the same |
| 281.06 | two lanes, run 1 | `make check` |
| 282.43 | two lanes, run 2 | `make check` |
| 283.30 | one whole two-lane `make check`, through the instrument | `make test-time LANES=check` |

**The saving is 18%, not the 45% that `max` over the lane sums promises.** Run alone, the Go lane's steps
sum to 147.34 s (`test-go` is 133.31 of them) and the frontend lane's to 191.33 s (`test-frontend` 185.52), so
191 s is the floor a perfectly-scaled box would give. What happens instead is that `go test`'s default package
parallelism and vitest's 8 fork workers each fill the 8 cores on their own — 7 `.test` binaries and 8 forks
were alive at once mid-run — so beside its sibling `test-go` took 263.67 s against 133.31 s and
`test-frontend` 248.60 s against 185.52 s. The minute saved is the part of the gate that waits on files
rather than on a core: the docs gates, `bridge-check`, the two builds. Re-read these rows with
`make check`, which prints both lanes' steps and seconds, and price a single gate alone with
`make test-time LANES=gates`.

The split changed no verdict a host could act on: every run of either shape printed the same 30 red tests
that are the two host facts this file names, plus between none and two that rotate. The serial runs added none
and one; the four two-lane runs added one, one, one and two. Every rotating name is a load-list mechanism —
the `t.TempDir()` cleanup race, or a frame its test stopped waiting for while the box held 7 `.test` binaries
and 8 vitest forks — and `docs/testing.md → Tests that fail only under load` is where each is recorded, with
the run-alone re-check that clears it. The extra lane is real pressure and that rate is its cost; the knob, if
the rate ever needs paying down, is one lane's worker count (`VITEST_MAX_WORKERS`, or `go test -p`), not the
split.

The critical path moved to the frontend lane, so the next thing to attack on this gate is vitest's start-up
(`docs/testing.md → vitest`), not `backend/server`.

A lane's output now arrives when the lane ends rather than as it is written, so `make check` prints heartbeats
and then a wall of text; `data/check-lanes/` holds the same text per lane for anyone watching with `tail -f`.
The per-step seconds inside a lane are inflated by the other lane, so they name the slow step of *that run*
and not the price of a gate.

What would reopen it: a step that reads what the other lane writes, which turns the lane wall clocks into a
race instead of a measurement; a machine with fewer cores than the two lanes want — these two want eight each,
and on a 2-core box the arithmetic above says the split is a wash; a rotation rate among the load-sensitive
tests that a clean host cannot live with; or a runner whose verdict cannot be trusted, which is what
`scripts/check-lanes_test.sh` is for.
