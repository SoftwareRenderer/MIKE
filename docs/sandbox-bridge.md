# Sandbox Bridge — Design & Architecture

This document describes the `sandbox-bridge`, the utility that runs inside sandbox containers to facilitate communication between the host and the containerized tools. Invariants are in `AGENTS.md → Sandboxing`.

## Architecture

### Why the bridge never gets a port
The bridge uses a **single stdio connection** per consumer. It is started via `podman exec -i … /sandbox-bridge`.
- **No Ports**: The bridge has no network listener or port. It wraps `os.Stdin`/`os.Stdout` in a `net.Conn` shim.
- **Lifespan**: The bridge process lives as long as its consumer.
- **Interactive Terminals**: These bypass the bridge and use a host-side PTY via `podman exec -it`.

### Deployment
The bridge enters containers via three paths:
1. **Image Build**: Baked into extension Dockerfiles.
2. **Creation**: Copied via `podman cp` during container creation to ensure the version matches the server.
3. **App Image**: Compiled and bundled with the server binary.

`make bridge-check` ensures the committed binary matches the source in `pkg/bridge/agent`.

### LSP Integration
Language Server Protocol (LSP) sessions are bound to their own dedicated bridge connection.
- **Isolation**: Every (workspace, language) pair gets its own `podman exec` session to prevent conflicting server states (e.g., different root directories).
- **CWD**: The server's current working directory is always the workspace mount root.
- **Persistence**: Dead sessions are not auto-restarted to avoid delivering uninitialized state as diagnostics.

## Forwarded Ports

The only published ports on a sandbox container are **user-configured** forwarded ports.
- **Binding**: The host bind address is passed to podman. An empty address (`0.0.0.0`, `::`) binds to all interfaces.
- **Host Access**: Because gVisor containers own the namespace's public address, a loopback bind results in an RST. Therefore, forwarded ports must be accessed via the **machine address, not `localhost`**.

## File Uploads

Files pasted or dropped into a message are written to the task's worktree at `.mike-uploads/`.
- **Placement**: Written on the host to the worktree root (sandboxed or otherwise).
- **Isolation**: The directory is `0700` and files are `0600`. Path traversal is prevented via `fsutil.Contained`.
- **Git Integration**: `.mike-uploads/` is added to `.git/info/exclude` to prevent it from appearing in `git status` or being included in stashes/cleans.
- **Lifetime**: Uploads are deleted only when the task's worktree is removed.

## Security & Performance Invariants

### Protocol & Framing
- **Wire IDs**: Method IDs (e.g., `0x05`-`0x08`) are retired and must not be reused. Parameters changes require new IDs.
- **Binary Data**: Binary writes use a dedicated method; they are never passed as JSON strings to avoid UTF-8 encoding issues.
- **Payload Bounds**: Every byte crossing the bridge is bounded. `protocol.MaxFramePayload` (64 MiB) and `protocol.MaxReadBytes` (16 MiB) prevent memory exhaustion attacks.

### Reliability
- **Exactly-Once Delivery**: A task's frames are delivered exactly once through a synchronized locking mechanism (`asm.mu` and `taskBufMu`).
- **Stream Completeness**: Read results are only dispatched after the stream is fully drained or the context expires, preventing partial reads.
- **Symmetry**: The host treats a closed LSP notification channel as a dead server and retires the session.

## Verification

- **Bridge Logic**: `pkg/bridge/protocol/protocol_test.go` and `pkg/bridge/agent/committed_bridge_test.go`.
- **Limits**: `pkg/bridge/protocol/frame_limit_test.go`, `pkg/bridge/client_limit_test.go`, `pkg/bridge/agent/read_limit_test.go`.
- **File Uploads**: `backend/exec/task_files_test.go`, `backend/server/handler_task_upload_test.go`, `backend/git/exclude_test.go`.
- **Symmetry/Race**: `backend/stream/app_stream_subscribe_race_test.go`.
