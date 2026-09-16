# Agent Extensions — Adapter & WASM Detail

Read this before writing or changing a WASM adapter, `backend/agent/`, or `backend/exec/loop_agent.go`.
The invariants live in `AGENTS.md → Agent Extensions`. Reference implementation:
`../MIKE-extensions/agent/pi/` (its unit tests live there too and are **not** part of
`go test ./backend/... ./pkg/...`).

## Layering

`Backend (executor) ←→ AgentRunner (I/O bridge) ←→ WASM Adapter (protocol) ←→ Agent Process`

The backend knows **nothing** about agent protocols — all protocol logic lives in the adapter.

## Adapter interface (`AgentAdapter`)

`Init(config)`, `SubprocessConfig()`, `EncodeCommand(cmd, data)`, `ParseOutput(bytes) → []Event`,
`HandleUIRequest(id, method)`, `Close()`.

Event types: `token`, `reasoning`, `tool_call`, `tool_result`, `status`, `stats`, `error`, `persist`,
`turn_complete`.

**Persistence:** `EventTypePersist` carries `[]PersistEntry` (pre-parsed assistant text / reasoning /
tool calls). It is the **only** path to DB persistence — the executor iterates entries generically,
with zero agent-specific persistence in the backend.

## WASM memory model (zero buffers)

- **Input** = `string` params; the runtime maps them to ptr+len in linear memory.
- **Output** = a `//go:wasmimport env store_result(string)` host callback that fires synchronously
  during `fn.Call` (a size-1 buffered channel safely captures it).
- The host calls `_initialize` first (wazero loader: `WithStartFunctions("_initialize")`), then the
  exported functions.
- No shared result buffers, no `malloc`/length bookkeeping.

## Creating an agent extension in `../MIKE-extensions/agent/<name>/`

| File | Role |
|---|---|
| `adapter.go` | Core protocol logic — **zero `backend/` imports**, so its unit tests run natively there |
| `wasm/main.go` | WASI reactor via `//go:wasmexport`, wrapping the adapter. `string` is allowed as a **param only**, never a return |
| `build.sh` | `GOOS=wasip1 GOARCH=wasm go build -trimpath -buildmode=c-shared` |
| `manifest.json` | Declares `agent.wasm_module` |
| `LICENSE` / `NOTICE` / `THIRD_PARTY_NOTICES.md` | Required — extensions are independently licensed projects |

`backend/agent`'s `TestWasmPiAdapter_*` tests are the only check that a rebuilt `pi.wasm` loads under
wazero, and they **skip** rather than fail when `extensions/agent/pi/pi.wasm` is absent — which it is in a
checkout with no installed pi extension. So `ok mike/backend/agent` says nothing about the artifact:
copy the built module to that path (gitignored) and re-run `-run TestWasmPiAdapter` to see them pass.

**URL scheme:** `agent://<name>` (legacy `pi://agent`). The shape has one home —
`types.AgentURLPrefix` with `AgentProviderURL` (emit) and `ParseAgentURL` / `IsAgentURL` (read) in
`backend/types/agent_url.go` — because `backend/agent` already sits *above* `backend/config` in the
import graph, and the config store is one of the readers. Every Go site calls it; the browser mirrors
the prefix as `AGENT_URL_PREFIX` (`frontend/src/lib/types.ts`). Both directions are held:
`TestRegistry_EmittedProviderURLsParseBack` re-reads every URL `ToProviders()` emits.

## Which binary the agent runs

`SubprocessConfig().Binary` is the **only** place the executable's name exists — never a manifest
field, never a name guessed in the backend. `agent.PrepareAgent` loads and initializes the adapter
and resolves that config without spawning anything; `Prepared.Binary()` reports it and
`Prepared.Start` spawns it. `NewAgentRunner` is the two in sequence.

The sandboxed task's pre-flight (`exec.prepareAgent`) therefore runs **after** the adapter is known
good, and asks the container about `Prepared.Binary()` — a hard-coded name refused every agent whose
binary was not literally `pi`, in sandboxed projects only. A refusal keeps its own code,
`agent_not_available`, distinct from `agent_start_failed`; the message names the binary it actually
looked for.

## An agent extension *is* a provider — and the UI learns about it live

An enabled agent extension becomes a provider with URL `agent://<name>`
(`AgentRegistry.ToProviders()`), merged into `GET /api/config` per request by
`buildConfigResponse` (`backend/config/config.go`) and never persisted
(`filterPersistedProviders`). So "install Pi" and "a new provider appears" are the same event,
and the client has to notice it:

- The server calls `SetOnExtensionChanged`'s callback (`backend/server/server.go`) on install,
  delete, enable/disable and config save — it reloads the agent registry **first**, then
  broadcasts `extension_change`, so a client that re-reads the config cannot lose a race.
- `App.svelte` answers that broadcast with `appStore.refreshConfig()`; `appStore.config` is the
  single source for every provider list (Settings → Providers, `CreateTaskModal`, `TaskChat`),
  so one refresh updates them all. Nothing here may be skipped or turned into a page reload.
- `ConfigModal.svelte` re-reads the form **once per dialog open** and afterwards reconciles only
  the derived agent rows: the dialog is open while the user installs the extension, and a full
  re-sync would discard the LLM provider they are editing (keys come back masked). Agents are
  the only rows the user does not own, so they are the only ones that move.
- `TaskChat.svelte` tracks `appStore.config` — it outlives the change (a chat stays mounted),
  unlike the modals, which are built per open. Its sync is `untrack()`ed because it reads and
  writes the provider/selection state it is updating.

Tests: `frontend/src/lib/stores/__tests__/app-store-refresh-config.unit.ts`,
`frontend/src/lib/components/__tests__/config-modal-live-agent-provider.unit.svelte.ts`,
`reselectProvider` in `frontend/src/lib/utils/__tests__/providers.unit.ts`.

## Naming the binary, and the agent as a provider

- `SubprocessConfig().Binary` is the only home for the executable's name — never a manifest field (a second
  source of truth that can disagree) and never a literal in the backend. A sandboxed task's
  `BinaryAvailable` check runs on the resolved name **after** the adapter is loaded and validated
  (`exec.prepareAgent` over `agent.PrepareAgent`/`Prepared.Start`; `NewAgentRunner` is the two in
  sequence), because a hard-coded `"pi"` refused every other agent in sandboxed projects only, and reported
  the extension rather than the binary it probed. The refusal keeps the code `agent_not_available`,
  distinct from `agent_start_failed`, and names the binary it looked for.
  Test: `backend/exec/agent_preflight_test.go`.
- The URL scheme `agent://<name>` is emitted and parsed in one place — `types.AgentProviderURL` /
  `types.ParseAgentURL` / `types.IsAgentURL` on `types.AgentURLPrefix` (`backend/types/agent_url.go`),
  which every reader calls including `backend/config` — which can never import `backend/agent`, since
  `agent → extensions → git → config`. The browser mirrors the prefix as `AGENT_URL_PREFIX`.
- An enabled agent extension **is a provider** with that URL: `GET /api/config` merges it per request and
  never persists it, so the client re-reads the config on the `extension_change` broadcast
  (`appStore.refreshConfig()` in `frontend/src/lib/stores/app.svelte.ts`). An agent that only shows up after
  a browser refresh is a bug.

## A manifest's sandbox mount is a proposal; the user's grant is the mount

`capabilities.agent.sandbox.mount` names where the agent keeps its configuration **inside** the container,
and what it would like on the host side of that path. It is registry text, so it is a proposal and never a
grant: what it declared used to be resolved through `config_schema` and mounted — `rw` — into every task
container of every sandboxed project, whatever provider the task ran, and `agent.Registry.GetAgentSandbox`
no longer does that.

- The host side comes from one place: `PUT /api/extensions/{id}/agent-mount`, which writes the extension's
  `sandbox_mount` config field. `extensions.AgentMountConfigKey` is the shared name; the route is the only
  writer, and `PUT /api/extensions/{id}` refuses that key so the edit form cannot be a second way in. An
  empty list revokes, which is why it is not a field of the configuration write.
- The container side stays the agent's own: the install dialog and the editor show the proposal's
  container path and mode, and only the host path is typed. `extensions.ValidateAgentMountGrant` expands
  `~`, resolves symlinks, defaults a missing mode to `ro`, and applies `checkGrantShape` — the same rule
  the dependency directory is measured by, with mount-shaped wording (`grantKind`). It refuses the root, a
  whole top level, the system trees, a home, anything containing a home, and the credential stores, so
  `~/.pi/agent` is grantable and `~/.ssh` is not.
- The directory need not exist — the agent makes it on first login — but something above it must, so the
  server never invents the top level of a path. `sandbox.Create` makes it (0755, like a project's own
  mount), which is what makes an `rw` grant writable from inside: no `:U`, no userns option is involved,
  the directory belongs to the server's user, which is what a rootless container's root maps to.
- `GetAgentSandbox` re-applies the rule when it reads (`refuseMount`), because a row can predate it and
  because `agent_config_dir` is a second way a host path reaches `Mounts[0]`. It applies only to a grant
  that already exists; with no grant it warns and mounts nothing.
- Only the agent that **is** a task's provider contributes: `AgentSandboxForURL(task.LlamaServerURL)`, via
  `taskExecSetup.providerURL()` on the four task create paths, `Server.agentGrantForTask` for the file
  endpoints, and `TerminalManager.agentGrantForShell` for shells (a project terminal names no task, so it
  inherits nothing). `GetAnyAgentSandbox` is gone: "the first enabled agent extension" is exactly the
  widening this removed.
- `TaskExecutor.agentGrantHash` hashes the provider URL **and** the grant. The grant's own shape cannot
  say "nothing granted" — hashing it alone gave `""` for a task on a plain provider and for a task whose
  agent had no grant, so switching away from an agent left its mount on the running container. Any change
  now recreates, `∅ → X` and `X → ∅` alike.
- The adapter still receives the template-resolved manifest sandbox (`runner.go`), because it derives the
  agent's config-directory variables from it. That is extension-side information, not a host grant.
- **Two declaration shapes, one reader**, as with the dependency directory. The registry index projects
  `capabilities.agent.sandbox.mount` (`scripts/gen-registry.py`, which resolves the manifest's `{{…}}`
  references against the `config_schema` defaults because the index is the only copy a pre-install view
  has); `GET /api/extensions/local` and the extension endpoints answer with the manifest, with the same
  resolution applied by `resolvedSandboxManifest` (`toResponse`, `localRegistryEntry`). The stored record
  keeps the reference. `agentMountDeclaration()` is the only reader. Drop the projection and the install
  review goes silent for every registry install — `capabilities.agent` would be `{}` again — and
  `python3 scripts/gen-registry.py --check` is what notices the generator and the published index disagree.
- The UI settles it at install: an entry declaring `agent.sandbox.mount` goes through the install review
  dialog (`requiresInstallReview`), whose two mount buttons are the only things that start that install —
  closing the dialog installs nothing. Afterwards the card carries the state: `needsAgentMountGrant` is the
  yellow dot and `Needs setup` chip, the granted path is shown there with `Unmount`, and `sandbox_mount` is
  host-owned in the edit form (`HOST_OWNED_AGENT_CONFIG_KEY`), so it is never sent by a configuration save.
  The editor saves the grant through the route inside its own Save, and re-derives its rows from the
  extension the route answered with — the stored value is the expanded, symlink-resolved path, not what was
  typed, and a dialog still open after a later failure must not offer the same write twice.

Tests: `backend/extensions/agent_mount_test.go` (the route and the rule, and the proposal as it is served),
`backend/agent/registry_test.go` (proposal vs grant, and the read-time refusal),
`backend/exec/agent_grant_test.go` and `backend/term/agent_grant_test.go` (who gets a mount), and
`frontend/src/lib/utils/__tests__/extension-permissions.unit.ts` (the two predicates and the install gate).
