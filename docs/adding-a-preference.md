# Recipe: Adding a Persisted Preference or Shortcut

Both go through the same config plumbing (`config` table → `GET/PUT /api/config` → `appStore.config`)
and both are 7-layer changes. Consult this file when adding one; the semantics that must not break
are in this file's *Full layer lists*, and the rule that they ride one shared Save/Cancel flow is in `AGENTS.md → Frontend`.

A preference about **this browser** rather than about the user does not belong in that plumbing — see
*When the preference belongs to a browser* below.

`PUT /api/config` is a **partial update** at the top level but **replaces whole objects** — a settings
payload must always send **all** `keyboard_shortcuts` bindings, because omitting one resets it to its
default.

## Adding a keyboard shortcut

1. `*string` field in `types.KeyboardShortcuts` (`backend/types/domain.go`).
2. All-nil check in `normalizeKeyboardShortcuts` (`backend/config/config.go`).
3. Field in `KeyboardShortcuts` (`$lib/types`).
4. Default in `DEFAULT_SHORTCUTS` — or a platform-aware `defaultXxx()` helper
   (`$lib/utils/keyboard.ts`) if the default differs per platform; platform-aware defaults are
   **not** `DEFAULT_SHORTCUTS` entries.
5. Row in `ConfigModal/PreferencesTab.svelte`.
6. Field in the `ConfigModal.svelte` save payload (always send all bindings).
7. Dispatch: global bindings in `use-keyboard-shortcuts.svelte.ts` (wired in `App.svelte`);
   dialog/panel-scoped bindings in the owning component (`CreateTaskModal`, `Chat/TaskChat`,
   `TerminalPanel`, `CardDetail`) — both explorer views share one handler, in
   `$lib/services/explorer-core.svelte.ts`.

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

1. `*bool` in `types.Config`, then five places in `backend/config/config.go`: read in `GetConfig`
   (`parseBoolPtr`), write in `UpdateConfig`, field in `configResponse` (`omitempty`, so an unset flag
   stays unset), PUT merge in `HandleConfig`, and **its key in `configRowKeys`**. A key missing from that
   list is never read from the `config` table, so the flag answers nil whatever was saved. Add a
   `ConfigStore` accessor returning a plain `bool` when a Go consumer needs one, and say in it what an
   unset value means — `GetReclaimIdleSandbox` reads unset as off.
2. Field in `Config` (`$lib/types`).
3. State / hydrate / save payload in `ConfigModal.svelte`.
4. Toggle row in `ConfigModal/PreferencesTab.svelte` (UI section) — or in
   `ConfigModal/TaskDefaults.svelte` when the preference is a *behaviour* default rather than something this
   browser renders (`run_siblings_parallel`, `reclaim_idle_sandbox`, `loop_breaker`).
5. Consume it. A browser-side preference reads `appStore.config` in the target component. A server-side
   one reads the accessor where the decision is made: `MemorySupervisor` calls `GetReclaimIdleSandbox` on
   the tick that decides. A stored preference is not a startup decision, so
   `AGENTS.md → Configuration`'s resolve-once rule does not apply to it.
6. When the action the flag unlocks belongs to another layer, pass that layer's door rather than a copy of
   its work. `loop_breaker` answers the run that hit its consecutive-failed-turn limit (`breakFailureLoop`,
   `backend/exec/loop_breaker.go`) by cutting the failed call and the turns after it, which is what the
   browser's "delete message" does. `exec.Deps.RewindConversation` is that door: `NewTaskExecutor` wires it
   to `rewindConversationForRow` (`backend/server/handler_message.go`), so `truncateMessagesFrom` keeps
   every step of the cut. An executor calling `DeleteTaskMessagesAfter` itself would leave the worktree
   ahead of the conversation that describes it.

**A flag that is on by default** (`auto_maximize_chat_mobile`) is the same plumbing read the other
way: nil means on, so the client consumes `!== false`, hydrates `!== false` and defaults the tab
prop to `true`. Only a stored `false` turns it off — which is why the round-trip row that catches
its regression is the explicit-false one, not the unset one.

**Tests to add/update:** `backend/config/config_test.go` (round-trip + omitted-when-nil),
`preferences-tab.unit.svelte.ts`, a component test for the gated widget (e.g.
`card-model-badge.unit.svelte.ts`), e2e `preferences-hide-model-badge.spec.ts` (isolated). A Task-tab row
instead extends `task-defaults.unit.svelte.ts` and the save-payload rows in
`config-modal-run-parallel-save.unit.svelte.ts`, and the behaviour the flag unlocks is tested where it lives
(`loop_breaker` in `backend/exec/loop_breaker_test.go`). A preference a browser never acts on needs no
spec — the Go round trip and the payload assertion are the round trip.

## When the preference belongs to a browser

A choice that only this browser can act on — a notification it must show, a permission only it granted —
must not be stored on the server, where every other browser of the same account would inherit it. Keep it
in the `app-settings` store through `$lib/storage` and mirror it into a store field the app can read
reactively: `uiStore.notifyOnReview` is the example (`docs/notifications.md`).

Three things stay the same as for a server-stored preference: the row lives in `PreferencesTab.svelte`,
`ConfigModal.svelte` hydrates it when the dialog opens and writes the local store inside `save()` (so
Cancel discards it), and `clearAll()` at sign-out takes it with the rest. The test is the store's own
round-trip with `$lib/storage` mocked — `stores/__tests__/notify-on-review-persist.unit.ts`.

## The keyboard-shortcut layers

Persisted as `keyboard_shortcuts` JSON in the `config` table through the shared Settings Save/Cancel flow. Adding one is a 7-layer change.

- Defaults + helpers in `$lib/utils/keyboard.ts`: `create_task` `C`, `create_task_submit`/`start_task` platform Ctrl/Cmd+Enter, `open_terminal` Ctrl+`, `toggle_fullscreen` Ctrl+Up, `multi_select_click` platform Ctrl/Cmd+Click. Platform-aware defaults are `default*()` functions, **not** `DEFAULT_SHORTCUTS` entries.
- **Format:** normalized `control`+`meta`+`shift`+`alt`+key; absent = default, `""` = disabled. The key may be the pseudo-key `click` for a **mouse binding** (`"shift+click"`; `normalizeClick` / `matchesClickBinding` / `effectiveMultiSelectClick` / `isBrowserLinkClick`) — only the primary button is bindable and an unmodified click is never a binding.
- `PUT /api/config` **replaces the whole object** — always send **all** bindings; omitting one resets it to default.
- **Capture** (`normalizeKey`) rejects modifier-only presses, Escape, unmodified named keys and browser-reserved `Ctrl+W/Q/N/T`. **Activation:** global in `use-keyboard-shortcuts.svelte.ts` (wired in `App.svelte`), dialog/panel-scoped in the owning component.
- **Tests:** `backend/config/config_test.go`, `utils/__tests__/keyboard.unit.ts`, `preferences-tab.unit.svelte.ts`, e2e `preferences-keyboard.spec.ts` (isolated).

### UI Preferences (Settings → Preferences → UI)

Top-level `*bool` config fields (`parseBoolPtr` in `GetConfig`, `omitempty` in `configResponse`) with the same Save/Cancel plumbing as shortcuts. Example: `hide_model_badge` gates `.model-badge` in `Card.svelte`.
## Checking this

`go test ./backend/config/` covers the stored shape.
`cd frontend && npx playwright test preferences-keyboard` covers the round trip through Settings.
