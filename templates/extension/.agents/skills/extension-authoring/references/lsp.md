# A language server (`lsp`)

An `lsp` extension makes MIKE speak one language properly: completion, hover, diagnostics,
go-to-definition, and the same tools for the agent. The extension does not implement any of that. It
names a program that does and MIKE runs it in a container.

## What you owe

- **The program to start**: `command`, the executable inside the image, and `args` if it takes any.
- **`file_types`**, at least one, each with `extension` (a bare lowercase suffix, no dot),
  `language_id` (what the server is told the document is — `typescriptreact` for `.tsx`, not
  `typescript`) and `label` for the editor's status bar.
- **`language`** — the fallback language id for a file no `file_types` entry claims.
- Optionally **`syntax`**: highlighting as data. MIKE holds no language knowledge of its own, so an
  undeclared profile means plain text in the editor.
- Optionally **`initParamsOverride`**: an object merged into the `initialize` parameters sent to the
  server.

## How the container gets its image

A development row runs the image its row names, and a press does not change that: the manifest's
`image` is read at install, not at Try Extension. So what runs here is a **`Dockerfile` committed at
this root** — that is what the sandbox finds and builds, by the repository's directory name. A
Dockerfile that exists only in the task's worktree is not found, and a manifest naming only an
`image` starts nothing: opening a file of the declared type answers that the extension has no
container image and no Dockerfile to build one from.

Declare `image` for whoever installs the folder later: an install accepts a fully qualified reference
only (`docker.io/library/node:22-bookworm-slim` — `node:22` is refused), and empty means build the
Dockerfile. A build always has a network, so the Dockerfile may install from the internet; the
agent's own tasks do not.

```json
"image": "docker.io/library/node:22-bookworm-slim",
"capabilities": {
  "lsp": {
    "command": "typescript-language-server",
    "args": ["--stdio"],
    "language": "typescript",
    "file_types": [
      { "extension": "ts", "language_id": "typescript", "label": "TypeScript", "syntax": "ts" },
      { "extension": "tsx", "language_id": "typescriptreact", "label": "TypeScript JSX", "syntax": "ts" }
    ],
    "syntax": { "ts": { "line_comment": "//", "block_comment": { "open": "/*", "close": "*/" },
                        "strings": [ { "quote": "\"", "escapes": true } ],
                        "pairs": [ { "open": "{", "close": "}" } ], "numbers": true } }
  }
}
```

## `syntax` — highlighting as data

Each named profile is what the file type's `syntax` names. A profile that declares nothing to
tokenize is refused.

| Field | Meaning |
|---|---|
| `line_comment` | The usual line marker, `//`. |
| `line_comments` | Extra line markers where a language has more than one (PHP: `//` and `#`). |
| `block_comment` | An `open`/`close` pair. |
| `strings` | One entry per string form: `quote` (1–3 characters, one character repeated), `escapes` (honours backslashes), `multiline` (may span lines). |
| `pairs` | The brackets, each an `open`/`close` pair of single characters, and the input to the generic fold provider. |
| `keywords`, `types`, `constants`, `builtins` | Word lists, matched whole — no spaces inside an entry. |
| `numbers` | Colour numeric literals. |
| `region` | An `open`/`close` pair marking foldable regions (`//#region`). |
| `capitalized_types` | Colour a name whose first letter is upper-case as a type. Defaults to true; say `false` to stop `ChatMessage` reading as a type. |
| `call_as_function` | Colour a name applied to arguments (`name(`) as a function. Defaults to true. |

## The three switches

`editor_enabled`, `tool_calls_enabled` and `chat_diagnostics_enabled` are three separate answers: the
editor's features (default true, applied by the browser), the agent's LSP tools (default true), and
the pass that hands a turn's edited files to the server (**default false** — ask for it, or nothing
is sent). All three are read from the stored row, so Settings can turn one off without touching the
others.

## `sandbox`

What the container gets beyond the workspace, checked against the same policy an installed extension
is held to — borrowed roots are read-only whatever the manifest asks, and an unusable declaration is
refused at load rather than skipped at every start.

| Field | Meaning |
|---|---|
| `network_isolation` | Whether the container is cut off from the network. Defaults to true. |
| `memory_limit` | MiB for the container. 0 takes the default; a negative value asks for no bound. |
| `mounts` | Each with `host_path`, `container_path` and `mode` (`rw` or `ro`) — the app's own roots only, and read-only where the host owns the path. |
| `envs` | Each with `key` and `value`, set in the container. |
| `forward_ports` | Each with `container_port`, optionally bound to a `host_ip`; the host port is assigned at start. |
| `capabilities` | Podman capabilities, named by the policy in `docs/sandbox-runtime.md`. There is no grant to ask for with them: `--privileged` is an argument, not a capability, and it is refused. |
| `reload_files` | Workspace-relative paths whose change means the server has to re-read the project (`go.mod`, a lockfile). A server sees no file-watch events through a bind mount. An absolute path, `..` and a backslash separator are refused. |
| `dependency_cache` | Where the server reads resolved dependencies from inside its own container: `container_path` plus `env`, a variable **name** (never an assignment). The host directory behind it is not yours to name — see below. |
| `environment` | A health gate: `check` (a `command` and its `args`, run in the container), `suppress_codes` (opaque diagnostic codes that only mean "I could not load the project") and `note` (one sentence shown instead of that noise). |
| `dependency_host_path` | **Never in a manifest.** It is the directory a person grants under Settings → Extensions, and a manifest carrying it is refused. |

## The check nobody can fake

There is no health verdict to read for an LSP extension — an `environment` check decides nothing.
The only proof a language server works is that it answered: open a file of the declared type in the
editor and ask it something. Say in your final message that you could not run it, if you could not.
