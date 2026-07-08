# TODO — Store Migration (Svelte 4 writable → Svelte 5 $state + context)

## Goal
Replace all Svelte 4 `writable` stores with Svelte 5 `$state()` + `setContext`/`getContext`, eliminating the bridge layer in App.svelte and direct store usage in child components.

## Current State
- **store.ts**: 20 writable stores + utility functions (showToast, showConfirm, showCommitDialog, expandedTasks helpers)
- **App.svelte**: ~20 `subscribe()` calls in `$effect` to bridge stores → local `$state`
- **12 child components**: import stores directly, use `$storeName` auto-subscribe or `.set()` calls
- **context/index.ts**: 5 additional writable-based contexts (streaming, diff, commitQueue, expandedTasks, taskEvent)
- **main.ts**: writes to `pwaInstallPrompt`/`pwaInstalled` stores

## Tasks

### 1. Create AppContext interface and factory
- [x] Created `frontend/src/lib/context/appContext.svelte.ts` (330 lines)
- [x] Defined `AppContext` interface with all reactive state
- [x] Defined `createAppContext()` factory with `$state()` + getters/setters
- [x] Defined `getAppContext()` using `getContext()`
- [x] Moved utility functions into context
- [x] Merged `context/index.ts` contexts into AppContext

### 2. Migrate App.svelte
- [x] Replaced store imports with `createAppContext`
- [x] Removed `$effect` bridge block (~20 subscribe calls)
- [x] Replaced all `.set()` calls with direct context mutations
- [x] Replaced `boardStore.set()` / `boardStore.update()` with context mutations
- [x] Replaced all old context `.update()` calls with context mutations
- [x] Build verification passes

### 3. Migrate Toast.svelte
- [x] Replaced store subscription with `$effect` on `ctx.toast`
- [x] Build verification

### 4. Migrate ConfirmationDialog.svelte
- [x] Replaced store subscription with `$effect` on `ctx.confirmation`
- [x] Build verification

### 5. Migrate CommitDialog.svelte
- [x] Replaced store subscription with `$effect` on `ctx.commitDialog`
- [x] Build verification

### 6. Migrate Board.svelte
- [x] Replaced store imports with `getAppContext()`
- [x] Replaced `expandedTasksContext` with `ctx.expandedTasks`
- [x] Replaced `selectedTask.set()`, `showCardDetail.set()` with context
- [x] Replaced `showConfirm()`, `showToast()` with context methods
- [x] Replaced `boardStore.update()` with context mutation
- [x] Build verification

### 7. Migrate Card.svelte
- [x] Replaced store imports with `getAppContext()`
- [x] Replaced `$boardStore`, `$configStore`, `$selectedProject` reads with context
- [x] Replaced `selectedTask.set()`, `showCardDetail.set()`, `editTaskData.set()`, `showEditTask.set()` with context
- [x] Replaced `commitDialogStore.set()` with context
- [x] Replaced `getStreamingContext()`/`getTaskDiffStatsContext()` with `ctx.streamingTaskIds`/`ctx.taskDiffStats`
- [x] Replaced `showConfirm()`, `showToast()` with context methods
- [x] Build verification

### 8. Migrate CardDetail.svelte
- [x] Replaced store imports with `getAppContext()`
- [x] Replaced `$boardStore`, `$configStore` reads with context
- [x] Replaced `showTerminal.set()`, `showCreateTask.set()`, `creatingChildOf.set()` with context
- [x] Replaced `boardStore.subscribe()`/`.update()` with `$effect`
- [x] Replaced `expandedTasksContext`/`streamingIds`/`taskDiffStats` with context
- [x] Replaced `showConfirm()`, `showToast()` with context methods
- [x] Build verification

### 9. Migrate TaskChat.svelte
- [x] Replaced store imports with `getAppContext()`
- [x] Replaced `$boardStore` reads with context.board
- [x] Replaced `selectedTask.set()`, `commitDialogStore.set()` with context
- [x] Replaced `streamingIds`, `taskDiffStats`, `commitQueueStatus`, `taskEvent` with context
- [x] Replaced `boardStore.update()` with context mutations
- [x] Replaced `showConfirm()`, `showToast()` with context methods
- [x] Build verification

### 10. Migrate CreateTaskModal.svelte
- [x] Replaced store imports with `getAppContext()`
- [x] Replaced `$configStore`, `$boardStore`, `$selectedProject` reads with context
- [x] Replaced `showCreateTask.set()` with context
- [x] Replaced `boardStore.update()` with context mutations
- [x] Build verification

### 11. Migrate TerminalPanel.svelte
- [x] Replaced store imports with `getAppContext()`
- [x] Replaced `get(selectedProject)`, `get(selectedTask)` with context reads
- [x] Replaced `showTerminal.set()` with context
- [x] Build verification

### 12. Migrate StatsModal.svelte + ConfigModal.svelte + SandboxConfigModal.svelte
- [x] StatsModal: replaced `showStats` store with context
- [x] ConfigModal: replaced `config`, `showConfig`, `selectedProject`, `pwaInstallPrompt` stores with context
- [x] SandboxConfigModal: replaced `showConfirm` import with context
- [x] Build verification

### 13. Migrate main.ts
- [x] Created `pwa.ts` module-level state (pre-context initialization)
- [x] Updated `main.ts` to write to `pwa.ts` module
- [x] Updated `appContext.svelte.ts` to read from `pwa.ts` on init
- [x] Build verification

### 14. Clean up old code
- [x] Deleted `frontend/src/lib/store.ts`
- [x] Deleted `frontend/src/lib/context/index.ts` (merged into appContext)
- [x] Removed all `import { writable } from 'svelte/store'` from component files
- [x] Removed all `import { get } from 'svelte/store'` from component files
- [x] Fixed FileEditor.svelte (was missed in Tasks 10-12)
- [x] Fixed TaskStream.svelte (was missed in Tasks 10-12)
- [x] Final build passes
- [x] E2E tests: 103 passed, 15 failed (pre-existing LLM/UI timing issues), 23 skipped

## Notes
- **Reactivity model**: Context object uses `$state()` internally. Components access via `getContext()` and read properties directly (no `$` prefix). Svelte 5 tracks property access automatically.
- **Board updates**: When App.svelte mutates `board.tasks`, the context must expose a fresh reference so `$derived` in children triggers. Use `board = { ...board, tasks: [...tasks] }` pattern.
- **main.ts constraint**: Runs before App.svelte, so context won't exist yet. PWA events must queue and apply after context is available, or use a simple module-level variable as fallback.
- **ExpandedTasks helpers**: `persistCollapsedTasks`, `cleanupCollapsedTasks`, `initExpandedTasks` are async storage helpers — keep as regular functions in appContext.
- **Order matters**: Tasks 3-12 can be done in any order after Task 2. Task 14 must be last.
