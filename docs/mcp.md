# MCP: calling a server somebody registered

The rules are in `AGENTS.md → MCP`. This file holds the protocol surface, the bounds, and the one rule
(`elicitation`) whose shape is not obvious from the code. The address rule is shared with contributed tools
and lives in `docs/confinement.md`; the sandbox warning a project sees is `docs/confinement.md` too.

`backend/mcp` owns the client side of the Model Context Protocol: dial one registered Streamable-HTTP
server, read the tools it lists, run one of them, and say in one kind what went wrong. It does not own the
registry (that is `backend/config`), the project's opt-in (that is `backend/store`), or the offer a model
sees (that is `backend/exec`). It holds nothing between a call and the next one.

## What must stay true

**One protocol era, and MIKE is what enforces it.** `Era` is the only version this speaks, and a server
answering another is refused by name rather than negotiated down to. Pinning
`ClientSessionOptions.ProtocolVersion` does not do it: the SDK falls back to the legacy `initialize`
handshake whenever `server/discover` fails for any reason, so a pinned version chooses only the first
attempt. `connect` asserts the negotiated version afterwards, and that assertion is the rule.

**Streamable HTTP, and nothing else.** No stdio means no subprocess family, no container question, and no
environment to leak. `DisableStandaloneSSE` is on because the long-lived GET stream is how the older era
pushed at a client, and a session that lives for one call cannot answer a push.

**Tools only, and the decline is said out loud.** No resources, no prompts, no sampling, no elicitation,
no roots. Passing an explicitly empty `ClientCapabilities` is the decline: the SDK adds a `roots`
declaration to every client unless the options say otherwise, so "set no handler" is not a decline.

**A project opts in, and the offer and the lookup are one path.** `toolsForProject` is the only reader of
the cache for both questions. A tool name the offer withheld — because the project stopped enabling that
server, or because its listing failed — is `Unknown tool` at dispatch too, never a call.

**The offered name is `mcp_<slug of server>_<slug of tool>`, and the prefix is reserved.**
`backend/extensions/tool_manifest.go` refuses a contributed tool that starts with `mcp_`, so an extension
cannot shadow an MCP tool, and dispatch routes on the prefix instead of trying both registries. A name that
collides with a core tool, or with another enabled server's tool, is reported and dropped, not renamed.

**The listing is a cache, and its staleness is the price of not dialling on every turn.** One entry per
server, keyed against the row's shape and reused for `refreshInterval`. There is no invalidation hook: a row
whose shape changed, or one that never had an entry, is re-listed on the next turn, and the Test action
stores what it just dialled. A failed listing keeps no tools, so the server offers nothing and is not
dialled again until the entry goes stale — one dial per interval for a dead server. The `Warnf` naming that
server is the report, and the Test action answers afresh on request. A verdict kept in a cache beside a
button that produces a fresh one is two answers to one question.

**One attempt per call.** `MaxRetries: -1`: a retry would run the tool twice, and a tool is not idempotent.

**Only a context deadline bounds a wait.** The SDK's `AsyncCall.Await` waits on the context, so a server
answering with some other request's JSON-RPC id is waited out by nothing else — an `http.Client` timeout
stops a request, not a wait. The `session` owns one context for its whole life, released by `close`: the
server's deadline when the row set one, the caller's cancellation always. A timeout of 0 sets no deadline, so
a call runs until it answers or the task that called it is stopped — which is what a long-running remote tool
needs. A turn's listing gets its own shorter budget either way, because a slow listing is no reason to hold a
turn.

**Every byte is bounded before it is read.** One server-sent event is capped at the
`httputil.MaxUpstreamBodyBytes` number and past that is its own kind; a tool's answer is capped at
`toolresult.MaxCaptureBytes` where it is captured, and the cap says so in the text.

**The address a person typed is the grant, and it is the only widening.** Requests go out through the
egress client, which admits loopback for that host alone and still refuses the metadata address. A
redirect is not followed, so one registered URL cannot become a proxy into the local network. The server's
headers ride that same client, because the SDK's transport exposes no per-request hook: a stored token is
attached by `httputil.WithHeaders` or it never reaches the wire.

**A failure carries a kind, and the text a person reads is MIKE's.** `unreachable`, `not_mcp`,
`wrong_era`, `refused`, `timeout`, `too_big`, `tool_error`. The transport's words stay in `Error.Cause` for
the log; a tool's own failure sentence is the exception, because it is the answer and the model is the
reader that can act on it.

**No secret leaves the process that stores it.** The config view answers a header value as the masked
placeholder, and `OpenMCPServers` is the one place it is opened. The Test action resolves a placeholder back
to the stored value server-side, so a saved token is exercised without the browser holding one.

**The registry is configuration, and the operator can freeze it.** `MIKE_RESTRICT_MCP=1` resolves once in
`backend/startup` and reaches `backend/config` as a value: `refuseMCPChange` compares what a save would write
against the stored row and refuses any difference, so a re-pointed URL or a retyped token is refused exactly
as a new server is. The Test probe is refused before it dials, because a probe reaches an address the browser
typed. `ErrMCPLocked` is the sentence a person reads and it names the variable.

**The freeze never touches the offer.** A frozen server is still listed, offered and called, and a project
still enables or disables it per project — that write is on the project row, not the registry row. Settings
reads `mcp_restricted` from the config response and shows the tab read-only.

## Elicitation is declined, not bridged

The pinned era forbids the request. A server cannot send `elicitation/create` while serving a `tools/call`:
the request would outlive the response that carries it. The SDK's server refuses it and names the shape the
era wants instead — answer with an input-request result and take the reply as an ordinary argument on the
next call (SEP-2322). `TestCallElicitationCannotInterruptAToolCall` pins that, because the server's refusal is
what the model reads, and the model is then free to call `ask_question` and re-invoke with the answer.

MIKE declines every channel that is left. No `ElicitationHandler` is registered, so a request that arrived by
some other route is answered at once with `client does not support elicitation`. `DisableStandaloneSSE` is on,
so there is no long-lived stream to push one down, and the empty `ClientCapabilities` says on the wire that
there is nobody to ask. URL mode gets that same refusal before its URL is even read: URL mode exists to hand a
browser a link, and MIKE has no browser to hand it to.

The reason MIKE never bridges it is its turn model. `ask_question` **ends the turn**: the loop persists the
question, moves the task to `paused`, and the answer arrives later as a new user message that starts a new
turn. An
elicitation must be answered **inside the live `tools/call`**, on that request's response stream while the
server's handler is blocked. A human answer arriving minutes later cannot be delivered onto a socket the
server and any proxy between have timed out. The stateless era removed sessions to discourage exactly this,
telling a server that must survive a wait to mint its own handle and take it as an ordinary tool argument —
which is what the refusal leaves the model to do.

Bridging the two would also mean choosing which turn owns a human answer while a tool call is open, and that
choice has no good answer here. The task may be stopped, the message may arrive during another tool, and
the call would have to hold the process open while a person reads.

## How it is held

- `backend/mcp/client.go` — `connect`, where the dial, the era assertion and the session's deadline are one
  function; `classify`, which turns every transport fault into one `Failure`; `session.listTools`, bounded,
  with the dropped count returned; and `session.call`. The empty `ClientCapabilities` is the decline said on
  the wire; the absent `ElicitationHandler` is what answers a server that asks anyway.
- `backend/mcp/registry.go` — the cache, `toolsForProject` as the single offer-and-lookup path, and
  `offeredName`. `refreshAll` reports a failed listing at `Warn` and stores nothing about it. An entry is
  dropped as soon as no registered row is behind it.
- `backend/mcp/probe.go` — the Test action's shape and its refusal of an undiallable form, through the same
  `types.ValidateMCPServer` a save runs.
- `backend/server/handler_mcp.go` — `POST /api/mcp/test`. A verdict is a 200 with a kind; only a body there
  is no point dialling is a 4xx naming the field. It writes nothing, and it is not in `tokenScope`. Under
  `MIKE_RESTRICT_MCP` it answers 403 before dialling.
- `backend/config/mcp.go` and `backend/types/mcp.go` — the stored registry, the sealed header values, the
  bounds and the row fence both callers share. `refuseMCPChange` and `ErrMCPLocked` are the freeze; the
  decision is a constructor value, never a variable read here. A project's own list is the `mcp_server_ids`
  column.
- `backend/exec` — the offer beside the contributed tools in every operating mode, and the prefix routing in
  `toolRunner.run` that decides which registry owns a name.
- `frontend/src/lib/components/ConfigModal/MCPTab.svelte` and `mcp-drafts.ts` — the registry form, which
  refuses the shapes the save refuses; `frontend/src/lib/utils/server-tool-network.ts` owns the sentence the
  Sandbox Configuration shows, because an MCP call runs in this process and the task container's network
  settings say nothing about it.

## Checking this

`go test ./backend/mcp/` — the era refusal with both `server/discover` and `initialize` proven on the wire,
the capability decline read off the wire, the listing bound, the capped answer, the waited-out server, the
address rule and its loopback grant, and the Test action's verdicts. It also measures the elicitation a tool
call cannot make, and the one report a failed listing leaves: a `Warn` naming that server. Run it under
`-race` with the rest of the lane named in `docs/testing.md`.

`go test ./backend/config/ -run Restricted` measures the freeze: the round trip that still saves, every edit
that is refused by name, and the row a refused save leaves alone. `go test ./backend/server/ -run Restricted`
adds the probe that refused before dialling and the config view that still lists the frozen server.
