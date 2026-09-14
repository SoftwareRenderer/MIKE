# Architecture Review — The Measured Baseline

`make archgate`'s comment points here: *scripts/archgate holds the reasons; docs/architecture-review.md holds
the numbers that justified them*. This is those numbers, the command that prints each one, and the readings
that turned out to be wrong. Every figure is a command's output, so a row the command no longer prints is
stale and gets replaced rather than defended.

Two snapshots of the same tree:

- **before** — `a2a8abf56`, the commit the review was taken at.
- **after** — `HEAD`: the `Deps` constructor, the tool-dispatcher extraction, the `backend/managers` →
  `backend/api` rename, and three archgate rules added on top of the ten that existed.

A row marked **held** survived the round unchanged, which is the reason to have written it down.

Commands are scoped `./backend/... ./pkg/... .` — `./...` reaches the installed extensions under
`extensions/` and fails for reasons that are not this tree's (`AGENTS.md → Known gaps`).

## 1. Churn is three packages deep

`git log --oneline <ref> -- <path> | wc -l` counts commits that touched the path:

| Path | before | after | |
|---|---|---|---|
| `backend/exec` | 434 | 438 | +4, this round |
| `backend/server` | 365 | 368 | +3 |
| `backend/managers` (now `backend/api`) | 253 | 255 | +2: the rename, and the constructor commit's two call sites there |
| next highest (`backend/extensions`) | 166 | 167 | unchanged in rank |
| commits in the repo | 3,616 | 3,626 | +10 |

`git log --oneline HEAD -- backend/exec backend/server backend/managers backend/api | wc -l` → **1,003**
distinct commits touched at least one of the three (999 at `a2a8abf56`) — over a quarter of the repo's
history, and the per-package sums above overlap because a commit crosses packages. `git log --format=
--name-only <ref> -- <dir> | grep '\.go$' | sort | uniq -c | sort -rn` then puts the edits inside a file:

| File | before | after |
|---|---|---|
| `backend/exec/loop.go` | 233 | 233 **held** |
| `backend/exec/executor.go` | 191 | 194 |
| `backend/exec/tools.go` | 72 | 73 |
| `backend/server/server.go` | 130 | 132 |
| `backend/managers/task.go` (now `backend/api/task.go`) | 135 | 136 |

**Consequence.** An edit to `loop.go`, `server.go` or `task.go` is not a local edit — it is the subsystem's
behaviour, re-litigated. That is the argument for gating shape and size mechanically instead of leaving
them to review, which is what §5 records as machine-gated.

## 2. The hottest files held three very large functions

Lines are the span from `func` to the closing brace — the span archgate's `funclen` measures, so
`make archgate RULE=funclen` re-checks that half. Branches count `if`/`for`/`range`/`switch`/`select` nodes
plus every `case` arm; depth counts each of those, a `case` arm and a func literal as one level. Both are
one-off `go/ast` walks, and the definitions are printed because the numbers move with them — the shape claim
(hundreds of branches, seven-to-eight levels) is the durable half, the exact integer is not.

| Function | before | after |
|---|---|---|
| `TaskExecutor.finalizeTurn` (`loop.go`) | 574 ln / 71 branches / depth 8 | 574 / 71 / 8 **held** |
| `TaskExecutor.executeTask` (`loop.go`) | 555 / 73 / 7 | 555 / 73 / 7 **held** |
| `TaskExecutor.executeToolInner` (`tools.go`) | 425 / 87 / 7 | gone — dispatched through `toolRunner` |
| `TaskExecutor` methods / files (`grep -rhE '^func \([a-z]+ \*TaskExecutor\)' backend/exec/*.go`) | 125 / 14 | 117 / 15 |
| `toolRunner` methods / files | — | 21 / 3, largest 116 ln / 15 branches / depth 2 |

**Consequence.** The two turn-loop functions are still the largest in the tree and sit on the `funclen`
baseline (ceiling 400): `funclen 2 baselined violation(s) across 2051 checked`. The extraction is the
evidence the split protocol works and the ratchet holds — 425 lines and 87 branches became 21 methods whose
largest is 116 lines at depth 2, in a collaborator holding no lock and no `executionState` — and a new giant
on the way would have been a new violation rather than a shrug. Clearing a baseline entry means shrinking
the function; raising the ceiling is not a move the rule allows.

## 3. Construction was bypassed, and two live defects were the result

| | before | after |
|---|---|---|
| `NewTaskExecutor` signature | 8 positional concrete params | `Deps{…}`, 8 named fields |
| `TaskExecutor{…}` literals in `backend/exec/*_test.go` (`grep -h 'TaskExecutor{' \| wc -l`) | 85, across 19 files | 0 |
| `NewTaskExecutor` calls in `backend/exec` tests | 0 | every fixture, through `newTestExecutorWith` (`testsupport_test.go`): 158 call sites in 32 of the 58 test files |
| literals anywhere in `backend/exec` | 86 | 1 — the constructor's own (`execctor`: *0 violations across 1 checked*) |

The consequences were bugs, not style:

- **The worktree slow-wait report ran in no test.** `te.worktreeMu.Configure(taskLockSlowWait, …)` had exactly
  one call site — `NewTaskExecutor` — and no test in `backend/exec` called the constructor (the two calls in
  the whole tree were in the handlers package's tests), so the report `AGENTS.md → Concurrency` names as the
  substitute for a seconds-scale git timeout was never wired in a test binary. Every fixture now runs it,
  because every fixture is built by the constructor. What is still true: nothing asserts the *sentence*. The
  mechanism's own tests are `pkg/keylock/keylock_test.go` and `backend/git/merge_lock_wait_test.go`;
  `taskLockSlowWait` at `backend/exec/executor.go:341` has no test naming it.
- **Production carried a test-shaped nil guard.** `ArmCommitAndDone` created `commitDone.tasks` on its first
  write (`backend/exec/merge.go:1086`, before) under the comment *"Created here, not in NewTaskExecutor: tests
  build a TaskExecutor as a struct literal."* That is a nil-map write waiting on a second write path, plus a
  defence comment in the shape `AGENTS.md → Comments` deletes on sight. The map is made once in
  `newMergeOrchestrator` (`backend/exec/merge.go:66`); the guard and the comment are gone.

**Consequence, and the rule that keeps it.** A dependency added to `TaskExecutor` used to arrive at one call
site and miss 85 fixtures, which is how the first defect stayed invisible. `execctor` reports any composite
literal outside `executor.go` with an **empty baseline** — one more literal in `backend/exec` fails `make
check` instead of joining a register.

## 4. The tests are enormous and welded to the layout

| | before | after |
|---|---|---|
| test lines / source lines, `backend` + `pkg` (`find … -name '*_test.go' \| xargs wc -l`) | 101,212 / 56,333 | 101,123 / 56,472 |
| test funcs in `backend/exec` (`grep -hE '^func Test'`) | 619 | 619 **held** |
| packages with test files (`go list -f '{{if .TestGoFiles}}…'`) | 34 of 36 | 34 of 36 **held** |
| packages with an external test package (`{{if .XTestGoFiles}}`) | 1 — `backend/toolresult` | 1 **held** |

**Consequence.** 33 of the 34 test packages are in-package, so every test file sits inside the layout it is
protecting: moving methods onto a collaborator means editing the tests that reach through the old receiver.
This round spent 24 of its 112 changed files turning 85 literals into 158 constructor calls — 298 lines added,
298 deleted, in `backend/exec` tests, for no behaviour change. That is the tax a split pays here, and the
`Deps` struct is what keeps it a one-time tax rather than a per-fixture one. It is not an argument for fewer
tests: 619 funcs in the package being cut are why the cut could be attempted. `backend/toolresult` is still
the only package whose tests can watch it from outside.

## 5. The split protocol was prose; three rules hold it now

`backend/exec/executor.go:42-57` is the protocol: split by moving methods onto a collaborator
`NewTaskExecutor` constructs and holds; **never split `executionState`** — its sandbox fields must be read as
one snapshot, because *a mix of old and new sandbox callbacks reads a stopped container through a live
client*; pass a lock as `func() func()` / `func(string) func()`, never `*sync.Mutex`; give a collaborator
**no way to take `taskMu` or `sandboxMu`**, only a func value whose body takes the lock on this side.

| Sentence | Rule | Baseline |
|---|---|---|
| built by `NewTaskExecutor`, nowhere else | `execctor` | empty |
| a collaborator holds no `*TaskExecutor` | `execseam` | 2 — `diagnosticsPass.te`, `handleLoopDetected`'s `te` param |
| a lock is never passed by pointer | `execseam` | 0 |
| `taskMu` / `sandboxMu` stay with their owner (`TaskExecutor` / `executionState`) | `execseam` | 0 |
| a function stays the size it is | `funclen`, 400 ln | 2 — `executeTask`, `finalizeTurn` |

archgate went from 10 rules and 10 baseline files to 13 and 13
(`grep -c 'Name:' scripts/archgate/rules.go`; `git ls-tree <ref> scripts/archgate/baseline/ | wc -l`), and
answers `execctor 0 violations across 1 checked`, `execseam 2 baselined … across 1451 checked`,
`funclen 2 baselined … across 2051 checked`. `make test-archgate` tests each rule in both directions,
including that a rule which stops matching reports stale baseline entries rather than a clean run.

**Deliberately still prose.** Two sentences have no shape for an AST walk to match: *never split
`executionState`* — nothing in the tree marks a field as belonging to the sandbox snapshot — and *a
`*executionState` is allowed only where the concern owns the fields it reads*, which is a judgement about
fields. `execseam` says so at its definition. The two baselined entries are the helpers that reach the
executor from inside one of its methods; each clears by being handed the func value in its place.

## 6. The seam idiom is func values, not interfaces

`grep -rn '^type [A-Za-z0-9_]* interface {' backend pkg main.go --include='*.go' | grep -v _test` → **33**, of
which **10** are exported and **18** have exactly one method. The widest is `lspManager` in
`backend/exec/lsp_manager.go` at **12** methods. All unchanged across this round.

The seams are func-typed, by design: `newMergeOrchestrator` receives `lockWorktree: te.LockWorktree`,
`storeTask`, `broadcastDiffStats`, `utilityTarget`, `chatCompletion`, `streamCompletion` — six func values, no
interface; `newToolRunner` receives `lspMgr func() lspManager` and `storeTask`. A func field is the smallest
possible interface, and it is what `execseam` prescribes for locks for the separate reason that a stored lock
pointer can be handed on. Interfaces appear where a test must answer a question: `agentBinaries` (1 method),
`taskGetter` (1), `lspManager` (12, the LSP surface `exec` uses).

**Consequence.** A low interface count is not a defect signal in this repo. Adding an interface where a func
value already names the one thing crossing the seam buys a name and loses the shape constraint.

## 7. Endpoint ownership

`grep -cE 'mux\.Handle(Func)?\("' backend/server/routes.go` → **109** registrations in one file: 107 name a
handler, 2 mount static trees (`/`, `/extensions/`). Attributing each of the 107 to the package that owns the
handler method — a one-off resolution of `s.<field>.<Method>` through the `Server` struct's field types, with
13 registrations read through the closure that wraps them — against the non-test files that declare a
`w http.ResponseWriter` handler:

| Package | routes | files |
|---|---|---|
| `backend/api` (was `backend/managers`) | 41 | 5 |
| `backend/server` | 34 | 17 |
| `backend/extensions` | 19 | 1 |
| `backend/exec` | 5 | 2 |
| `backend/config` | 3 | 2 |
| `backend/term` | 2 | 1 |
| `backend/lsp` | 2 | 1 |
| `backend/stream` | 1 | 1 |
| `backend/httputil` | — | 2 |

`grep -rlE '\(w http\.ResponseWriter' backend pkg --include='*.go' | grep -v _test | xargs -n1 dirname |
sort | uniq -c` counts plumbing with endpoints, which is why `server` reads 17 (it includes `middleware.go`,
`compress.go`, `rate_limit.go`, `routes.go`, `server.go`) and `api` reads 5 (`http.go` is shared preambles,
not a route); `httputil`'s two files are `Fail` and the WebSocket writer, not an endpoint owner. Both counts
are identical before and after the rename, apart from the name.

**Consequence.** *A domain package serves its own endpoints*, and `managers` was the outlier for stating no
domain — hence `backend/api`, whose types are `TaskHandler`, `ProjectHandler`, `BoardHandler`, `GitHandler`.
The rule, the exemptions and what a new endpoint does about `?parsed=1` are in
`docs/api-contract.md → Which package owns an endpoint`; this file carries only the counts.

## Corrections — do not re-derive these

Each of these was derived wrongly in the round that produced the findings above.

- **`api` is not a banned Go package name.** The package-naming guidance enumerates `common`, `util`,
  `shared`, `base`, `misc`; `api` is absent, and `k8s.io/api`, `google.golang.org/api`, `docker/docker/api` and
  `go.etcd.io/etcd/api/v3` are counter-examples in production. The rename bought truthfulness about what the
  package holds and no structural change with it.
- **Folding every handler into one package is impossible, not unfashionable.** `go list -deps ./backend/api |
  grep -c kanban/backend/server` → **0**, and `backend/server` is `backend/api`'s only importer. Inside
  `backend/server`, 175 call sites reach a function or method declared in a sibling file of the same package
  (a one-off `go/ast` walk, as in §2), and `handler_fs.go`/`handler_fs_raw.go`/`handler_fs_upload.go` are 19
  `*Server` methods calling `s.validateFSPath`, `s.writeContainedFile`, `s.fsWriteTarget` and
  `s.requireTaskOwnership` (declared in `handler_fs.go`) and `s.resolveWorktreeForFile`
  (`handler_preview.go`); `s.broadcastTaskDiff` and `s.lockWorktree` are declared in `handler_thread.go` and
  called from `handler_message.go`. `s.gitTask` is a field, `gitTaskLocks` (`server.go:99`), taken as
  `s.gitTask.Lock(taskID)` at `handler_thread.go:629` — not a method. `Server` itself holds 33 fields,
  including `mux`, `tlsConfig`, the shutdown stage table (`shutdown [stageCount]func()`) and the detached base
  context, and 80 methods. Moving the handlers either drags `Server` with them or takes a `server ↔ api`
  cycle. **The one-way `server → api` edge is load-bearing**: a handler that needs the hub or a lock is
  injected with a func value, which is §6.
- **Splitting `backend/api` by domain would need an invented shared-helper package.** Its 31 cross-file calls
  run `git.go → http.go`, `task.go → http.go`, `task.go → task_update.go`, `board.go → task.go`,
  `project.go → task.go`: `resolveTask` and `requireWorktreeMgr` (`http.go`) are used by `task.go` and `git.go`
  both, and `resolveWorktree` by `git.go`. A `handlers-shared` package solves a naming problem by adding a
  package.
- **The `Get*` accessors are a considered non-fix.**
  `grep -rhE '^func .*\) Get[A-Za-z0-9]+\(' backend/store backend/config backend/extensions backend/lsp backend/sandbox --include='*.go' | grep -v _test | grep -oE 'Get[A-Za-z0-9]+' | sort -u | wc -l`
  → **53** before, **53** after: a real, linter-invisible deviation from the accessor idiom (`GetTaskByID`,
  `GetProviderAPIKey`, …) that is deliberately left alone, because the names are exported API and the change
  is zero behaviour and pure churn. The count is scope-sensitive — all of `backend` + `pkg` answers 62
  distinct names across 78 declarations — so it is quoted here with its scope.
- **Naming, stutter and doc-comment form are already machine-gated.** `make lint` runs
  `staticcheck -checks=all`, which includes stylecheck: `staticcheck -list-checks` (2026.2.1 / 0.8.1) lists
  **18** `ST10xx` checks including ST1000, ST1003, ST1016, ST1020, ST1021, ST1022, and
  `staticcheck -checks=ST1003 ./backend/... ./pkg/... .` reports **0** findings — which is what "package
  stutter measured 0" means. What is left for a human, and what this file measures, is shape, size, coupling
  and ownership.
- **Figures from the findings round that did not reproduce**, recorded so they are not copied forward: the
  giants as 577/81, 578/66, 426/83 (§2 prints 555/73, 574/71, 425/87 for the same three functions);
  `lspManager` as 7 methods (12 in `exec`, 5 in `lsp`; the 7-method interface is `pkg/agent`'s `AgentAdapter`);
  "10 interfaces in 56k lines" (10 is the exported count, 33 is the total); endpoint ownership as `server` 13
  files and handlers 4 (§7's definition answers 17 and 5); "30 of 31 packages are in-package" (34 of 36 by
  `go list`, 33 of 34 in-package); "53 `Get*` accessors" without its scope.

## Not measured here

`environment` on an LSP extension (declared, validated, never called) has no number behind it yet. The other
ten archgate rules — `http`, `env`, `envassign`, `locks`, `routes`, `writeselect`, `wsevents`, `wsframe`,
`status500`, `ctxroot` — are documented at their definitions in `scripts/archgate/rules*.go` and in the
subsystem docs `AGENTS.md` indexes; this file holds what was measured for the review, and what re-deriving it
got wrong.

This file has one pointer, the `make archgate` comment; it is deliberately not in `AGENTS.md`'s Docs Index,
because it measures no subsystem a reader is about to edit — it is the record behind the gate.
