# TODO

## Goal
On mobile (≤768px), in the Task View header, tapping the worktree path (`.project-prefix`, between the sandbox button and the status badge) shows the full path as a tooltip.

## Context (analysis)
- Target element: `frontend/src/lib/components/CardDetail/TaskHeader.svelte` — `<span class="project-prefix" title={getWorktreeDisplayPath(...)}>` in `.header-main`, sitting between the sandbox button/badge and the status `.badge`. It is `flex: 1` with `overflow: hidden; text-overflow: ellipsis`, so on mobile it truncates hard.
- It already carries a native `title`, but `title` only shows on hover — mobile taps do nothing today.
- The span is a plain label (not a button) → no tap-conflict with expand/collapse.
- `useIsMobile()` composable (768px breakpoint, `$lib/composables/use-is-mobile.svelte`) is the established mobile gate.
- No persistent `transform` on header ancestors → a `position: fixed` tooltip renders against the viewport (safe).
- Old TODO.md (provider task-concurrency) is fully `[x]` — replaced by this plan.

## Design decisions
- **Mobile-only** gate (`useIsMobile`); desktop behavior unchanged (native `title` hover tooltip).
- **Tap → tooltip** with the full display path (`getWorktreeDisplayPath`, same string as shown/truncated). Every mobile tap shows it (even when not truncated — simple + predictable; worktree paths are usually long in production). Re-tap the path toggles it off.
- **Placement:** below the span (`rect.bottom + 6`, header is at the very top so no room above), left-aligned to the span, clamped to the viewport (8px margins; post-layout measure-and-shift for the real wrapped width).
- **Dismissal:** auto-hide after 2.5 s; any outside tap (document `pointerdown`, capture) while open; resize/orientationchange (fixed-px anchor goes stale). One tooltip at a time (only one such element exists).
- **Rendering:** `{#if}` root-level `<div class="path-tooltip" role="tooltip">` in TaskHeader (sibling of `.header`) with `position: fixed` — escapes the span's `overflow: hidden` clip. `pointer-events: none` (not selectable; tapping over it = outside tap → dismiss). `max-width: min(80vw, 360px)` + `word-break: break-all` for very long paths. `z-index: 9000` (above content, below toast 9999).
- No keyboard focusability added (span stays a non-focusable label); no `role="button"` — mobile-tap-only feature per the request.

## Tasks

### 1. Tooltip implementation — `TaskHeader.svelte`
- [x] Import `useIsMobile`; derive `fullPath = getWorktreeDisplayPath(workspaceDir || '', task.project_id, taskId)`.
- [x] State: `pathTip = $state<{top, left} | null>(null)`, auto-hide timer ref, `pathSpanEl`/`pathTipEl` as `$state` DOM refs (FileEditor pattern).
- [x] `handlePathTap()` on the span: no-op unless `isMobile` and `fullPath`; toggle off if open; else anchor below span with viewport clamp, start 2.5 s auto-hide, arm outside-`pointerdown` (capture, ignores taps originating on the span so re-tap toggles off via click) + `resize`/`orientationchange` listeners, and a `requestAnimationFrame` measure-and-shift clamp for the actual rendered width.
- [x] `clearPathTip()` removes all listeners/timer, nulls state (single exit path); `$effect` cleanup on unmount.
- [x] Markup: tooltip div after the header (`role="tooltip"`, `style:top/left`, `bind:this`, `aria-describedby` wiring).
- [x] CSS: `.path-tooltip` per design decisions above (uses existing vars: `--color-surface-3`, `--color-border`, `--radius-md`, `--font-mono`, `--color-text-primary`).
- [x] `svelte-check` clean — two svelte-ignore comments for the intentional non-interactive tap label (BaseModal precedent).

### 2. Frontend unit tests
- [x] New `frontend/src/lib/components/__tests__/task-header-path-tooltip.unit.svelte.ts` (pattern: `card-action-padding.unit.svelte.ts` — `vi.stubGlobal('matchMedia', …)`, mock `$lib/stores` + Icon/shield svgs + `$lib/api`):
  - [x] mobile stub: tapping `.project-prefix` (pointerdown+click) → `.path-tooltip` visible, text = expected full path (+role/aria wiring).
  - [x] re-tap (real tap sequence) → hidden — caught & fixed a toggle bug: the document capture pointerdown closed then the click reopened; handler now ignores pointerdowns originating on the span.
  - [x] auto-hide: `vi.useFakeTimers()`, advance 2500 ms → hidden.
  - [x] outside `pointerdown` → hidden (async Svelte flush → `vi.waitFor`).
  - [x] desktop stub (`matches: false`): tap does nothing.
- [x] `cd frontend && npm test` green — 104 files / 1225 tests. (Required fix: pre-existing `task-header-sandbox-dot.unit.svelte.ts` renders TaskHeader without a matchMedia stub → added the standard desktop polyfill, same as card-action-padding.)

### 3. E2E test
- [x] New `frontend/tests/e2e/task-header-path-tooltip-mobile.spec.ts` (main project — no config changes, stays out of `isolated`):
  - [x] Create a task via API (`createTaskViaApi`), `navigateToTask`.
  - [x] `page.setViewportSize({ width: 390, height: 844 })` (established mobile e2e convention, reset to 1280×720 in `finally`).
  - [x] Tap `.project-prefix` → `.path-tooltip` visible, text equals the span's `title` value; bounding box below the label and clamped inside the viewport.
  - [x] Tap again → hidden. Re-open, tap the status badge (neutral outside target) → hidden.
  - [x] Re-open, auto-hide (≤4 s poll) → hidden.
  - [x] Desktop viewport: tapping the path does not open the tooltip.
  - [x] Cleanup: delete task, reset viewport.
- [x] `cd frontend && npx playwright test task-header-path-tooltip-mobile` green (7.1 s). (Env note: `frontend/dist` had to be built first — e2e serves the SPA from it and global-setup does not build; this sandbox had no dist.)

### 4. Validation
- [x] `cd frontend && npm run check` (svelte-check) — 0 errors, 0 warnings (after final component edits).
- [x] `cd frontend && npm run build` — clean (server serves `dist/`).
- [x] Regression batch: `npx playwright test task-header-path-tooltip-mobile task-view-column-nav card-title-truncation` — 11 passed (30.4 s).
- [x] Test-server ports/state cleaned up after runs (teardown SIGTERM doesn't take in this sandbox — manual kill per AGENTS.md stale-state procedure).

## Discovered Tasks — copy button in the tooltip (added after user request)

### 5. Copy button — `TaskHeader.svelte`
- [x] Copy `<button type="button" class="path-tooltip-copy" aria-label="Copy path">` inside the tooltip (copy icon; becomes a check + `Copied` while confirmed). Button gets `pointer-events: auto`; the tooltip container keeps `pointer-events: none` so taps on the tooltip body still pass through and dismiss.
- [x] `handleCopyPath()`: `navigator.clipboard.writeText(fullPath)`, falling back to a hidden textarea + `document.execCommand('copy')` if the async API is unavailable (non-secure contexts). `try/catch` — a failed copy leaves the tooltip open for retry.
- [x] On copy: `pathCopied` state for ~1.5 s (button switches to check/"Copied", `aria-label`/`title` update) + restart the 2.5 s auto-hide timer so the confirmation is visible.
- [x] Outside-`pointerdown` handler: also ignores pointerdowns originating inside the tooltip (`pathTipEl`), not just the label (same capture-order bug as the label toggle — verified by unit test).
- [x] Tooltip markup split into `.path-tooltip-text` span + button (existing text assertions updated to the scoped locator); hover highlight uses border/text (surface-3 hover would blend into the tooltip's own background).
- [x] `svelte-check` 0/0 (real `<button>` → no a11y ignores needed).

### 6. Copy button — tests + validation
- [x] Unit (`task-header-path-tooltip.unit.svelte.ts`): button present when open; click → `navigator.clipboard.writeText` called with the full path (mocked); copied state shown; tap on the button does NOT dismiss the tooltip; `execCommand` fallback used when the async API is unavailable; failed copy (rejection) → no confirmation, tooltip stays open. 10/10 pass.
- [x] E2E (`task-header-path-tooltip-mobile.spec.ts`): `context.grantPermissions(['clipboard-read', 'clipboard-write'])`; tap path → click copy button → `navigator.clipboard.readText()` equals the full path; "Copied" confirmation visible; tooltip stays open, then the re-armed auto-hide fires.
- [x] Validation: svelte-check 0/0 · full vitest 104 files / 1239 tests green · `npm run build` clean · `npx playwright test task-header-path-tooltip-mobile task-view-column-nav` — 10 passed (31.5 s) · test-server ports cleaned.
- [x] Count note: the 1225→1239 unit-test delta = +4 (new copy tests) + 10 (`extension-permissions.unit.ts` 13→23, pulled in when the environment merged `main` into this worktree at `b03164fc` mid-session; verified via per-file sums and git).

## Notes
- Scope is exactly the Task View header path per user clarification — diff file paths, editor toolbar path, and other truncated paths are out of scope.
- Tooltip text = same string the span displays (home-relative worktree path, e.g. `~/projects/kanban/.worktrees/proj/task-id`); in the e2e env (worktrees under `/tmp`) the display falls back to the task-id basename — the test asserts tooltip text equals the `title` attribute, which holds in both cases.
- (Superseded) The old "no way to copy the path" limitation is addressed by the copy button (Discovered Tasks 5–6).
- No backend changes.
