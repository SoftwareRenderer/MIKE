# Recipe: Adding a Persisted Preference or Shortcut

Preferences and shortcuts use the same configuration system: `config` table $\to$ `GET/PUT /api/config` $\to$ `appStore.config`. Follow the steps below to implement a new one. Note that they share a single Save/Cancel flow (`AGENTS.md → Frontend`).

Preferences specific to a single browser (not the user account) should not use this system—see *When the preference belongs to a browser* below.

`PUT /api/config` performs a partial update at the top level but replaces objects entirely. You must send all `keyboard_shortcuts` bindings; omitting one resets it to the default.

## Adding a keyboard shortcut

1. **Backend Type**: Add a `*string` field to `types.KeyboardShortcuts` (`backend/types/domain.go`).
2. **Backend Normalization**: One line, in `Empty()` (`backend/types/domain.go`): `&& k.NewField == nil`. That
   is what decides whether `keyboard_shortcuts` reaches the response at all, because
   `normalizeKeyboardShortcuts` (`backend/config/config.go`) asks it rather than naming the bindings.
   `Empty()` is hand-written, so forgetting it is possible —
   `TestKeyboardShortcutsEmptyCoversEveryField` (`backend/types/domain_test.go`) walks the struct by
   reflection and fails when a binding set on its own still reads as all-default.
3. **Frontend Type**: Add a field to `KeyboardShortcuts` (`$lib/types`).
4. **Default**: Set a default in `DEFAULT_SHORTCUTS`. If the default varies by platform, use a `defaultXxx()` helper in `$lib/utils/keyboard.ts`.
5. **UI Row**: Add a row to `ConfigModal/PreferencesTab.svelte`.
6. **Save Payload**: Include the field in the `ConfigModal.svelte` save payload (send all bindings).
7. **Dispatch**: 
   - Global bindings: `use-keyboard-shortcuts.svelte.ts` (wired in `App.svelte`).
   - Scoped bindings: The owning component (`CreateTaskModal`, `Chat/TaskChat`, `TerminalPanel`, `CardDetail`).
   - Explorer views: `$lib/services/explorer-core.svelte.ts`.

**Capture rules** (`normalizeKey` in `$lib/utils/keyboard.ts`): Rejects modifier-only presses, Escape, named keys without a modifier, and browser-reserved `Ctrl+W/Q/N/T`. Click bindings capture `mousedown` via `PreferencesTab.svelte`.

**Hints**: Text buttons show a `<kbd class="key-hint">` and a `title` tooltip. These are hidden by `hide_hints` or on screens below 768px.

**Tests**: `backend/types/domain_test.go` (the `Empty()` walk over every binding), `backend/config/config_test.go`, `utils/__tests__/keyboard.unit.ts`, `preferences-tab.unit.svelte.ts`, and e2e `preferences-keyboard.spec.ts`.

## Adding a UI preference (`*bool`)

Example: `hide_model_badge` gates `.model-badge` in `Card.svelte` (`appStore.config?.hide_model_badge === true`).

1. **Backend Setup**: 
   - Add `*bool` to `types.Config`.
   - Add one `flagPref` entry to `configPrefs` (`backend/config/preferences.go`): the key, and the two
     closures that read and write the field. `configRowKeys`, `GetConfig`, `UpdateConfig` and the PUT
     merge in `HandleConfig` all come out of that entry, so the read, the save and the merge follow the
     table. `TestPreferenceTableCoversEveryPreference` (`backend/config/preferences_test.go`) walks
     `types.Config` by reflection and fails when a preference there has no entry here.
   - Add the field to `configResponse` (`omitzero`) and to `buildConfigResponse`. That pair stays
     hand-written on purpose: the response is not the row set — it carries what the process knows and
     leaves `registry_url` out.
   - Add a `ConfigStore` accessor returning a plain `bool` for Go consumers, reading the row through
     `storedFlag` so its key comes from the table (e.g., `GetReclaimIdleSandbox`).
2. **Frontend Type**: Add a field to `Config` (`$lib/types`).
3. **UI Integration**: Add state/hydration/save logic in `ConfigModal.svelte`.
4. **UI Row**: Add a toggle to `ConfigModal/PreferencesTab.svelte` (UI) or `ConfigModal/TaskDefaults.svelte` (behaviour defaults like `run_siblings_parallel`).
5. **Consumption**:
   - Browser: Read `appStore.config` in the component.
   - Server: Use the `ConfigStore` accessor (e.g., `MemorySupervisor` check).
6. **Cross-Layer Actions**: If the preference triggers an action in another layer, use that layer's API. For example, `loop_breaker` uses `exec.Deps.RewindConversation` rather than calling `DeleteTaskMessagesAfter` directly to keep the worktree in sync with the conversation.

**Defaults**: For flags that are "on" by default (e.g., `auto_maximize_chat_mobile`), treat `nil` as `true`. The client should consume `!== false`.

**Tests**: `backend/config/config_test.go`, `backend/config/preferences_test.go` (the table walk), `preferences-tab.unit.svelte.ts`, a component test for the gated widget, and the e2e Save-flow suite `preferences-keyboard.spec.ts` (its last two tests move a UI toggle, not a binding).

## When the preference belongs to a browser

Choices that only the current browser can act on (e.g., notification permissions) must be stored locally to avoid syncing across accounts. Use the `app-settings` store via `$lib/storage` and mirror it to a reactive store field (e.g., `uiStore.notifyOnReview` in `docs/notifications.md`).

- **UI**: Row lives in `PreferencesTab.svelte`.
- **Lifecycle**: `ConfigModal.svelte` hydrates it on open and writes to the local store on `save()`. `clearAll()` at sign-out removes it.
- **Tests**: Store round-trip with `$lib/storage` mocked (`stores/__tests__/ui-preference-persist.unit.svelte.ts` — one data-driven suite over every browser preference, each row naming its key, its default and what a stored value that is not a boolean means).

## Verification

- **Stored shape**: `go test ./backend/config/`
- **A preference the table forgot**: `go test ./backend/config/ -run TestPreferenceTableCoversEveryPreference`
- **All-default omission**: `go test ./backend/types/ ./backend/config/ -run KeyboardShortcuts`
- **Round trip**: `cd frontend && npx playwright test preferences-keyboard`
