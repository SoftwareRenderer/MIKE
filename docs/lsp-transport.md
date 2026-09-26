# The LSP transport — Mechanism

Read this before touching `backend/lsp/handler.go`, `backend/lsp/extension_manager.go`,
`frontend/src/lib/utils/lsp-connection-manager.ts` or the browser's wait in
`frontend/src/lib/utils/lsp-hover-actions.ts`. The hard rules live in `AGENTS.md → Extensions` and
`AGENTS.md → Frontend`.

## What this owns

The path one editor message takes: the browser's `/api/lsp` JSON-RPC socket, the handler's ID remapping,
one workspace's queue to its own language server, and the answer's way home. It also owns how long a
browser waits for that answer. It does not own discovering or installing a server
(`docs/extension-lifecycle.md`), which language a file routes to (`docs/languages.md`), or the bridge's
wire protocol beneath it (`docs/sandbox-bridge.md`).

## Must stay true

1. **The socket's read loop never waits on a language server.** `handleClientMessage` ends at an enqueue.
   A queue that cannot take the message answers the request there and then.
2. **Every forwarded request is answered exactly once.** A failure reaches the browser through
   `answerError`; a delivered answer removes its own mapping (`forget`, `dropLocked`). A test that lets a
   request reach the writer and answers nothing is a browser waiting on its own timer.
3. **A notification is written before the writer takes the next message; a request is not.**
   `serveWorkspaceWrites` calls `writeNotification` inline and `writeRequest` on a goroutine, so a
   `didChange` is never overtaken by the hover computed from it and a slow hover holds nothing else.
4. **Nothing between a language server and the router throws a message away.** `deliverToRouter` waits
   for room, and the manager's `done` is what ends that wait at `Close`.
5. **No delivery path writes a browser's socket inline.** `ClientSession.deliver` queues,
   `ClientSession.serve` is the only writer, and a queue at its bound ends that one socket
   (`StatusTryAgainLater`) rather than waiting for the browser.
6. **The browser's request ids are never what the server sees.** `remapRequestID` stamps the forwarded
   id, `remapResponseIDWithChannel` restores the browser's, and `rewriteCancelID` follows a
   `$/cancelRequest` into its params. The two counters are independent, so a number passing through
   would name a different request.
7. **Every response a browser must route names its channel.** The handler stamps `_ch`;
   `dispatchMessage` routes a response by that hint alone and reports a response with no listener.
8. **Every bound in `backend/lsp` is a count, not a duration.** Where a wait exists the caller's context
   ends it, which is why `lspSendTimeout` is a ceiling on a context rather than a timer of its own.
9. **The card always ends on something readable, and each half of an answer lands on its own clock.**
   `DEFAULT_HOVER_WAITING` replaces the placeholder with the server's name, and `DEFAULT_HOVER_CEILING`
   replaces it with the non-answer. `remember` drops a request that failed, so the next hover asks again.
   Documentation the server sent is never held behind a definition request it has not answered.

## How it is held

One goroutine per browser (`ClientSession.serve`) and one ordered writer per workspace
(`serveWorkspaceWrites`) meet in `writeJob`, which carries both ids, the channel, the workspace key and
the session that has to be answered. `enqueueWrite` refuses a dropped or saturated entry, and
`abandonQueued` answers everything still queued when an entry is dropped mid-flight.

`nextBackendID` numbers what reaches the server; `track` records the way back and hands the caller the
oldest request when a session crosses `DefaultMaxPendingRequests`, so the bound is a lookup and the
evicted request gets its error in the same step. `workspaceState.decideInitialize` keeps one `initialize`
per workspace and replays the cached result, which is what `$/lspReady` means to the browser.
`routeServerMessage` splits answers from notifications: `routeResponse` remaps the id and delivers to one
session, `routeNotification` fans out to every session using that workspace.

The browser side owns the wait. `lsp-connection-manager.ts` multiplexes every editor tab over one socket
and owns the channel bookkeeping; `lsp-hover-actions.ts` decides what a person sees while a request is
out. `armWaitClocks` starts the two clocks, `waits` holds the outstanding pair so abandoning a card sends
`$/cancelRequest` for it, and the caches are per-view and keyed by document offsets, which is why any edit
clears them.

## Checking this

`go test ./backend/lsp/` measures the ordering, the exactly-once answering, the cancel's id and the
bounds. `cd frontend && npx vitest run src/lib/utils/__tests__/lsp-hover-actions.unit.ts` measures the
card's clocks and the memo, and `cd frontend && npx playwright test editor-hover-definition` measures the
path with a real browser and a scripted server. `make check-race` covers the writer goroutines, which a
package's own green run does not.
