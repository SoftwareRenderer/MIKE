# Link-like Rows — Design Detail

Read this before modifying `App/Sidebar.svelte`, `Card.svelte`, `App/TopBar.svelte`, `CardDetail/TaskHeader.svelte`, `ExplorerHeader.svelte`, `$lib/utils/app-link.ts`, `$lib/services/url-router.svelte.ts`, or the click routing in `use-board-arrow-nav.ts` / `keyboard.ts`. The hard rules are in `AGENTS.md → Frontend`.

Sidebar project rows, task cards, Explorer buttons, and Terminal buttons are reachable via real URLs. This allows native browser actions (Open in New Tab, Copy Link Address, middle-click) to work. They use deep links (e.g., `?project=`, `?project=&task=`, `&view=explorer`) managed by `url-router.svelte.ts`. The app is a single-page application (SPA) with only one backend route (`/`).

## Address Bar Management

`url-router.svelte.ts` is the single point of truth for the address bar:
- `write()`: The only function that calls `history.pushState` or `replaceState`.
- `read()`: The only function that reads `location.search`.
- `applyView()`: Maps a `view` name to the corresponding screen.

Components like `Board.svelte` (pushes `task`) and `Chat/TaskChat.svelte` (pushes `thread`) use `write()`, which preserves existing params from the live address bar.

## Project Rows

Project rows are rendered as `<a class="project-item" href="/?project=<id>" role="button" tabindex="0">`.

- **Click Behavior**: Standard clicks call `preventDefault()` and trigger `onSelectProject`. Modifier clicks (Ctrl/Cmd/Shift) are **not** cancelled, allowing the browser to open a new tab/window.
- **Cloning**: A cloning project has no `href` (it remains focusable and inert).
- **Keyboard**: Because of `role="button"`, the Space key is handled explicitly (native anchors only respond to Enter).

### A project the app just made
Adding a project navigates the user to its board. `selectProject` clears existing `view`, `file`, and `task` params and calls `applyView()`. 

**Cloning**: A clone's row is selected before the project exists (using a `clone-pending-…` placeholder). `selectProject` performs no server requests in this state. Once the clone completes, the real project is selected without pushing a new history entry.

## Task Cards

To avoid "anchor-in-anchor" markup (since subtask outline rows are also cards), the card root is a `div` with a full-cover overlay anchor:

```html
<div class="card" role="button" tabindex="0" draggable>
  ...
  <a class="card-open-link" aria-hidden="true" tabindex="-1" draggable="false"></a>
</div>
```

- **Overlay**: The `<a>` uses `position: absolute; inset: 0; z-index: 1`.
- **Click Routing**: The overlay's handler calls `stopPropagation()` to prevent the card root from handling the click twice. It cancels navigation unless `isBrowserLinkClick()` indicates the browser should handle it.
- **Multi-select**: Bound modifier clicks (e.g., Ctrl/Cmd+Click) trigger the multi-select toggle; unbound modifier clicks are passed to the browser.
- **Known Limitation**: On queued (offline-pending) cards, the lifted content partially covers the overlay, limiting the link menu's availability to the card's padding.

## Explorer and Terminal Buttons

Both use the `<a class="icon-btn" role="button" tabindex="0" draggable="false">` shape.

- **Explorer Buttons**: Link to `/?project=<id>&view=explorer` or `/?project=<id>&task=<id>&view=task-explorer`. They only link to the explorer view, regardless of the currently open file.
- **Terminal Buttons**: Link to `/?project=<id>&view=terminal` (and optionally `&task=<id>`).
- **Routing**:
  - **Standard Click**: Opens the Terminal as a panel over the current screen (`uiStore.showTerminal`).
  - **Link/New Tab**: Renders the `view=terminal` screen—a standalone fullscreen terminal without the standard app chrome (no sidebar, top bar, or board).
- **Common Logic**: Modifier clicks are left to the browser. Space key activation is handled via `handleLinkKeydown` in `keyboard.ts`.

## Explorer File Rows

File rows are rendered as `<button>`s to preserve `role="treeitem"` semantics and allow custom right-click menus.

- **Opening Windows**: Rows can be opened in a new window via the file menu or a middle-button `mousedown`. `openInNewWindow` handles both.
- **Link Generation**: `explorerPathHref` in `$lib/utils/app-link.ts` builds the URL. Folders are marked by a trailing slash.
- **Validation**: Paths are clamped using `explorerPathRoots` and `ExplorerCore.isPathSafe` to ensure they reside within valid roots (e.g., the checkout or a container mount).
- **Navigation**: Switching projects drops any open file that the new project cannot show.

## Markup and Stacking Contract

- **Project Rows**: Use `.project-item`.
- **Task Cards**: Use `.card` and `.card-open-link`.
- **Stacking (`Card.svelte`)**: `.card-title` is not lifted so the overlay covers it. `.card-actions`, `.subtask-progress-bar`, and `.subtask-list` use `var(--z-content)`.
- **Requirement**: Never replace these with `<button>` or `<div>` + a faked context menu, as native link menus are not synthesizable. Keep selectors tag-agnostic.

## Tests

- `components/__tests__/link-rows.unit.svelte.ts`: Hrefs, click routing, and cloning.
- `components/__tests__/explorer-buttons-are-links.unit.svelte.ts`: Explorer button hrefs.
- `components/__tests__/terminal-buttons-are-links.unit.svelte.ts`: Terminal button hrefs.
- `components/__tests__/file-browser-context-menu.unit.svelte.ts`: File row window opening and middle-click.
- `services/__tests__/url-router.unit.svelte.ts`: Address bar $\leftrightarrow$ router state mapping.
- `utils/__tests__/app-link.unit.ts`: Href generation and root validation.
- `utils/__tests__/keyboard.unit.ts`: Click bindings.
- `link-like-rows.spec.ts` (e2e): Tab opening, drag sources, and terminal screen.
- `preferences-keyboard.spec.ts` (e2e): Rebinding modifier clicks.
- `add-project-navigation.spec.ts` (e2e): Added project navigation and cloning.

## Verification

`cd frontend && npm test` verifies the markup in `link-rows.unit.svelte.ts`.
