# Pi Agent Extension

This extension integrates the [Pi Coding Agent](https://pi.dev/) as an agent extension for MIKE. When a task is configured with `llama_server_url: "agent://pi"`, MIKE spawns the `pi` binary and manages its lifecycle via a WASM adapter.

## Overview

Pi is a full coding agent that handles its own LLM calls, tool execution, and reasoning. MIKE provides sandbox isolation, manages the subprocess lifecycle, and streams events to the frontend. All protocol logic lives in the WASM adapter.

```
MIKE (Go server)
    │
    │  JSON config
    ▼
WASM Adapter (pi.wasm)
    │
    │  JSONL commands / output
    ▼
Pi Agent Process (binary on $PATH)
    │
    │  LLM API calls, tool execution
    ▼
LLM / Tools
```

## Building

```bash
cd extensions/agent/pi/wasm
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o ../pi.wasm .
```

Requires Go 1.24+ (for WASI reactor mode with `-buildmode=c-shared`).

## Architecture

### WASM Adapter (`wasm/main.go`)

The WASM adapter is a [WASI reactor module](https://go.dev/blog/wasmexport#building-a-wasi-reactor) that implements the `AgentAdapter` interface. It translates between MIKE's generic agent events and Pi's JSONL protocol.

**Memory model** ([wazero](https://wazero.io/) allocation pattern):
- **Host → WASM**: The host calls `Alloc(size)`, writes data to linear memory, then passes `(ptr, len)` as a `string` parameter to the exported function.
- **WASM → Host**: The module calls `store_result(data string)`, which the host reads via `m.Memory().Read(ptr, length)`.

**Exported functions**:
| Function | Purpose |
|----------|---------|
| `Init(config string) int32` | Initialize adapter with config JSON |
| `SubprocessConfig()` | Return process config (binary, args, env) |
| `EncodeCommand(cmd, data string)` | Encode command to PI JSONL bytes |
| `ParseOutput(data string)` | Parse Pi JSONL stdout into events |
| `HandleUIRequest(id, method string)` | Generate UI request response |
| `Close()` | Clean up adapter state |

### Pi Protocol

Pi communicates via JSONL (JSON Lines) on stdin/stdout. Each line is a JSON object:

```jsonl
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"Hello"}}
{"type":"tool_execution_start","toolName":"read","toolCallId":"call-1","args":{"path":"main.go"}}
{"type":"tool_execution_end","toolName":"read","toolCallId":"call-1","isError":false,"result":{"content":[{"type":"text","text":"..."}]}}
{"type":"agent_end","stopReason":"stop","messages":[...]}
```

The adapter converts these into generic agent events (`token`, `reasoning`, `tool_call`, `tool_result`, `persist`, etc.).

### Concurrency

The WASM adapter uses a mutex to serialize all WASM calls. This is critical because:
- The `readOutput` goroutine calls `ParseOutput` without holding the runner's mutex
- The `Prompt`/`Compact`/etc. methods call `EncodeCommand` while holding the runner's mutex
- Without serialization, concurrent calls corrupt the WASM module's bump allocator and crash with `unsafe.Slice: len out of range`

### Tool Mapping

Pi tool names are mapped to MIKE tool names:

| Pi Tool | MIKE Tool |
|---------|-------------|
| `read` | `read_file` |
| `write` | `write_file` |
| `edit` | `edit_file` |
| `bash` | `bash` |
| `request_review` | `request_review` |
| `final_answer` | `request_review` |
| `ask_question` | `ask_question` |
| `create_task` | `create_task` |

## Configuration

The extension reads its config from the extension manifest. Key settings:

```json
{
  "agent": {
    "wasm_module": "pi.wasm"
  }
}
```

Sandbox mounts and environment variables are configured via the manifest's `config_schema`.

## Manifest

See `manifest.json` for the extension metadata, config schema, and WASM module path.

## Files

| File | Description |
|------|-------------|
| `adapter.go` | Pi protocol adapter (pure Go, no backend imports) |
| `adapter_test.go` | Unit tests for the adapter |
| `wasm/main.go` | WASI reactor wrapper with `#[wasmexport]` functions |
| `manifest.json` | Extension metadata and configuration schema |
| `build.sh` | Build script for the WASM module |
| `pi.wasm` | Prebuilt WASM module |
