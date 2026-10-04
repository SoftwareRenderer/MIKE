# Server lifecycle: construction order, shutdown, base contexts

The rules are in `AGENTS.md → Server Management`.

## Shutdown is a declared stage list

- A component registers its stop step with `s.reg(stage, name, step)` at a stage declared in
  `backend/server/lifecycle.go`, **where it is constructed**, so construction order is shutdown order and
  `NewServer` reads as the list of things that need stopping. Registering a stage twice panics at startup,
  and a nil step is refused at registration — otherwise it panics at shutdown, far from its cause.
- The first stage cancels the server's base context (`Server.Detached()`), before any resource a detached
  operation still writes through is taken away. What sets the rest of the order is the pipe/`WaitDelay`
  rule in `docs/sandbox-runtime.md`: websocket watchers → terminal sessions → lsp → extensions → sandbox
  containers → database, so a session's shell is killed while its container can still receive it.
- The **websocket watchers** stage is `AppStreamManager.Close`, and it closes all three watchers that
  manager constructs — worktree, project, branch. A watcher built there but left out of `Close` keeps its
  inotify watches and keeps broadcasting through the rest of the shutdown: it watches paths the later
  stages are deleting, and `/api/stream` has no stage of its own, so the frames it produces go to a hub
  nobody is closing. Test: `backend/stream/app_stream_close_watchers_test.go`.
- SIGTERM must end the process. `srv.Shutdown` runs under `server.RunShutdownWithDeadline`
  (`backend/server/shutdown.go`, 15 s). Every step waits on something uninterruptible (a container not
  answering podman, an LSP bridge whose peer died), and a shutdown that hangs leaves the process **holding
  the data directory open** — it re-creates `kanban.db`/`-wal` after whatever deleted them, so the next run
  boots on the previous run's database. A bounded *context* bounds nothing here; the deadline is enforced
  by leaving anyway. Each step logs `shutdown: <name>` before running, so the last line names what wedged.
- Never `log.Fatalf` on a `Serve()` error (`main.go`): run the signal cleanup path (flight recorder,
  `srv.Shutdown`) and then exit 1. `log.Fatalf` skips that cleanup and makes any listener error an abrupt
  exit.
- Tests: `backend/server/lifecycle_test.go` (`TestNewServerRegistersEveryStage` refuses a step that moved
  out of `NewServer`), `backend/server/shutdown_test.go`.

## Every base context is registered, with its reason

`context.Background()`/`context.TODO()` in production Go is one archgate rule (`ctxroot`). Baseline entries
are `file|function|reason|x<count>`, where the reason is:

- **root** — nothing above it to cancel from;
- **detached** — a write whose point is to leave state behind, so it must outlive the request that began
  it, and its parent is `Server.Detached()`;
- **legacy** — a caller has a context and does not pass one.

The count is part of the key, so a second base context inside an already-classified function is a finding,
and the rule refuses a baseline line it cannot parse rather than silently un-classifying that site.
`backend/server/sideeffects.go` is the same idea as data for HTTP — detached operations, request-scoped
reads, and the idempotent writes allowed on a read — held by `sideeffects_test.go` against the real route
table.

`Server.Detached()` is the only parent for work that outlives a request: shutdown cancels it in the one
place that knows when to stop, and a test or ad-hoc caller cannot start detached work that no shutdown will
ever reach.

## The TLS/plain listener

The first-byte sniff must not run inside `hybridListener.Accept()`: `net/http` accepts **serially**, so one
peer that connects and sends nothing stalls every later handshake, and returning that peer's EOF reads as a
dead listener, ends `Serve` and kills the process. `Accept()` waits on a queue a per-connection goroutine
fills — not a worker pool, since N workers still block everyone at N silent peers. Silent peers are closed
and logged per host, never handed to `net/http`, and only the listener's own accept error may end `Serve`.
The plain half exists to send `RedirectHTTPSMiddleware`'s 301 and nothing else: the server has no
plain-HTTP mode, so a cleartext request is one click on a wrong address, not a deployment.
Test: `backend/server/listener_test.go`.
## Checking this

`make check-race` runs `./backend/server/`, where the tests boot a server and take it down again: the
shutdown stages, the base-context registry and the TLS listener.
