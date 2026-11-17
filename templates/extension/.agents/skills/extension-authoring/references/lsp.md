# A language server (`lsp`)

An `lsp` extension makes MIKE speak one language properly: completion, hover, diagnostics,
go-to-definition, and the same tools for the agent. The extension does not implement any of that. It
names a program that does and MIKE runs it in a container.

## What you owe

- **A way to run the server.** Either an `image` in the manifest (a full registry reference,
  `docker.io/library/…` — a short name is refused) or a `Dockerfile` in this folder that installs the
  language server. An `lsp` extension with neither is refused: the sandbox would have nothing to
  start. A build always has a network, so the Dockerfile may install from the internet; the agent's
  own tasks do not.
- **`capabilities.lsp.command`** — the program to start inside that image, and `args` if it needs any.
- **`capabilities.lsp.file_types`**, at least one, each with `extension` (a bare lowercase suffix, no
  dot), `language_id` (what the server is told the document is — `typescriptreact` for `.tsx`, not
  `typescript`) and `label` for the editor's status bar.
- **`capabilities.lsp.language`** — the fallback language id.
- Optionally **`capabilities.lsp.syntax`**: highlighting as data (`line_comment`, `block_comment`,
  `strings`, `pairs`, `keywords`, `types`, …). MIKE holds no language knowledge of its own, so an
  undeclared profile means plain text in the editor.

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

## The three switches

`editor_enabled`, `tool_calls_enabled` and `chat_diagnostics_enabled` are three separate answers:
the editor's features, the agent's LSP tools, and the pass that hands a turn's edited files to the
server. All three are read from the stored row, and the first is applied by the browser only.

## What you may not declare

`capabilities.lsp.sandbox.dependency_host_path` is a grant the person applies under
Settings → Extensions; a manifest that names a host directory is refused. `sandbox.mounts` and extra
capabilities are checked against the same policy an installed extension is, and borrowed roots are
read-only whatever the manifest asks.

## The check nobody can fake

There is no health verdict to read for an LSP extension — an `environment` check decides nothing.
The only proof a language server works is that it answered: open a file of the declared type in the
editor and ask it something. Say in your final message that you could not run it, if you could not.
