# Another agent (`agent`)

An `agent` extension makes MIKE drive a whole coding agent that is not the built-in one — its own
process, its own protocol, its own tool loop. MIKE's backend knows nothing about agent protocols: an
adapter translates between MIKE's events and the agent's wire format, and it runs as **WASM**.

## What this costs

- A **WASM adapter module**: Go compiled with `GOOS=wasip1 GOARCH=wasm`, exporting the adapter's
  functions. This is the only extension type that ships a binary artifact, and it is the only one
  whose correctness is invisible until a real turn runs.
- A container and usually an image: the agent process runs in the task's sandbox, so it must exist in
  the image (`image` in the manifest, or a `Dockerfile` here).
- The adapter is a **separate Go module** with its own `go.mod` and a `build.sh`. The published
  adapters are the reference; their tests live with them, not in MIKE's test suite.

## What you declare

`capabilities.agent.wasm_module` names the module relative to this folder. `agent.description` and
`agent.setup_info` are the markdown the provider card shows. `agent.config_schema` holds the
options the person sets, and `agent.sandbox` declares the default mounts and environment — checked
against the same policy as every other sandbox declaration, with borrowed roots read-only whatever
the manifest asks.

## Whether to write one at all

An adapter is the largest thing a person can ask an agent to write here, and a half-written one
fails as "the agent never answered" rather than as an error. If the description is really "I want a
different model", that is an `llm` extension or a provider setting, not an adapter. Say so.

If it is genuinely an adapter, do not fake the module: no stub WASM will load, and a module that
will not load is a hard failure at the first turn. Tell the person what this folder still needs —
the adapter's Go module, its build script, the agent's image — and stop with the manifest, the
declaration and the instructions for building it.
