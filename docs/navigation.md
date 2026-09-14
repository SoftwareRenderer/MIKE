# Keyboard Navigation — Board and Task View

Read this before touching arrow-key handling in `App.svelte`, `App/Sidebar.svelte`,
`CardDetail.svelte`, or the two resolver modules. The hard rules live in
`AGENTS.md → Keyboard Navigation`.

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
- Up/Down dispatch on the focused column (overview cards / page-scroll chat / diff file headers).

Tests: `composables/__tests__/task-detail-column-nav.unit.ts`, e2e `task-view-column-nav.spec.ts`.
