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
