# Third-Party Notices

MIKE: Integrated Kanban Environment (MIKE) is licensed under the Apache License, 
Version 2.0 (see `LICENSE`). This document inventories the third-party 
libraries that MIKE depends on and their respective licenses.

Full license texts are available inside each dependency's package.

**Scope.** Only what is compiled into what MIKE distributes — the `kanban` and `sandbox-bridge`
binaries and `frontend/dist`. An extension's own dependencies (its language server, WASM adapter or
bundled JS) are covered by that extension's `LICENSE`, `NOTICE` and `THIRD_PARTY_NOTICES.md`.

**Last updated:** 2026-09-11

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
`frontend/dist`.

| Package | License |
|---------|---------|
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
| @xterm/addon-fit | MIT |
| @xterm/addon-webgl | MIT |
| @xterm/xterm | MIT |
| codemirror | MIT |
| crelt | MIT |
| diff | BSD-3-Clause |
| dompurify | (MPL-2.0 OR Apache-2.0) — treated as Apache-2.0 |
| marked | MIT |
| style-mod | MIT |
| w3c-keyname | MIT |
