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

**URL scheme:** `agent://<name>` (legacy `pi://agent`).

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
