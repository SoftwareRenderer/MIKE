# Testing Conventions & Mock LLM

Read this when writing or debugging e2e/unit tests. The commands, the test-server table and the
stale-state cleanup stay in `AGENTS.md → Testing`.

## E2E conventions
- Descriptive spec filenames in `frontend/tests/e2e/`; shared helpers in `helpers.ts`.
- `test.describe.serial()` when tests share state; unique identifiers (`Date.now()`) to avoid
  interference; wait for selectors, no arbitrary delays (drag-and-drop is the known exception —
  headless is slow).
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
- **The harness serves `frontend/dist`, and `global-setup` never builds it.** An e2e run proves what
  the last `npm run build` contained, never what is in `src/`, so a frontend change nobody built
  reads as green either way. Build before the run, then make it prove it bites: revert the changed
  file, rebuild, and watch the spec fail before believing the green.
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
- **A spec that starts a task or sends a chat message owns its own project.** The executor queues
  turns per project, so on the shared project the answer lands behind five other workers' tasks:
  the symptom is `.btn-send` (idle) never becoming visible, in whichever chat spec lost the race
  that run. Cheap to honour, and it also makes each task's worktree its own.
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
- **`[role="dialog"]` is not a selector here.** BaseModal, `CommitDiffOverlay`,
  `TerminalPanel` and BOTH explorer views carry `role="dialog"`, and every ChatMessage's
  edit `<dialog>` has it implicitly — so the bare locator is a strict-mode violation
  whenever a chat or the explorer is mounted, and `expect(x).not.toBeVisible()` then
  proves nothing. Scope by content: `[role="dialog"]`.filter({ has: page.locator('.commit-textarea') }),
  or the login modal by its `input[type="password"]`.
- **Which control you actually need** (each of these was a silently-wrong assertion):
  the Changes panel lists `.changes-file-row`s and the `.diff-row-clickable`s only render
  for the *selected* file — and an UNTRACKED file has no line diff until staged, so a
  "click diff line" test must `git add` first. `BranchFileBrowser` uses class
  `.new-file-input` for BOTH its new-file and new-folder rows (disambiguate with
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

**Concurrency is part of the environment, not an optimization.** `workers: 4` is measured, not
guessed: six workers run the suite ~2.7 min instead of ~3.0, and buy that speed with 1-4 failures
per run that ROTATE between specs (chat-core, chat-messages, diff-viewer, link-like-rows,
thread-new-thread — every one of them green alone and in pairs). They share the one lane server,
which queues executor turns, so under six workers "wait for the task to go idle" becomes a race
against everyone else's turns. Any new turn-bound wait must say so in its budget (use the
`waitForSendButton(page, timeout)` turn budgets in `helpers.ts`, not the 10s `expect.timeout`).

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


## Diagnostic scripts (ad-hoc investigation)

For request-pattern/race investigation, write standalone Playwright scripts in `frontend/tests/` as
`.mjs` files run against a live dev server: `make run` then `node frontend/tests/<script>.mjs`. Use
`page.evaluate()` for API calls (CSRF token from `/api/auth/session` for POST/PUT/DELETE),
`route.fetch()` + `route.fulfill()` for interception, `Date.now()` offsets for timing, and
`page.screenshot({ path: '/tmp/debug.png' })`.

## E2E failure patterns

- **`ERR_CONNECTION_REFUSED`** / wrong-protocol response: stale processes from an interrupted run hold
  the ports — run the cleanup block in `AGENTS.md → Testing → Test Environment`.
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

## Conventions that bite (moved from AGENTS.md)
### E2E Tests
Specs in `frontend/tests/e2e/` (helpers in `helpers.ts`); **mock LLM by default** (`frontend/tests/mock-llm.js`, port 9999, fixtures in `frontend/tests/fixtures/`). Conventions that bite:
- **Config-modifying specs** (settings, models, shortcuts, mode select, …) must run in the `isolated` project — add the spec name to `ISOLATED_SPECS` in `frontend/playwright.config.ts` (one array; the chromium lane's `testMatch` is derived from it, so the two lanes cannot drift).
- **Heavy fixtures get a dedicated repo** — never write big files into the shared `/tmp/kanban-test-repo`, where another spec's `git add -A && git commit` races with them.
- `test.describe.serial()` + `Date.now()` identifiers for shared state; wait for selectors, not delays (drag-and-drop is the known exception).
- **Every spec that touches the board owns its project** via `isolatedProject()` + `gotoProject()` (`helpers.ts`) — the lane used to share global-setup's one project across ~9 concurrent specs, and two runs of identical code then differed by 15 failures. Key it at the *worker*, never the file, because `fullyParallel` spreads one file across workers. Specs asserting on the **global** view (sidebar project list, file browser, add-project flow) must not use it, and must never do `.project-item` + `.first()` — use `selectProject(page, id)`. `serial` amplifies leftover coupling: one failure skips its whole block as "did not run".
- **`workers: 4` is measured** (see `playwright.config.ts`): 6 is ~10% faster and buys 1-4 rotating turn-queue failures per run.
- **A spec that runs an executor TURN owns its own project** — turns are queued per project, so on the shared project a fork's answer waits behind every other worker's task and the spec times out waiting for the task to go idle (`.btn-send` never returns). Same for anything that mutates the project: `git add`/`commit`, or its **sandbox configuration**. Concurrent git in the shared repo makes *other* specs lose their file listing (it is a live `git status` that never retries after an error), and flipping `sandbox_enabled` makes the project explorer seed the **container** mount path and ask the host for it — 400 `path outside allowed project directory`, panel stuck on the error.
- **Pin the mock LLM's fixture with `llm-fixture:<name>` in the prompt.** Keyword routing is unreachable from an ordinary task prompt — the plan-first skill is in every system prompt, so plan routing answers first and a spec asking for "edit" silently gets `create_task`. Convention + the two measured chat behaviours (`.awaiting-llm` = "request sent, nothing back yet", absent while streaming; a task auto-starts, so wait for idle and assert on the entry that appears *after* your send): `docs/testing.md`.
- **`[role="dialog"]` is never a unique locator** (BaseModal, both explorer views, `TerminalPanel`, `CommitDiffOverlay`, and every ChatMessage's `<dialog>` all have it) — filter by the content that identifies the dialog you mean. Selector-by-component and the commit/review-bar rules: `docs/testing.md`.
- **A spec exists only if it needs a browser or the real backend** (DnD, real focus/keyboard, scroll/CSS geometry, iframes + CSP, git/worktrees, WS reconnect). Component state belongs in vitest — 1.4k assertions in ~20 s versus a page-load tax per spec. And **never write a `readFileSync('../Foo.svelte')` + `toContain(...)` "test"**: it cannot fail a real bug and breaks on renames. The suite is budgeted at a few minutes for a full run; keep it there.

