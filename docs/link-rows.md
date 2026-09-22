# Link-like Rows — Design Detail

Read this before touching `App/Sidebar.svelte`, `Card.svelte`, `App/TopBar.svelte`,
`CardDetail/TaskHeader.svelte`, `ExplorerHeader.svelte`, `$lib/utils/app-link.ts`, or the click routing
in `use-board-arrow-nav.ts` / `keyboard.ts`. The
hard rules live in `AGENTS.md → Frontend`.

Sidebar project rows, task cards, both Explorer buttons and all four Terminal buttons are reachable by
real URLs, so the browser's own link actions work on them: right-click → *Open Link in New Tab/Window*,
middle click, *Copy Link Address*, status-bar URL. They use the same deep links the app itself writes
(`?project=`, `?project=&task=`, `&view=explorer`, canonical order from `updateUrl` in `App.svelte`), which
`loadAppData` + `restoreTaskFromUrl` + `applyUrlView` already restore — no backend route is involved
(`/` is the only SPA document). The Terminal links are the one exception to "the tab shows what a click
shows"; see *Terminal buttons*.

## Project row

`App/Sidebar.svelte` renders `<a class="project-item" href="/?project=<id>" role="button"
tabindex="0">` — no nested interactive content, so the button→anchor swap is total. Unmodified click
→ `preventDefault()` + `onSelectProject`; **any modifier click is deliberately left uncancelled** so
the browser opens a tab/window. A cloning project gets **no href** (still focusable, still inert).
`role="button"` is kept, so Space is handled explicitly (a native anchor only activates on Enter).

## Task card: full-cover overlay, not a root anchor

The card root stays `<div class="card" role="button" tabindex="0" draggable>` and gets
`<a class="card-open-link" aria-hidden="true" tabindex="-1" draggable="false">`
(`position:absolute; inset:0; z-index:1`) as its last child.

A root anchor is not an option: cards nest subtask `Card`s and their own buttons, which would be
anchor-in-anchor markup and would need `preventDefault()` in every early-return branch to stop
full-document navigations. `draggable="false"` keeps the **card** (not the URL) as the HTML5 drag
source; `aria-hidden` keeps the card root as the only accessible name.

**Click routing:** the overlay's own handler always `stopPropagation()`s (so the card root doesn't
handle one click twice) and cancels navigation *except* when `isBrowserLinkClick()` says the browser
owns it. The card's multi-select toggle fires only for the bound modifier (`multi_select_click`,
default platform Ctrl/Cmd+Click) — unbound modifier clicks reach the browser instead.

**Known limitation:** on a **queued** (offline-pending) card the lifted content covers most of the
overlay, so the link menu is only available over the card's padding.

## Explorer buttons

`App/TopBar.svelte` (`.explorer-btn`) and `CardDetail/TaskHeader.svelte` (the icon-only button, absent
in the floating view) are `<a class="icon-btn" role="button" tabindex="0" draggable="false">` carrying the
plain view link the button itself writes: `/?project=<id>&view=explorer` and
`/?project=<id>&task=<id>&view=task-explorer`. Deliberately the plain view only — no `review` /
`task-review` and no `file=` — so a tab opened from the button lands on the explorer, not on whatever
file happens to be on screen.

Neither button claims a modifier binding, so **every** modifier click is left uncancelled and no
`isBrowserLinkClick()` call is needed; a middle click is an `auxclick`, which no handler sees at all.
The two guards these buttons do need — leave a modifier click to the browser, and activate on Space
because `role="button"` gets no free one — are `handleLinkClick` / `handleLinkKeydown` in
`keyboard.ts`, shared with the Terminal links.
`role="button"` keeps them announced as buttons (`getByRole('button', { name: 'Open explorer' })` is
used by two specs).
`draggable="false"` keeps the text inside the top-bar button from becoming a link drag payload.

## Terminal buttons

`App/TopBar.svelte`, `CardDetail/TaskHeader.svelte` (absent in the floating view, like the Explorer
button) and `ExplorerHeader.svelte` — the header both explorer screens share — carry the link
`$lib/utils/app-link.ts` builds: `/?project=<id>&view=terminal`, with `&task=<id>` where the button
belongs to a task. That file owns the param names, the href shape and `asksForTerminal`, because
`updateUrl`'s order and `applyUrlView`'s reader have to keep agreeing with it. Same anchor shape and
click routing as the Explorer buttons.

**The link is a screen, the click is a panel.** A plain click still opens the panel over the current
screen (`uiStore.showTerminal`, which the URL never carries, so nothing goes stale when the panel
closes); opening the link renders `view=terminal` — the terminal alone, with no sidebar, top bar, board
or task detail. `applyUrlView` owns `terminalOnlyVal`, so `restoreTaskFromUrl` and `handlePopState`
restore it, and `selectProject` must name `terminal` in the `view` it writes back: that call *is* the
launch rewrite, and a `view` it does not name is stripped from the URL before the board arrives.

That screen renders `TerminalPanel` with `standalone`: permanent fullscreen, minus the chrome that only
makes sense for a panel over the board — the dimming overlay, the height handle, the close button, the
fullscreen toggle, the focus trap, the saved height, and the shortcut that closes the panel. Nothing on
the screen can open a second terminal or a task, so `boardShortcutsEnabled()` and
`terminalShortcutEnabled()` are both false there, and the document title is `app.title.terminal`.

The screen renders nothing until its target exists, because `TerminalPanel` chooses task vs project
from `appStore.selectedTask` at mount: it waits for the project (and for the URL's task, when it names
one), then creates the worktree with `initTask` — what `openTerminal()` does before it opens the panel —
and shows the loading screen meanwhile, or the failure sentence when the worktree cannot be created.

## Explorer file rows

A row in either explorer's file list can open its own window two ways: **Open in new window** in the file
menu, and a middle `mousedown` on the row (`button === 1`, cancelled so the browser's own autoscroll
stays out). `openInNewWindow` in `FileBrowserView.svelte` is the one function both gestures reach, and
`mousedown` is the event that carries the user activation a popup needs.

**The rows are `<button>`s, and that is deliberate.** The file list owns right-click: it calls
`preventDefault()` and draws its own menu, so the browser's link menu is unreachable on a row whatever
the markup. An anchor's remaining offer is a middle click that opens a *tab*, and a window is what this
row promises, so the handler cancels that default too. Keeping the row a button also keeps its
`role="treeitem"` semantics and its long-press multi-select untouched.

**The link is one the app already writes.** `explorerPathHref` in `$lib/utils/app-link.ts` builds
`?project=&[task=&]view=explorer|task-explorer&file=<path>`; `explorerPathHref(…, isDir)` marks a folder
by ending the value in `/`, and `parseExplorerFile` is the reader that tells the two apart. A folder link
seeds the listing and opens no tab. A file link opens the tab, and the new window merges it into the tabs
IndexedDB restores: both windows hold the same store, so a file link always arrives against a stored list.

**Both explorer screens take `initialFile`,** and each clamps it twice: `explorerPathRoots` with
`fileWithinRoots` decide in `handlePopState` which roots a `file=` may sit under (the checkout, the URL
task's worktree, and each container mount), and `ExplorerCore.isPathSafe` clamps again where the path is
opened. The `..` row is refused by both entrances through `rowIsSelectable`.

## Tests

`components/__tests__/link-rows.unit.svelte.ts` (hrefs, click/modifier routing, Space, cloning,
overlay attributes), `components/__tests__/explorer-buttons-are-links.unit.svelte.ts` (both Explorer
buttons), `components/__tests__/terminal-buttons-are-links.unit.svelte.ts` (the three terminal links and
the link each explorer hands its header),
`components/__tests__/file-browser-context-menu.unit.svelte.ts` (the file rows' window item, the URL it
carries, and the middle-button gesture), `utils/__tests__/app-link.unit.ts` (the hrefs, the reader, the
file param's roots),
`utils/__tests__/keyboard.unit.ts` (click bindings), e2e
`link-like-rows.spec.ts` (middle-click opens a real tab, no document navigation on left click, drag
source is the card not the link, second tab lands on the linked project/task/explorer, and the terminal
tab holds a terminal and nothing else), e2e
`preferences-keyboard.spec.ts` (isolated; rebinding frees Ctrl+Click).

## Markup and stacking contract

Sidebar project rows and task cards are real links, so the browser's own link actions work on them (open in new tab, middle click, copy link address, status-bar URL) using the same deep links the app writes (`?project=`, `?project=&task=`).

- **Project row** = `<a class="project-item" href="/?project=<id>" role="button" tabindex="0">`; unmodified click `preventDefault()`s, **any modifier click is left uncancelled**. A cloning project gets **no href**. `role="button"` means Space is handled explicitly.
- **Task card** root stays `<div class="card" role="button" tabindex="0" draggable>` with a full-cover `<a class="card-open-link" aria-hidden tabindex="-1" draggable="false">` overlay (`inset:0; z-index:1`) as last child — a root anchor is impossible (nested subtask `Card`s → anchor-in-anchor).
- **Stacking contract** (`Card.svelte`): `.card-title` is deliberately NOT lifted so the overlay still covers the title; instead `.card-actions`, `.subtask-progress-bar`, `.subtask-list` carry `z-index: 2`, and `.card.queued > *` is scoped `> :not(.card-open-link)`.
- **Never** re-tune these rows with `<button>`/`<div>` + a faked context menu — the link menu is DOM-level and unsynthesizable. Keep selectors tag-agnostic (`.project-item`, not `button.project-item`).
## Checking this

`cd frontend && npm test` covers the row and card markup in `link-rows.unit.svelte.ts`.
