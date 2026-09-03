# UI Extensions — Serving, CSP and the Channel Handshake

Read this before touching `backend/server/routes.go` (the `/extensions` tree),
`frontend/public/host-client.js`, `frontend/src/lib/services/extension-manager.svelte.ts` or
`ui-injector.svelte.ts`. The hard rules live in `AGENTS.md → UI Extensions`.

## Serving and listing

`ui.module_url` is served by the Go static handler under `/extensions/...`. That tree goes through
`noListingFS` (`backend/server/routes.go`), so `/extensions/` and `/extensions/<author>/` never
render a directory index (they 404) — the installed-extension tree is not an unauthenticated
listing; only directories that actually hold an `index.html` open, which keeps the extension entry
contract (`/extensions/<author>/<name>/`) intact. `manifest.json` carries metadata,
`ui.injection_points`, optional `config_schema`, and `priority` (lower loads first; ties by name).

**CSP:** `/extensions/...` goes through `CORSMiddleware`, so `script-src 'self' 'nonce-…'` applies —
**inline `<script>` in an extension's HTML is blocked**; load code as external files
(`<script src="app.js">`). `<style>`/inline styles are fine (`style-src 'unsafe-inline'`). The entry
also lands on its **directory** URL (Go's `FileServer` 301s `/index.html` → `./`), so match a mounted
frame by directory path, not by `index.html`.

## Channel handshake (document identity)

The host mints a per-mount token, passes it as `?extch=` in the iframe URL and arms the channel only
when the document echoes it back (`exthello`), because `ev.origin === 'null'` + `ev.source` identify
the *frame*, not the document the host mounted — any document that lands in a sandboxed frame would
otherwise receive `init` (config/grants/context), pushed events (`editor.contentChange`) and be able
to call the granted capabilities.

- Nothing is posted to the frame before arming (`exthello-request` excepted); a `call` from an
  un-armed frame is refused **without a response** (a silent channel leaks nothing).
- On the 2nd+ iframe `load` the channel warns and **disarms** (event listeners torn down, calls
  refused) then re-requests a hello, so a legitimate reload re-handshakes and keeps working — the
  watchdog never tears the iframe down.
- Only the mounted frame's own `load` counts: an extension's **nested** iframes (html-preview
  renders the previewed document in one) may navigate freely, because a nested load belongs to the
  nested browsing context — counting it would disarm the channel on every preview refresh.
  Cross-origin self-navigation is additionally blocked in flight by the host `frame-src 'self'`; the
  same-origin case (a sibling file taking the frame over) is what the handshake covers.
- `frontend/public/host-client.js` reads `extch` and answers `exthello` on every load — a hand-rolled
  client that never does hangs at `extHost.ready()` (fails closed).

Regression-tested by `frontend/tests/e2e/extension-frame-channel.spec.ts` (isolated; its fixture
must use external scripts, see above).

## Install model

Entries are git-pinned (`author`/`repo`/`commit`/`subdir`); the server clones the repo, checks out
exactly `commit` (full SHA-1/SHA-256), verifies `git rev-parse HEAD` matches, then materializes the
subdir into `./extensions/<author>/<name>`. Deploy via Settings → Extensions → "Install from
Registry".

**A missing install is re-materialized at startup.** That directory *is* the extension —
`/extensions/<author>/<name>/index.html` is what gets framed — so a wiped `extensions/` tree leaves every
UI extension enabled and inert with nothing to see: the frame 404s, the document never boots, so there is
no channel and no capability call, and `extHost.ready()` simply never resolves. At startup the server
re-checks out each row's **own** pinned commit for any install whose `manifest.json` is missing or
unreadable (`backend/extensions/repair.go`, `[extensions] repair:` log lines). Nothing is fetched for a row
with no `repo`/`commit` (a local install has no second copy to come from), and nothing is fetched at all
when the files are present.

**Bundling a runtime library (isolation):** keep it **inside the extension folder** (bundle an ESM
npm package to a single IIFE committed in the extension dir, with its own `package.json` + build).
The core `frontend/` workspace must NOT gain an extension's runtime dependencies.

**Extension tests:** colocated under the extension dir's `tests/` (loading Playwright from the
frontend workspace), not in `frontend/tests/`. Shared extension logic is a plain script loaded by
both the extension and its tests — never copy it.
