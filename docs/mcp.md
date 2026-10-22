# MCP: Model Context Protocol

This module implements the client side of the Model Context Protocol (MCP). It handles dialing registered servers, listing tools, executing them, and reporting errors.

The address rules and sandbox warnings for MCP servers are detailed in `docs/confinement.md`.

## Protocol and Network Rules

- **Era Pinning**: MIKE supports only one protocol era. Servers using different versions are refused by name rather than negotiated down.
- **Transport**: Uses Streamable-HTTP only. Stdio is not supported to avoid subprocess management and environment leakage. Standalone SSE is disabled.
- **Capabilities**: Only "tools" are supported. All other MCP capabilities (resources, prompts, sampling, elicitation) are explicitly declined via `ClientCapabilities`.
- **Security**:
  - **Egress**: Requests use an egress client that only allows loopback for the specific host.
  - **No Redirects**: Redirects are not followed to prevent proxying into the local network.
  - **Headers**: A stored token is attached to a request only for the host the row was registered
    with, and it is never echoed in a response body. The Test action resolves a masked header from the
    registry rather than the browser, so the row's id and the address are two separate pieces of request
    text: `probeTarget` sends the saved value only when the typed address names the same host
    (`types.SameHost`) as the saved one, and says in its answer when it withheld one.
- **Resource Bounds**: All incoming data is capped (e.g., `httputil.MaxUpstreamBodyBytes` for SSE and `toolresult.MaxCaptureBytes` for tool results).
- **Wait Times**: Only a context deadline bounds a wait. A timeout of `0` means a call runs until it answers or the task is stopped.

## Routing and Registry

- **Project Opt-in**: Projects opt into specific MCP servers. The offer (to the model) and the lookup (at dispatch) share a single path (`toolsForProject`) to ensure consistency.
- **Naming**: MCP tools are prefixed as `mcp_<server_slug>_<tool_slug>`. This prefix is reserved to prevent shadowing contributed tools. Colliding names are reported and dropped.
- **Caching**: Tool listings are cached per server and refreshed based on `refreshInterval`. Failed listings are not cached, meaning a dead server is dialed once per interval.
- **Registry Freeze**: If `MIKE_RESTRICT_MCP=1`, changes to the server registry (URLs, tokens) are refused. However, project-level enablement and disablement still work.

## Elicitation (Why it is not bridged)

MCP elicitation allows a server to request human input *during* a tool call. MIKE explicitly declines this.

**The Turn Model Conflict**:
In MIKE, `ask_question` ends the current turn. The system persists the question, pauses the task, and starts a new turn only after the user answers. 

Bridging MCP elicitation would require:
1. Holding the tool's request and the server's handler open while waiting for a human.
2. Keeping the process active and the socket open for minutes or hours.
3. Managing turn ownership for a response that arrives while another tool may be running.

Because the MCP era removed sessions to discourage long-lived waits, the correct approach is to refuse elicitation. The model is then free to use `ask_question` to get the needed information and re-invoke the tool with the answer as a standard argument.

## Implementation Details

- **`backend/mcp/client.go`**: Manages connection, era assertion, and tool execution.
- **`backend/mcp/registry.go`**: Handles tool listing, caching, and naming.
- **`backend/mcp/probe.go`**: Implements the "Test" action for registered servers.
- **`backend/server/handler_mcp.go`**: Provides the `/api/mcp/test` endpoint.
- **`backend/config/mcp.go`**: Manages stored registry data and the freeze logic.
- **`backend/exec`**: Integrates MCP tools into the general tool runner and handles prefix routing.

## Verification

- `go test ./backend/mcp/`: Verifies era refusal, capability decline, listing bounds, and egress rules.
- `go test ./backend/config/ -run Restricted`: Verifies the registry freeze and configuration round-trips.
- `go test ./backend/server/ -run Restricted`: Verifies the Test probe behavior under a freeze.
