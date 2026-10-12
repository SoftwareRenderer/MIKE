# Recipe: Adding a Persisted Preference or Shortcut

Preferences and shortcuts use the same configuration system: `config` table $\to$ `GET/PUT /api/config` $\to$ `appStore.config`. Follow the steps below to implement a new one. Note that they share a single Save/Cancel flow (`AGENTS.md → Frontend`).

Preferences specific to a single browser (not the user account) should not use this system—see *When the preference belongs to a browser* below.

`PUT /api/config` performs a partial update at the top level but replaces objects entirely. You must send all `keyboard_shortcuts` bindings; omitting one resets it to the default.

## Adding a keyboard shortcut

1. **Backend Type**: Add a `*string` field to `types.KeyboardShortcuts` (`backend/types/domain.go`).
2. **Backend Normalization**: Add an all-nil check in `normalizeKeyboardShortcuts` (`backend/config/config.go`).
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

**Tests**: `backend/config/config_test.go`, `utils/__tests__/keyboard.unit.ts`, `preferences-tab.unit.svelte.ts`, and e2e `preferences-keyboard.spec.ts`.

## Adding a UI preference (`*bool`)

Example: `hide_model_badge` gates `.model-badge` in `Card.svelte` (`appStore.config?.hide_model_badge === true`).

1. **Backend Setup**: 
   - Add `*bool` to `types.Config`.
   - In `backend/config/config.go`, update: `GetConfig` (`parseBoolPtr`), `UpdateConfig`, `configResponse` (`omitempty`), `HandleConfig` (PUT merge), and **`configRowKeys`**.
   - Add a `ConfigStore` accessor returning a plain `bool` for Go consumers (e.g., `GetReclaimIdleSandbox`).
2. **Frontend Type**: Add a field to `Config` (`$lib/types`).
3. **UI Integration**: Add state/hydration/save logic in `ConfigModal.svelte`.
4. **UI Row**: Add a toggle to `ConfigModal/PreferencesTab.svelte` (UI) or `ConfigModal/TaskDefaults.svelte` (behaviour defaults like `run_siblings_parallel`).
5. **Consumption**:
   - Browser: Read `appStore.config` in the component.
   - Server: Use the `ConfigStore` accessor (e.g., `MemorySupervisor` check).
6. **Cross-Layer Actions**: If the preference triggers an action in another layer, use that layer's API. For example, `loop_breaker` uses `exec.Deps.RewindConversation` rather than calling `DeleteTaskMessagesAfter` directly to keep the worktree in sync with the conversation.

**Defaults**: For flags that are "on" by default (e.g., `auto_maximize_chat_mobile`), treat `nil` as `true`. The client should consume `!== false`.

**Tests**: `backend/config/config_test.go`, `preferences-tab.unit.svelte.ts`, a component test for the gated widget, and e2e `preferences-hide-model-badge.spec.ts`.

## When the preference belongs to a browser

Choices that only the current browser can act on (e.g., notification permissions) must be stored locally to avoid syncing across accounts. Use the `app-settings` store via `$lib/storage` and mirror it to a reactive store field (e.g., `uiStore.notifyOnReview` in `docs/notifications.md`).

- **UI**: Row lives in `PreferencesTab.svelte`.
- **Lifecycle**: `ConfigModal.svelte` hydrates it on open and writes to the local store on `save()`. `clearAll()` at sign-out removes it.
- **Tests**: Store round-trip with `$lib/storage` mocked (`stores/__tests__/notify-on-review-persist.unit.ts`).

## Verification

- **Stored shape**: `go test ./backend/config/`
- **Round trip**: `cd frontend && npx playwright test preferences-keyboard`
