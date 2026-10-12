# Keyboard Navigation — Board and Task View

Read this before touching arrow-key handling in `App.svelte`, `App/Sidebar.svelte`, `CardDetail.svelte`, or the resolver modules. Rules live in `AGENTS.md → Frontend`.

## Board Overview

Arrow keys move **real DOM focus** (the `:focus-visible` ring), not a separate "highlighted" store.

- **Columns**: `[Projects][Backlog][In Progress][Review][Done]`.
- **Left/Right**: Skip empty columns; do not wrap.
- **Up/Down**: Wrap within the current column.
- **Project List**: The leftmost column. Focus lands on the active project. Focus is shown as an **inset box-shadow** on `.project-item:focus` because ancestors clip outlines.

**Card activation**: `Card.svelte` handles Enter/Space only if the event target is the card root. Keydowns on nested controls (icons, subtask cards) are handled by those controls; `preventDefault()` there cancels the card's activation.

Resolvers: `resolveArrowTarget` / `resolveOverviewArrowTarget` in `composables/use-board-arrow-nav.ts`.

Tests: `composables/__tests__/board-arrow-nav.unit.ts`, `components/__tests__/card-actions-keyboard.unit.svelte.ts`, e2e `board-arrow-nav.spec.ts`.

## Task View (`CardDetail.svelte`)

The `[Board][Chat][Diff]` columns are focusable. Handlers live in `CardDetail.svelte`'s window keydown listener, using resolvers from `composables/use-task-detail-column-nav.ts`.

- **Left/Right**: Cycle Board $\to$ Chat $\to$ Diff $\to$ Board.
- **Ctrl+Alt+Left/Right**: Switch columns from any focus (except when editing a file).
- **Up/Down & PageUp/PageDown**:
    - **Board/Diff**: Focusable rows; PageUp/Down is not supported.
    - **Chat**: Answers all four from any focus inside the column; uses `chatScrollDelta` to scroll the transcript (which is not a tab stop).
- **Editable fields**: Plain arrows in inputs keep native text behavior.

Tests: `composables/__tests__/task-detail-column-nav.unit.ts`, e2e `task-view-column-nav.spec.ts`, and `chat-scroll-takeover.spec.ts`.

## File List (`FileBrowserView.svelte`)

The file list panel handles clipboard and rename keys (Ctrl+X, Ctrl+C, Ctrl+V, F2, Delete). These only work when focus is inside the panel; the editor's own Ctrl+C remains separate.

`keyIsOurs` returns keys to the browser when:
- Focus is in an input field (path field, renaming a file, new-file row).
- The context menu is open.

**Focus persistence**: When a folder changes, the row holding focus may vanish. If the panel held focus before the change, `loadDir` ensures the listing itself (`tabindex="-1"`) is focused after rendering new rows. If the panel did not hold focus, it stays unfocused to avoid stealing focus from the editor.

**Selection**:
- **No selection**: A key aimed at a row targets that row (`focusedRowPath` reads `[data-index]`). Ctrl+V pastes into the current folder.
- **With selection**: Actions apply to the selection.
- **No focus/selection**: Silent no-op.

Tests: `components/__tests__/file-browser-clipboard.unit.svelte.ts`.

## Verification

- `cd frontend && npm test`: Covers resolvers.
- `cd frontend && npx playwright test ui-navigation focus-management`: Verifies real DOM focus moves.
