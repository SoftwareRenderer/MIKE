# TODO

## Goal
Clean up dead code, remove redundancy, and extract sub-components from monolithic frontend files to improve maintainability.

## Tasks

### 1. Delete unused files (zero risk)
- [x] Delete `frontend/src/lib/extensions/diffGutter.ts` — CodeMirror extension, never imported
- [x] Delete `frontend/src/lib/context/streaming.ts` — exact duplicate of `index.ts` exports, never imported
- [x] Delete `frontend/src/lib/api.ts` — parent-level barrel that duplicates `api/index.ts`
- [x] Fix `api/index.ts`: remove duplicate `isFromCache` export, add missing `getProject`, `stageFile`, `validateForwardPorts`

### 2. Remove dead functions (low risk)
- [x] Remove `handleDragStart`/`draggedTaskId` from CardDetail.svelte — never wired up
- [x] Remove `handleProviderHeaderKeydown` from ConfigModal.svelte — never called
- [x] Remove `getDockerfileName` from SandboxConfigModal.svelte — never called
- [x] Remove `getUntrackedFileContent` from api/git.ts + barrel re-export — never used

### 3. Clean up storage.ts exports (low risk)
- [x] Make `openDB` private (remove export) — internal use only
- [x] Delete `clearUnsavedFiles` — never called
- [x] Delete `clearCollapsedTask` — never called
- [x] Delete `clearAppSetting` — never called
- [x] Delete `getCacheAge` — never called
- [x] Delete `clearBoardCache` — never called
- [x] Delete `clearProjectCache` — never called

### 4. Extract DiffViewer CSS (medium risk)
- [x] Extract ~600 lines of `<style>` into `styles/diff-viewer.css` (592 lines)
- [x] Import via `import '$lib/styles/diff-viewer.css'` in script block
- [x] DiffViewer.svelte reduced from 1552 to 962 lines
- [x] All diff e2e tests pass

### 5. Extract ProviderManager from ConfigModal (high risk, deferred)
- Provider CRUD (~250 lines TS) + cards/markup (~310 lines) + CSS (~200 lines) = ~760 lines
- Deeply coupled with handleSave() form flow — requires restructuring save logic
- Drag-and-drop reordering, model fetching, connection testing need dedicated visual regression tests
- **Recommendation**: Execute as a standalone refactor in a separate session

### 6. Migrate stores from Svelte 4 to Svelte 5 context (high risk)
- [ ] Create `frontend/src/lib/app-state.ts` with `$state()` + Svelte 5 context for all current stores
- [ ] Convert: `projects`, `selectedProject`, `board`, `config`, `selectedTask`, `sidebarCollapsed`, `showCreateTask`, `creatingChildOf`, `showConfig`, `showTerminal`, `showStats`, `showCardDetail`, `showSandboxConfig`, `showEditTask`, `editTaskData`, `toast`, `confirmation`, `commitDialog`, `pwaInstallPrompt`, `pwaInstalled`
- [ ] Convert helper functions (`showToast`, `showConfirm`, `showCommitDialog`, `persistCollapsedTasks`, `cleanupCollapsedTasks`, `initExpandedTasks`) to use the new context
- [ ] Update `App.svelte` to use `createAppState()` context provider and remove Svelte 4 subscribe bridge (~20 lines)
- [ ] Update all 15 importing components to use `getAppState()` context instead of store subscriptions
- [ ] Delete `frontend/src/lib/store.ts`
- [ ] Verify all functionality (run full e2e test suite)

## Status
- **Tasks 1-4: Complete** — 3 dead files deleted, 4 dead functions removed, 7 unused storage exports cleaned, 600 lines CSS extracted
- **Task 5: Deferred** — ProviderManager extraction (~760 lines), requires dedicated session
- **Task 6: Deferred** — Store migration (Svelte 4→5), touches every component, requires dedicated session

## Net Impact (Tasks 1-4)
- Files deleted: 3 (`extensions/diffGutter.ts`, `context/streaming.ts`, `api.ts`)
- Lines removed: ~120 (dead functions + unused exports + barrel fix)
- Lines extracted: ~590 (DiffViewer CSS → separate file)
- DiffViewer.svelte: 1552 → 962 lines (-38%)
- api/index.ts: fixed duplicate export + added 3 missing exports (`getProject`, `stageFile`, `validateForwardPorts`)

## Notes
- After each task, run `npm run build` + relevant e2e tests to verify no regressions.
- Pre-existing test failure: `settings-power.spec.ts` (unrelated to these changes).
