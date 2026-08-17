# TODO

## Goal
In Task View (CardDetail), Left/Right cycle focus between the Board, Chat, and Diff columns, and Up/Down behavior dispatches on which column has focus (Board: task cards, Chat: page scroll, Diff: diff file headers).

## Tasks

### 1. Pure resolvers (TDD red → green)
- [x] Write failing unit tests `frontend/src/lib/composables/__tests__/task-detail-column-nav.unit.ts`:
  - `resolveDetailColumn(order, current, key)` — cyclic Right: board→chat→diff→board; cyclic Left: board→diff→chat→board; neutral anchor (current=null): Right→first, Left→last
  - `resolveDiffFileTarget({ files, activeElement, key })` — Down/Up wrap across diff file header elements; anchor: focus inside a file section → next/prev header; neutral focus → first (Down) / last (Up); empty list → null
- [x] Implement `frontend/src/lib/composables/use-task-detail-column-nav.ts` exporting `DetailColumnId` (`'board' | 'chat' | 'diff'`), `resolveDetailColumn`, and `resolveDiffFileTarget`
- [x] `cd frontend && npm test` — new tests pass (green)

### 2. Key handling in CardDetail.svelte
- [x] Left/Right handler in `handleKey`: cyclic column switching with gates — desktop only (skip when `isMobileCtx.isMobile`), no alt/ctrl/meta/shift, `!e.defaultPrevented`, skip when focus is in an editable element (input keeps text-cursor behavior), skip when any modal/overlay is open (existing modal gate), skip when `editingFile` or `diffFullscreen`/`chatFullscreen`
- [x] Landing targets: Board → currently focused overview card, else first `.task-list .task-wrapper > .card`, else the `.column-context` container; Chat → chat input (ChatForm textarea in `.column-chat`), fallback `.chat-messages`; Diff → first `button.diff-file-header`, else the `.column-diff` container; `preventDefault` + `focus()` + `scrollIntoView({ block: 'nearest' })`
- [x] Up/Down dispatch by active column (keeping existing gates + mobile gate):
  - focus in Board column or neutral (outside columns) → existing `resolveOverviewArrowTarget` overview-card navigation (unchanged behavior)
  - focus in Chat column, non-editable → page-scroll `.chat-messages` by ±`clientHeight`
  - focus in Diff column, non-editable → `resolveDiffFileTarget` over `button.diff-file-header` (wraps at both ends)
- [x] Add `tabindex="-1"` to the `.column-context` and `.column-diff` containers (fallback focus targets)
- [x] Add `:focus-visible` styles if missing: `.diff-file-header` and `.chat-messages` in their components' styles

### 3. E2E test
- [x] New `frontend/tests/e2e/task-view-column-nav.spec.ts` (chromium project, `describe.serial`), modeled on `board-arrow-nav.spec.ts` + `diff-viewer.spec.ts` worktree setup:
  - fixture: task moved to in_progress, two files written into its worktree, task view open with ≥2 diff file sections
  - Right from a board card → chat input focused; Right again → first diff file header; Right again → wraps to a board card
  - Left from the diff column → chat input; Left from chat → board card
  - Up/Down in the board column still moves between overview cards (regression)
  - Down/Up in the diff column cycles diff file headers (wraps at both ends)
  - Down with focus on `.chat-messages` scrolls the container down (scrollTop increases); Up scrolls back
  - Down with focus in the chat input does NOT scroll (text editing preserved)
  - Alt+ArrowRight does not switch columns (modifier gate)
- [x] `npx playwright test task-view-column-nav` — passes (6/6)

### 4. Verification & docs
- [x] `cd frontend && npm run check` — no new TS errors (0 errors, 0 warnings)
- [x] `cd frontend && npm test` — full vitest suite green (1049 passed)
- [x] Update AGENTS.md: new "Task View Column Navigation" section (keys, dispatch rules, landing targets, gates, test files) alongside the existing Board Arrow-Key Navigation section

## Notes
- No backend changes; Go tests unaffected.
- Pre-existing e2e failures (NOT regressions — verified by stashing this change and rerunning on the clean baseline): `board-arrow-nav.spec.ts` "Left/Right cycle the 5-column ring" and `diff-viewer.spec.ts` "diff API returns committed diff for done tasks" (`workspace_dir` undefined in the node-side config read).
- Diff file headers are already focusable `<button>` elements — no DiffViewer markup change needed; Enter on a focused header keeps its existing expand/collapse behavior.
- Left/Right DO fire from the chat input (horizontal arrows switch columns so the cycle always works from any landing target); vertical arrows in the input remain native text editing. All other editable targets (e.g. dropdowns) are skipped.
- Mobile: column switching is skipped entirely (mobile already has the tab bar); Up/Down keeps its existing mobile gate (active only on the context tab).
