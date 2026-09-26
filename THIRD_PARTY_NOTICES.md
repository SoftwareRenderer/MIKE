# Third-Party Notices

MIKE: Integrated Kanban Environment (MIKE) is licensed under the Apache License, 
Version 2.0 (see `LICENSE`). This document inventories the third-party 
libraries that MIKE depends on and their respective licenses.

Full license texts are available inside each dependency's package.

**Scope.** Only what is compiled into what MIKE distributes — the `kanban` and `sandbox-bridge`
binaries and `frontend/dist`. An extension's own dependencies (its language server, WASM adapter or
bundled JS) are covered by that extension's `LICENSE`, `NOTICE` and `THIRD_PARTY_NOTICES.md`.

**Last updated:** 2026-09-25

---

## Go (backend) dependencies

Source of truth: the modules compiled into the binary —

```
go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./backend/... ./pkg/... . | sort -u
```

`go list -m all` answers the module *graph*, which also lists modules required by a dependency's
`go.mod` but never imported here. The linked set is the same on linux, darwin and windows.

| Module | License | Copyright |
|--------|---------|-----------|
| github.com/andybalholm/brotli | MIT | The Brotli Authors |
| github.com/coder/websocket | ISC | Coder |
| github.com/creack/pty | MIT | Keith Rarick |
| github.com/fsnotify/fsnotify | BSD-3-Clause | The Go Authors / fsnotify Authors |
| github.com/google/uuid | BSD-3-Clause | Google Inc. |
| github.com/tetratelabs/wazero | Apache-2.0 | wazero authors (see NOTICE) |
| golang.org/x/crypto | BSD-3-Clause | The Go Authors |
| modernc.org/sqlite | BSD-3-Clause | The Sqlite Authors |
| github.com/dustin/go-humanize (indirect) | MIT | Dustin Sallings |
| github.com/remyoudompheng/bigfft (indirect) | BSD-3-Clause | The Go Authors |
| golang.org/x/sys (indirect) | BSD-3-Clause | The Go Authors |
| modernc.org/libc (indirect) | BSD-3-Clause | The Libc Authors |
| modernc.org/mathutil (indirect) | BSD-3-Clause | The mathutil Authors |
| modernc.org/memory (indirect) | BSD-3-Clause | The Memory Authors |

## Frontend production dependencies

Source of truth: `frontend/package-lock.json` (production `dependencies`) as bundled into
`frontend/dist`. A package a dependency imports for its **types only** is absent — nothing of it reaches
`dist` — and the diagram renderer's chunks are present but fetched on demand, so they are listed beside
the code that is loaded at startup.

| Package | License |
|---------|---------|
| @braintree/sanitize-url | MIT |
| @chevrotain/gast | Apache-2.0 |
| @chevrotain/regexp-to-ast | Apache-2.0 |
| @chevrotain/utils | Apache-2.0 |
| @codemirror/autocomplete | MIT |
| @codemirror/commands | MIT |
| @codemirror/lang-css | MIT |
| @codemirror/lang-html | MIT |
| @codemirror/lang-javascript | MIT |
| @codemirror/lang-json | MIT |
| @codemirror/lang-markdown | MIT |
| @codemirror/lang-sql | MIT |
| @codemirror/lang-yaml | MIT |
| @codemirror/language | MIT |
| @codemirror/lint | MIT |
| @codemirror/lsp-client | MIT |
| @codemirror/search | MIT |
| @codemirror/state | MIT |
| @codemirror/view | MIT |
| @iconify/utils | MIT |
| @lezer/common | MIT |
| @lezer/css | MIT |
| @lezer/highlight | MIT |
| @lezer/html | MIT |
| @lezer/javascript | MIT |
| @lezer/json | MIT |
| @lezer/lr | MIT |
| @lezer/markdown | MIT |
| @lezer/yaml | MIT |
| @marijn/find-cluster-break | MIT |
| @mermaid-js/parser | MIT |
| @upsetjs/venn.js | MIT |
| @xterm/addon-fit | MIT |
| @xterm/addon-webgl | MIT |
| @xterm/xterm | MIT |
| chevrotain | Apache-2.0 |
| clsx | MIT |
| codemirror | MIT |
| cose-base | MIT |
| crelt | MIT |
| cytoscape | MIT |
| cytoscape-cose-bilkent | MIT |
| cytoscape-fcose | MIT |
| d3 | ISC |
| d3-array | ISC |
| d3-axis | ISC |
| d3-brush | ISC |
| d3-color | ISC |
| d3-dispatch | ISC |
| d3-ease | BSD-3-Clause |
| d3-format | ISC |
| d3-hierarchy | ISC |
| d3-interpolate | ISC |
| d3-path | ISC |
| d3-sankey | BSD-3-Clause |
| d3-scale | ISC |
| d3-scale-chromatic | ISC |
| d3-selection | ISC |
| d3-shape | ISC |
| d3-time | ISC |
| d3-time-format | ISC |
| d3-timer | ISC |
| d3-transition | ISC |
| d3-zoom | ISC |
| dagre-d3-es | MIT |
| dayjs | MIT |
| diff | BSD-3-Clause |
| dompurify | (MPL-2.0 OR Apache-2.0) — treated as Apache-2.0 |
| elkjs | EPL-2.0 |
| es-toolkit | MIT |
| internmap | ISC |
| katex | MIT |
| khroma | MIT |
| layout-base | MIT |
| lodash-es | MIT |
| marked | MIT |
| mermaid | MIT |
| roughjs | MIT |
| style-mod | MIT |
| stylis | MIT |
| svelte | MIT |
| ts-dedent | MIT |
| uuid | MIT |
| w3c-keyname | MIT |
| workbox-window | MIT |
