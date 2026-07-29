# Agent Extensions

The agent extensions system allows [MIKE](https://getmike.dev) to run third-party AI coding agents (like [Pi](https://pi.dev/)) as subprocesses, while keeping all protocol logic inside a WASM adapter. The backend knows nothing about any specific agent protocol — it only interacts with the generic `AgentAdapter` interface.

## Architecture

```
MIKE Backend (Go)
    │
    │  AgentAdapter interface
    ▼
Agent Runner (backend/agent/)
    │
    │  WASM calls (wazero)
    ▼
WASM Adapter (extensions/agent/<name>/wasm/main.go)
    │
    │  JSONL / text / bytes
    ▼
Agent Process (binary on $PATH)
    │
    │  LLM API / Tools
    ▼
LLM Provider
```

### Key Components

| Component | Location | Responsibility |
|-----------|----------|----------------|
| `AgentAdapter` interface | `pkg/agent/types.go` | Generic interface the backend uses |
| `AgentRunner` | `backend/agent/runner.go` | Spawns agent subprocess, bridges I/O, dispatches events |
| `WasmAdapter` | `backend/agent/loader.go` | Loads WASM module, calls exported functions |
| WASM wrapper | `extensions/agent/<name>/wasm/main.go` | WASI reactor with `#[wasmexport]` functions |
| Adapter core | `extensions/agent/<name>/adapter.go` | Protocol logic (pure Go, no backend imports) |
| Extension registry | `backend/server/server.go` | Maps `agent://name` URLs to extensions |

### Data Flow

```
User sends message
    │
    ▼
WebSocket → TaskExecutor → AgentRunner.Prompt()
    │
    ▼
AgentRunner → WASM Adapter.EncodeCommand() → Agent stdin
    │
    ▼
Agent processes → Agent stdout
    │
    ▼
AgentRunner.readOutput() → WASM Adapter.ParseOutput() → Events
    │
    ▼
Events → WebSocket → Frontend
```

## How It Integrates With the Backend

### 1. Extension Registration

Agent extensions are registered in `extensions/registry.json` and loaded into the database. The server's extension registry maps `agent://<name>` URLs to extension entries.

```go
// server.go: Agent registry maps agent:// URLs
agentRegistry := agent.NewRegistry(extensionStore)
// agent://pi → "pi" extension
```

### 2. Task Execution

When a task is created with `llama_server_url: "agent://pi"`, the `TaskExecutor` calls `executeTaskWithAgent`:

```go
// exec/loop_agent.go
func (te *TaskExecutor) executeTaskWithAgent(..., serverURL string, ...) {
    agentName := strings.TrimPrefix(serverURL, "agent://")  // "agent://pi" → "pi"
    ext := te.agentRegistry.LookupByName(agentName)        // find extension
    runner, err := agent.NewAgentRunner(ctx, ext, ...)    // create runner
}
```

### 3. WASM Loading

`NewAgentRunner` loads the WASM adapter and initializes it:

```go
// backend/agent/runner.go
adapter, err := loadAdapter(ext)              // loadWasmAdapter
adapter.Init(ctx, cfgBytes)                   // WASM Init()
spCfg := adapter.SubprocessConfig(ctx)        // WASM SubprocessConfig()
cmd := exec.Command(spCfg.Binary, spCfg.Args...)  // spawn agent
```

### 4. Event Loop

The runner spawns a `readOutput` goroutine that reads agent stdout, calls `ParseOutput`, and dispatches events:

```go
// backend/agent/runner.go
go func() {
    for {
        n, _ := r.stdout.Read(buf)
        events := r.adapter.ParseOutput(ctx, buf[:n])  // WASM ParseOutput()
        for _, event := range events {
            r.dispatchEvent(ctx, event)  // route to channels/WebSocket
        }
    }
}()
```

### 5. User Messages

User messages flow through the runner to the adapter and then to the agent:

```go
// backend/agent/runner.go
func (r *AgentRunner) Prompt(ctx context.Context, message string, behavior ...string) error {
    bytes := r.adapter.EncodeCommand(ctx, CommandPrompt, dataBytes)  // WASM EncodeCommand()
    r.stdin.Write(bytes)  // to agent stdin
}
```

## The AgentAdapter Interface

Defined in `pkg/agent/types.go`:

```go
type AgentAdapter interface {
    Init(ctx context.Context, config []byte) error
    SubprocessConfig(ctx context.Context) SubprocessConfig
    EncodeCommand(ctx context.Context, cmd string, data []byte) []byte
    ParseOutput(ctx context.Context, data []byte) []Event
    HandleUIRequest(ctx context.Context, id, method string) []byte
    Close()
}
```

### Event Types

| Type | Purpose |
|------|---------|
| `token` | Text delta from agent |
| `reasoning` | Thinking/reasoning delta |
| `tool_call` | Agent requests tool execution |
| `tool_result` | Tool execution result |
| `status` | Status change (running, idle, etc.) |
| `stats` | Session/token statistics |
| `persist` | Turn-end data for database persistence |
| `error` | Error from agent |

### Persistence

The `persist` event carries `PersistEntry` objects that the executor saves to the database. This is the **only** path to DB persistence — the executor knows nothing about agent-specific message formats. The adapter pre-parses assistant text, tool calls, tool results, and token counts into `PersistEntry`.

## Concurrency Model

```
AgentRunner (1 mutex: r.mu)
    ├── Prompt/Compact/Stats/GetState/Abort  → hold r.mu
    └── readOutput goroutine
        ├── ParseOutput()  → does NOT hold r.mu
        └── dispatchEvent
            ├── HandleUIRequest()  → does NOT hold r.mu
            └── stdin.Write()  → holds r.mu
```

The `WasmAdapter` has its own mutex (`a.mu`) that serializes all WASM calls, preventing concurrent access to the wazero module, the bump allocator, and the `resultCh` channel.

## Sandbox Integration

When sandboxing is enabled, the agent runs inside a gVisor sandbox:

```go
// backend/agent/runner.go
if sandboxEnabled {
    ph, err := sandboxMgr.StartProcess(taskID, spCfg.Binary, spCfg.Args)
    r.stdin = ph.Stdin
    r.stdout = ph.Stdout
} else {
    cmd := exec.Command(spCfg.Binary, spCfg.Args...)
    r.stdin, _ = cmd.StdinPipe()
    r.stdout, _ = cmd.StdoutPipe()
}
```

Sandbox mounts and environment variables are resolved from the extension manifest's `config_schema` and passed to the adapter via the `config` JSON.
