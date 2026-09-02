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
