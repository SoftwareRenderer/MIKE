# TODO

## Goal

Fix the intermittent editor bug where content becomes unreadable while scrolling
because an LSP hover tooltip lingers open and covers the code.

## Root Cause (confirmed)

The editor uses CodeMirror 6 with two hover tooltips that come from LSP
(`@codemirror/lsp-client`'s documentation hover tooltip + a custom
"Go to Definition" tooltip in `FileEditor.svelte`).

- `@codemirror/lsp-client`'s `hoverTooltips()` is configured with
  `hideOn: tr => tr.docChanged` — it only hides on **document changes**, not on scroll.
- CodeMirror's `HoverPlugin` only closes hover tooltips on `mousemove` (mouse
  leaves the hovered range) or `mouseleave`. It does **not** close on scroll.
- Therefore, when a user hovers over a symbol (a large documentation card opens,
  up to 240px tall, covering ~10 lines of code) and then scrolls with the mouse
  wheel (cursor stationary → no `mousemove`), the tooltip stays open and follows
  the content, covering code at the scroll position → "content becomes unreadable."
  It is intermittent (only when hovering + scrolling), LSP-related, and can appear
  at any line (the user observed it from line ~100 up).

## Tasks

### 1. Add a "close hover tooltips on scroll" utility
- [x] Create `frontend/src/lib/utils/close-hover-on-scroll.ts` exporting a
      `closeHoverOnScroll()` `ViewPlugin` factory.
- [ ] The plugin watches `ViewUpdate.viewportChanged`; when it fires and
      `hasHoverTooltips(view.state)` is true, dispatch the `closeHoverTooltips`
      effect (both from `@codemirror/view`) so any open LSP hover tooltip closes
      when the editor scrolls.

### 2. Wire the utility into the editor
- [x] Import and add `closeHoverOnScroll()` to the extension list in
      `FileEditor.svelte` (`createEditor`).

### 3. Add a unit test
- [x] Create `frontend/src/lib/utils/__tests__/close-hover-on-scroll.unit.ts`
      (vitest, jsdom) verifying the plugin dispatches `closeHoverTooltips` on a
      `viewportChanged` update when hover tooltips are active, and does nothing
      otherwise.

### 4. Verify
- [x] `cd frontend && npm run check` — no TS errors.
- [x] `cd frontend && npm test` — new unit test passes.
- [x] `cd frontend && npm run build` — build succeeds.
- [x] Clean up reproduction artifacts (temp server, `/tmp/repro-editor`,
      `/tmp/kb-repro-*`, temp DB) and confirm `git status` is clean.

## Notes

- This only closes hover tooltips on scroll; it does not disable them. Hover
  behavior is unchanged when not scrolling.
- The sticky-scroll header (separate feature) was confirmed to overlay content but
  the user explicitly ruled it out ("nothing pinned to top"), so it is intentionally
  left untouched.
