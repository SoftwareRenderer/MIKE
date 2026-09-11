# Recipe: Adding a Persisted Preference or Shortcut

Both go through the same config plumbing (`config` table → `GET/PUT /api/config` → `appStore.config`)
and both are 7-layer changes. Consult this file when adding one; the semantics that must not break
are in `AGENTS.md → Keyboard Shortcuts` and `AGENTS.md → UI Preferences`.

`PUT /api/config` is a **partial update** at the top level but **replaces whole objects** — a settings
payload must always send **all** `keyboard_shortcuts` bindings, because omitting one resets it to its
default.

## Adding a keyboard shortcut

1. `*string` field in `types.KeyboardShortcuts` (`backend/types/domain.go`).
2. All-nil check in `normalizeKeyboardShortcuts` (`backend/config/config.go`).
3. Field in `KeyboardShortcuts` (`$lib/types.ts`).
4. Default in `DEFAULT_SHORTCUTS` — or a platform-aware `defaultXxx()` helper
   (`$lib/utils/keyboard.ts`) if the default differs per platform; platform-aware defaults are
   **not** `DEFAULT_SHORTCUTS` entries.
5. Row in `ConfigModal/PreferencesTab.svelte`.
6. Field in the `ConfigModal.svelte` save payload (always send all bindings).
7. Dispatch: global bindings in `use-keyboard-shortcuts.svelte.ts` (wired in `App.svelte`);
   dialog/panel-scoped bindings in the owning component (`CreateTaskModal`, `Chat/TaskChat`,
   `TerminalPanel`, `CardDetail`, `ExplorerView`, `TaskExplorerView`).

**Capture rules** (`normalizeKey` in `$lib/utils/keyboard.ts`): rejects modifier-only presses, Escape
(cancels capture), named keys without a modifier, and browser-reserved `Ctrl+W/Q/N/T`. A click binding
captures a `mousedown` instead — `PreferencesTab.svelte` adds a window capture listener while that row
is capturing, refuses a bare click and any keypress, and skips its own `.key-chip`/`.shortcut-action`
so click-again-to-cancel, Reset and Clear keep working.

**Hints:** text buttons show a `<kbd class="key-hint">` + the shortcut in the `title` tooltip; hidden
by `hide_hints` (tooltips keep the text) and below 768px; `aria-label`s stay stable for tests.

**Tests to add/update:** `backend/config/config_test.go`,
`utils/__tests__/keyboard.unit.ts`, `preferences-tab.unit.svelte.ts`, e2e
`preferences-keyboard.spec.ts` (isolated), and — if it drives a widget — a spec like
`terminal-shortcut`. `preferences-keyboard.spec.ts` already covers rebinding/clearing/resetting
the submit and fullscreen bindings, so a new widget-level shortcut does not need its own spec.

## Adding a UI preference (`*bool`)

Example in-repo: `hide_model_badge` gates `.model-badge` in `Card.svelte`
(`appStore.config?.hide_model_badge === true`).

1. `*bool` in `types.Config` + read in `GetConfig` / write in `UpdateConfig` + field in
   `configResponse` + PUT merge in `HandleConfig` (all `backend/config/config.go`;
   `parseBoolPtr` on read, `omitempty` on the response so an unset flag stays unset).
2. Field in `Config` (`$lib/types.ts`).
3. State / hydrate / save payload in `ConfigModal.svelte`.
4. Toggle row in `ConfigModal/PreferencesTab.svelte` (UI section).
5. Consume from `appStore.config` in the target component.

**A flag that is on by default** (`auto_maximize_chat_mobile`) is the same plumbing read the other
way: nil means on, so the client consumes `!== false`, hydrates `!== false` and defaults the tab
prop to `true`. Only a stored `false` turns it off — which is why the round-trip row that catches
its regression is the explicit-false one, not the unset one.

**Tests to add/update:** `backend/config/config_test.go` (round-trip + omitted-when-nil),
`preferences-tab.unit.svelte.ts`, a component test for the gated widget (e.g.
`card-model-badge.unit.svelte.ts`), e2e `preferences-hide-model-badge.spec.ts` (isolated).

## Full layer lists (moved from AGENTS.md)
### Keyboard Shortcuts (Settings → Preferences)
Persisted as `keyboard_shortcuts` JSON in the `config` table through the shared Settings Save/Cancel flow. Adding one is a 7-layer change — recipe: **`docs/adding-a-preference.md`**.

- Defaults + helpers in `$lib/utils/keyboard.ts`: `create_task` `C`, `create_task_submit`/`start_task` platform Ctrl/Cmd+Enter, `open_terminal` Ctrl+`, `toggle_fullscreen` Ctrl+Up, `multi_select_click` platform Ctrl/Cmd+Click. Platform-aware defaults are `default*()` functions, **not** `DEFAULT_SHORTCUTS` entries.
- **Format:** normalized `control`+`meta`+`shift`+`alt`+key; absent = default, `""` = disabled. The key may be the pseudo-key `click` for a **mouse binding** (`"shift+click"`; `normalizeClick` / `matchesClickBinding` / `effectiveMultiSelectClick` / `isBrowserLinkClick`) — only the primary button is bindable and an unmodified click is never a binding.
- `PUT /api/config` **replaces the whole object** — always send **all** bindings; omitting one resets it to default.
- **Capture** (`normalizeKey`) rejects modifier-only presses, Escape, unmodified named keys and browser-reserved `Ctrl+W/Q/N/T`. **Activation:** global in `use-keyboard-shortcuts.svelte.ts` (wired in `App.svelte`), dialog/panel-scoped in the owning component.
- **Tests:** `backend/config/config_test.go`, `utils/__tests__/keyboard.unit.ts`, `preferences-tab.unit.svelte.ts`, e2e `preferences-keyboard.spec.ts` (isolated).

### UI Preferences (Settings → Preferences → UI)
Top-level `*bool` config fields (`parseBoolPtr` in `GetConfig`, `omitempty` in `configResponse`) with the same Save/Cancel plumbing as shortcuts. Example: `hide_model_badge` gates `.model-badge` in `Card.svelte`. Full recipe + tests: **`docs/adding-a-preference.md`**.
