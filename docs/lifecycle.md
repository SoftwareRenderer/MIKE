# Server Lifecycle: Construction, Shutdown, and Base Contexts

The primary rules are in `AGENTS.md → Server Management`.

## Shutdown Stage List

Components register their stop step via `s.reg(stage, name, step)` during construction. This ensures that construction order mirrors shutdown order, and `NewServer` serves as the definitive list of components that require stopping.

**Shutdown Order**:
WebSocket watchers $\to$ Terminal sessions $\to$ LSP $\to$ Extensions $\to$ Sandbox containers $\to$ Database.

This order ensures that dependent resources are removed after the components that use them (e.g., a shell is killed before its container is deleted).

- **WebSocket Watchers**: `AppStreamManager.Close` must close all watchers. If a watcher is omitted, it may keep inotify watches active and continue broadcasting frames after the hub is closed.
- **Deadlines**: Shutdown runs with a 15s deadline (`backend/server/shutdown.go`). Because some steps may hang (e.g., a non-responsive container), the process will exit after the deadline to avoid locking the data directory. Each step logs `shutdown: <name>` for debugging.
- **Error Handling**: Never use `log.Fatalf` for `Serve()` errors in `main.go`. Instead, run the signal cleanup path (flight recorder, `srv.Shutdown`) before exiting with 1. This prevents abrupt exits that skip resource cleanup.

**Tests**: `backend/server/lifecycle_test.go` and `backend/server/shutdown_test.go`.

## Base Contexts

Production code must not use `context.Background()` or `context.TODO()` without registration (`ctxroot` rule). Registrations are categorized by reason:

- **root**: No parent context exists.
- **detached**: Work that must outlive the initiating request; these use `Server.Detached()` as the parent.
- **legacy**: A caller lacks a context and cannot pass one.

`Server.Detached()` is the only parent for work that outlives a request, ensuring that all detached operations are cancelled during server shutdown.

`backend/server/sideeffects.go` tracks detached operations and request-scoped reads to ensure consistency across the route table.

## TLS and Plain Listener

The first-byte sniff is performed before `hybridListener.Accept()`. Because `net/http` accepts connections serially, a peer that connects without sending data would otherwise stall all subsequent handshakes. 

- **Silent Peers**: Connections that send no data are closed and logged per host, never reaching `net/http`.
- **Plain HTTP**: The plain-text listener exists solely to serve 301 redirects to HTTPS. The server does not support a full plain-HTTP mode.
- **Termination**: Only an error from the listener's own `Accept()` call should end `Serve()`.

**Tests**: `backend/server/listener_test.go`.

## Verification

Running `make check-race` executes `./backend/server/`, which boots and shuts down the server, verifying the shutdown stages, base-context registry, and TLS listener.
