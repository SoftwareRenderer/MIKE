# LSP Extensions

LSP (Language Server Protocol) extensions provide advanced code intelligence—such as completions, hover information, diagnostics, and go-to-definition—by running language servers for [MIKE](https://getmike.dev).

## Architecture

LSP extensions run as standalone containers managed by the backend. The backend uses a proxy called `sandbox-bridge` to communicate with the language server inside the container via JSON-RPC.

```
MIKE Backend (Go)
    │
    │  LSP Client (JSON-RPC)
    ▼
gVisor Sandbox (Podman Container)
    │
    │  sandbox-bridge (Entrypoint)
    ▼
Language Server (e.g., gopls, tsserver)
    │
    │  Analysis of /mnt/<project>
    ▼
Project Files
```

### Key Components

| Component | Location | Responsibility |
|-----------|----------|----------------|
| `ExtensionSandboxManager` | `backend/extensions/sandbox.go` | Manages gVisor container lifecycle, mounts, and bridge sessions |
| `sandbox-bridge` | `pkg/bridge/` | Small binary that runs inside the sandbox to proxy LSP traffic |
| `LSP Config` | `manifest.json` | Defines the server command, arguments, and supported languages |
| `Environment` | `Dockerfile` | Defines the OS, toolchain, and server installation |

## Structure

An LSP extension is organized as a directory within `extensions/lsp/`:

```text
extensions/lsp/<name>/
├── Dockerfile
└── manifest.json
```

### manifest.json

The manifest specifies how the language server should be launched and which files it handles.

```json
{
    "display_name": "Go Language Server",
    "capabilities": {
        "lsp": {
            "command": "gopls",
            "args": ["serve"],
            "extensions": ["go"],
            "language": "go",
            "initParamsOverride": {
                "hints": { "parameterNames": true }
            }
        }
    }
}
```

- `command`: The binary to execute inside the container.
- `args`: Command-line arguments for the server.
- `extensions`: List of file extensions (e.g., `["go", "vue"]`) that trigger this server.
- `language`: The LSP language identifier.
- `initParamsOverride`: Custom parameters sent during the `initialize` request.

### Dockerfile

The Dockerfile must install the language server and the `sandbox-bridge`. The container's `CMD` must be `/usr/local/bin/sandbox-bridge`.

Example pattern:
1. Use a builder stage to install the LSP server.
2. Use a slim runtime stage.
3. Copy the LSP server and the `sandbox-bridge` binary.
4. Set `CMD ["/usr/local/bin/sandbox-bridge"]`.
