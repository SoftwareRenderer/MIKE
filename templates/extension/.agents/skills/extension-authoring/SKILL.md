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
| Real code intelligence for a language: completions, diagnostics, go-to-definition | `lsp` | a container and an image build |
| Talk to a model provider MIKE does not speak (its request/response dialect) | `llm` | a mappings file, no container |
| Drive a whole agent that is not the built-in one | `agent` | a WASM adapter, a container and an image build |

Pick the cheapest type that does the job. Most requests are `ui`: if the person can see it and click
it, it is a frame with a script in it.

**A description that fits two types is two extensions**, not one manifest. `tools` may not be declared
beside `lsp`, `ui`, `css`, `agent`, `llm` or `i18n`, and an `i18n` pack may declare nothing else —
both are refused at load, and the row is stored under one type so the other's lookup would miss it.
If the ask is genuinely two things, say so and write both folders; if it is one thing that looks like
two, the cheap one usually wins.

Read `references/manifest.md` before writing a manifest. Read `references/lsp.md`, `references/agent.md`
or `references/llm.md` when you picked one of those three — they cost an image or a WASM module and
need a Dockerfile, and none of them has an example to copy.

## The loop

1. Decide the type. Say which and why, in the person's terms, in your final message.
2. Copy the closest example: `examples/ui/`, `examples/css/`, `examples/i18n/`, `examples/tool/`.
   Each is a complete extension that passes MIKE's own validation.
3. Write `manifest.json` at the repository root, beside the files it names. Every declared path is
   relative to this directory and must stay inside it; `..` and a URL with a scheme are refused.
4. Keep the entry files' names the ones the manifest names. A `ui` extension's entry page is always
   `index.html`; its `<script>` tags are external files (inline script is blocked by the CSP), and
   it loads `/host-client.js` before its own script.
5. Stop. The person presses **Try Extension** on this task; MIKE validates and reports. If the
   message names a manifest field, fix that field and press it again.

Only what the manifest names is used or copied — an extra file is dead weight the browser can still
fetch, and a file the manifest forgot is invisible however correct it is.

## What MIKE will refuse

- A manifest with no capabilities: nothing runs.
- An `i18n` pack that declares anything else; `tools` beside another executing capability.
- A declared path that escapes the directory, or a URL with a scheme where a file was asked for.
- An `i18n` `locale` that is not one bare lowercase subtag (`fr`, not `fr-FR`).
- A `tool` with no `capabilities.network`, or whose `request.url` does not begin with
  `{{config.<key>}}` naming a `config_schema` property of type `"url"`. The address is the person's
  grant, never yours.
- A `lsp` extension with no `file_types`, or no image and no Dockerfile to build one from.

These are all answers you get from **Try Extension**, in the words above. Relay them; do not guess
around them by removing the capability that made the thing useful.
