# Keyboard Navigation — Board and Task View

Read this before touching arrow-key handling in `App.svelte`, `App/Sidebar.svelte`,
`CardDetail.svelte`, or the two resolver modules. The hard rules live in
`AGENTS.md → Frontend`.

## Board overview

Arrow keys move **real DOM focus** (the global `:focus-visible` ring) — no separate "highlighted"
store. A 5-column ring `[Projects][Backlog][In Progress][Review][Done]`: the project list is the
leftmost column (always lands on the active project), board Left/Right skip empty columns and never
wrap, Down/Up wrap within a column.

Resolvers: `resolveArrowTarget` / `resolveOverviewArrowTarget` in
`composables/use-board-arrow-nav.ts`, wired in `App.svelte`; project-list keydown in
`App/Sidebar.svelte`. The project focus ring is an **inset box-shadow** on `.project-item:focus` —
ancestors clip outlines, and `click()+focus()` defeats `:focus-visible`.

`Card.svelte`'s `handleCardKeydown` takes Enter/Space **only when the event target is the card
root**: a keydown aimed at a nested control (action icon, expand bar, subtask card) is that
control's own activation, and `preventDefault()` on it cancels the button. Only a browser turns a
keypress into a button click, so the activation itself is covered by e2e, its non-prevention by
vitest.

Tests: `composables/__tests__/board-arrow-nav.unit.ts`, `components/__tests__/card-actions-keyboard.unit.svelte.ts`,
e2e `board-arrow-nav.spec.ts`.

## Task View (`CardDetail.svelte`)

The three columns `[Board][Chat][Diff]` are focusable; arrow keys move real DOM focus. All handlers
live in `CardDetail.svelte`'s window keydown listener, using pure resolvers from
`composables/use-task-detail-column-nav.ts` (`resolveDetailColumn`, `resolveDiffFileTarget`,
`canSwitchDetailColumn`).

- Left/Right cycle Board→Chat→Diff→Board.
- Plain arrows in any editable focus (incl. the chat input) keep native text behavior;
  **Ctrl+Alt+Left/Right** switches columns from any focus (except while editing a file).
- Up/Down and PageUp/PageDown dispatch on the focused column (overview cards / page-scroll chat /
  diff file headers). The chat column answers all four from any focus inside it — its transcript is not a
  tab stop, so the browser's own scroll keys would move some other scroll chain — with the distance from
  `chatScrollDelta`. The other two columns are rings of focusable rows, so a page is not a step on them and
  they answer arrows only.

Tests: `composables/__tests__/task-detail-column-nav.unit.ts`, e2e `task-view-column-nav.spec.ts`, and the
chat half of both by `chat-scroll-takeover.spec.ts` (which also needs the follow tracker to leave the
reader where a key landed them — `AGENTS.md → Frontend`).
## File list (`FileBrowserView.svelte`)

The listing owns its clipboard keys and the panel that wraps it carries them. Ctrl+X, Ctrl+C, Ctrl+V, F2
and Delete reach the file selection wherever focus sits inside that panel, and nowhere outside it — the
editor's Ctrl+C stays the editor's. `keyIsOurs` hands the keys back to the browser while focus sits in a
field being typed (the path field, a name being retyped, the new-file row) and while the context menu is
open.

**A folder change must not strand the keyboard.** A new listing can remove the row that holds focus, and a
keystroke arriving on `<body>` afterwards reaches no handler at all. `loadDir` records whether this panel
holds focus, and when the panel ends up holding none once the new rows are rendered it focuses the listing,
which is a focus target through `tabindex="-1"`. A panel that holds no focus keeps it that way, so a
listing refreshed by the watcher never pulls the keyboard out of the editor.

A key aimed at one row with an empty selection is about that row: `focusedRowPath` reads the
`[data-index]` of the event target, the same read the menu key makes. Ctrl+V pastes into the folder being
listed, while the menu's Paste takes the folder the menu names (`pasteTarget`). No selection and no row
under focus is a silent no-op.

Tests: `components/__tests__/file-browser-clipboard.unit.svelte.ts` delivers every key to the focused
element as a browser does, including a cut in one folder and a paste in another.

## Checking this

`cd frontend && npm test` covers the resolvers (`board-arrow-nav.unit.ts` is one of them).
`cd frontend && npx playwright test ui-navigation focus-management` proves real focus moves, which a unit
test cannot.
