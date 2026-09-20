# Sandbox Bridge — Design History

Read this before changing `pkg/bridge/`, `backend/sandbox/sandbox.go`,
`backend/extensions/sandbox.go` or `backend/lsp/extension_manager.go`. The invariants that must
survive any edit live in `AGENTS.md → Sandboxing`; this file is the *why*.

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

A language server's **cwd is the workspace mount root**: the `podman exec` that starts the bridge
carries `-w <MountPath>` and `newLSPSession` sets no `cmd.Dir`, so the server never lands inside a
sub-directory of the workspace, however deep the package it serves lives. An editor gives a language
server the same cwd, so a project with its build config in a sub-directory has to be resolvable from
that config's own location: `frontend/svelte.config.js` declares the `$lib` alias for its preprocessor
rather than relying on the one in `vite.config.ts`, because Vite discovers its config file from the
process cwd and finds none at a workspace root that keeps the frontend below it.

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

## Where a pasted or dropped file lands

A file the user pastes or drops into a message is neither an image (which travels inside the
message as base64) nor text (which is inlined into it), so it has to become a file the agent
opens. `POST /api/projects/{projectID}/tasks/{taskID}/uploads` hands it to
`TaskExecutor.WriteTaskFile` (`backend/exec/task_files.go`), which writes it **where that task's
actions run** and answers with the path the message then names:

- **The destination is the task's own worktree, `<worktree>/.mike-uploads/`, written on the host.**
  `project.sandbox_enabled` decides only the path handed back: `sandbox.MountPath(project.Name)` plus
  `.mike-uploads/` for a sandboxed task, because that mount *is* its worktree, and the host path for an
  unsandboxed one. One write, two views of the same bytes, so there is no host fallback to refuse — the rule
  in AGENTS.md ("a sandboxed task gets no host fallback") is about paths the agent cannot open, and it can
  open either of these.
- **No container has to be running.** A paused, idle or not-yet-started task accepts an attachment, and a
  plan-mode task's read-only mount does not stop one: that mount is the guest's and the write is the host's.
  What must exist is the worktree, because **bringing an environment up is not this call's side effect** —
  preparing one creates the worktree, its branch and the task-row writes that go with them. A task with no
  worktree gets the conflict and starts the task first.
- **A second upload of one name gets `-1`, then `-2`** (`report.pdf`, `report-1.pdf`). Two gestures can carry
  the same name and the message that named the first must keep naming the bytes it was written for, so the
  conflict is settled where the bytes land. `Lstat`, not `Stat`: a symlink holding the name is the name being
  taken.
- **The jail runs before the write.** `fsutil.Contained` on the lexical *and* the symlink-resolved path
  against the worktree root, because the task's own actions can replace `.mike-uploads` with a symlink. The
  directory is `0700`, the file `0600`.
- **The upload is invisible to git, which is what makes the placement safe.** The first write appends
  `.mike-uploads/` to the repository's local `.git/info/exclude` (`git.EnsureExcluded`, and
  `docs/git-operations.md`), which keeps it out of `git status`, out of `git add -A`, out of `git stash -u`
  and out of reach of the `git clean -fd` that a checkpoint restore runs. Without that line the file is git's
  to discard.
- **The file's lifetime is the task's.** It goes with the worktree, and the worktree goes with the task and
  with nothing else: `RemoveWorktree` holds the only `os.RemoveAll` of a worktree directory, and its callers
  are the task-deletion path and one merge rollback that undoes a worktree made moments earlier. Stop, pause,
  mark-done, a model or mode switch and a server restart all leave the file where the message says it is —
  `StopSandbox` takes the container and nothing in the worktree, where it used to take the task's uploads
  with it. A message naming an upload therefore outlives its bytes only when the task itself is gone.

Tests: `backend/exec/task_files_test.go` (where the bytes land, the name rule, the mount path answered to a
sandboxed task, the symlink refusal, the refusal with no worktree),
`backend/server/handler_task_upload_test.go` (verbs, CSRF, 413/400/404/409/200, the path answered is
the path written, the upload surviving everything but its task's deletion),
`backend/git/exclude_test.go` (the exclude line and what git then cannot do to the upload),
`backend/server/project_delete_teardown_test.go` (a deleted project releases each task's worktree
and the uploads inside it), `backend/sandbox/stop_retry_test.go` (a container that refused `podman rm`
stays tracked, so a later stop can still remove it).

## Invariants that must not break

- A task sandbox has exactly one connection for its lifetime; an **extension sandbox has one per LSP session**. `ExtensionSandboxManager.StartSession`/`ReleaseSession` (`backend/extensions/sandbox.go`) own it and `retire` (`backend/lsp/extension_manager.go`) hands it back — the connection *is* the sandbox reference.
- **A task's sandbox callbacks are read as one snapshot.** The eight `sandbox*`/`recoverBridge` fields of `executionState` are guarded by `sandboxMu` and reachable only through `sandbox()` (returns a copy) and `setSandbox()`. Setup, a mode switch, an agent-config switch and bridge recovery each replace the whole set, so reading fields one at a time can mix the old container's reader with the new one's writer — which reads as a broken pipe and gets retried. `sandboxRecreate.underLock` therefore takes the `*sandboxState` being written, not the `*executionState`: `sandboxMu` is already held and is not reentrant. `sandboxMu` is never held while taking `taskMu`.
- A repeated `start` of the same command+args is **idempotent**; a different one is **refused, never replaced**. The agent kills its session when the connection ends (`handler.close`) — the only thing that reaps the server — and never auto-restarts one.
- The host treats a closed `LSPNotificationCh()` as "this server is gone" (`sandboxClient.dead` → `usable()` → retire), since the connection outlives the process.
- **A client request is always answered:** `forwardRequest` (`backend/lsp/handler.go`) reports a failed send *and* answers the browser with a JSON-RPC error; a failed first `initialize` clears the workspace's pending marker and answers everyone queued behind it.
- Absent diagnostics while a file is unloaded are **unknown, not clean**: `backend/exec/diagnostics.go` refuses to read loader-state entries as findings and counts the file `unloaded`.
- Wire method IDs `0x05`-`0x08` are retired (they were `shell_open`/`shell_io`/`shell_resize`/`shell_close`) and **must not be reused** — a stale bridge binary must get a clean "unknown method" error, not a wrong handler. **A params change is a new ID, never a new field**, for the same reason: a guest that does not know a field answers success anyway. `MethodWriteBytes` (0x0B) is therefore its own method rather than `WriteParams.Data`, because a bridge that ignored that field would write base64 text and report the byte count of it. `Data` is `[]byte` for the matching reason — `encoding/json` base64s a `[]byte` and gives the same bytes back, while a Go `string` carrying them is re-encoded as UTF-8 and returns with every invalid sequence replaced by U+FFFD. `protocol.MaxWriteBytes` (20 MiB) is enforced in `handleWriteBytes` *and* in `Client.WriteFileBytes` before the send, like the read cap. Tests: `pkg/bridge/agent/write_bytes_test.go`, `pkg/bridge/protocol/protocol_test.go`, `pkg/bridge/client_limit_test.go`.
- **Every byte crossing the bridge is bounded where it is captured.** `protocol.MaxFramePayload` (64 MiB) is checked in `ReadFrame` *before* it allocates: the length is a 4-byte field, so a peer that announces a payload it never sends otherwise costs the host the whole claim. `protocol.MaxReadBytes` (16 MiB) is enforced twice — `handleRead` refuses the file in the guest and `waitForCtx` stops counting chunks on the host — because a bound living only inside the container is not a bound, since a stale bridge keeps streaming. Sandbox `bash` output accumulates through `toolresult.Capture` (`captureOutput` and the streaming callback), the 1 MiB the host bash path already used. Both numbers sit far above anything the model can use: `read_file` shows 50 KB, the tool preview 1 KB. Tests: `pkg/bridge/protocol/frame_limit_test.go`, `pkg/bridge/client_limit_test.go`, `pkg/bridge/agent/read_limit_test.go`, `pkg/bridge/agent/committed_bridge_test.go`, `backend/exec/output_limits_test.go`.
- **A streamed answer is never partial.** The readLoop closes `chunkCh` before dispatching the Result, and `waitForCtx` drains what is still queued before taking it — taking the answer on its own returned a random prefix of the stream as a success. A chunk that cannot be queued fails the request (`ErrStreamOverflow`) rather than being dropped, because `ReadFile` cannot tell a short body from a complete one.
- The only `-p` publishing on a sandbox container is **user-configured** forwarded ports (`forwardPortSpec` passes the entry's bind address through; see "Why the bridge never gets a port"), never the bridge; container ports outside 1-65535 are skipped.
- **Build:** `make bridge` — `CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o sandbox/sandbox-bridge ./pkg/bridge/agent`. The artifact is **committed**, and it is what a source checkout hands to a container, so an edit under `pkg/` that is not re-built is an edit that never runs: `make bridge-check` fails when the committed binary is not a build of this tree. **Discovery** (`pkg/sandboxruntime/runtime.go`, `GetAgentBinaryPath`): the path the process was started with (`sandboxruntime.Boot.AgentPath`, resolved once by `backend/startup` from `MIKE_AGENT_PATH` — the runtime itself never reads the environment), then `sandbox-bridge` / `sandbox/sandbox-bridge` next to the executable, then the same paths in the CWD; container creation fails if not found. **Protocol:** binary framing — 9-byte header (MsgID + Type + Length) + typed payload, Chunk frames stream before the final Result/Error, `MethodLSP` (0x0A) is the JSON-RPC passthrough.

## Wire IDs and the committed binary

Wire IDs `0x05`-`0x08` are **retired and must not be reused**: a stale bridge must get "unknown method", not
a wrong handler. For the same reason **a params change is a new ID, never a new field** — a guest that
ignores a field answers success anyway (`0x0B write_bytes` exists because of exactly that).

`sandbox-bridge` is rebuilt from source and the artifact is committed: an edit under `pkg/` that is not
rebuilt is an edit that never runs, and `make bridge-check` fails when the committed binary is not a build
of this tree. Note that `deadcode .` (server binary only) reports `protocol.NewResult`, `NewChunk`,
`ParseRequest` and the rest of `pkg/bridge/protocol` as unreachable — they are reached from
`./pkg/bridge/agent`, the module's other main. Run both roots before believing a hit.

## Forwarded ports

The only `-p` publishing on a sandbox container is **user-configured** forwarded ports, never the bridge.
Each entry may name a host interface (`types.PortForward.HostIP`); empty — like `0.0.0.0` and `::` — is
published with no bind address, which is what podman wants for every interface. The address is passed
through untouched, and it is not cosmetic: pasta derives the forward *target* from it, and a gVisor guest
owns only the namespace's public address, so a loopback bind maps a port nothing answers. A forwarded port
therefore answers on the **machine address, not `localhost`**.
