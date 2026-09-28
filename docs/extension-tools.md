# Contributed tools: an extension that gives the agent a tool

The rules are in `AGENTS.md → Extensions`. This file holds the declaration grammar, where the permission
comes from, and what core may never learn. Egress mechanics stay in `docs/extension-lifecycle.md`; the
address rule lives in `docs/confinement.md` and `backend/httputil/client.go`.

A contributed tool is **data**. The extension's manifest names the tool, describes it to the model, says
what arguments it takes, templates its request and says which fields of the answer to show. Core runs
that declaration and holds nothing about the upstream: there is no `web_search` in the tree, no provider
name in `backend/`, and no language or endpoint knowledge to keep in step with somebody else's API.

## What the declaration looks like

`capabilities.tools` beside `capabilities.network`; the extension's type is inferred as `tool`
(`inferExtensionType`), and a manifest pairing tools with another executing capability (`lsp`, `ui`,
`css`, `agent`, `llm`, `i18n`) is refused at install — the row is stored under one type and the other's
lookup would miss it.

```jsonc
"capabilities": {
  "network": { "allowed_domains": [], "headers": { "Authorization": "Bearer {{auth_token}}" } },
  "tools": [{
    "name": "web_search", "display_name": "Search",
    "description": "Search the web …",
    "parameters": { "type": "object", "properties": { "query": { "type": "string" } }, "required": ["query"] },
    "request":  { "method": "GET", "url": "{{config.base_url}}/search",
                  "query": { "format": "json", "q": "{{args.query}}" }, "timeout_seconds": 15 },
    "response": { "results": "results", "total": "number_of_results",
                  "fields": [ { "label": "title", "key": "title" }, { "label": "url", "key": "url" } ],
                  "max_results": 8, "max_snippet_chars": 400 }
  }]
}
```

`backend/extensions/tool_manifest.go` refuses anything it cannot execute — where the manifest is stored,
never worked around at call time: name shape and core-name collision, `parameters.type == "object"`,
method in {GET, POST}, every placeholder naming something declared, `max_results` and snippet length
within bounds, at most `MaxToolsPerExtension` tools (every definition rides on every request to the
provider). `Extension.UsableContributedTools()` re-applies the same rules when a **stored** row is read,
so a row that predates a rule loses that tool and reports it by name rather than sending an unchecked
template out of the process.

## The grammar

`backend/extensions/tool_request.go` is the only reader of the request templates, and
`ToolResponse.RenderAnswer` the only reader of the answer shape.

- `{{config.k}}` is the extension's configuration value, `{{args.p}}` one argument of the call, and
  `{{args.p|default}}` the same with a fallback. A placeholder that resolves to nothing and has no
  default **drops the query entry** rather than sending it empty — an argument the model left out is not
  a request for an empty value.
- `request.url` must **begin** with `{{config.<key>}}`, bare, and the rest is a path: no `?`, `#`,
  whitespace, `@` or `//` after it. Parameters belong in `request.query`, and a secret belongs in a
  header template.
- `request.query` is filled for both verbs: a GET has it in the URL, a POST sends it as an
  `application/x-www-form-urlencoded` body. The model never supplies a body.
- `response.results` is a dotted path to the array of items (`meta.items.0` reaches a nested answer);
  `response.fields` are rendered in declaration order, each label once, the first on the item's own line.
  `response.total` names the upstream's own count for the header line.
- The answer is plain text. It is somebody else's page text arriving in the model's context, so nothing
  renders it as HTML anywhere, and `toolresult.MaxCaptureBytes` bounds the whole answer after the
  declared per-item caps.

## The address is the grant

A `config_schema` property of `type: "url"` is a **network target a person typed**. That is what makes it
the permission rather than a string:

- The origin a contributed tool may contact is the origin stored in the `url` property its `request.url`
  names (`validateURLTemplate`), so **every host a contributed tool can reach is a host a person saved**.
  A manifest cannot write its own host, and a `url` property may declare no `default` — registry text
  that arrives in a field and is written back by the next Save would be the manifest choosing the peer.
- `Extension.GrantedHosts()` is that set, and it is allowed beside the manifest's `allowed_domains` in
  `allowHost`. `allowed_domains` may stay empty: a self-hosted instance lives wherever the person put it.
- The grant is also the only thing that widens the address rule, and only for **loopback** (127.0.0.0/8,
  `::1`) when the dial hostname equals a granted host — `httputil.NewExtensionEgressClientGranting`.
  Link-local (the cloud metadata address), the unspecified address and every other non-public range stay
  refused *even when named*: "allow localhost" does not widen into "allow the internal network". With no
  grants the granting constructor returns the strict client unchanged, so every other extension's egress
  is byte-for-byte what it was.
- `POST /api/extensions/{id}/proxy` passes **no grants**: its URL is chosen by extension script, not by a
  person under Settings → Extensions, so it keeps the strict client.

## Offering and dispatch

`extensions.ToolRegistry` answers from the stored rows on every question — enabling an extension, editing
its address or installing another one all change the answer, and a cached one shows a tool that is gone or
hides one that exists. A tool is offered only when a call could succeed: the extension enabled, the
declaration still valid, and its `url` property filled. The debug log (`component=extensions`) carries each
refusal reason — the difference between an absent tool and one a person can fix.

- Appended in `toolDefinitionsFor` for plan, explore and act alike, through `Deps.ExtensionTools`;
  `llama.ContributedToolDefinitions` turns `types.ToolSpec` into the provider's definition. `types` is
  the floor `extensions` and `llama` meet on so neither imports the other.
- A contributed tool never takes a core tool's name (`types.CoreToolNames` is the only spelling of that
  list, and `TestToolSetsUseOnlyDeclaredNames` plus `TestEveryDeclaredToolIsDispatched` pin the offered and
  dispatched names to it), and a name already contributed by another enabled extension is reported, not
  silently dropped.
- Dispatch: `toolRunner.run` falls through to `runContributedTool` for a name no core handler claims. A
  tool the registry withheld answers in the existing `Unknown tool: <name>` vocabulary.
- Display names: `types.ToolDef.DisplayName` is `json:"-"`, so the card label comes from an
  executor-level resolver (`TaskExecutor.toolDisplayName`) rather than the static core table.
- Failure sentences are provider-neutral and name what a person can correct — HTML instead of JSON, the
  rate limiter, a missing auth token. `extensions.FailureHint` is the one home of that reading, shared by
  the tool answer and the probe. The cause goes to the log (`component=tools`, `source=<tool name>`).

## It runs in the server process

Both halves — the offer and the call — live server-side, so a sandboxed and an unsandboxed project get the
same tool, and **a project's Network Isolation says nothing about it**: isolation bounds the task's
container and this request never enters it. `configResponse.server_network_extensions`
(`ToolRegistry.NetworkedExtensions`, derived and never stored) is what lets the Sandbox Configuration say
that out loud when isolation is on; `frontend/src/lib/utils/server-tool-network.ts` is the rule and
`SandboxConfigModal/NetworkRow.svelte` / `SandboxPanel.svelte` are the two places it is said.

## Test connection

`POST /api/extensions/{id}/test-url` sends the **declaration's own request** to the address in the body,
with the form's current values overlaid on the stored configuration — so a typed-but-unsaved address and
an unsaved token are both exercised. It writes nothing: the answer belongs to the open dialog, and a
stored probe is a setting nobody saved and a stale one besides. It is classified by failure rather than
by HTTP syntax (`unreachable`, `not_json`, `rate_limited`, `refused`, `unexpected_shape`, `empty`,
`reachable`) because each class has a different fix in a different program. A `url` property that no
contributed tool reads is refused: the probe is a dialer with a person's authority behind it, so it dials
only the addresses the extension's own tools would send to. Tests: `tool_probe_test.go`.

## What core may never learn

No provider name, no endpoint knowledge, no field names of an upstream answer, no remediation text that
names another program's configuration keys. The instance-side fixes (a search endpoint that has to be
told to serve JSON, a limiter to turn off, a bind address that a container's loopback cannot reach) belong
to the extension's own README, which is where a person who installed it will look for them.

The declaration surface can grow `"impl": "wasm"` later — a module under the existing wazero pattern with
a `fetch` host import — without changing the tool contract or the dispatch path.

## Tests

`tool_manifest_test.go` (the reference declaration installs and survives a JSON round trip; malformed
declarations refused by field name), `tool_registry_test.go` (offering gated on enabled + configured,
name collisions reported), `egress_test.go` and `backend/httputil/client_test.go` (grant, allowlist,
loopback admitted only for the named host, the metadata address refused even when named), `tool_probe_test.go`
(typed address dialled rather than the stored one, unsaved token reaching the header, nothing stored, each
failure class its own sentence), `backend/exec/tool_extension_offer_test.go` and
`tool_extension_run_test.go` (the offer in all three modes; twelve calls over the real dispatch path with
an injected egress seam, so no test needs a socket).
## Checking this

`go test ./backend/extensions/ ./backend/exec/` covers the tool manifest grammar and the run behind it,
which is where a `url` grant is honoured or refused.
