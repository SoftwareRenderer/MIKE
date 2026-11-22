# Another agent (`agent`)

An `agent` extension makes MIKE drive a whole coding agent that is not the built-in one — its own
process, its own protocol, its own tool loop. MIKE's backend knows nothing about agent protocols: an
adapter translates between MIKE's events and the agent's wire format, and it runs as **WASM**.

## What this costs

- A **WASM adapter module**: Go compiled with `GOOS=wasip1 GOARCH=wasm`, exporting the adapter's
  functions. This is the only extension type that ships a binary artifact, and it is the only one
  whose correctness is invisible until a real turn runs.
- A container and an image: the agent process runs in the task's sandbox, so it must exist there. As
  for `lsp`, that image comes from a **`Dockerfile` committed at this root**; the manifest's `image`
  is read at install time only.
- The adapter is a **separate Go module** with its own `go.mod` and a `build.sh`. The published
  adapters are the reference; their tests live with them, not in MIKE's test suite.

## What you declare

| Field | Meaning |
|---|---|
| `wasm_module` | The adapter module, relative to this folder. Nothing checks the bytes at load; a module that will not load fails the first turn. |
| `description` | The markdown the provider card shows. |
| `setup_info` | The setup instructions the provider card shows, newline-separated items. |
| `config_schema` | The options the person sets, in the same shape as the manifest's own — see `manifest.md`. |
| `sandbox` | The container's defaults: `mount` and `env`. |

`sandbox.mount` entries carry `host_path`, `container_path` and `mode`; `sandbox.env` entries carry
`key` and `value`. Both are usually declared as `mount` and `env` `config_schema` properties with an
array `default`, so the person sees a folder list and a variable list and can change them. The host
owns those two keys: `sandbox_mount` is saved by its own endpoint (Settings → Extensions) and the
edit form leaves it out of what it sends back.

A `sandbox` value may be filled from such an array's first entry with
`{{<property_key>_<field>[]}}` — `{{sandbox_mount_host_path[]}}` reads `host_path` out of
`sandbox_mount`'s `default[0]`. Templates resolve in the copy that runs; the saved record keeps them.

Declarations are checked against the same policy as every other sandbox block, and a folder on this
machine is asked of the person before the version runs — `Try Extension` answers with the dialog
naming each `host_path` and lets them grant or decline it.

## Whether to write one at all

An adapter is the largest thing a person can ask an agent to write here, and a half-written one
fails as "the agent never answered" rather than as an error. If the description is really "I want a
different model", that is an `llm` extension or a provider setting, not an adapter. Say so.

If it is genuinely an adapter, do not fake the module: no stub WASM will load, and a module that
will not load is a hard failure at the first turn. Tell the person what this folder still needs —
the adapter's Go module, its build script, the agent's image — and stop with the manifest, the
declaration and the instructions for building it.
