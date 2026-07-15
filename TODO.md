# TODO

## Goal
Standardize naming conventions, extract magic strings/numbers, improve code organization, and remove dead code across the frontend.

## Tasks

### 1. Create `$lib/constants.ts` — Centralized Constants
- [x] Create `frontend/src/lib/constants.ts` with grouped constants:
  - `TASK_STATUSES = ['backlog', 'in_progress', 'review', 'done']`
  - `EXECUTION_STATES = ['running', 'idle', 'stopped', 'failed', 'pulling_image', 'building_image', 'starting_container', 'cancelled', 'awaiting_review', 'completed']`
  - `SCROLL_BOTTOM_THRESHOLD = 32` (pixels)
  - `DRAFT_SAVE_DEBOUNCE_MS = 100`
  - `STALE_THRESHOLD_MS = 60_000`
  - `HEARTBEAT_INTERVAL_MS = 30_000`
  - `RECONNECT_MAX_DELAY_MS = 30_000`
  - `HIGHLIGHT_CACHE_MAX = 50`
  - `TOAST_DURATION_MS = 4000`
  - `TOOL_RESULT_TRUNCATE_BYTES = 200_000` (if found in codebase)
- [ ] Add JSDoc comments to each constant group
- [ ] Verify `npm run check` passes

### 2. Wire Constants Into Existing Code
- [x] `use-auto-scroll.svelte.ts`: Replace inline `32` with `SCROLL_BOTTOM_THRESHOLD`
- [x] `TaskChat.svelte`: Replace inline `DRAFT_SAVE_DEBOUNCE = 100` with constant
- [x] `stream.ts`: Replace inline `STALE_THRESHOLD`, `HEARTBEAT_INTERVAL`, `maxDelay` with constants
- [x] `terminal.ts`: Replace `TERMINAL_STALE_THRESHOLD` with shared `STALE_THRESHOLD_MS`
- [x] `highlighter.ts`: Replace inline `HIGHLIGHT_CACHE_MAX = 50` with constant
- [x] `dialogs.svelte.ts`: Replace inline `4000` with `TOAST_DURATION_MS`
- [x] `TaskChat.svelte`: Replace inline `32` in scroll handler with `SCROLL_BOTTOM_THRESHOLD`
- [x] Verify `npm run check` passes

### 3. Extract `updateTaskInBoard()` Helper
- [x] Create helper in `$lib/stores/app.svelte.ts` (or new `$lib/stores/board-helpers.ts`):
  ```ts
  export function updateTaskInBoard(taskId: string, updated: Task) { ... }
  ```
- [x] Replace 14+ occurrences of `appStore.board = { ...appStore.board!, tasks: appStore.board!.tasks.map(t => t.id === taskId ? updated : t) }` in:
  - [x] `Card.svelte` (~10 occurrences)
  - [x] `App.svelte` (~2 occurrences)
  - [x] `Board.svelte` (~1 occurrence)
  - [x] `use-board-dnd.svelte.ts` (~1 occurrence)
- [x] Verify `npm run check` passes

### 4. Extract `mergeQueuedUpdate()` Helper
- [x] Create helper in `$lib/utils/` (e.g., `task-utils.ts`):
  ```ts
  export function mergeQueuedUpdate<T extends { queued?: boolean }>(task: T, updated: T): T { ... }
  ```
- [x] Replace 5 occurrences of `Object.assign(task, (updated as any).queued ? { ...task, ...updated } : updated)` in:
  - [x] `Card.svelte` (~4 occurrences)
  - [x] `TaskChat.svelte` (~1 occurrence)
  - [x] `Board.svelte` (~1 occurrence)
- [x] Verify `npm run check` passes

### 5. Remove Deprecated `getCheckpointEntryCheckpointState`
- [x] Remove the function from `$lib/utils/chat-entries.ts`
- [x] Remove its test usage in `$lib/utils/__tests__/chat-entries.unit.ts` (the verification helper that calls it)
- [x] Verify `npm run check` passes
- [x] Verify `npm test` passes

### 6. Remove Unused `useCheckpointTracker` Composable
- [x] Delete `frontend/src/lib/composables/use-checkpoint-tracker.svelte.ts`
- [x] Remove the unused import from `TaskChat.svelte`
- [x] Verify `npm run check` passes

### 7. Add JSDoc to Exported Functions
- [x] `$lib/utils/sort-tasks.ts` — already has JSDoc
- [x] `$lib/utils/format-time.ts` — already has JSDoc
- [x] `$lib/utils/path.ts` — already has JSDoc
- [x] `$lib/composables/use-auto-scroll.svelte.ts` — already has JSDoc
- [x] `$lib/composables/use-board-dnd.svelte.ts` — already has JSDoc
- [x] `$lib/composables/use-subtask-hierarchy.svelte.ts` — already has JSDoc
- [x] `$lib/composables/use-cursor-position.ts` — added JSDoc
- [x] `$lib/composables/use-is-mobile.svelte.ts` — already has JSDoc
- [x] Verify `npm run check` passes

### 8. Final Verification
- [x] Run `npm run check` — pre-existing errors only (152, none from our changes)
- [x] Run `npm test` — all 94 unit tests pass
- [x] Run `npm run build` — frontend builds successfully
- [x] Verify no magic numbers > 10 without named constants (spot check — all extracted)
- [x] Verify status strings use `TASK_STATUSES` constant where appropriate (API strings remain as-is per backend contract)

## Notes
- `context/index.ts` provides Svelte context (setContext/getContext) — NOT dead code, different from stores
- `stores/app.ts` is a legacy compat layer still used by `main.ts` for PWA — keep it
- `$derived.by()` usage in components is intentional (complex derived state) — no changes needed
- `svelte-ignore non_reactive_update` comments are for DOM refs via `bind:this` — these are valid and needed
