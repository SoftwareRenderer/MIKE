# ADR-0003 — The config-modifying specs get N lane servers, one worker each

**Status:** accepted

**Rules that follow:** `docs/testing.md → Test environment`, `docs/testing.md → E2E conventions`

## Abstract

The specs that mutate server-global state run on `ISOLATED_SHARDS` lane servers instead of one serialized
queue. `frontend/tests/lanes.ts` deals `ISOLATED_SPECS` onto the shards longest-file-first, and each shard is
a Playwright project with its own port, database, session files and one worker. The count is 2: the largest
deal that lost no test on a shard lane across six full runs. The array stays the only list a new
config-modifying spec is added to.

## Context

One `isolated` project, one server on 3001, `workers: 1`. The reason was real — those specs write
`/api/config`, install extensions and change task concurrency, so two of them on one server are each other's
race. But Playwright walks its queue in project order, so the lane also sat *behind* the chromium lane: 85
tests in 13 files, 147.8 s of test time at the tail of a 760.5 s run body that lost 32 tests — a body
inflated by each failure spending its own timeout, so the like-for-like before-and-after is the 1-shard row
below. Nothing about `settings-sessions` (3 s) needs to run after `html-preview` (33 s).

Measured on this host (8 cores, 15 GB) at the chromium lane's ceiling of 4 workers, by `make test-time`; every
run of a configuration is listed, not its best:

| Shards | Run bodies | Longest shard | Unexpected | Lost on a shard lane |
|---|---|---|---|---|
| 1 | 644.9 s | 169.6 s | 19 | never |
| 2 | 566.5 · 556.1 · 553.1 · 569.3 · 532.0 · 562.2 s | 99.4-103.9 s | 15-21 | never, in 6 runs |
| 3 | 527.6 · 543.5 · 531.0 · 553.2 s | 84.9-94.7 s | 17-21 | once in four |
| 4 | 538.5 s | 79.1 s | 19 | never |

No row is green and no row can be. Between 15 and 21 failures per run rotate through the chromium lane's
turn-bound specs at every shard count including 1, and the run before this change lost 31 of them. So the
claim a shard count can be judged on is the one it owns: **a shard must not add a failure to the specs dealt
to it**. That is the last column, and it is the column that rejects 3.

What 3 costs is visible in the lane's dearest file. `html-preview`'s own summed test time is 36.6 s at one
shard and 38.3-40.1 s at two, but 49.3-60.2 s at three, where its slowest test ran 18.8 s against the 15 s
`expect` ceiling inside it — and that wait is what failed in one of the four three-shard runs. Three shards
bought 21.9 s of median run body (537.2 s against 559.1 s), less than the 25.6 s those four three-shard runs
spread across, and spent the slack of the one file with least to spare.

Memory is not the scarce thing, contrary to the first reading of this change. Available memory never fell
below 12.7 GB across the candidate runs at 5 s sampling, and 25 browsers was the most any run held. The 4 GB
idle that `docs/testing.md → Tests that fail only under load` records was an 11 GB build cache under `/tmp`,
since removed. What runs out is timing slack inside the specs.

## Decision

`ISOLATED_SHARDS` is 2. `isolatedLanes()` sorts `ISOLATED_SPECS` by measured per-file seconds and deals them
round-robin, so shard k takes the k-th and (k+N)-th dearest file. The deal is a function of the array and the
count, so it is stable across runs and rebalances itself when a spec is added. Each shard is a project with
`workers: 1` — the invariant the design keeps, because a lane's server has global state and two writers on it
is the bug the lane exists to prevent.

Playwright's top-level `workers` is the cap across every project, so it is the number of browsers the machine
runs at once, and it stays at the chromium lane's ceiling of 4 rather than becoming `4 + N`. A lane worker
takes a slot when the chromium lane stops using one, so the shards start at the tail and run beside each
other. Widening the pool looks like a win and is not one. Six workers with two shards measured 501.4 s and 21
unexpected, and eight workers with four shards measured 451.4 s and 14 — but that second run skipped the
remaining 24 tests of `chat-core` after its first one failed, so it covered less rather than losing less, and
both widened runs lost a spec on a shard lane (`extension-frame-channel` at 6 workers, `html-preview` at 8).
Trading coverage for 10-19% of the wall clock is the trade `docs/testing.md` already refuses for the chromium
lane.

`MIKE_E2E_ISOLATED_SHARDS` re-deals on a machine measuring its own ceiling; a value outside 1..N names the
variable and refuses the run. Adding a config-modifying spec is one string in `ISOLATED_SPECS`: the chromium
lane's `testMatch` excludes the whole array and the shards are dealt out of it, so there is nothing else to
keep in step. The table of per-file seconds only weighs the deal — an unlisted spec gets the median.

## Consequences

Two shards cut the lane's queue from 169.6 s (one server, 13 files) to 99-104 s on its longest shard, and the
run body from 644.9 s to 532-569 s at a comparable failure count. One file is the floor of any N ≥ 2:
`html-preview`, 36.6 s of one-shard test time sitting on a shard whose other six files total 44 s. Splitting
it is the only change that makes a third shard worth measuring again, and it is the change that would make
the third shard honest rather than lucky.

A merge that landed after these runs replaced fixed sleeps in the lane specs with condition waits and cut two
of the per-file entries by a third. The same 2-shard deal then measured 538.0 s with shard wall clocks of
99.7 s and 85.7 s and still lost no test on a shard lane, so the count stands; what such a merge moves is the
weighing in `lanes.ts`, not the decision.

Each shard is a server, so a run holds `1 + N` ports and `1 + N` databases. Teardown stops each by identity —
the PID file plus a `/proc` argv match on `--data-dir <dir>`, token for token — because with a dozen lanes on
one `/tmp`, a prefix match would let one shard's directory answer for its neighbour's. A run started with a
lower shard count sweeps the maximum deal too: a shard-3 server started by hand and left standing was stopped
by the next 2-shard run's setup, leaving nothing on 3003 and no `/tmp/kanban-test-data-isolated-3`. Every lane's
seed project is the same fixture-repo copy, so the lanes share one repository and one worktree root. That is
safe while `GarbageCollect` runs only at server startup, which is while no lane has a task to lose; a lane
that starts tasks heavily, or a GC that starts running periodically, reopens it.

What reopens the count: `html-preview` split, a host whose chromium ceiling is above 4 workers and whose
turn-bound specs stop rotating, or a lane spec whose state is global across *lanes* — a registry fixture two
shards install differently, which no shard count can share and which must then own its own server. Re-measure
with `MIKE_E2E_ISOLATED_SHARDS=3 make test-time` three times and read the lane rows: `make test-time` prints
each lane's wall clock, and a candidate is only a candidate while no lane loses a test.
