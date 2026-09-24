# Testing Conventions & Mock LLM

Read this when writing or debugging e2e or unit tests. `AGENTS.md → Testing` keeps the commands and the
short rules; this file holds the environment, the conventions, the mock-LLM fixtures and the tests that fail
only under load.

## E2E conventions
- Descriptive spec filenames in `frontend/tests/e2e/`; shared helpers in `helpers.ts`.
- `test.describe.serial()` when tests share state; unique identifiers (`Date.now()`) to avoid
  interference; wait for selectors, no arbitrary delays (drag-and-drop is the known exception —
  headless is slow). `serial` amplifies whatever coupling is left: one failure skips the rest of the
  block as "did not run".
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
- **Config-modifying tests** (settings, models, shortcuts, mode select, …) must run in the `isolated`
  project — add the spec name to `ISOLATED_SPECS` in `frontend/playwright.config.ts` (one array; the
  chromium lane's `testMatch` is derived from it, so the two lanes cannot drift apart).
- **The harness serves `frontend/dist` from disk** — its server is `go run .`, a development build — and
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
- **Offline = abort `**/api/**`, not `context.setOffline(true)`.** The offline screen
  (`App.svelte`'s `hasLocalCache` branch) exists for "app booted, API unreachable, cache
  available"; going fully offline takes the document down with it and you are asserting on
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

`make test-frontend` ends with two gates over `frontend/src`:

- **`npm run check:dead`** — knip over the **production view**: unused files, exports, exported types,
  duplicate exports, unlisted imports, unused dependencies. `ignore` in the config excludes every test
  file, so a symbol kept alive only by its own unit test is still reported — that is the property the gate
  exists for, and it is how a test import hid an export no product code read.
- **`npm run check:unused`** — `frontend/scripts/check-unused.sh` runs `svelte-check` on
  `tsconfig.unused.json`, which turns on `noUnusedLocals` and `noUnusedParameters`; `npm run check` does
  not. The script keeps the two `is declared but …` messages, prints `file:line name`, and fails on any.
  It covers test files too, because `tsconfig.json` includes `src/**/*.ts`.

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
that text is gone. The declaration is tagged `@knipignore` and `tags: ["-knipignore"]` in `knip.json` is the
config's only exemption. Both gates are also blind to an object property nobody reads, a CSS class no
template uses, and a REST route the UI stopped calling.

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
- **Drag-and-drop flakes:** headless is slow — add `waitForTimeout` after the drag, increase the
  predicate timeout.
- **Mock LLM routing wrong:** check keyword matching in `mock-llm.js` and the fixture files.

## Test environment

Two dedicated test servers with isolated databases plus one shared mock LLM, fully managed by
`frontend/tests/global-setup.ts` (torn down and rebuilt each run):

| Server | Port | Data dir | Purpose |
|--------|------|----------|---------|
| Main | 3000 | `/tmp/kanban-test-data` | `chromium` Playwright project (default tests) |
| Isolated | 3001 | `/tmp/kanban-test-data-isolated` | `isolated` project (serialized, 1 worker) — config-modifying suites; see `ISOLATED_SPECS` in `frontend/playwright.config.ts` |
| Mock LLM | 9999 | — | shared; `frontend/tests/mock-llm.js` |

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

Test servers are spawned via `go run .` and **inherit your shell environment** — export
`MIKE_ENCRYPTION_KEY` before running e2e, or global-setup fails with "Server on port 3000 did not start
within 15000ms".

They also start with `MIKE_DISABLE_UPDATES=1`, so no run is ever a browser that owes the release opt-in.
The boot pass would ask the question of every fresh context, and a yes there means an outbound check to
the manifest host. `disabled` is the posture the client stops on — no dot, no question
(`docs/auto-update.md`). A spec that wants the dot stubs `/api/updates/status`, as
`settings-update-dot.spec.ts` does.

### A run must leave nothing behind

`globalTeardown` is `tests/global-teardown.ts`, whose default export is *only* teardown — naming
`global-setup.ts` there re-runs setup at the end of every run and leaves a server pair, lock and database
nothing will stop. Verify after any change to setup/teardown: `pgrep -xc mike` → 0, nothing listening on
3000/3001/9999, no `/tmp/kanban-test-data*`. Teardown stops servers by **identity** (`--data-dir <dir>`):
they are `setsid go run .` grandchildren, so the port owner is not the process that should stop, and
`lsof -ti:PORT` also answers with clients.

If a prior run was interrupted, clean stale state first (stale processes hold the ports — symptom
`ERR_CONNECTION_REFUSED`, or a stale TLS server answering a `--no-tls` test — and a stale lock makes
global-setup run teardown instead of setup):

```bash
lsof -ti:3000,3001,9999 | xargs kill 2>/dev/null
rm -f /tmp/kanban-test-setup.lock
rm -rf /tmp/kanban-test-data /tmp/kanban-test-data-isolated /tmp/kanban-test-worktrees
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

- **Tests inside a Go package run concurrently now.** `backend/exec` calls `t.Parallel()` in 664 of its
  top-level tests and the package went 130 s → 43 s; it is `make test-go`'s critical path, so the suite went
  131 s → 43 s. Serially, 13 tests each paid the 3 s `executorExitGrace` and three more waited out a real
  provider-silence ceiling.
- **The tests left serial are the ones touching a process global.** `t.Setenv` refuses to run in a parallel
  test, which is why `bash_stream_test.go` is one file. The other global is the CWD: template resolution reads
  it on every render (`prompts.resolvePromptDir`). So the 23 exec tests that chdir to the repo root cannot
  overlap — a concurrent restore moves the CWD mid-turn, and the failure reads
  "open prompts/summarize.tmpl: no such file or directory". The rule sits at `chdirRepoRoot`.
- **`-parallel 4` in `test-go` is capacity, not a claim about the suite**, and its value is measured: see the
  `backend/stream` entry under *Tests that fail only under load*.

### Running less than the gate

- `make test-diff` runs the Go packages holding a changed `.go` file — staged, unstaged or untracked — and
  leaves `go test`'s result cache **on**, which is the saving. It builds `GO_SCOPE` first: it runs no
  dependent of a changed package, so a signature change is caught by that build and not by a test. The list
  is `scripts/touched-packages.sh`.
- `npm run test:related -- <changed files>` runs the vitest files that import them. Measured on two library
  files: 329 tests in 8 s, against 3227 in 36 s for the suite.
- `npx playwright test --last-failed` re-runs the previous run's failures, `--only-changed` the files git
  says moved. Neither is a gate: `make check` and a full e2e run stay the claims.

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
the base image never pulls. Check it with `podman images` on nothing running: a red naming one of those
suites, with that error in the log, is a reading about the machine.

## Tests that fail only under load

- **What runs short is memory, and it is usually already gone before the run starts.** `/tmp` is tmpfs
  on this machine, so anything large under it is RAM. A project's `rw` sandbox mount at
  `/tmp/kanban-mount` — 11 GB of Go build cache — left 4 GB available at idle, and `free -m` read 0
  partway through a run. Under that shortage the failures rotate: turn-bound chat waits report neither
  send nor stop, and a middle-click tab opens but its navigation stalls. Check `df -h /tmp` before
  believing a red that names a spec on this list. Measured on the starved machine: `workers: 4` failed 5,
  `--workers=3` failed 8, and raising the popup wait from 20 s to 30 s changed nothing. The worker count
  is not the variable; the free memory is.

- **`backend/extensions`' `TestHandleInstall_BuildFailureLeavesNoRowAndNoFiles`** fails on roughly 1 in 3
  `make check` / `-parallel 8` runs **on a CPU-loaded machine** and never alone or unloaded, so a red
  `test-go` naming it is not necessarily your regression. Re-run it alone; if it passes, read the mtime
  line the failure prints (`describeTree` in `handler_test.go`) before believing either conclusion.
  Established: the rollback runs and logs the right path, the surviving tree carries the original
  checkout's mtimes, and `RepairMissingFiles` and the fake podman's fail-once budget are ruled out.
  Unestablished: who leaves the tree behind.
- **`backend/server`'s `TestTaskMoveUpdatesUpdatedAt`** was seen failing once, under `make check-race` over
  five packages, and not since: 10 `-count=10 -race` runs on the branch and 10 on `HEAD` came back clean.
  It asserts that two backlog tasks the test never writes come back ordered by `updated_at`, so the likeliest
  reading is a timestamp tie broken differently under load — *likeliest*, not established. A single
  unreproduced red naming it is not evidence of anything; neither is a green run proof it cannot recur.
- **`backend/sandbox`'s `TestMemoryLimitEnforced`** fails roughly 2 in 8 with two container suites running
  concurrently and 6/6 alone. It asserts the guest OOM-killed one process, and under host pressure the
  **host** memcg killer takes the whole runsc sandbox instead (exit 128 plus `WaitPID … EOF`, container
  gone). A red naming it is a reading about the machine, not about a memory limit that stopped working.
- **`backend/server`'s `TestTaskOperatingModeRemount/does not remount when running`** failed once in a full
  `make check` run with `expected sandbox to be created after start`, and passed alone and in two further
  full runs. **Mechanism inferred, not proven — and the inference no longer holds**: it was read as the task
  waiting in the memory gate because the suite was consuming the machine's RAM, and the gate no longer looks
  at what the host's other users hold (`docs/sandbox-runtime.md → The memory budget`). No supervisor pause
  was logged and the refusal path logs nothing, so the observation stands with no explanation. What remains
  established is that a test which starts a sandboxed task and expects a container depends on the gate
  admitting it.
- **A `t.TempDir()` cleanup failing with `unlinkat …: directory not empty`** means a subprocess is still
  writing while the removal walks. Two tests carry it here: `backend/exec`'s
  `TestToolCallWithIntactArgumentsRunsTheSameCommand`, and `backend/server`'s
  `TestCommitAndDoneMergeConflictSystemMessage` under `make check-race`. The exec one showed once inside
  `make check` at `-parallel 8` and in 4 of 61 filtered single-test runs on one branch, against 0 of 50 with
  that branch's new `backend/exec` test file set aside and 0 of 21 at the revision before it. No
  full-package run of either package shows it. The turn's own goroutine is waited for — the start helper
  cancels it and waits (`llm_crash_test.go`) — so the writer is something the harness does not join, and a
  context-less local git call survives that cancel. Unestablished: which call, and why the rate moves with a
  test file that does not run. A red naming one of these while other packages run concurrently is a reading
  about the machine — re-run the package alone before believing it, and the single test if that passes.
- **`backend/stream`'s `TestAConnectionThatMissedFramesIsEndedSoTheClientResyncs`** waits up to 10 s for the
  writer goroutine to end a connection with a hole in it, and fails with "still streaming a gap" when the
  bound passes. It went red in 2 of 2 `make check` runs and 1 of 4 full-scope `go test -parallel 8` runs, and
  in 0 of 3 at `-parallel 4` — which is why `test-go` runs 4. Alone it is green 5 of 5, three of those under a
  12-way spinner load, so raw CPU load is not the trigger and the other packages are. No exec test file
  reaches `backend/stream`'s binary, so the reading is one goroutine starved rather than a shared fixture;
  *mechanism not established*.
