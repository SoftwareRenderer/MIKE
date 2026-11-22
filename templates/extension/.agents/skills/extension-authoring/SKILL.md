---
name: extension-authoring
description: Decide what kind of MIKE extension to build and write it. Use when asked for an extension, a plugin, a new tool for the agent, a new editor or board feature, a colour theme, a language, or a provider adapter. Chooses the type from what the person asked for, then writes the manifest and the files that manifest names.
---

# Authoring a MIKE extension

A MIKE extension is a folder with a `manifest.json` at its root and the files that manifest names.
There is no build step, no package manager and no framework: MIKE reads the folder, validates the
manifest, and runs whatever it declares. **Your first job is to choose the type — the person never
chooses it, you do.** The type decides whether there is a container, an image, a network grant, and
which files you have to write.

## The seven types, in the person's terms

| They asked for | Type | What it costs |
|---|---|---|
| A button, a panel, a badge, an extra view in the editor / card / sidebar / chat | `ui` | nothing — plain JS in a sandboxed frame |
| A colour scheme, bigger fonts, "make it look like X" | `css` | nothing — one stylesheet |
| Translate the interface, "MIKE in French" | `i18n` | nothing — a JSON catalog, optionally translated prompts |
| Let the agent ask some external service something (search, tickets, a package registry) | `tool` | one network grant; runs in the server process |
| Real code intelligence for a language: completions, diagnostics, go-to-definition | `lsp` | a container, and a committed Dockerfile to build it |
| Talk to a model provider MIKE does not speak (its request/response dialect) | `llm` | a mappings file, no container |
| Drive a whole agent that is not the built-in one | `agent` | a WASM adapter, a container and a committed Dockerfile |

Pick the cheapest type that does the job. Most requests are `ui`: if the person can see it and click
it, it is a frame with a script in it.

**Declare one executing capability** — `ui`, `css`, `i18n`, `tools`, `lsp`, `llm` or `agent`. Only two
pairings are refused: `tools` beside another executing capability, and `i18n` beside anything at all.
Any other pair loads and then half-runs, because the row is stored under one type and the other
type's reader never sees it — a manifest with `ui` and `css` shows the frame and never the
stylesheet. **A description that fits two types is therefore two extensions**: write both folders.
If the ask is genuinely two things, say so; if it is one thing that looks like two, the cheap one
usually wins.

Read `references/manifest.md` before writing a manifest. Read `references/lsp.md`,
`references/agent.md` or `references/llm.md` when you picked one of those three — none of them has an
example to copy.

## The loop

1. Decide the type. Say which and why, in the person's terms, in your final message.
2. Copy the closest example: `examples/ui/`, `examples/css/`, `examples/i18n/`, `examples/tool/`.
   Each is a complete extension that passes MIKE's own validation.
3. Write `manifest.json` at the repository root, beside the files it names.
4. Name the entry files the way the manifest names them — `references/manifest.md` says what a `ui`
   entry page is.
5. Check your own work against the list under "What the load will not catch". A press that reports no
   error is not proof the extension does anything.
6. Stop. Once the task is in Review the person presses **Try Extension** on it; MIKE validates the
   manifest, reports, and reloads the tab when the version loads. If the message names a manifest
   field, fix that field and press it again. A version the person has not approved yet comes back as
   a question first — say in your final message what it asks for, so the question is expected.

## What the load refuses

These come back from **Try Extension** as a sentence naming the field; relay them and fix that field.

- A sandbox declaration the container cannot honour: a mount outside the app's own roots, or a
  capability past the policy.
- `lsp`: no `file_types`, a suffix with a dot or a slash or upper case, a duplicated suffix, an empty
  or upper-case `language_id`, a `syntax` profile with nothing to tokenize or a malformed
  delimiter, a `dependency_cache` whose `env` is not a variable name or whose path covers the
  server's own state, `reload_files` reaching outside the workspace, an `environment` gate with no
  command, or a `dependency_host_path` in the manifest at all.
- `i18n`: a pack declaring anything else, a `locale` that is not one bare lowercase subtag
  (`fr`, not `fr-FR`), a catalog that is not a JSON object of translated strings or valid plural
  tables, a `prompts_dir` that is not a shipped directory, holds no `.tmpl`, or holds one that does
  not parse.
- A declared path that escapes the directory, or carries a scheme, where a file was asked for.
- `tools`: declared beside another executing capability, or with no `capabilities.network`; more
  than 8 of them; a `name` that is malformed, duplicated, a core tool's, or `mcp_`-prefixed; a
  `request.url` that does not begin with `{{config.<key>}}` naming a `config_schema` property of
  type `"url"`; a `url` property with a pre-filled `default`; a template naming a parameter nobody
  declared.
- A version asking for the network, `network.proxy`, `preview.serveScripts`, `preview.serveNetwork`,
  or a folder on this machine — refused with the consent dialog until the person approves it.

## What the load will not catch

These load, and the row says it loaded. Check them yourself; the person's only evidence is that
nothing happened.

- **No capabilities.** A manifest with no `capabilities`, or an empty one, loads enabled and runs
  nothing — and one declaring `network` alone is the same.
- **Two executing capabilities.** One of them is silently never read (see the rule above).
- **A declared file that does not exist.** `css_url`, `mappings_file` and `wasm_module` are not read
  at load: the stylesheet 404s, the mappings are skipped with a log line, the adapter fails at the
  first turn. Only the `i18n` pack's files are checked during a load.
- **A file in a subdirectory.** A press copies the repository's own files, plus what the manifest
  names (`css_url`, `messages_url`, `mappings_file`) and `prompts_dir`'s templates — nothing else.
  A `ui` extension whose `index.html` loads `js/TaskCard.js` is served an `index.html` whose script
  404s, so every file an extension needs sits beside `manifest.json`.
- **A file too large, or too many of them.** A press refuses a file over 2 MiB, more than 16 MiB or
  256 files in one version, and anything under a dot-prefixed or `node_modules` name — a `.env` is
  unreachable rather than safe.
- **An `lsp` or `agent` extension with nothing to run.** The container runs what a `Dockerfile`
  committed at this root builds; without one the failure arrives when a file of the declared type is
  opened, not at the press.
- **An unknown injection point or permission.** Ignored — the frame is not mounted, or the call is
  refused in the frame. Names come from `references/manifest.md`.
- **A missing `display_name`, `description`, `author` or `version`.** Loaded as blank; the card
  and Settings have nothing to show.

Do not guess around a refusal by removing the capability that made the thing useful.
