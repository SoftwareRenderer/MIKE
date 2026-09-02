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
(`project.ForwardPorts` / an extension's `forward_ports`), never for the bridge; `forwardPortSpec`
binds the host side to `127.0.0.1` so a container's listener is never exposed on the LAN. Container
ports outside 1-65535 are skipped.

## Open work: the extension sandbox is still per *extension*

Its mount set is derived from the workspace that created it (a project's `node_modules` mount is
adapted onto that workspace's path), so a second workspace with an incompatible set recreates the
container and destroys every server in it — today a workspace switch answers the first one with
`sandbox client stale` instead of hanging. Making the sandbox per **(extension, project)** is the
remaining fix for that, and for the cross-project read surface (`collectProjectMounts` mounts every
registered project). This is open work, not a TODO.md item — `TODO.md` is a recycled per-plan
scratchpad.
