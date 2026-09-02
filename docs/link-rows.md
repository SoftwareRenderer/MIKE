# Link-like Rows — Design Detail

Read this before touching `App/Sidebar.svelte`, `Card.svelte`, or the click routing in
`use-board-arrow-nav.ts` / `keyboard.ts`. The hard rules live in `AGENTS.md → Link-like Rows`.

Sidebar project rows and task cards are reachable by real URLs, so the browser's own link actions
work on them: right-click → *Open Link in New Tab/Window*, middle click, *Copy Link Address*,
status-bar URL. Both use the same deep links the app itself writes (`?project=`, `?project=&task=`,
canonical order from `updateUrl` in `App.svelte`), which `loadAppData` + `restoreTaskFromUrl` already
restore — no backend route is involved (`/` is the only SPA document).

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

## Tests

`components/__tests__/link-rows.unit.svelte.ts` (hrefs, click/modifier routing, Space, cloning,
overlay attributes), `utils/__tests__/keyboard.unit.ts` (click bindings), e2e
`link-like-rows.spec.ts` (middle-click opens a real tab, no document navigation on left click, drag
source is the card not the link, second tab lands on the linked project/task), e2e
`preferences-keyboard.spec.ts` (isolated; rebinding frees Ctrl+Click).
