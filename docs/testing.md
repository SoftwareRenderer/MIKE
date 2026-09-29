# Testing Conventions & Mock LLM

Read this when writing or debugging e2e or unit tests. `AGENTS.md → Testing` keeps the commands and the
short rules; this file holds the measured cost of each gate, the environment, the conventions, the mock-LLM
fixtures and the tests that fail only under load.

## Measured gate cost

`make test-time` measures what the test gates cost; `scripts/test-timings.sh` is the instrument. This section
is the record: one dated block per measurement, wall-clock seconds, ranked slowest-first as the tool prints
them. An unmeasured cost is a work estimate, and `make docs-check` refuses one in a rules document
(`docs/documentation.md → The gate`). The help lines reading `~10s` and `~5min` were exactly that. The
instrument is not a gate: it times a red unit and a green one the same way, prints an exit code beside every
row, and exits non-zero when a unit exits other than expected.

- **A cost claim with no measured number behind it is not a rule here.** Quote a row below, or measure it and
  add the row.

Re-price one row without paying for a suite:

| the row to re-measure | command |
|---|---|
| each `make check` sub-gate | `make test-time LANES=gates` |
| one whole `make check` | `make test-time LANES=check` |
| one Go package, or the whole Go scope | `make test-time GO_PKGS=./backend/git/` |
| vitest, or one vitest path | `make test-time LANES=vitest`, with `VITEST_FILTER=src/lib/utils` |
| one Playwright spec file | `make test-time E2E_GREP=chat-core` |

Every run writes one `<utc>.json` plus its reporter artifacts under `data/test-timings/`. That directory is
gitignored, so a number worth keeping belongs in a dated block here. The e2e lane stops a running dev
server, because `frontend/tests/global-setup.ts` owns ports 3000/3001/9999; it restarts one if it stopped
one, and prints the leftover check either way.

### 2026-09-29 — 8 CPUs, go1.27.0, node v22.23.2, Playwright 1.62.1, warm build cache

Run `2026-09-29T031753Z` measured four lanes serially in 2941 s; run `2026-09-29T105721Z` measured the
whole-check lane on its own. Three reds on this host are host facts, listed at the end of this block, and
every red unit is still timed.

#### The `make check` sub-gates, one at a time

| seconds | sub-gate | what its help line claimed |
|---|---|---|
| 485.41 | `test-go`, exit 2 — see the host facts below | nothing |
| 434.93 | `test-frontend` (vitest plus ten gates) | "vitest, ~10s" |
| 28.38 | `lint` (gofmt, vet, staticcheck, deadcode) | nothing |
| 28.11 | `npm run check` (svelte-check) | nothing |
| 22.88 | `docs-check` | "~0.2s" |
| 12.56 | `check-release` | "~5s" |
| 11.38 | `test-archgate` | "~0.2s" |
| 8.29 | `archgate` | "~0.2s" |
| 6.13 | `test-docs-check` | "~0.2s" |
| 5.54 | `bridge-check` | nothing |
| 4.63 | `go build -trimpath -o mike .`, which is `check`'s build step | nothing |

The eleven rows sum to 1048.24 s. One whole `make check` measured **374.77 s**: the same steps, serially,
inside one make, priced second, on the caches the first pass had filled. The gap is cache and not
make. Inside that same pass vitest reported 190.47 s, against the 365.83 s it had taken on its own minutes
earlier. One number on this host is therefore soft. Price a change by running the same lane twice in the
same order, and read each new row beside the row it came from.

Plain `make check` cannot be timed on a tree that is red anywhere. It is one target whose recipe is every
step, and make abandons a recipe at its first failing line: `-k` continues *other* targets, not the rest of
that recipe. The lane therefore times `make check -i`, which runs every line and whose exit code means
nothing. Its own closing echo still prints all gates green on a red tree, so the lane greps that log's `FAIL`
lines instead of trusting that line. The exit column above is the verdict, and inside this pass
the red steps were `backend/extensions`, `backend/git`, `backend/sandbox` and `backend/server` — the host
facts at the end of this block.

Two readings of this table matter more than either total. The four rows whose help line said `~0.2s` are
48.7 s together. And `test-frontend` is one gate over eleven commands, and vitest alone took 365.83 s of its
434.93 s, so pricing that gate cannot say which command moved.

#### Go: `go test -json -count=1 -parallel 4 ./backend/... ./pkg/... .`

387.93 s for 36 packages and 4787 tests: 4741 pass, 30 fail, 16 skip. The same scope through `make test-go`
measured 485.41 s above — the -json pass ran second, on a build cache the first pass had filled. Package
seconds overlap, because packages run side by side.

| seconds | package | | seconds | package |
|---|---|---|---|---|
| 371.18 | `mike/backend/server` (fail) | | 16.71 | `mike/backend/config` |
| 148.00 | `mike/backend/git` (fail) | | 13.38 | `mike/backend/llama` |
| 109.43 | `mike/backend/exec` | | 11.32 | `mike/backend/sandbox` (fail) |
| 108.61 | `mike/backend/api` | | 10.38 | `mike/backend/lsp` |
| 79.74 | `mike/backend/extensions` (fail) | | 8.14 | `mike/pkg/keylock` |
| 76.30 | `mike/backend/stream` | | 3.01 | `mike/backend/term` |
| 44.17 | `mike/backend/store` | | 2.85 | `mike/backend/auth` |
| | | | … | 21 packages under 2 s |

| seconds | slowest test | package |
|---|---|---|
| 18.60 | `TestThreadForkFromInactiveThread` | `mike/backend/server` |
| 17.28 | `TestQuietStreamCeilingIsReportedAsSilence` | `mike/backend/exec` |
| 15.57 | `TestFailedInstallSettlesTheStateBeforeItReportsFailure` | `mike/backend/extensions` |
| 15.16 | `TestLLMCrashMidTurnWithToolCallLeavesNoPartialTurn` | `mike/backend/exec` |
| 15.11 | `TestLLMProviderDiesBeforeFirstFrameRetriesThenFailsIntoReview` | `mike/backend/exec` |
| 11.25 | `TestTaskOperatingModeRemount`, fail | `mike/backend/server` |
| 10.59 | `TestChatStream_ProductionTimedOutRace` | `mike/backend/llama` |
| 9.44 | `TestSubtaskMergeFullFlow` | `mike/backend/exec` |

No single test is long: the ceiling is 18.6 s, and 6 of these 8 sit in `backend/exec` and `backend/server`,
which is the suite's critical path — the same reading as the `t.Parallel()` note in
`docs/testing.md → Go unit test environment`. `mike`, the root package, is 0.09 s, and that is what the
trailing `.` in the scope buys — `./backend/... ./pkg/...` does not reach that package at all.

#### vitest: `npx vitest run`

365.83 s of wall clock for 289 files and 3711 tests, no failures. vitest's own split is the row to read:

```
Duration 359.18s (transform 47.52s, setup 120.78s, import 195.39s, tests 83.37s, environment 1886.96s)
```

The tests are 83 s of it. The 289 per-file durations add up to 67.6 s of the 365.83 s, because a file's own
duration excludes its worker's transform, setup and jsdom environment. The slowest file is 6.77 s
(`file-browser-multi-select.unit.svelte.ts`), the fifth is 2.04 s and the thirtieth is 0.46 s, so no
file-level edit reaches the 276 s that is not tests. `npm run test:related` is the answer to a small change
for exactly that reason.

#### Playwright: `npx playwright test --reporter=list,json`

The harness is measured around the tests, because its fixed cost is what a one-spec run pays:

| seconds | the harness around the tests |
|---|---|
| 6.40 | globalSetup — two servers, two databases, seed projects and fixture repos, worker startup |
| 1.21 | globalTeardown — both servers stopped by identity, data directories deleted |
| 4.93 | harness fixed cost alone: setup plus teardown with zero tests, via a `--grep` matching nothing |
| 10.66 | `npm run build` — global-setup serves `frontend/dist` and never builds it |
| 828.54 | the whole lane: 482 tests, 400 passed, 29 failed, 8 skipped, 45 did not run |

| seconds of test time | tests | project | not passing |
|---|---|---|---|
| 2559.5 | 397 | chromium (4 workers) | 28 |
| 156.8 | 85 | isolated (1 worker, serialized) | 1 |

chromium's test time is summed over 4 workers, so the wall clock is near a quarter of it. Per-spec cost is
dominated by specs that wait on an executor turn or on a return-to-chat repaint, not by specs with many
tests:

| seconds | tests | spec | | seconds | tests | spec |
|---|---|---|---|---|---|---|
| 188.51 | 3 | provisional-review-paint | | 92.00 | 1 | return-transcript-order |
| 124.63 | 9 | task-open-timing | | 89.35 | 14 | task-view-multi-select |
| 108.61 | 1 | return-after-review | | 79.84 | 6 | away-and-back |
| 105.14 | 21 | diff-viewer | | 78.26 | 22 | task-lifecycle |
| 95.99 | 25 | chat-core | | 67.41 | 11 | file-editor |
| 92.74 | 1 | focus-return-review | | 66.22 | 2 | reconnect-refresh |

The isolated lane's own worst rows are `settings-provider` (39.76 s over 18 tests), `html-preview` (36.07 s
over 13) and `preferences-keyboard` (22.10 s over 14).

#### Three host facts behind these numbers

- `podman info` answers `'overlay' is not supported over overlayfs`, so every suite that creates a container
  fails here — 29 of the 30 failing Go tests, counting `backend/server` and `backend/extensions` asking
  podman a question it cannot answer, and the sandbox specs too. The `mount_program` switch that answers it:
  `docs/testing.md → A machine that cannot start a container`.
- `git --version` is 2.43.0, whose `git stash push` answers exit 1 with no output while `index.lock` is
  held. `backend/git`'s `TestPushStashCarriesGitReason` asserts git's own text reaches the error, so this
  host refuses it. The probe is in `docs/testing.md → Go unit test environment`.
- 29 e2e failures arrived as 45 s and 90 s timeouts, and 45 tests then "did not run" behind them in `serial`
  blocks. One lane server answers 4 workers and queues every worker's turn —
  `docs/testing.md → Tests that fail only under load`.

A machine that answers those three checks clean prints different totals for the Go and e2e rows. The shape
will not: `test-go` and `test-frontend` decide what "done" costs, `backend/exec` and `backend/server` are
the Go critical path, and vitest's wall clock is not its tests.

### 2026-09-29 — one typecheck compile, the frontend gates side by side

Same host, same instrument (`make test-time LANES=gates`, run `2026-09-29T114934Z`). Two shapes changed. The
two typecheck passes became one compile (`frontend/scripts/svelte-check-once.sh`), and `make test-frontend`
became one runner whose slots start together (`frontend/scripts/frontend-gates.sh`). The reported vitest
duration moved from 187 s to 174 s across these runs, so read every total beside the vitest row of its own
run.

#### One compile, two verdicts

| step | two compiles (s) | one compile (s) |
|---|---|---|
| `npm run check`, nothing compiled yet | 11.50 | 10.45 |
| `npm run check:unused` after it | 10.63 | 0.25 |
| the pair, as `make check` runs it | 22.13 | 10.70 |

The cache key is a hash of the typechecked file contents, the three configs and
`frontend/package-lock.json`. mtimes are not in it: a checkout moves them without moving a byte, and a stale
report read as clean is the failure the gate cannot have. A cached report still has to carry its `COMPLETED …
FILES` summary. svelte-check's own exit code is never the verdict either — 18 exempt `_`-prefixed
declarations make the strict run exit 1 while both answers are green.

#### `make test-frontend`, in series and in parallel

| `make test-frontend` | total (s) | its own vitest (s) | everything else (s) |
|---|---|---|---|
| ten commands in series, the old recipe | 208.19 | 187.01 | 21.18 |
| the runner, cold typecheck cache | 188.71 | 180.60 | 8.11 |
| the runner, warm typecheck cache | 182.36 | 174.30 | 8.06 |

The runner's wall is its slowest slot. Its own printout, from the cold run, is the record of what runs
beside vitest:

| slot | seconds | what it is |
|---|---|---|
| `vitest` | 183.67 | 289 files, 3711 tests |
| `types` | 19.20 | the one compile, beside vitest (10.45 s alone) |
| `fetch` | 8.69 | `npm run check:fetch` (1.82 s alone) |
| `dead` | 6.97 | knip (2.89 s alone) |
| `types-test` | 5.93 | `npm run check:typecheck-test`, 36 cases |
| `colors` | 4.54 | `npm run check:colors` (1.02 s alone) |
| `i18n` | 4.10 | gate, counter and the counter's test (2.16 s alone) |
| `surface` | 2.61 | dead-surface count and its test (1.10 s alone) |
| `sinks` | 2.34 | sink audit and its test (0.88 s alone) |

Eight cores are the binding, so every slot but vitest is slower here than alone. The parallel shape is
measured rather than assumed: beside the compile, vitest measured 203.26 s against 202.14 s alone, so 1.1 s
buys a compile that costs 10.45 s on an idle machine.

#### The same lane, as the instrument prints it

`make test-time LANES=gates` prices `npm run check` at 12.38 s and `npm run check:unused` at 0.30 s: the
second row is the first one's report, read again. `make test-frontend` in that lane measured 259.17 s with
vitest at 252.89 s, and its `types` slot cost 1.15 s — the compile had already happened earlier in the lane,
which is what `make check` looks like. That lane's `test-go` row (145.38 s, exit 2) is the host facts above:
`backend/extensions`, `backend/git` and the suites that create a container.

### 2026-09-29 — the iteration lane, three edit shapes

Run `2026-09-29T122749Z` priced one whole `make check` (`-i`, so every step ran) at **347.73 s**, and the
twelve sub-gates at 369.34 s when each runs alone: `test-frontend` 194.66, `test-go` 147.47 (exit 2, the host
facts), `lint` 12.36, `docs-check` 4.58, `check-release` 2.50, `test-archgate` 2.41, `archgate` 1.52, `build`
1.24, `test-docs-check` 0.93, `bridge-check` 0.74, `svelte-check` 0.62, `check-unused` 0.31.

`make check-fast` has no lane in the instrument, because what it costs is a function of what the tree
touched. These rows are its own summary wall clock — the same script prints it — on that warm tree.

| seconds | the tree | what ran |
|---|---|---|
| 15.82 | one `.go` file in `backend/store` | gofmt, the full-scope build, `go vet` and `go test` of `./backend/store/`, the three gates |
| 7.85 | that file again, second run | the same steps, with `go test` answered from the result cache in 0.27 s against 7.98 s |
| 33.51 | one file under `frontend/src` | the build, `test:related` (16.07 s), the typecheck compile (10.34 s), the three gates |
| 23.38 | that file again, second run | the same steps, with the typecheck compile read from its cache in 0.25 s |
| 7.31 | nothing | the build and the three gates; every other step printed its skip reason |

The floor under all five rows is `docs-check` + `archgate` + `bridge-check` + the build, and no edit shape
escapes it. `npx vitest related --run src/lib/utils/task-utils.ts` measured 16.64 s for 15 files and 146
tests, of which the tests are 1.91 s: the frontend lane's cost is vitest's start-up.

### 2026-09-29 — the gate as two lanes

`make check` runs its eleven sub-gates as two lanes (`scripts/check-lanes.sh`), so its wall is the slower
lane and not the sum. Both shapes below ran the same eleven commands on the same warm tree, with staticcheck
and deadcode installed: `scripts/lint-tools.sh` prints the install command for either, and the Go lane stops
at `lint` without them. `make check` prints the lane table. The serial rows are the same runner with all
eleven steps in one lane, through its `MIKE_CHECK_LANES` hook.

| wall (s) | the shape |
|---|---|
| 339.00 | eleven gates in one lane, run 1 |
| 345.58 | eleven gates in one lane, run 2 |
| 281.06 | two lanes, run 1 |
| 282.43 | two lanes, run 2 |

A lane beside another lane is slower, not free. `go test`'s default package parallelism (GOMAXPROCS) fills
the 8 cores, and so do vitest's 8 fork workers: 7 `.test` binaries and 8 forks were alive at once mid-run.
The saving is the part of the gate that waits on files rather than on a core.

| lane | alone (s) | its steps, alone (s) | its slow step, in a two-lane run (s) |
|---|---|---|---|
| `go` | 147.34 | `test-go` 133.31, `lint` 6.15, `check-release` 2.47, `test-archgate` 2.43, `archgate` 1.53, `bridge-check` 0.85, `build` 0.60 | `test-go` 263.67 |
| `frontend` | 191.33 | `test-frontend` 185.52, `docs-check` 4.55, `test-docs-check` 0.97, `svelte-check` 0.29 | `test-frontend` 248.60 |

Six runs of the two shapes here printed the same 30 host-fact reds, plus between none and two that rotate: the
two serial runs added none and one, the four two-lane runs added one, one, one and two. Every rotating name is
a load-list mechanism — the `t.TempDir()` cleanup race, or a frame its test stopped waiting for — and
`TestManualSummarizePublishesCompressedContextUsed` joins that list here. The extra lane is real pressure, so
that rate is what to watch; `docs/testing.md → Tests that fail only under load` names the run-alone re-check
that clears any of them.

The critical path is the frontend lane now, so the row to attack on this gate is vitest's start-up
(`docs/testing.md → vitest`), not `backend/server`. Re-price the whole gate with `make test-time LANES=check`
and one step with `make test-time LANES=gates`; `make check` itself prints both lanes' steps and seconds.

## E2E conventions
- Descriptive spec filenames in `frontend/tests/e2e/`; shared helpers in `helpers.ts`.
- `test.describe.serial()` when tests share state; unique identifiers (`Date.now()`) to avoid
  interference; wait for a condition, never for a duration. Every `waitForTimeout` is counted and gated;
  *The sleep gate* below owns what is exempt and why. `serial` amplifies whatever coupling is left: one
  failure skips the rest of the block as "did not run".
- **Mock LLM is default** — tests use pre-recorded fixtures, never the real endpoint. Fail gracefully
  when the LLM is slow/unavailable.
- **LSP specs come in two kinds, and only one may gate on the server.**
  `editor-hover-definition.spec.ts` answers `/api/extensions` itself and fakes the `/api/lsp` socket, so
  it needs nothing installed — gating it on `hasInstalledLspExtension()` measures a list the suite
  replaces, and since `global-setup` wipes the per-run database it always answered "skip": all nine
  tests silently stopped covering anything, which is also how a stale stub and a wrong assertion stayed
  hidden. `lsp-integration.spec.ts` keeps the gate because it really does talk to a language server, but
  a wall of `skipped` is not coverage: before believing an LSP change is tested, run it against a server
  that has the language server installed (they never run by default).
- **Config-modifying tests** (settings, models, shortcuts, mode select, …) must run on a lane server, never
  on the shared one — add the spec name to `ISOLATED_SPECS` in `frontend/tests/lanes.ts`. That one array is
  the whole rule: the chromium lane's `testMatch` excludes all of it and the shard projects are dealt out of
  it, so a new entry needs no other edit and the lanes cannot drift apart.
- **The harness serves `frontend/dist` from disk** — its server is a development build, and
  `global-setup` never builds it. So an e2e run proves what the last `npm run build` contained, never what
  is in `src/`, and a frontend change nobody built reads as green either way. Build before the run, then
  make it prove it bites: revert the changed file, rebuild, and watch the spec fail before believing the
  green. A `frontend/dist` with no `index.html` in it is the one case that does not read as a test
  failure — the compiled-in copy is built from that same directory, so the server refuses its boot and a
  spec sees `ECONNREFUSED` on 3000 (see `AGENTS.md → Debugging` for the stale-process version).
- **Never write a test that greps a component's source.** `readFileSync('../Foo.svelte')` +
  `expect(source).toContain('…')` cannot fail a real bug, breaks on any rename, and duplicates what
  `npm run check` already gives you. Assert on rendered output (`render(Foo, props)`) or on behaviour
  in a spec. `tests/e2e/debug-*.mjs` and the other one-off probes were removed for the same reason —
  keep the recipe (below), not the stale transcripts.
- **Prefer `isolatedProject()`** (helpers.ts) for anything that touches the board, task state
  or per-project config: call it once at file top level, then `gotoProject(page)` instead of
  `page.goto('/')` and `getProjectId(page)` as before. It gives the *worker* its own project
  (a copy of the fixture repo at `/tmp/kanban-test-repos/w<n>-<port>` plus global-setup's two
  seed tasks). Keyed at the worker, not the file, because `fullyParallel` spreads one file's
  tests over several workers. Specs that assert on the **global** view (sidebar project list,
  file browser, add-project flow, project-id derivation) must NOT use it.
- **A spec that starts a task or sends a chat message owns its own project.** The executor queues a turn per
  project, so on the shared project an answer lands behind five other workers' tasks. The symptom is `.btn-send`
  (idle) never becoming visible, in whichever chat spec lost the race that run. Honouring this is cheap, and it
  makes each task's worktree its own as well.
- **A project's `sandbox_enabled` is not a scratch flag.** The explorer seeds the folder it
  browses from it (`sandboxEnabled ? mountPath : projectPath`), so a spec that turns sandboxing on
  for the shared project makes a *concurrent* spec request the container path from the host —
  `/api/fs` refuses it (`path outside allowed project directory`) and the panel never recovers.
  That is why `agent-network-isolation` and the sandbox specs each run against their own project.
- **Assert the directory, not just that a list exists.** `/api/fs` resolves the project *from the
  path*, so an explorer holding a stale folder asks for a path outside the current project and gets
  400 `path outside allowed project directory` — the panel then shows an error and never retries.
  `ExplorerView` reseeds its folder when `projectPath` changes; a spec that clicks into the tree
  should pin the directory it means (`.file-browser-path-input` value) before clicking.
- **Never `.project-item` + `.first()`** once per-worker projects exist — that is somebody
  else's project. Use `selectProject(page, projectId)`. Same trap: a helper that returns "the
  first entry in the worktrees dir" (`findWorktreeDir(projectId)` is keyed by project).
- **A spec that stages or commits in a PROJECT directory must own that project**
  (`isolatedProject()`). `git add`/`git commit`/`git reset` in the shared
  `/tmp/kanban-test-repo` races with the other specs' index writes, and it showed up as
  *unrelated* specs losing their `.file-browser-list` — the explorer's listing is a live
  `git status` and it does not retry after an error. Worktree git (`gitWithRetry`,
  `writeFileInWorktree`) is already per-task and needs nothing.
- **The two file browsers are one component (`FileBrowserView`) whose listing answers to two names.** Every
  listing is `.file-browser-list` and every row `.file-browser-item`. The task explorer's listing and rows carry
  `file-list` / `file-entry` as well, because that is the name its specs locate them by (the props are
  `listingClass` / `rowClass`, and `file-drop.css` matches either). Drop the second name and no gate fails — only
  `editor-line-task`.
- **`toHaveCount(0)` is not a visibility check.** It counts attached nodes. An element the component
  builds with the card and reveals by unhiding its row is attached the whole time — the hover card's
  "Go to Definition" button is one. Say `toBeHidden()` for "not on screen yet", and keep
  `toHaveCount(0)` for a node the code never creates.
- **`[role="dialog"]` is not a selector here.** BaseModal, `CommitDiffOverlay`,
  `TerminalPanel` and BOTH explorer views carry `role="dialog"`, and every ChatMessage's
  edit `<dialog>` has it implicitly — so the bare locator is a strict-mode violation
  whenever a chat or the explorer is mounted, and `expect(x).not.toBeVisible()` then
  proves nothing. Scope by content: `[role="dialog"]`.filter({ has: page.locator('.commit-textarea') }),
  or the login modal by its `input[type="password"]`.
- **Which control you actually need** (each of these was a silently-wrong assertion):
  the Changes panel lists `.changes-file-row`s and the `.diff-row-clickable`s only render
  for the *selected* file — and an UNTRACKED file has no line diff until staged, so a
  "click diff line" test must `git add` first. `FileBrowserView` (the listing both explorers render)
  uses class `.new-file-input` for BOTH its new-file and new-folder rows (disambiguate with
  `[aria-label="New folder name"]`; the folder button is `.new-file-btn[aria-label="Create new folder"]`,
  NOT `nth(1)`, which is New file). The review bar has three `.btn-commit-done` buttons —
  only `.btn-commit-and-done` opens the commit dialog, and it is gated on STAGED changes
  while a trailing *user* chat entry makes it render "Resume" instead (`waitForTurnAnswered`).
- **Offline = abort `**/api/**`, not `context.setOffline(true)`.** The offline screen  (`App.svelte`'s `hasLocalCache` branch, from `session-boot.svelte.ts`) exists for "app booted, API
  unreachable, cache available"; going fully offline takes the document down too, so you assert on
  Chrome's error page.
- **`locator.isVisible({ timeout })` does not wait** — Playwright's own types say so ("does
  not wait for the element to become visible and returns immediately"). So
  `const seen = await x.isVisible({ timeout: 10000 }).catch(() => false)` is an
  *immediate* read dressed up as a wait, and it goes false under load while passing alone.
  Write `x.waitFor({ state: 'visible', timeout: 10000 }).then(() => true).catch(() => false)`,
  and keep the bare `isVisible()` only where "right now" is the actual question (a
  must-not-be-thet-yet check). Fifteen such sites were fixed in one sweep.
- **Never click coordinates captured earlier.** `boundingBox()` then `mouse.click(x, y)`
  lands on a neighbour once the board re-renders (diff stats arrive, heights change) —
  use `locator.click({ button: 'middle' })`, which re-resolves the box when it acts.
- **Timeouts:** `expect` 15 s / test 45 s. One server answers every worker, so an executor
  turn queues behind the other workers' turns; assertions that wait on it need slack and
  cost nothing when green. Where a spec waits on something slower still (the explorer's
  `git status`, a file-picker listing) say WHY next to the raised number.
- `test.fixme(title, fn)` marks a test whose *expectation* is unverified — never a flake
  mask. Fixing the six that were here turned up six test bugs (wrong directory, wrong
  button, wrong line of the file, `[role=dialog]` ambiguity), not six product bugs.

- **Heavy fixtures get a dedicated repo** — specs that create large files or mutate the working tree
  must NOT write into the shared `/tmp/kanban-test-repo` (other specs' `git add -A && git commit`
  races with them). Init a dedicated repo (e.g. `/tmp/kanban-test-repo-<name>`), create its project
  in `beforeAll` (use `browser.newContext({ storageState })` — `page` is not available in
  `beforeAll`), delete both in `afterAll`, and wrap the describe in `.serial`. See
  `editor-wrap-highlight.spec.ts`.
- global-setup sets a **local git identity** on the test repos — don't remove it: backend
  commit-and-done commits don't pass `-c` author flags, and without it those e2e tests fail silently
  (commit skipped, task still moves to Done).

**Concurrency is part of the environment, not an optimization.** `workers: 4` is measured, not guessed. Six
workers run the suite in ~2.7 min instead of ~3.0 and pay for it with 1-4 failures per run that rotate between
specs (chat-core, chat-messages, diff-viewer, link-like-rows, thread-new-thread — every one of them green alone
and in pairs). One lane server answers all the workers and queues executor turns, so under six workers "wait for
the task to go idle" is a race against everyone else's turns. Any new turn-bound wait must say so in its budget (use the
`waitForSendButton(page, timeout)` turn budgets in `helpers.ts`, not the 10s `expect.timeout`).

- **A spec that opens Settings names the tab it works in.** The Settings control opens the tab the update
  dot points at (`docs/auto-update.md`). The dot comes from the pass that runs at boot:
  `updateCheck.start()` sits in `loadAppData`, which both boot paths reach. So it can be lit before a
  spec clicks anything — that is how `settings-provider.spec.ts` timed out waiting for
  `.provider-card-header` with the About panel on screen. Click the `button.tab-btn` you need, the idiom
  `settings-api-tokens.spec.ts` already uses. The routing rule belongs to `settings-update-dot.spec.ts`.
- **A Browse or Installed extension card is addressed by `data-identity`, never by its display name.**
  Identity is author+name (`$lib/utils/extension-identity`), and `extensions/` is per-machine and
  gitignored. A developer's own install can therefore display a fixture's name while being a different
  extension: two cards, one name, and a `hasText` locator that breaks on that machine only.

## Mock LLM server

`frontend/tests/mock-llm.js` (Node, port 9999). Selects a fixture by request body (tools present +
message keywords). Fixtures: `frontend/tests/fixtures/` — JSON arrays of OpenAI streaming SSE chunks.

Disable: `MIKE_USE_MOCK_LLM=0 npx playwright test` (real LLM endpoint).

Fixture format — one entry per SSE `data:` frame:

```json
{"choices": [{"delta": {"content": "Hello"}, "finish_reason": null}]}
```

Tool calls use `delta.tool_calls` with `finish_reason: "tool_calls"`; the final frame has
`finish_reason: "stop"` plus `usage` (incl. `prompt_ms`/`predicted_ms`).

**Recording a new fixture:** run with `MIKE_USE_MOCK_LLM=0`, capture the real SSE stream (browser
DevTools Network tab or server logs), save it as a JSON array in `frontend/tests/fixtures/`, and add
keyword routing in `mock-llm.js` if needed.

**Pin the fixture you mean: `llm-fixture:<name>` in the prompt.** Keyword routing is not
reachable from a normal task prompt: the plan-first skill is in **every** system prompt, so the
`isPlanMode` branch answers first and a spec that asks for "edit" gets `create_task`. That is why
several chat specs asserted against the wrong tool card for their whole life — and why the fix is
a pin, not a reordering — `fill` a prompt like `edit 1749… llm-fixture:tool_call_edit_file`.
That reachability is a machine condition: `prompts/skills/` is gitignored, so a checkout without it
has no plan route at all — `isPlanMode` answers false and the keyword table decides. Such a checkout
sees `editor-line-task`'s real-time-streaming test wait 30 s for a `.tool-call-entry` that never
arrives, because the mock answered that turn with plain text. Measured both ways on that one spec: red
without the directory, green with one `plan-first/SKILL.md` inside it.
**A pinned tool-call fixture also needs its operand**: the tool runs in the task's worktree, and the
e2e repos hold only a README, so a fixture that edits `src/lib/utils/format.ts` fails on a missing
file unless the spec writes it first (`writeFileInWorktree`, task-scoped). And the task's **creation**
prompt must stay keyword-free: `edit-file-<ts>` matches the mock's `\bedit\b` route, so the initial
turn replays that same fixture with the same static tool-call id — and the second card *replaces*
the first, so the count the spec waits on never grows.
Names and their chunk delays live in `FIXTURES_BY_NAME` (`text_slow` keeps its 100ms delay —
pinning it at 1ms streams the whole turn in ~16ms and any mid-stream wait fails forever). An
unknown name logs a warning and falls through, so a typo is visible rather than mysterious. **A spec
whose waits assume the turn ENDS must pin `text`** (or another terminating fixture): a plan-mode
answer keeps starting work, the task never returns to idle, and no wait budget is big enough.
`thread-new-thread` failed for exactly this reason in every full run, budget-independent, while
passing alone — unpinned, its throwaway prompts reached the plan route.

Two measured behaviours that chat specs keep getting wrong, both worth designing around:
- **`.awaiting-llm` means "request sent, nothing back yet"** — it is *absent while chunks
  arrive*. So `slow-start` (a 3s warm-up) is what makes the indicator assertable, a second message
  sent during the warm-up leaves no indicator window at all, and a send that interrupts a live
  stream never re-enters it. "Streaming" is observable as *stop button up, indicator gone*.
- **A task auto-starts**, so its own initial prompt/turn is in the chat before the test's first
  message: wait for `.btn-send` (idle) before sending, and assert on the entry that appeared
  *after* the send (`.last()`, or a count you captured before), never on `.first()`.


## Frontend cleanliness gates (run by `make check`)

`make test-frontend` runs these gates as parallel slots: most read `frontend/src`, one reads the e2e suite's
waits. Each bullet names a gate, and a gate's self-test runs in front of it inside the same slot.

- **`npm run check:dead`** — knip over the **production view**: unused files, exports, exported types,
  duplicate exports, unlisted imports, unused dependencies. An ignored test file stays a *user* of what it
  imports, so a symbol kept alive only by its own unit test is not an issue there; the seam rule below is
  what sees it. `svelte-check` is one of the names in knip's `ignoreDependencies`: knip counts a
  devDependency as used when a `package.json` script names it, and the typecheck compile runs inside
  `frontend/scripts/svelte-check-once.sh` now, with the typecheck gate as the proof the tool is used.
- **`npm run check:dead-surface`** — `frontend/dead-surface.py` measures what knip's own exemption hides and
  what no linter counts: an export alive only for its unit test, and a CSS declaration line copied into a
  second stylesheet. The section below owns both rules and the ceiling's re-freeze.
- **`npm run check:unused`** and **`npm run check`** are two verdicts out of one compile.
  `frontend/scripts/svelte-check-once.sh` runs `svelte-check` once on `tsconfig.unused.json` — the `check`
  config plus `noUnusedLocals` and `noUnusedParameters` — and both answers come out of that one machine
  report. The stricter config reports every diagnostic the looser one reports, plus
  `'x' is declared but (its value is never read|never used)`, so `check` takes every error except that class
  and `check:unused` takes that class minus the `_`-prefixed names. The exemption covers `$effect`
  subscription reads and `_` destructuring placeholders, where the read is the purpose. It covers test files
  too, because `tsconfig.json` includes `src/**/*.ts`. `npm run check:relation` re-measures the set relation
  on a tree that is red on purpose; it writes one scratch file under `src/`, so it runs alone. The report is
  cached, keyed by the contents of the typechecked files plus the configs and `frontend/package-lock.json` —
  mtimes prove nothing — and a cached report still has to carry its `COMPLETED … FILES` summary.
  `frontend/scripts/svelte-check-once_test.py` holds both derivations and the cache rule, in both directions.
- **`npm run check:fetch`** — `frontend/scripts/check-fetch.sh` fails on a `fetch(` in any `*.svelte` or
  `*.ts` file under `src/` outside `src/lib/api/`. The API layer owns credentials, the CSRF token and the
  reading of a failed request, so a second caller answers one outage in its own words: a bare `TypeError`,
  no offline flag raised, and no queued write. Two skips are named and reasoned inside the script — a test
  file, whose `fetch(` is a stub or an adversarial payload string, and `src/lib/i18n/load-pack.ts`, which
  must read a missing pack as English-plus-a-report instead of as going offline (`docs/i18n.md`). An exempt
  path that has stopped fetching fails too, so the exemption list cannot rot. The pattern wants a call
  position, so prose naming `fetch()` — one capability description does — is not a request. A scan that
  finds no file to scan is a failure, not a clean run.
- **`npm run check:colors`** — `frontend/scripts/check-colors.sh` fails on a hex colour literal in a
  component or a stylesheet under `src/`, because every chrome colour comes from the palette in
  `src/app.css`; its header holds the exceptions.
- **`npm run check:sleeps`** — `frontend/sleep-gate.py` counts `waitForTimeout` in `tests/`, which is the
  wall clock the suite pays for waits it never performs. The section below owns both measurements.

### How the gates run

`make test-frontend` runs `npm run check:gates-test`, then `npm run check:gates` —
`frontend/scripts/frontend-gates.sh`. Its table is one slot per row: vitest, the typecheck compile, knip, and
the scans. A slot is a command chain, so a gate follows its own self-test in one slot. Each slot writes its
stdout and its exit code under `node_modules/.cache/frontend-gates`; the run prints a summary block (seconds,
exit code, log path per slot) and then each slot's log. A green slot's numbers stay on stdout, because these
gates make their claim there. The vitest slot prints its summary lines and names the log: its per-file lines
are hundreds.

Nothing here may read green that did not run. A slot that exits non-zero, exits 127, leaves no exit-code
file, or is missing from the count of slots fails the run and is named. The runner's own test asserts each of
those both ways, and that the table still names every `check:*` script in `frontend/package.json` except the
exemptions it carries with a reason. Vitest runs beside the typecheck: on 8 CPUs the pair costs what vitest
alone costs, and the compile disappears into it. The dated block under "Measured gate cost" prices both
shapes.

### The dead-surface gate

`frontend/dead-surface.py` is two measurements with different politics: one must read zero, the other has a
frozen ceiling. It prints both numbers on every run, green included, so the run makes the claim and the exit
code only repeats it.

**An untagged test-only export is a finding.** A declaration is test-only when no *other* production file
names it and at least one unit file names it. Nothing in the app reads it: the `export` exists so a test can
reach an internal. That is a seam, and a retained seam says so on its declaration.

```ts
/** @knipignore The coalescing tests measure this window; nothing in the app reads it. */
export const MIRROR_COALESCE_MS = 1500;
```

There is no allowlist, so the permission dies with the symbol and the seam count moves only when the code
moves. knip cannot make this call itself. `ignore` in `knip.json` keeps an ignored test file's imports
counting as usage, and `tags: ["-knipignore"]` exempts a tagged declaration from its report, so a tag costs
nothing there. A tag is free to knip and binding here: an untagged seam fails the run, and the tag's one-line
reason is what a reviewer reads.

**Naming is textual, which is the safe direction.** A production file names a symbol when the identifier
appears anywhere in it outside a comment, a quoted string or a `<style>` block — a template-literal hole and a
Svelte mustache included. A barrel and the English catalog name what they carry without being measured, so a
symbol the app reaches through `index.ts` is not a seam. An export no file names at all reaches its consumer
through a signature, and `npm run check:dead` owns that question.

**The CSS count is a ceiling, not a target.** Every `src/**/*.css` file and every `<style>` block is cut into
declaration runs: the text up to the `;`, or up to the brace that closes it. Comments and whitespace are
stripped, a string literal becomes `S`, a hex colour `I`, and a custom property keeps its name — two tokens of
the same property are two rules, not one rule copied twice. A declaration held by more than one block
contributes every occurrence past its first.

That number cannot be zero and is not meant to be. `display: flex` alone is written in 100 blocks, because a
scoped `<style>` cannot reach another block's rule and a shared class trades a duplicate line for an unscoped
selector. `CSS_CEILING` in the script holds the measured count, so the gate fires on a new copy rather than on
the old pile.

**Re-freezing the ceiling.** `cd frontend && python3 dead-surface.py --update-ceiling` measures the tree and
writes the number into `CSS_CEILING`. A consolidation lands the smaller constant beside the diff. A rise runs
it only with the reason in the commit message, because new scoped CSS that repeats `display: flex` is a
decision someone signs.

**Checking this.** `cd frontend && npm run check:dead-surface-test` asserts each rule in both directions. The
seam that must be reported is reported, and the barrel, the re-export and the signature that must not be are
absent; a copied declaration raises the count and two rules that merely resemble each other do not. A gate
that stopped measuring fails here first.

**Names starting with `_` are exempt, deliberately.** `$effect(() => { const _v = uiInjector.getPointVersion(); … })`
subscribes by *reading*; the value is never used and the read is the whole point. TypeScript exempts
`_`-prefixed parameters natively but not locals, so the script filters them — deleting one of those
declarations unsubscribes the effect. An unused positional argument that must stay for its position gets
the same prefix, which is also TypeScript's own exemption.

Knip traps, each measured on this tree:

- **Never pass `--production`.** It breaks module resolution, and knip then names every runtime
  dependency (22 of them) as unused.
- **The config lives in `frontend/`.** knip takes the workspace root from the config file's directory, so
  `-c /tmp/knip.json` analyzes a different tree and reports different numbers.
- **`cycles` stays out of `include`.** `src/lib/components/Card.svelte` imports itself so a subtask renders
  with the same markup, so the cycle it reports is intended structure. (It is not a resolution artifact:
  the self-import is on line 9 of that file, not in `components/Card/`.)

**What neither gate sees.** A declaration read by something outside the JS module graph:
`AppStreamEventType` in `src/lib/api/events.ts` is exported for the archgate `wsevents` rule, which locates
the union by the text `export type AppStreamEventType` (`scripts/archgate/rules_routes.go`) and errors if
that text is gone. The declaration carries the same `@knipignore` tag every retained seam carries, and
`frontend/dead-surface.py` is what requires the tag and its reason there. Both gates are also blind to an
object property nobody reads, a CSS class no template uses, and a REST route the UI stopped calling.

### The sleep gate

`frontend/sleep-gate.py` owns one rule: an e2e wait names the condition it waits for. A
`page.waitForTimeout(n)` spends `n` milliseconds whether or not its subject has arrived. It is too short
under load and forever too long when green.

Behind almost every sleep there is a condition, and `tests/e2e/helpers.ts` holds the waiting for each
kind. A locator (`toBeVisible`, `toBeHidden`, `toHaveCount`) once the DOM shows it. `expect.poll` or
`waitForStable` once the DOM reaches it over several frames. `waitForSendButton` or `waitForTurnAnswered`
once an executor turn answers; those carry the turn budget, because `expect.timeout` is shorter than a
queued turn. `waitForStored` once a debounced browser write has landed, or a re-read through the API the
spec already uses. `waitForChatStill` and `waitForChatGrowth` are the two the chat specs share, and
`waitForReaction` is the frame-count bound a must-not-happen check has.

**Measurement 1 — a sleep with no reason on its line must be 0.** The exemption is a duration that *is*
the measurement. Drag settle and drag pacing qualify, as do an animation or debounce window the spec
measures, a sampling cadence, and the seconds a second-precision timestamp needs to order three writes.

```ts
await page.waitForTimeout(1100); // moved_at has second precision: three moves need three distinct seconds
```

The reason sits on the sleep's own line, in at least four words. The line above is where a spec explains
the code around a wait, which is how an arbitrary delay acquires a plausible comment.

**Measurement 2 — how many sleeps the suite keeps, frozen as `SLEEP_CEILING`.** A reason makes a sleep
legal, not free: every one of them is still paid on every run. The count only goes down; a rise is
re-frozen with `cd frontend && python3 sleep-gate.py --update-ceiling`, and the reason belongs in the
commit message.

**Checking this.** `cd frontend && npm run check:sleeps-test`. The bare sleep is reported. The call named
in prose, and the one inside a string, are not counted. A comment too short to be a reason fails. The
ceiling bites, and `--update-ceiling` rewrites the constant it printed. The real tree is scanned too, so a
walk that stopped finding specs cannot read as a clean run.

## Diagnostic scripts (ad-hoc investigation)

For request-pattern/race investigation, write standalone Playwright scripts in `frontend/tests/` as
`.mjs` files run against a live dev server: `make run` then `node frontend/tests/<script>.mjs`. Use
`page.evaluate()` for API calls (CSRF token from `/api/auth/session` for POST/PUT/DELETE),
`route.fetch()` + `route.fulfill()` for interception, `Date.now()` offsets for timing, and
`page.screenshot({ path: '/tmp/debug.png' })`.

## E2E failure patterns

- **`ERR_CONNECTION_REFUSED`** / wrong-protocol response: stale processes from an interrupted run hold
  the ports — run the cleanup block in `docs/testing.md → A run must leave nothing behind`.
- **A panel container is visible before its content is.** `.git-graph` is on screen while the history
  panel still reads "Loading commits…", so counting `.git-commit-row` right after it answers 0 for a
  repo that has commits — wait for the first row, not for the container.
- **A spec that is green alone and red in a full run is sharing a repository.** Two specs `git add`
  in the same checkout and one loses with `index.lock: File exists` or an ENOENT worktree; give the
  spec its own repo (`isolatedProject()` / a dedicated fixture repo) instead of adding a retry.
- **Chat/board assertions that depend on the plan-first skill cannot run on a fresh clone:**
  `prompts/skills/` is gitignored, so with no skill installed the mock's `isPlanMode` branch never
  fires and prompts fall through to plain text. Same class: specs that install a **local** extension
  need it present under `extensions/` (also gitignored) — `GET /api/extensions/local` lists nothing
  else and the helper reports `not in local extensions list`.
- **`waiting for locator('.project-item')`**: test project not created — check global-setup logs.
  **`waiting for locator('.card')`**: no tasks exist.
- **Drag-and-drop flakes:** headless is slow — the settle after the drag is one of the durations the sleep
  gate exempts, so name it on the line and raise the predicate timeout instead of the sleep.
- **Mock LLM routing wrong:** check keyword matching in `mock-llm.js` and the fixture files.

## Test environment

One server per lane plus one shared mock LLM, fully managed by `frontend/tests/global-setup.ts` (the
servers are rebuilt each run, the fixture artifacts are cached). `frontend/tests/lanes.ts` is the table's
source: it holds `ISOLATED_SPECS`, deals that set onto its shard count (2 today — ADR-0003), and names the
port, data dir and session files of every lane that deal produces.

| Lane (= Playwright project) | Port | Data dir | Holds |
|---|---|---|---|
| `chromium` | 3000 | `/tmp/kanban-test-data` | every spec `ISOLATED_SPECS` does not name; 4 workers |
| `isolated-1` | 3001 | `/tmp/kanban-test-data-isolated-1` | shard 1 of the config-modifying suites; 1 worker |
| `isolated-2` | 3002 | `/tmp/kanban-test-data-isolated-2` | shard 2 of the same set; 1 worker |
| Mock LLM | 9999 | — | shared by every lane; `frontend/tests/mock-llm.js` |

A lane exists only because `lanes.ts` declares it: the projects, the servers, the ports teardown verifies
and the data dirs teardown removes are all read from that one list. Port 3001 is shard 1, so "3001" still
names a lane server, and `MIKE_E2E_ISOLATED_SHARDS` re-deals the set (and re-opens that many ports) on a
machine measuring its own ceiling.

Playwright's `workers` is the cap across **every** project, so it is the number of browsers the machine runs
at once — 4, the chromium lane's measured ceiling — and not `4 + shards`. A lane worker takes a slot when the
chromium lane stops using one, so the shards start at the tail of the run and run beside each other. Giving
them their own workers measures faster and is not available: the lane specs lose their timing slack first.

Every lane's seed project is a copy of the same fixture repo, so every lane's project id is the same and
their worktrees share `/tmp/kanban-test-worktrees/<projectId>/<taskId>`. That is safe because task ids
differ per lane and `GarbageCollect` runs at server startup only — while no lane has a task to lose.

Test repos live at `/tmp/kanban-test-repo` (+ `-2`), worktrees at `/tmp/kanban-test-worktrees/`.

The registry is pinned to a local fixture (`frontend/tests/registry-fixture.json`) so registry e2e never hits the
hosted registry. The servers read `/tmp/kanban-test-registry.json`, which setup writes from that fixture with the
i18n pack's `__I18N_PACK_COMMIT__` placeholder resolved to the HEAD of the local git repo it builds from
`tests/fixtures/i18n-pack-repo` — a git-pinned install needs a commit that exists.

A spec that installs a **published** extension with `install-local` needs its directory on disk, and the product
is what removes it: **deleting an extension deletes its directory**. So setup seeds the published sources from
`../MIKE-extensions` where absent, and refuses the run naming the path when the sibling checkout is missing.
Otherwise the run after an uninstall reports
`install-local failed (404): <name> not in local extensions list`, which reads as a code regression.

**One binary, one server per lane.** Setup runs `go build -trimpath -o /tmp/kanban-test-bin/mike .` once and
spawns that binary for every lane: one compile per run, not one per server. It is a development build,
never `-tags release`, and each lane passes `--frontend-dist <repo>/frontend/dist`. That flag is the
difference between serving the disk copy by intent and by cwd — a release build refuses it and dies at
boot (`AGENTS.md → Workflow`). Each lane writes its own output to `/tmp/kanban-test-lane-<lane>.log` (the
mock LLM's to `/tmp/kanban-test-mock-llm.log`), truncated at each spawn, and the startup line there is the
proof: `serving the frontend from: <repo>/frontend/dist`. That file is also the whole diagnosis when a lane
never answers — a port that stays dark means the process exited, and its reason is nowhere else. `go build`
runs every run because its own cache is the content key, and a lane on a stale binary would report green
about code that never ran.

**Every lane boots together.** The lane servers and the mock LLM are spawned back to back and awaited
concurrently, then each lane's own seed work (login → config → project → two tasks) runs in parallel with
every other lane's. The mock LLM is up before any lane creates a task, because title-gen dials the provider
immediately.

**The fixtures are cached on a content hash.** A marker file beside each cached artifact
(`<artifact>.sha`) holds the hash of the source it was built from — for the two fixture repos, the i18n
pack repo and the generated registry index. The marker counts only while the artifact still works. For
the pack repo that means `git rev-parse HEAD` answers a 40-hex id *and* `git cat-file -e` resolves it,
because a git-pinned install is resolved by commit. A moved source, a missing artifact or
`MIKE_E2E_REBUILD=1` rebuilds it; nothing reads an mtime. The repo cache holds a pristine *template*
(`/tmp/kanban-test-repo.tpl`), and the lane's repo is a fresh copy of it every run — the copy is where
specs commit and leave worktree metadata.

Test servers are spawned with **your shell environment** inherited — export `MIKE_ENCRYPTION_KEY` before
running e2e, or global-setup fails with "Server on port 3000 did not start within 15000ms".

They also start with `MIKE_DISABLE_UPDATES=1`, so no run is ever a browser that owes the release opt-in.
The boot pass would ask the question of every fresh context, and a yes there means an outbound check to
the manifest host. `disabled` is the posture the client stops on — no dot, no question
(`docs/auto-update.md`). A spec that wants the dot stubs `/api/updates/status`, as
`settings-update-dot.spec.ts` does.

### A run must leave nothing behind

`globalTeardown` is `tests/global-teardown.ts`, whose default export is *only* teardown — naming
`global-setup.ts` there re-runs setup at the end of every run and leaves a server pair, lock and database
nothing will stop. Verify after any change to setup/teardown: `pgrep -xc mike` → 0, nothing listening on
3000, 3001..3000+shards and 9999, no `/tmp/kanban-test-data*`, no `/tmp/kanban-test-lane-*.pid`.

Each lane server is spawned detached and directly, and its PID is written to
`/tmp/kanban-test-lane-<lane>.pid` (the mock LLM's to `/tmp/kanban-test-mock-llm.pid`); a `setsid`
wrapper would leave the harness holding the wrapper's PID, not the server's. Teardown stops a lane by
**identity**: the PID file, plus a `/proc` argv sweep matching `--data-dir <dir>` token-for-token. A PID
read from a file counts only while its `/proc/<pid>/cmdline` still carries that pair, since PIDs get reused.
Token equality is what keeps `/tmp/kanban-test-data` from answering for `/tmp/kanban-test-data-isolated-1`,
and one shard's dir from answering for its neighbour's — with a dozen lanes on one `/tmp`, that is the whole
difference between stopping your own lane and stopping someone else's.
SIGTERM, then wait for the PID to disappear — 18 s, because `server.ShutdownTimeout` is 15 s and a server
shutting down politely is worth waiting out — then SIGKILL. The port is never the identity:
`lsof -ti:PORT` answers with clients too, so the port check runs *after* the identity stop, to report and
take back a port a foreign process (a `make run` dev server, a straggler) still holds.

A lane whose shard a lower `MIKE_E2E_ISOLATED_SHARDS` does not deal with is still this harness's lane: the
maximum deal (one shard per spec) is swept too, so lowering the count does not strand a server, a port or a
database.

The data dirs are then removed with `fs.rmSync` retried on `ENOTEMPTY`/`EBUSY` for ~5 s. That retry is
what the `lsof +D <dir>` poll it replaced was standing in for, and the poll walked the whole open-file
table each time. `make stop` is not involved: `pkill -x mike` would take a developer's own server down
with the lane's.

What a clean run deliberately leaves in `/tmp` is the cache and the boot logs: `kanban-test-bin/`, the two
repo templates and their `.sha` markers, `kanban-test-i18n-pack-repo{,.sha}`, `kanban-test-registry.json{,.sha}`
and `/tmp/kanban-test-lane-*.log`. The cache is keyed on content, so a stale one rebuilds; `rm -rf` them to
start from nothing. The logs are overwritten by the next spawn and survive a failed run on purpose.

If a prior run was interrupted, the next run's stale-state cleanup stops the lane by identity and removes
its data dirs on its own. To do it by hand (stale processes hold the ports — symptom
`ERR_CONNECTION_REFUSED`, or a stale TLS server answering a `--no-tls` test — and a stale lock makes
global-setup run teardown instead of setup):

```bash
lsof -ti:3000,3001,3002,9999 | xargs kill 2>/dev/null
rm -f /tmp/kanban-test-setup.lock /tmp/kanban-test-lane-*.pid /tmp/kanban-test-mock-llm.pid
rm -rf /tmp/kanban-test-data* /tmp/kanban-test-worktrees
```

## Go unit test environment

- **No `MIKE_ENCRYPTION_KEY` is needed.** The secret box is injected (`crypto.New` in `NewServer`) and no
  package reads the variable, so `go test ./backend/...` runs with it unset — a test that needed one once
  failed with `crypto: MIKE_ENCRYPTION_KEY environment variable is not set` instead of a readable failure.
  A test that needs a box derives one: `backend/config` shares a single package-level box and
  `backend/crypto` a `testBox`, because the KDF costs hundreds of milliseconds on purpose and a derivation
  per test would dominate the suite.
- **No global git identity is needed**, so `make check` never depends on the developer's config: every
  fixture commits through `backend/testutil/testgit`, which puts `-c user.name` / `-c user.email` on every
  command. A `Committer identity unknown` failure names a
  fixture that shelled out to raw `git commit` — fix the fixture, do not set `user.email` globally.
  Do **not** set `init.defaultBranch` to `main` globally either: fixture repos then collide with their own
  `git worktree add -B main` and a batch of `backend/api` tests fails in setup, which looks exactly like a
  regression.
- **`git stash push` with `index.lock` held answers some git versions with exit 1 and no output at all**
  (measured on git 2.43.0, with both a tracked change and an untracked file). `backend/git`'s
  `TestPushStashCarriesGitReason` asserts that git's own refusal reaches the error, so it fails on such a
  host while the product is fine: `PushStash` cannot carry a reason the subprocess never printed.
  Confirm with `git init && echo x > f && git add f && git commit -qm one && : > .git/index.lock &&
  git stash push -m x; echo $?` before reading anything into the red.
- Sandbox tests need `runsc` **and** a working podman storage: under a nested/overlayfs root they all fail
  with `'overlay' is not supported over overlayfs, a mount_program is required` (podman exit status 125).
  That is environmental, not a regression — verify with `podman info` before reading anything into it.
  The reach is wider than `backend/sandbox`: `backend/server`'s `TestTaskOperatingModeRemount` fails with
  "expected sandbox to be created after start", and `backend/extensions`' `TestExtensionImageSource` fails
  on the short-ref case because `sandboxruntime.ResolveImageRef` asks podman whether the local build exists
  and falls back to `docker.io/library/…` when the answer is a storage error.
  The converse error is a bare `podman run --runtime=runsc ubuntu:latest echo ok` answering failure on a
  host that runs MIKE's sandboxes fine: that command dies on SELinux and on
  `cgroup.subtree_control: permission denied`, both of which `sandboxruntime.GVisorCreateArgs` already
  answers (`--security-opt label=disable` and its two `--runtime-flag` values). Ask the question with that
  preface — not with `go test ./backend/sandbox/`, whose `TestMain` deletes a live server's sandboxes
  (`docs/sandbox-runtime.md`). A graph-driver refusal is a different question, and no runtime flag reaches
  it.
- `backend/startup`'s defaults test clears every variable the ladder reads, so an exported `MIKE_DATA_DIR`
  or `DANGER_ZONE` — the normal state of a deployed container — cannot change what it measures. Every other
  startup test sets only what it asserts and inherits the rest.
- The Pi agent adapter's unit tests live in `../MIKE-extensions/agent/pi` and are run there — they are
  **not** part of `./backend/... ./pkg/...`.
- **A run that ended is not a thread that is complete.** `failTask` persists the task with
  `execution_state = failed` and writes the `error` row afterwards, so a test that polls the state and then
  reads `GetTaskMessages` once can read between the two writes — it fails under load saying `no error row
  stored` while the roles it prints a moment later (a second read) include the row. Wait for the row as well
  as the state; `backend/exec`'s `TestLLMCrashMidTurnWithToolCallLeavesNoPartialTurn` does.
- **`make check-race`'s `RACE_PKGS` is not the whole concurrency surface.** It omits `./backend/lsp/`,
  `./backend/term/` and `./backend/httputil/`, and the app-stream/LSP/terminal lock fixes are held by tests
  in those three: `backend/lsp`'s `TestSecondInitializeDuringFirstResponseRouting` (one `initialize` reaches
  the language server across an overlapping second one, every client answered with its own id) proves a
  lock-order bug only under the detector, and `backend/httputil`'s `TestKeepalive*` measure the pinger
  against injected ticks (20 ms) and 150–300 ms windows, so they prove the deadline-after-the-`TryLock` rule
  that no `-race` run of another package can see. Name them when touching those packages:
  `make check-race RACE_PKGS="./backend/stream/ ./backend/server/ ./backend/lsp/ ./backend/api/ ./backend/exec/"`.
  Neither is on the load list — 10 rounds of the first and 8 of the second, run while a five-package `-race`
  sweep already loaded the machine, came back green.

- **Top-level Go tests in a package run concurrently.** `t.Parallel()` opens 671 of the 706 top-level tests
  in `backend/exec`, 438 of the 485 in `backend/server`, and 128 of the 136 in `backend/api`. Measured twice
  each at `-parallel 4` with `go test ./backend/server/ ./backend/api/ -count=1 -parallel 4`, best run:
  `backend/server` 336 s → 136 s, `backend/api` 67 s → 27 s. The whole scope
  `go test ./backend/... ./pkg/... . -count=1 -parallel 4` went 183 s → 117 s. `backend/exec` went 130 s →
  43 s the same way, where 13 tests each paid the 3 s `executorExitGrace` and three more waited out a real
  provider-silence ceiling.
- **Seven smaller packages joined it.** 611 of their 666 top-level tests are parallel, and 87 subtests. The
  counts: 43 of 43 in `backend/types`, 67 of 67 in `backend/config`, 56 of 56 in `backend/llama`, 40 of 42
  in `backend/updates`, 31 of 42 in `backend/agent`, 32 of 46 in `backend/fsutil`, 342 of 370 in
  `backend/extensions`. A package's share is one command, run per package:
  `grep -A1 '^func Test' backend/agent/*_test.go | grep -c 't\.Parallel()'` — the 87 subtests are the
  rest of the 698 calls.
- **What those seven are worth.** Measured on the test binaries, best of three, `-test.parallel=1` against
  `-test.parallel=4` on an idle machine: `backend/extensions` 29 s → 17 s, `backend/config` 7.5 s → 3.2 s,
  `backend/agent` 0.40 s → 0.20 s, `backend/updates` 0.22 s → 0.12 s, `backend/types` 0.14 s → 0.09 s. The
  seven together: 51 s → 31 s. The binaries are the honest instrument, because `go test` adds about 1.5 s per
  package of its own bookkeeping, which is more than four of these packages cost to test.
- **Two of the seven do not move, and the counts say why.** 14 of `backend/fsutil`'s 46 tests set the
  environment, so they run in the serial phase, and that phase is the package: 0.48 s → 0.46 s.
  `backend/llama` reads 13.1 s → 10.6 s because `TestChatStream_ProductionTimedOutRace` takes 10.6 s by
  itself, and no schedule removes a floor.
- **`backend/git` and `backend/store` run concurrently too.** `t.Parallel()` sits in 182 of `backend/git`'s
  191 top-level tests and 170 of `backend/store`'s 174, and in five independent subtests. One binary per
  revision, run alternately in one window at `-parallel 4`: git 104 s → 27 s, store 27 s → 9 s. The same
  tests through `go test -count=1` answer 114 s → 28 s and 57 s → 13 s, which is what `make test-go` pays.
- **The parallelized packages hold up under the detector.** `make check-race` over `backend/server` and
  `backend/api` went green with no data race at 218 s and 57 s; the only red was
  `TestTaskOperatingModeRemount`, which this host cannot pass (*A machine that cannot start a container*).
  Two `-race` sweeps of the seven smaller packages at `-parallel 4` found no race either, and reported the
  same one machine-bound red.
- **A test left serial names the process state it shares**, at its own site or at the helper that takes it,
  so the callers need no comment of their own.
  - `t.Setenv` refuses to run in a parallel test, which is why exec's `bash_stream_test.go` is one file. The
    variables are the process's own: `HOME` decides a file jail (`jailedProjectHandler`,
    `jailedHostRootServer`), `TMPDIR` decides where an upload is staged (`taskUploadProject`),
    `MIKE_ENCRYPTION_KEY` decides the golden capture (`setupGoldenFixture`), `MIKE_ALLOWED_HOSTS` decides
    Host pinning, and four `env_test.go` tests plus the `backend/store` contention profile pin their own.
    The seven smaller packages hold the same rule at `jail` (`backend/fsutil`'s `HOME`), at
    `unsetAllowlisted` (`backend/agent`), and at `fakePodmanExit`, which is the only way a stand-in podman on
    `PATH` is reachable.
  - `logging.TestSink()` re-points the process-wide sink, so another test's lines land in this buffer and
    this test's restore lands on that test's sink. Ten tests in `backend/server`, one in `backend/api`, the
    `backend/git` merge-wait test and three `backend/store` migration tests read it. `captureLog` in
    `backend/updates` is that rule at its two callers.
  - The working directory is process state too. Template resolution reads it on every render
    (`prompts.resolvePromptDir`) and `/extensions/` is a CWD-relative `http.Dir`, so the exec prompt tests
    that chdir keep the rule at `chdirRepoRoot` and `TestExtensionsStatic_NoDirectoryListings` is the server
    test that calls `t.Chdir`. A concurrent restore moves it mid-turn, and the failure reads
    "open prompts/summarize.tmpl: no such file or directory". `withProjectRoot` calls `os.Chdir` to the
    repo root for the five `backend/agent` WASM tests reached through `loadPiWasm`, and the two
    `TestBuildExtensionImage_*` that call `t.Chdir` keep the same place.
  - A package-level seam or accessor: `previewRand`; `egressCall`, which `allowLoopbackProxy` swaps, so two
    callers would each run the other's dial policy; and the four `TestGitAuthorFlags_*` tests that write the
    commit identity, which is process-wide and accessor-only (the Confinement rule in `AGENTS.md`).
  - A keyed global is shared by every test holding one key, so a parallel test takes a key nobody else uses:
    `registerStubAgent` derives its adapter-factory name from `t.Name()` for that reason.
  - A reading of the process or the host is somebody else's noise: the `runtime.ReadMemStats` deltas behind
    the size-cap refusals, and the canary path under `/tmp` that a rejected `git show --output=` payload would
    create. `TestExtensionImageSource` asks podman what a short ref means, so what it reports is this
    machine's storage.
  - One fixture shared by a table's cases is shared state, not process state: `TestIsGitIgnored`'s table
    stays serial because every case runs `git check-ignore` inside the same repository, and
    `TestUniqueName`'s rows all write into the one `t.TempDir()` their parent made.
  - A test that starts a container is machine-bound and queues at the memory gate behind every other start:
    `TestTaskOperatingModeRemount`, and in `backend/extensions` `TestApplyMemoryLimit_RunningSandbox` and
    `TestCreate_AcceptsValidImage`. `docs/sandbox-runtime.md` owns that budget.
- **Shared is not mutable.** `sharedSecretBox` in `backend/config` and `testBox` in `backend/extensions` are
  derivations hundreds of tests read and none replaces. Each costs hundreds of ms on purpose, so one box per
  package is the point. Mutating one is a race with every sibling.
- **A parallel subtest outlives its parent's body.** The parent's `defer` runs at return, before the subtest
  starts; only its `t.Cleanup` functions run afterwards, and `t.TempDir()` rides on one. So a subtest goes
  parallel only under a parent that does, over a fixture the parent handed to `t.Cleanup` — `testStore`,
  `t.TempDir` — and never over one a `defer` closes. A local from a call is shared machinery: a parent's
  `testHandler(t)` is one object its rows would argue over, while a table literal is data and reading it from
  several rows is the point of one. The rows in `backend/server` and `backend/api` that do call it build their
  own server or handler inside the row.
- **A serial test that changes the environment shares a package with a parallel one.** A parallel top-level
  test resumes only after `main` has finished the serial list. The cleanup that puts `HOME` or `PATH` back
  has already run by then.
- **`-parallel 4` in `test-go` is capacity, not a claim about the suite**, and its value is measured: see the
  `backend/stream` entry under *Tests that fail only under load*.

### Running less than the gate

- **`make check-fast` is the lane between edits**, and `scripts/check-fast.sh` is the mechanism. It is
  `make test-diff` and `npm run test:related` composed with the `make check` sub-gates that cost seconds.
  Every step runs whatever an earlier one decided, then one table prints step, seconds and exit. A step with
  nothing to check prints its reason there rather than reading green.
- `make test-diff` runs the Go packages holding a changed `.go` file — staged, unstaged or untracked — and
  leaves `go test`'s result cache **on**, which is the saving. It builds `GO_SCOPE` first: it runs no
  dependent of a changed package, so a signature change is caught by that build and not by a test. The list
  is `scripts/touched-packages.sh`.
- `npm run test:related -- <changed files>` runs the vitest files that import them. Measured on two library
  files: 329 tests in 8 s, against 3227 in 36 s for the suite.
- `npx playwright test --last-failed` re-runs the previous run's failures, `--only-changed` the files git
  says moved. Neither is a gate: `make check` and a full e2e run stay the claims.
- `make test-time` runs the full e2e suite and splits its wall clock into `setup=… tests=… teardown=…`,
  plus the slowest spec files. Playwright's own summary prints one number for the tests, and the lane's
  fixed cost — the binary build, the fixture cache, two servers, two databases — happens outside it, so a
  harness change is invisible there. The split is read off the lines the harness already prints
  (`Running N tests using W workers`, `[teardown] ENTRY`) and the `json` reporter's per-spec durations;
  nothing in `global-setup.ts` is instrumented to be measured. `scripts/e2e-time.py`.

What the lane runs, in the order it runs it:

| step | what it asks about |
|---|---|
| `gofmt` | `gofmt -l` over the changed `.go` files, listed by `scripts/touched-packages.sh files` |
| `build` | `go build` over `GO_SCOPE`, always: this lane tests no dependent of a changed package |
| `vet`, `go test` | the packages holding a change, listed by `scripts/touched-packages.sh`, cache left on |
| `vitest related` | `npm run test:related -- <changed files>`, listed by `scripts/touched-frontend-files.sh` |
| `typecheck` | one `npm run check` compile — whole-tree and content-cached, since svelte-check has no per-file mode |
| three gates | `archgate`, `docs-check`, `bridge-check` |

What it cannot catch, widest first:

- **the tests of a package that merely imports a changed one.** The full-scope build reports its compile
  errors; only `make test-go` reports its behaviour.
- **a lane that skipped itself.** No file under `frontend/` changed means no typecheck, and that compile is
  whole-tree, so a type error in an untouched frontend file survives a Go-only edit. One changed file
  anywhere under `frontend/`, other than the `tests/` and `dist/` the script excludes, runs it over
  everything.
- **`make lint` past gofmt** — staticcheck and deadcode — plus gofmt over a file nobody edited; the frontend
  cleanliness gates and knip (`make test-frontend`); the `-tags release` variant (`make check-release`);
  the e2e suite; and every race (`make check-race`).
- **the gates' own self-tests**, `make test-archgate` and `make test-docs-check`.

**`make check` stays the only claim of done.** A green lane is a statement about the files its summary table
names, and nothing else.

### Memory in a test

A test that needs a container to hold memory builds its allocation with one command substitution
(`x=$(head -c 67108864 /dev/zero | tr "\0" a)`), not a doubling loop. Doubling holds the old copy and the
new one while copying, so it reaches well past its target — measured, a loop targeting 200 MB peaked at
525 MB — and every cap in the test has to be raised to survive it. The cap matters twice over: a container
is charged its cap by the memory budget, so a test that asks for 2 GB occupies 2 GB of the budget while it
runs and can hold another task's start in the gate queue. The gVisor tests that allocate now ask for a
192 MB cap and a 64 MB string, which is enough to make the recorded peak several times the idle reading.
`TestMemoryLimitEnforced` keeps a 128 MB cap because that is the smallest cap measured to kill the process
and leave the sandbox up; see `docs/sandbox-runtime.md`.

## A machine that cannot start a container

`podman info` answering `'overlay' is not supported over overlayfs, a mount_program is required` means the
host's own root is an overlayfs and podman was never given a `mount_program` — the image entrypoint makes
that switch, a bare test host does not. Every test that creates a container then fails
(`TestTaskOperatingModeRemount`, the `backend/sandbox` and `pkg/sandboxruntime` container suites), because
the base image never pulls. The image half of podman fails the same way: `backend/extensions`'
`TestExtensionImageSource` is red in 10 of 10 package runs here, naming that same graph driver. Check it
with `podman images` on nothing running: a red naming one of those suites, with that error in the
log, is a reading about the machine.

## Tests that fail only under load

- **What runs short is memory, and it is usually already gone before the run starts.** `/tmp` is tmpfs
  on this machine, so anything large under it is RAM. A project's `rw` sandbox mount at
  `/tmp/kanban-mount` — 11 GB of Go build cache — left 4 GB available at idle, and `free -m` read 0
  partway through a run. Under that shortage the failures rotate: turn-bound chat waits report neither
  send nor stop, and a middle-click tab opens but its navigation stalls. Check `df -h /tmp` before
  believing a red that names a spec on this list. Measured on the starved machine: `workers: 4` failed 5,
  `--workers=3` failed 8, and raising the popup wait from 20 s to 30 s changed nothing. The worker count
  is not the variable; the free memory is.

- **`html-preview` is the spec that pays for the shards.** Its own summed test time measured 36.6 s when one
  lane server ran the config-modifying set, 39-40 s with two shards and 49-60 s with three. Its slowest test
  ran 18.8 s at three shards, against the 15 s `expect` ceiling inside it. A red naming it while a third shard
  is dealt (`MIKE_E2E_ISOLATED_SHARDS=3`) is a reading about the shard count, not about the preview; the
  shipped deal is two shards, which holds it at ~40 s.

- **`backend/extensions`' `TestHandleInstall_BuildFailureLeavesNoRowAndNoFiles`** fails on roughly 1 in 3
  `make check` / `-parallel 8` runs **on a CPU-loaded machine** and never alone or unloaded, so a red
  `test-go` naming it is not necessarily your regression. Re-run it alone; if it passes, read the mtime
  line the failure prints (`describeTree` in `handler_test.go`) before believing either conclusion.
  Established: the rollback runs and logs the right path, the surviving tree carries the original
  checkout's mtimes, and `RepairMissingFiles` and the fake podman's fail-once budget are ruled out.
  Unestablished: who leaves the tree behind. It holds its serial place for a second reason that has nothing to
  do with load: its fixture is `fakePodmanExit`, which calls `t.Setenv`, and that refuses a parallel test — so
  the package's own concurrency cannot reach it either way. Rate there: 0 failures in 10 package runs at
  `-parallel 8`, and 0 in two `-race` runs at `-parallel 4` and in a full-scope run.
- **`backend/exec`'s `TestAbortedStreamIsRetriedAndTheTurnCompletes`** went red once in a whole-scope
  `go test ./backend/... ./pkg/... . -count=1 -parallel 4` run with "assistant answer stored 0 times, want
  1". Its wait ends at a second role while its assertion reads one row, which is the shape the entry above
  refuses. Green alone and in the whole-scope runs since; re-run it alone before reading a red naming it.
- **`backend/server`'s `TestTaskMoveUpdatesUpdatedAt`** was seen failing once, under `make check-race` over
  five packages, and not since: 10 `-count=10 -race` runs on the branch and 10 on `HEAD` came back clean.
  It asserts that two backlog tasks the test never writes come back ordered by `updated_at`, so the likeliest
  reading is a timestamp tie broken differently under load — *likeliest*, not established. A single
  unreproduced red naming it is not evidence of anything; neither is a green run proof it cannot recur.
- **`backend/sandbox`'s `TestMemoryLimitEnforced`** fails roughly 2 in 8 with two container suites running
  concurrently and 6/6 alone. It asserts the guest OOM-killed one process, and under host pressure the
  **host** memcg killer takes the whole runsc sandbox instead (exit 128 plus `WaitPID … EOF`, container
  gone). A red naming it is a reading about the machine, not about a memory limit that stopped working.
- **`backend/server`'s `TestTaskOperatingModeRemount`** does not belong on this list. It is one of the
  container tests named in *A machine that cannot start a container*. It failed 2 of 2
  `make check-race` runs, alone under `-race`, and alone without it, with `podman info` answering the
  unsupported-overlayfs error every time. That error is what the assertion reports as
  `expected sandbox to be created after start`. It was once read as the memory gate refusing the start
  (`docs/sandbox-runtime.md → The memory budget`); that inference no longer holds. `podman images` on
  nothing running is the check that decides a red naming it.
- **A `t.TempDir()` cleanup failing with `unlinkat …: directory not empty`** means a subprocess is still
  writing while the removal walks. Three tests carry it here: `backend/exec`'s
  `TestToolCallWithIntactArgumentsRunsTheSameCommand`, and `backend/server`'s pair of merge-conflict tests,
  `TestCommitAndDoneMergeConflictSystemMessage` and `TestCommitAndDoneMergeConflictPreservesAutoMerge`. The
  exec one showed once inside `make check` at `-parallel 8` and in 4 of 61 filtered single-test runs on one
  branch, against 0 of 50 with that branch's new `backend/exec` test file set aside and 0 of 21 at the
  revision before it. A full-package run does show the server pair: `make check` at `-parallel 4` hit
  `TestCommitAndDoneMergeConflictPreservesAutoMerge`, and that one failed 1 of 3 single runs on an idle
  machine, so it is not only a load reading. The turn's own goroutine is waited for — the start helper
  cancels it and waits (`llm_crash_test.go`) — so the writer is something the harness does not join, and a
  context-less local git call survives that cancel. Unestablished: which call, and why the rate moves with a
  test file that does not run. A red naming one of these while other packages run concurrently is a reading
  about the machine — re-run the package alone before believing it, and the single test if that passes.
  Re-measured where `backend/exec`'s event-stand-in sleeps became condition waits: 0 of 10 filtered
  single-test runs and 0 of 5 full-package runs, so the sleeps that went are not what this cleanup waits
  for. The `backend/server` one appeared in 1 of the 2 `make check-race` runs made for the same change.
- **`pkg/subproc`'s `TestWatchStopsReportingOnceTheSubprocessFinished`** counts a watchdog report that
  arrives after `stop()` returned: "a finished subprocess reported 1 time(s), want 0". Measured here: 1 of
  12 single runs on an idle machine, 5 of 6 against a 16-way CPU spinner, once inside `make check`. It reads
  its own counter under its own mutex and names none of the packages on the parallel list, so the reading is
  a 20 ms tick in flight between the deadline and the stop. A busy machine widens that window; nobody has
  made it narrow.
- **A package that dies at its own 10-minute timeout with a test under `running tests:` whose worker
  goroutine is missing from the dump** is a `t.Fatalf` inside a spawned goroutine, not a wedge. `Goexit`
  runs the deferred `wg.Done` but never the `close(start)` the test is blocked on, so a failure nobody
  gets to read becomes a package-wide panic. `backend/exec`'s
  `TestCommitAndMerge_SerializesPerProject` did this in 2 of 2 full-scope `-parallel 4` runs, where
  `runGit` failed under contention. Its staging calls are on the test goroutine now and its receives are
  bounded (`awaitSignal` in `wait_test.go`), so the same git failure prints git's own words. Read a fresh
  one the same way: find the goroutine that is *not* in the dump, then the receive waiting on it.
- **`backend/exec`'s `TestManualSummarizePublishesCompressedContextUsed`** failed inside a two-lane
  `make check` with `no summarization frame was published`, while the log line carrying the same `task_id`
  says `manual summarization complete`. Green 4 of 4 alone. The work finished and the wait for its frame did
  not, which is the starved-goroutine shape of the `backend/stream` row below; the box held 7 `.test` binaries
  and 8 vitest forks on its 8 cores.

- **`backend/stream`'s `TestAConnectionThatMissedFramesIsEndedSoTheClientResyncs`** waits up to 10 s for the
  writer goroutine to end a connection with a hole in it, and fails with "still streaming a gap" when the
  bound passes. It went red in 2 of 2 `make check` runs and 1 of 4 full-scope `go test -parallel 8` runs,
  which is why `test-go` runs 4. `-parallel 4` does not hold: a nine-run sweep of six packages went red
  3 times on this revision, 0 of 3 on the revision before it. **Running it alone is not a clean reading
  either.** With `-run` selecting only it, 1 of 10 `-count=1` runs failed. The same selection on the
  revision before this task's sleeps became condition waits failed 2 of 10. So the earlier "green 5 of 5
  alone" is dropped: a red naming it with nothing else running is a one-in-ten starved goroutine, not the
  change under test. Repeating it inside one process (`-count=5`) put a failure in 3 of 9 process-runs
  here and in 0 of 3 on the earlier revision, samples too small to rank either way. No exec test file
  reaches `backend/stream`'s binary, so the reading is one goroutine starved rather than a shared fixture;
  *mechanism not established*.
