# Sandbox Bridge — Design History

Read this before changing `pkg/bridge/`, `backend/sandbox/sandbox.go`,
`backend/extensions/sandbox.go` or `backend/lsp/extension_manager.go`. The invariants that must
survive any edit live in `AGENTS.md → Sandbox Bridge`; this file is the *why*.

## Why one stdio connection per consumer

`sandbox-bridge` runs inside each sandbox container and services tool calls (read, write, edit,
bash — plus `bash_cancel` and the LSP JSON-RPC passthrough) over the stdin/stdout of one
`podman exec -i … /sandbox-bridge` **per connection**. The process (and its connection) lives for
as long as its consumer does, so tool calls don't pay for a `podman exec` each. There is **no
listener and no port**: the agent (`pkg/bridge/agent/main.go`) wraps `os.Stdin`/`os.Stdout` in a
`net.Conn` shim (`stdioConn`), the host side (`sandbox.NewClient(stdin, stdout)`) speaks the same
framing into those pipes, and nothing in the repo calls `net.Dial`. Logging goes to stderr only.

Interactive terminals do **not** go through the bridge: `sandbox.SandboxManager.startShell` runs
`podman exec -it <container> <shell>` with a host-side PTY (`backend/term/terminal.go`), which is
why the bridge protocol has no shell methods.

## How the bridge gets into a container

Three routes, and knowing all three is the point: a container can hold a bridge while the image build
that was supposed to provide one failed, which is exactly how a two-day build breakage looked like a
running sandbox.

1. **Baked at image build** — extension Dockerfiles `COPY sandbox/sandbox-bridge`, from a staged build
   context (`BuildExtensionImage`, `backend/extensions/image_build.go`).
2. **Copied at create** — `ExtensionSandboxManager.Create` and `SandboxManager` both `podman cp` the
   host's bridge to `/sandbox-bridge`, so a container never runs a bridge older than the server that
   started it, whatever the image held.
3. **Built into the app image** — `Dockerfile` compiles `./pkg/bridge/agent` and copies it beside the
   server binary (that is why the committed `sandbox/sandbox-bridge` is excluded from the app build
   context: the image must not carry a second, staler copy).

`make bridge-check` is the gate on route 3's committed twin: an edit under `pkg/` that was not rebuilt
is an edit that never runs.

## Why an LSP session is bound to its connection

A language server is initialized with one workspace root and answers about the documents opened on
its connection, so every (workspace, language) gets its own connection:
`ExtensionSandboxManager.StartSession(extID)` (in `backend/extensions/sandbox.go`) spawns a
dedicated `podman exec` and `ReleaseSession` ends it — the connection *is* the sandbox reference,
and `retire` in `backend/lsp/extension_manager.go` is what hands it back.

Sharing one connection across workspaces — which is what this replaced — meant the second
workspace's `LSPStart` destroyed the first one's server (root, loaded packages, open documents,
handshake) while its holder kept sending it requests.

`pkg/bridge/agent/lsp.go` also no longer auto-restarts a dead session, because a server started
that way has never seen `initialize`, and gopls answers a document sent to it with a *warning about
itself* (`No active builds contain …`, `addView called before server initialized` in its own log)
rather than an error — which the turn-boundary diagnostics pass then delivered to the model as a
code problem.

## Why the bridge never gets a port

The only `-p` publishing on a sandbox container is for **user-configured** forwarded ports
(`project.ForwardPorts`), never for the bridge. An extension's declared `forward_ports` are validated
and stored but never published - the extension sandbox builds its own create args, and its containers
show `9999/tcp` exposed with no host binding.

`forwardPortSpec` passes the entry's bind address to podman unchanged, and empty - which is also what
`0.0.0.0` and `::` normalize to - means no bind address at all. The address matters more than it looks:
pasta derives the forward *target* from it, and a gVisor container owns only the namespace's public
(mirrored host) address, so a loopback bind publishes a port podman reports as mapped while every
connection gets an RST. Accepted as the user's choice; `--no-splice` is what would make it work.
Practical consequence: the URL for a forwarded port is the **machine address, not `localhost`** - and a
sandboxed server that pins its own Host must list that address, since a wildcard listen host names no
host. Container ports outside 1-65535 are skipped.

## Open work: the extension sandbox is still per *extension*

Its mount set is derived from the workspace that created it (a project's `node_modules` mount is
adapted onto that workspace's path), so a second workspace with an incompatible set recreates the
container and destroys every server in it — today a workspace switch answers the first one with
`sandbox client stale` instead of hanging. Making the sandbox per **(extension, project)** is the
remaining fix for that, and for the cross-project read surface (`collectProjectMounts` mounts every
registered project). This is open work, not a TODO.md item — `TODO.md` is a recycled per-plan
scratchpad.

## Invariants to preserve (moved from AGENTS.md)
Invariants to preserve (the why, and the open work, are in `docs/sandbox-bridge.md`):
- A task sandbox has exactly one connection for its lifetime; an **extension sandbox has one per LSP session**. `ExtensionSandboxManager.StartSession`/`ReleaseSession` (`backend/extensions/sandbox.go`) own it and `retire` (`backend/lsp/extension_manager.go`) hands it back — the connection *is* the sandbox reference.
- **A task's sandbox callbacks are read as one snapshot.** The eight `sandbox*`/`recoverBridge` fields of `executionState` are guarded by `sandboxMu` and reachable only through `sandbox()` (returns a copy) and `setSandbox()`. Setup, a mode switch, an agent-config switch and bridge recovery each replace the whole set, so reading fields one at a time can mix the old container's reader with the new one's writer — which reads as a broken pipe and gets retried. `sandboxRecreate.underLock` therefore takes the `*sandboxState` being written, not the `*executionState`: `sandboxMu` is already held and is not reentrant. `sandboxMu` is never held while taking `taskMu`.
- A repeated `start` of the same command+args is **idempotent**; a different one is **refused, never replaced**. The agent kills its session when the connection ends (`handler.close`) — the only thing that reaps the server — and never auto-restarts one.
- The host treats a closed `LSPNotificationCh()` as "this server is gone" (`sandboxClient.dead` → `usable()` → retire), since the connection outlives the process.
- **A client request is always answered:** `forwardRequest` (`backend/lsp/handler.go`) reports a failed send *and* answers the browser with a JSON-RPC error; a failed first `initialize` clears the workspace's pending marker and answers everyone queued behind it.
- Absent diagnostics while a file is unloaded are **unknown, not clean**: `backend/exec/diagnostics.go` refuses to read loader-state entries as findings and counts the file `unloaded`.
- Wire method IDs `0x05`-`0x08` are retired (they were `shell_open`/`shell_io`/`shell_resize`/`shell_close`) and **must not be reused** — a stale bridge binary must get a clean "unknown method" error, not a wrong handler.
- The only `-p` publishing on a sandbox container is **user-configured** forwarded ports (`forwardPortSpec` passes the entry's bind address through; see "Why the bridge never gets a port"), never the bridge; container ports outside 1-65535 are skipped.
- **Build:** `make bridge` — `CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o sandbox/sandbox-bridge ./pkg/bridge/agent`. The artifact is **committed**, and it is what a source checkout hands to a container, so an edit under `pkg/` that is not re-built is an edit that never runs: `make bridge-check` fails when the committed binary is not a build of this tree. **Discovery** (`pkg/sandboxruntime/runtime.go`): `MIKE_AGENT_PATH`, then `sandbox-bridge` / `sandbox/sandbox-bridge` next to the executable, then the same paths in the CWD; container creation fails if not found. **Protocol:** binary framing — 9-byte header (MsgID + Type + Length) + typed payload, Chunk frames stream before the final Result/Error, `MethodLSP` (0x0A) is the JSON-RPC passthrough.
