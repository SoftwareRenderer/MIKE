# TODO

## Goal
Break down the 3 largest components (TaskChat 1383 lines, Card 1148 lines, App 1484 lines) into smaller, focused sub-components and extract complex logic into composables — no component exceeds 500 lines, all functionality preserved.

## Tasks

### Phase 1 — TaskChat Composables (1383 → ~450 lines)

- [x] 1.1 Create `useChatConnection.svelte.ts` — Extract WebSocket connect/disconnect/reconnect, `handleEvent()` echo logic, `connect()`, `handleRetryConnect()`, `handleDismissError()`, `destroyed` flag, `streamingIds` mark/unmark, and the `onMount`/`onDestroy` lifecycle (visibility change, beforeunload, queue subscription). Inputs: `projectId`, `taskId`, `stream`, `taskEventContext`, `getAppStreamRef`. Outputs: `connected`, `connecting`, `handleEvent` function.
- [x] 1.2 Create `useChatHistory.svelte.ts` — Extract `loadHistory()` (with DB merge/dedup logic), `loadThreads()`, `updateActiveThread()`, `resetStreamState()`, `mergeTaskIntoBoard()`, and `entryMetas` computation. Inputs: `projectId`, `taskId`, `stream`, `taskStatus`. Outputs: `threads`, `activeThreadId`, `loadHistory(scrollAfter)`, `loadThreads()`, `updateActiveThread(id)`, `entryMetas` (derived).
- [x] 1.3 Create `useChatActions.svelte.ts` — Extract `handleSend()`, `handleRegenerate()`, `handleStartTask()`, `handleStop()`, `handleCommitAndDone()`, `handleMoveToDone()`, `handleDeleteRequest()`, `handleEditFork()`, `handleEditSend()`, `handleSummarize()`, `handleSummarizeCancel()`, `handleModelSelect()`, `handleOperatingModeToggle()`, `sendWithTracking()`, `genId()`. Inputs: `projectId`, `taskId`, `stream`, `taskStatus`, `threads`, `activeThreadId`, callback refs. Outputs: all action handlers as functions.
- [x] 1.4 Refactor `TaskChat.svelte` — Replace inline logic with 3 composable calls. Keep: props, draft/attachment IndexedDB persistence, scroll management, input value state, mobile keyboard viewport listener, template markup. Verify no behavioral changes.

### Phase 2 — Card Composables + Sub-Components (1148 → ~350 lines)

- [x] 2.1 Create `useCardModelPicker.svelte.ts` — Extract model picker dropdown: `modelPickerOpen`, `modelBadgeRef`, `pickerRect`, `togglePicker()`, `closePicker()`, `selectModel()`, `selectAgent()`, click-outside/escape-key cleanup `$effect`, and overflow flip `$effect`. Inputs: `task`, `projectId`, `providers`, `boardStore`. Outputs: picker state + handlers.
- [x] 2.2 Create `useCardActions.svelte.ts` — Extract `handlePause()`, `handleResume()`, `cycleStatus()`, `handleDelete()`, `handleCreateChild()`, `handleStart()`, `toggleExpanded()`, `startEdit()`, `handleEditSaved()`, `handleEditCancelled()`, `handleCardKeydown()` (Alt+Arrow navigation), `handleCardClick()`, `handleMouseDown()`, `handleDragStart()`, `truncate()`. Inputs: `task`, `columnId`, `projectId`, `childrenMap`, `hasChildren`, `hasChanges`, `hasRunningDescendant`, callback refs. Outputs: all action handlers.
- [x] 2.3 Create `Card/CardHeader.svelte` — Extract title row: title text with generating spinner, action icons (add subtask, edit, pause/resume/idle/delete/move), drag-and-drop attributes, keyboard handler, click handler. Props: `task`, `columnId`, `actionCount`, `isDoneSubtask`, `isPausable`, `isResumable`, `isIdle`, `hasChildren`, `inline`, `hasChanges`, `nextStatus`, `statusBadge`, `statusLabels`, `statusColors`, `hasStagedChanges`, event callbacks.
- [x] 2.4 Create `Card/CardBadges.svelte` — Extract badges row: status badge, model badge (with Pi agent variant), diff stats row. Props: `statusBadge`, `modelBadge`, `diffStats`. Move related CSS (~150 lines).
- [x] 2.5 Create `Card/SubtaskProgress.svelte` — Extract progress bar + expandable subtask list. Props: `subtaskProgress`, `expanded`, `hasChildren`, `children`, `columnId`, `projectId`, `childrenMap`, `expandedTasks`, `onToggleExpanded`, `onCreateChildTask`, `columnColor`, `parentSelected`, `showSubtaskBorder`, `onSelect`, `onRefreshBoard`. Move progress bar CSS (~80 lines).
- [x] 2.6 Refactor `Card.svelte` — Replace inline logic with 2 composables + 3 sub-components. Keep: props, queued actions tracking, `children`/`hasChildren`/`expanded`/`statusColor`/`isDoneSubtask`/`hasRunningDescendant`/`actionCount`/`isSelected`/`showSelected` derived state, root `<div class="card">` wrapper with drag/selection classes, model picker portal, remaining card CSS. Verify no behavioral changes.

### Phase 3 — App Composables (1484 → ~450 lines)

- [x] 3.1 Create `useAppStream.svelte.ts` — Extract WebSocket lifecycle: `createAppStream()` callback construction (`onProjectCounts`, `onRefresh`, `onDiffStats`, `onDiff`, `onBranch`, `onFsEvent`, `onTaskUpdated`, `onTaskDeleted`, `onCommitStatus`, `onTaskEvent`, `onConnected`, `onReconnectScheduled`, `onConnectAttempt`, `onPortConflict`, `onProjectPorts`), `loadFromCache()` fallback stream, `handleVisibilityChange()`, `tryReconnectNow()`, countdown/stale-time tickers, task subscription `$effect`. Inputs: `currentProject`, `selectedTaskVal`, stores. Outputs: `appStream`, `wsConnected`, `countdownSeconds`, `staleTimeText`, `nextReconnectAt`, `tryReconnectNow()`, `loadFromCache()`.
- [x] 3.2 Create `useAppRouting.svelte.ts` — Extract URL management: `updateUrl()`, `handlePopState()`, `restoreTaskFromUrl()`, `checkUrlChange()` rAF loop, `clearTaskDetail()`, `selectProject()` (URL + board loading + subscription), `disconnectBoardStream()`, `refreshBoard()`, `silentRefresh()`, `handleExplorerFileChange()`, `restoreExplorerSidebar()`. Inputs: `currentProject`, `projects`, `tasks`, `boardStore`, stores. Outputs: `urlFileId`, `showExplorerViewVal`, `selectedTaskVal`, all routing functions.
- [x] 3.3 Refactor `App.svelte` — Replace inline logic with 2 composable calls. Keep: props (none), auth state (`isLoadingAuth`, `isAuthenticated`, `hasLocalCache`), `onMount` auth check + initial data load, `onDestroy` cleanup, `getProjectAbbr()`, `computePageTitle()`, project management (`openAddProject`, `handleLogout`, `handleSelectProjectPath`, `removeProject`, `openSandboxConfig`, `handleSandboxSaved`), sidebar state (`collapsed`, `toggleSidebar`), terminal lazy-loading, template markup (Sidebar, TopBar, Board, CardDetail, ExplorerView, modals). Verify no behavioral changes.

### Phase 4 — Verification

- [x] 4.1 Confirm line counts: `TaskChat.svelte` ≤ 500, `Card.svelte` ≤ 500, `App.svelte` ≤ 500, `CardHeader.svelte` ≤ 200, `CardBadges.svelte` ≤ 150, `SubtaskProgress.svelte` ≤ 150
- [x] 4.2 Run frontend unit tests: `cd frontend && npm test`
- [x] 4.- [ ] 4.3 Run e2e tests: `cd frontend && npx playwright test`
- [x] 4.- [ ] 4.4 Run Go tests: `go test ./backend/... -count=1 -parallel 8`
- [x] 4.- [ ] 4.5 Manual smoke test: build frontend (`cd frontend && npm run build`), start server, verify task creation, chat, thread switching, card drag-and-drop, model picker, project switching all work

## Notes

- **Composable naming**: `use*.svelte.ts` in `$lib/composables/` — follows existing project convention
- **No breaking changes**: All props/emit contracts on existing components remain identical
- **CSS strategy**: Card sub-components get their own `<style>` blocks; TaskChat/App CSS stays in orchestrators (layout-level styles belong there)
- **Dependency order**: Composables before orchestrator refactoring (each composable is self-contained)
- **Test strategy**: No new tests required — existing e2e/unit tests validate preserved behavior. If a test breaks, the refactoring introduced a regression.
