# Agent Extensions — Adapter & WASM Detail

Read this before writing or changing a WASM adapter, `backend/agent/`, or `backend/exec/loop_agent.go`.
Rules are defined in `AGENTS.md → Extensions`. Reference implementation:
`../MIKE-extensions/agent/pi/`, which speaks Pi's RPC JSONL (its unit tests live there too and are
**not** part of `go test ./backend/... ./pkg/...`). `../MIKE-extensions/agent/claude-code/` is a
second adapter over a different wire — Claude Code's stream-json transport, where a turn ends on a
`result` line and a tool's output arrives back on a `user` message. Read it before adding an agent
whose protocol is neither. Both emit the same `pkg/agent` events; nothing in this repository
knows the difference.

## Layering

`Backend (executor) ←→ AgentRunner (I/O bridge) ←→ WASM Adapter (protocol) ←→ Agent Process`

The backend is agnostic to agent protocols; the adapter handles all protocol-specific logic.

## Adapter interface (`AgentAdapter`)

`Init(config)`, `SubprocessConfig()`, `EncodeCommand(cmd, data)`, `ParseOutput(bytes) → []Event`,
`HandleUIRequest(id, method)`, `Close()`.

Event types: `token`, `reasoning`, `tool_call`, `tool_result`, `status`, `stats`, `error`, `persist`,
`turn_complete`.

- **Persistence:** `EventTypePersist` uses `[]PersistEntry` to save assistant text, reasoning, and tool calls to the database. This is the only way to persist data, allowing the backend to remain protocol-agnostic.

**Stats:** `EventTypeStats` provides `SessionStatsData` (context, input/output tokens, and cost) for the task card via `sendAgentStats` in `backend/exec/loop_agent.go`. Cache figures (`TotalCacheRead`/`TotalCacheWrite`) are currently unused and report 0; enabling them requires a coordinated frontend update.

## WASM Memory Model

- **Input:** String parameters are mapped to pointer and length in linear memory.
- **Output:** A host callback `store_result(string)` is called synchronously during `fn.Call` and captured via a buffered channel.
- The host calls `_initialize` first (wazero loader: `WithStartFunctions("_initialize")`), then the
  exported functions.
- There are no shared result buffers or `malloc`/length bookkeeping.

## Creating an agent extension in `../MIKE-extensions/agent/<name>/`

| File | Role |
|---|---|
| `adapter.go` | Core protocol logic — **zero `backend/` imports**, so its unit tests run natively there |
| `wasm/main.go` | WASI reactor via `//go:wasmexport`, wrapping the adapter. `string` is allowed as a **param only**, never a return |
| `build.sh` | `GOOS=wasip1 GOARCH=wasm go build -trimpath -buildmode=c-shared` |
| `go.mod` | The adapter is a separate module. It uses `replace mike => ../../../MIKE` to access `pkg/agent` types. `build.sh` executes from the `wasm/` directory. |
| `manifest.json` | Declares `agent.wasm_module` |
| `LICENSE` / `NOTICE` / `THIRD_PARTY_NOTICES.md` | Required — extensions are independently licensed projects |

Tests in `backend/agent` (`TestWasmPiAdapter_*`) load `pi.wasm` through the production loader. A module
nobody built skips with the path it looked for; a module that is on disk and will not load **fails**, so a
broken build cannot hide behind a green suite.

**Where the module comes from.** `resolveWasmPath` asks `extensions.InstalledDir(ext)` first — the same
`<extensions>/<author>/<name>/` folder that removal, repair and the config form derive. Then the
adapter's source tree, then the bare module name, which is also what the error names when nothing
exists. An extension installed from a folder on this machine
(`POST /api/extensions/install-local`, offered by the Browse dialog through `GET /api/extensions/local`)
therefore loads the module its own install materialized, with no registry in the loop.
`backend/agent/loader_test.go` fails when that ordering breaks.

**URL Scheme:** Use `agent://<name>` (replacing `pi://agent`). Definitions and parsing are centralized in `backend/types/agent_url.go` to avoid import cycles with `backend/config`. The frontend mirrors this as `AGENT_URL_PREFIX`. `TestRegistry_EmittedProviderURLsParseBack` verifies that emitted URLs can be parsed back.

## Which binary the agent runs

`SubprocessConfig().Binary` is the sole source of the executable's name; it is not stored in the manifest or guessed by the backend. `agent.PrepareAgent` loads and initializes the adapter and resolves that config without spawning anything; `Prepared.Binary()` reports it and `Prepared.Start` spawns it. `NewAgentRunner` is the two in sequence.

Pre-flight: `exec.prepareAgent` runs after the adapter is initialized. In sandboxed projects, it verifies the binary name via `Prepared.Binary()`. If the binary is not found or refused, it returns `agent_not_available`. A spawn failure returns `agent_start_failed`. See `backend/exec/agent_preflight_test.go`.

## Agent Extensions as Providers

An enabled agent extension becomes a provider with URL `agent://<name>`
(`AgentRegistry.ToProviders()`), merged into `GET /api/config` per request by
`buildConfigResponse` (`backend/config/config.go`) and never persisted
(`filterPersistedProviders`). So "install Pi" and "a new provider appears" are the same event:

- The server uses `SetOnExtensionChanged` (`backend/server/server.go`) during install, deletion, or configuration changes. It reloads the registry before broadcasting `extension_change` to prevent race conditions.
- `App.svelte` handles the broadcast by calling `appStore.refreshConfig()`. Since `appStore.config` is the single source for all provider lists, this updates the entire UI without requiring a page reload.
- `ConfigModal.svelte` reads the form when opening and then only updates agent rows. This prevents the modal from discarding user edits to LLM providers when an agent extension is installed.
- `TaskChat.svelte` tracks `appStore.config` and uses `untrack()` when updating provider selection to avoid circular reactivity.

An agent extension that only appears in the list after a browser refresh is a bug in this path, not in the installation. Tests: `frontend/src/lib/stores/__tests__/app-store-refresh-config.unit.ts`, `frontend/src/lib/components/__tests__/config-modal-live-agent-provider.unit.svelte.ts`, and `reselectProvider` in `frontend/src/lib/utils/__tests__/providers.unit.ts`.

## Sandbox Mounts: Proposal vs Grant

`capabilities.agent.sandbox.mount` defines the desired configuration path inside the container and the requested host path. This is a request from the registry, not a grant: `agent.Registry.GetAgentSandbox` only mounts what the user has approved.

- The host path is set via `PUT /api/extensions/{id}/agent-mount`, which writes the extension's `sandbox_mount` config field. `extensions.AgentMountConfigKey` is the shared name. `PUT /api/extensions/{id}` refuses this key to prevent accidental edits via the general form.
- The container path is defined by the agent. The install dialog and editor show the proposed path and mode; only the host path is user-editable. `extensions.ValidateAgentMountGrant` expands `~`, resolves symlinks, and applies `checkGrantShape` to prevent mounting sensitive directories (e.g., root, SSH keys).
- The directory does not need to exist beforehand; the agent creates it on first login. The server ensures the parent directory exists. Ownership is managed as described in `docs/sandbox-runtime.md → Extension sandbox mounts`.
- `GetAgentSandbox` re-verifies the grant upon reading (`refuseMount`) to handle stale records.
- Only the agent assigned as the task's provider gets the mount: `AgentSandboxForURL(task.LlamaServerURL)`, via `taskExecSetup.providerURL()` during task creation, `Server.agentGrantForTask` for file endpoints, and `TerminalManager.agentGrantForShell` for shells.
- `TaskExecutor.agentGrantHash` hashes both the provider URL and the grant. Any change to either recreates the mount to ensure the container is up to date.
- The adapter still receives the manifest-defined sandbox path (`runner.go`) for internal variable resolution; this is extension-side metadata, not a host grant.
- **Two declaration shapes, one reader**: The registry index projects `capabilities.agent.sandbox.mount` for pre-install views, while `GET /api/extensions/local` provides the raw manifest. Both are resolved by `resolvedSandboxManifest`. `agentMountDeclaration()` is the sole reader.
- The UI manages the grant at install via the install review dialog. The extension card then displays the grant status (`needsAgentMountGrant` yellow dot) and provides an `Unmount` option. The host path is managed in the edit form (`HOST_OWNED_AGENT_CONFIG_KEY`) and saved via its own route.

Tests: `backend/extensions/agent_mount_test.go`, `backend/agent/registry_test.go`, `backend/exec/agent_grant_test.go`, `backend/term/agent_grant_test.go`, and `frontend/src/lib/utils/__tests__/extension-permissions.unit.ts`.
