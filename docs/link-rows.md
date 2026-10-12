# Link-like Rows — Design Detail

Read this before modifying `App/Sidebar.svelte`, `Card.svelte`, `App/TopBar.svelte`,
`CardDetail/TaskHeader.svelte`, `ExplorerHeader.svelte`, `$lib/utils/app-link.ts`,
`$lib/services/url-router.svelte.ts`, or the click routing in `use-board-arrow-nav.ts` / `keyboard.ts`.
The hard rules are in `AGENTS.md → Frontend`.

Sidebar project rows, task cards, both Explorer buttons and all four Terminal buttons are real links, so
the browser's own link actions work on them: right-click → *Open Link in New Tab/Window*, middle click,
*Copy Link Address*, status-bar URL. They use the same deep links the app writes (`?project=`,
`?project=&task=`, `&view=explorer`), in the order `write` in `$lib/services/url-router.svelte.ts` owns.
That module's `read()`, plus `loadAppData` and `restoreTaskFromUrl`, restore them; no backend route is
involved, because `/` is the only SPA document. The Terminal links are the one case where a tab shows
something other than what a click shows — see *Terminal buttons*.

**One module owns the address bar.** `write()` is the shell's only `history.pushState`/`replaceState`
call and `read()` its only reader of `location.search`; the screen a `view` names is its `applyView()`.
Two components push entries of their own: `Board.svelte` pushes a card's `task` before selecting it, and
`Chat/TaskChat.svelte` owns `thread`. So `write()` takes the params it preserves from the live address
bar, not from the state it last wrote.

## Project row

`App/Sidebar.svelte` renders `<a class="project-item" href="/?project=<id>" role="button"
tabindex="0">` — no nested interactive content, so the button→anchor swap is total. A plain click calls
`preventDefault()` and `onSelectProject`; **any modifier click is deliberately left uncancelled** so the
browser opens a tab or window. A cloning project gets **no href** (still focusable, still inert).
`role="button"` is kept, so Space is handled explicitly — a native anchor only activates on Enter.

## A project the app just made

Adding a project navigates to it, and to its board: `handleSelectProjectPath` and `handleCloneProject` in
`App.svelte` both reach `selectProject(p, push, { board: true })`, which names `project`, clears
`view`/`file`/`task`, and calls `applyView()` — the screen on screen belongs to the project the person
left. Switching between two projects the person has seen keeps that screen (the `view` `currentView()`
answers), so `board` is the caller's decision, not a rule inside `selectProject`.

**A clone's row is selected before its project exists.** The placeholder id (`clone-pending-…`) is this
browser's own, so for a project whose `cloning` is set `selectProject` asks the server nothing: no board,
no cached mirror, no project subscription, no `last_project` setting. When the clone answers, the real
project is selected with `push = false` — an address the server never served is a dead entry under the
back button. A clone that fails while its row holds the screen takes the screen with it: the row goes and
a surviving project replaces it, or the empty state does.

The add writes push, so Back returns to the project open at the click, on the screen it was showing.
`frontend/tests/e2e/add-project-navigation.spec.ts` holds those three shapes, and is what fails when an
added project inherits the explorer of the project before it.

## Task card: full-cover overlay, not a root anchor

The card root stays `<div class="card" role="button" tabindex="0" draggable>` and gets
`<a class="card-open-link" aria-hidden="true" tabindex="-1" draggable="false">`
(`position:absolute; inset:0; z-index:1`) as its last child.

A root anchor is not an option: a card's subtask outline rows are `Card`s themselves and every one holds
its own buttons, which would be anchor-in-anchor markup and would need `preventDefault()` in every
early-return branch to stop full-document navigations. `draggable="false"` keeps the **card** (not the URL)
as the HTML5 drag source, and `aria-hidden` keeps the card root as the only accessible name.

**Click routing:** the overlay's own handler always `stopPropagation()`, so the card root does not handle
one click twice, and it cancels navigation *except* when `isBrowserLinkClick()` says the browser owns it.
The card's multi-select toggle fires only for the bound modifier (`multi_select_click`, default platform
Ctrl/Cmd+Click); unbound modifier clicks reach the browser instead.

**Known limitation:** on a **queued** (offline-pending) card the lifted content covers most of the
overlay, so the link menu is only available over the card's padding.

## Explorer buttons

`App/TopBar.svelte` (`.explorer-btn`) and `CardDetail/TaskHeader.svelte` (the icon-only button, absent in
the floating view) are `<a class="icon-btn" role="button" tabindex="0" draggable="false">` carrying the
plain view link the button itself writes: `/?project=<id>&view=explorer` and
`/?project=<id>&task=<id>&view=task-explorer`. Deliberately the plain view only — no `review` /
`task-review` and no `file=` — so a tab opened from the button lands on the explorer, not on whatever file
happens to be on screen.

Neither button claims a modifier binding, so **every** modifier click is left uncancelled and no
`isBrowserLinkClick()` call is needed; a middle click is an `auxclick`, which no handler sees at all. The
two guards these buttons do need — leave a modifier click to the browser, and activate on Space because
`role="button"` gets no free one — are `handleLinkClick` / `handleLinkKeydown` in `keyboard.ts`, shared
with the Terminal links. `role="button"` keeps them announced as buttons
(`getByRole('button', { name: 'Open explorer' })` is used by two specs), and `draggable="false"` stops the
text inside the top-bar button from becoming a link drag payload.

## What a close lands on

The × of an explorer writes the address of the screen behind it, then opens that screen. The close has to
make the app agree with the address it wrote: an explorer that arrived by address has the task detail
closed (`restoreTaskFromUrl`), and an address naming a task with no screen of its own *is* that detail.
`closeTaskExplorer` in `App.svelte` therefore writes `?project=&task=` with no `view` and sets
`uiStore.showCardDetail` from what `applyView()` answers.

- A **task explorer**'s × lands on that task's detail, including in a tab that opened on the explorer link.
- The **Review Changes** panel's × is the panel's own: it returns to the explorer it belongs to, and that
  explorer's × then follows the rule above.
- The **project explorer**'s × writes `?project=` with no `view`, which is the board, and restores the
  sidebar it auto-collapsed on a tablet.
- Both closes push, so the browser's Back returns to the explorer. Escape reaches the same close through
  `ExplorerCore.handleKeydown`.

## Terminal buttons

`App/TopBar.svelte`, `CardDetail/TaskHeader.svelte` (absent in the floating view, like the Explorer
button) and `ExplorerHeader.svelte` — the header both explorer screens share — carry the link
`$lib/utils/app-link.ts` builds: `/?project=<id>&view=terminal`, with `&task=<id>` where the button
belongs to a task. That file owns the param names, the href shape and `asksForTerminal`, because
`url-router.svelte.ts`'s `write` order and its `applyView` reader have to keep agreeing with it. The
anchor shape and click routing are the Explorer buttons' too.

**The link is a screen, the click is a panel.** A plain click still opens the panel over the current
screen (`uiStore.showTerminal`, which the URL never carries, so nothing goes stale when the panel closes);
opening the link renders `view=terminal` — the terminal alone, with no sidebar, top bar, board or task
detail. `urlRouter.terminalOnly` is the router's; `restoreTaskFromUrl` and `handlePopState` restore it
through `applyView()`. `selectProject` must name `terminal` in the `view` it writes back, which is what
`currentView()` answers: that call *is* the launch rewrite, and a `view` it does not name is stripped from
the URL before the board arrives.

That screen renders `TerminalPanel` with `standalone`: permanent fullscreen, minus the chrome that only
makes sense for a panel over the board — the dimming overlay, the height handle, the close button, the
fullscreen toggle, the focus trap, the saved height, and the shortcut that closes the panel. Nothing on the
screen can open a second terminal or a task, so `boardShortcutsEnabled()` and `terminalShortcutEnabled()`
are both false there, and the document title is `app.title.terminal`.

The screen renders nothing until its target exists, because `TerminalPanel` chooses task vs project from
`appStore.selectedTask` at mount: it waits for the project (and for the URL's task, when it names one),
then creates the worktree with `initTask` — what `openTerminal()` does before it opens the panel — and
shows the loading screen meanwhile, or the failure sentence when the worktree cannot be created.

## Explorer file rows

A row in either explorer's file list can open its own window two ways: **Open in new window** in the file
menu, and a middle `mousedown` on the row (`button === 1`, cancelled so the browser's own autoscroll stays
out). `openInNewWindow` in `FileBrowserView.svelte` is the one function both gestures reach, and
`mousedown` is the event that carries the user activation a popup needs.

**The rows are `<button>`s, and that is deliberate.** The file list owns right-click: it calls
`preventDefault()` and draws its own menu, so the browser's link menu is unreachable on a row whatever the
markup. An anchor's remaining offer is a middle click that opens a *tab*, and a window is what this row
promises, so the handler cancels that default too. Keeping the row a button also keeps its
`role="treeitem"` semantics and its long-press multi-select untouched.

**The link is one the app already writes.** `explorerPathHref` in `$lib/utils/app-link.ts` builds
`?project=&[task=&]view=explorer|task-explorer&file=<path>`; the `isDir` argument marks a folder by ending
the value in `/`, and `parseExplorerFile` is the reader that tells the two apart. A folder link seeds the
listing and opens no tab. A file link opens the tab, and the new window merges it into the tabs IndexedDB
restores: both windows hold the same store, so a file link always arrives against a stored list.

**Both explorer screens take `initialFile`,** and each clamps it twice: `explorerPathRoots` with
`fileWithinRoots` decide which roots a `file=` may sit under (the checkout, the URL task's worktree, and
each container mount), and `ExplorerCore.isPathSafe` clamps again where the path is opened. The roots
question has one home: `explorerRootsFor` in `App.svelte`. `handlePopState` calls it, and
`urlRouter.declareFileRoots` holds it for every write, so an address never names a file its own project or
task cannot show. A switch of subject therefore drops the file the old subject held, and the explorer that
lands writes the one it restored. The `..` row is refused by both entrances through `rowIsSelectable`.

## Tests

- `components/__tests__/link-rows.unit.svelte.ts` — hrefs, click/modifier routing, Space, cloning, and
  overlay attributes.
- `components/__tests__/explorer-buttons-are-links.unit.svelte.ts` — both Explorer buttons.
- `components/__tests__/terminal-buttons-are-links.unit.svelte.ts` — the three terminal links and the link
  each explorer hands its header.
- `components/__tests__/file-browser-context-menu.unit.svelte.ts` — the file rows' window item, the URL it
  carries, and the middle-button gesture.
- `services/__tests__/url-router.unit.svelte.ts` — the address bar ↔ router state mapping: the param
  order, what an omitted param means, which navigation is a Back entry, and the `file=` a destination
  cannot show.
- `utils/__tests__/app-link.unit.ts` — the hrefs, the reader, and the file param's roots.
- `utils/__tests__/keyboard.unit.ts` — click bindings.
- `link-like-rows.spec.ts` (e2e) — middle click opens a real tab, left click navigates no document, the
  drag source is the card not the link, a second tab lands on the linked project/task/explorer, and a
  terminal tab holds a terminal and nothing else.
- `preferences-keyboard.spec.ts` (e2e, isolated) — rebinding frees Ctrl+Click.
- `add-project-navigation.spec.ts` (e2e) — an added project's address names it with no carried-over
  screen, and a cloning row is the one on screen.
- `task-explorer-close.spec.ts` (e2e) — a task explorer's × lands on that task's detail whichever way the
  explorer arrived, the Review Changes × keeps its explorer, and Back returns to the explorer the close
  left.

## Markup and stacking contract

- **Selectors**: project rows use `.project-item`; task cards use `.card` and `.card-open-link`; explorer
  and terminal buttons use `.icon-btn`. Keep them tag-agnostic (`.project-item`, not
  `button.project-item`).
- **Stacking** (`Card.svelte`): `.card-title` is deliberately not lifted, so the overlay still covers the
  title; instead `.card-actions`, `.subtask-progress-bar` and `.subtask-list` carry the content rank
  (`var(--z-content)`), and `.card.queued > *` is scoped `> :not(.card-open-link)`. A root anchor is
  impossible for the reason in *Task card* above, and `docs/subtask-outline.md → How it is held` owns the
  row markup that forces it.
- **Never** replace these rows with `<button>`/`<div>` plus a faked context menu — the link menu is
  DOM-level and cannot be synthesized.

## Checking this

`cd frontend && npm test` covers the row and card markup in `link-rows.unit.svelte.ts`, and
`make test-e2e SPECS=task-explorer-close` covers the screen each close lands on.
