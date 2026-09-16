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

The embedded SPA goes through the same wrapper, so `GET /assets/` 404s rather than handing an
unauthenticated caller the list of every chunk the build made. Any chunk whose name is known is still
fetchable without a session (`isPublicPath`); what the wrapper removes is the enumeration
(`TestAssetDirectoryIsNotListed`).

**CSP:** `/extensions/...` goes through `CORSMiddleware`, so `script-src 'self' 'nonce-…'` applies —
**inline `<script>` in an extension's HTML is blocked**; load code as external files
(`<script src="app.js">`). `<style>`/inline styles are fine (`style-src 'unsafe-inline'`).

**An extension document is sandboxed by its own response, not only by the frame that holds it.** The tree
is public and on this origin, and `noListingFS` opens any directory holding an `index.html` — so an
installed UI extension also has a URL reachable *outside* a host-mounted frame: typed, pasted, or clicked
from a relative link that survived sanitizing (keeping same-origin hrefs is what a markdown sanitizer is
for). `sandbox="allow-scripts"` is something the host sets on the frame it mounts and says nothing about
that visit, so `CORSMiddleware` appends `sandbox allow-scripts` to the CSP of every `/extensions/`
response. That is exactly the grant the iframe already carries, so a mounted frame loses nothing, while a
top-level document gets an opaque origin: no `localStorage`, and no reading of `GET /api/auth/session`,
whose answer plus the session cookie is every API. `/api/preview/...` declares the same directive for the
same reason (`previewDocCSPStrict`); the two are one rule stated twice because the two trees are served by
two handlers. Pinned by `TestIsolationHeaders` (the header is sent) and by the last test of
`extension-frame-channel.spec.ts` (what the browser does with it, which only a browser can say).

**The entry is the extension's directory URL.** The host asks for the directory, never
`<dir>/index.html`: `FileServer` answers the file form with a 301 to `./`, and the per-mount
`?extch=` token keeps that redirect out of every cache, so every mount would pay the extra round
trip again. Asking for the directory is not a wider request — `noListingFS` opens a directory only
when it holds an `index.html`. Either way, match a mounted frame by **directory** path, never by
`index.html`.

**The frame mounts when the file is open, not before.** `.ext-anchor` lives in the editor's
loaded branch, so a UI extension registered at `fileEditor` gets an armed, granted channel only
after `GET /api/fs/read` answered. That is a deliberate cost: mounting the anchor as soon as a path
is known measured ~2 round trips better (Preview button 1253 ms → 670 ms at 120 ms RTT) but arms
`editor.getContent` / `editor.setOverlay` / `preview.serve*` over a full-bleed overlay while the
host still does not know the file is openable, and boots a frame for every open that ends in
`fileNotFound` / `fileTooLarge`. Taking it also requires `editor.addFooterButton` to wait for
`.editor-footer .footer-left` instead of answering `editor footer not found` — an installed
extension stops asking after 8 tries at 250 ms (`Preview.js:250`), and that budget belongs to the
pinned artifact, not to the host.

**Cross-Origin-Resource-Policy — `/host-client.js` is an opaque-origin path like the two trees above.**
The mounted frame is `sandbox="allow-scripts"` *without* `allow-same-origin`, so its document has no
origin to name and CORP `same-origin` refuses every subresource it fetches — including the bridge it
loads out of the app root (`<script src="/host-client.js">`, which every shipped extension carries).
Marked, the frame still mounts and its own `.js` still loads, but `window.extHost` is never defined,
so the extension dies on `ReferenceError: extHost is not defined`. `isOpaqueOriginAsset`
(`backend/server/middleware.go`) therefore exempts `/extensions/`, `/api/preview/` **and**
`/host-client.js`, the last under the same constant that exempts it from authentication.

Nothing is given up by that: the file is public, the same bytes for every visitor, and holds no user
data — CORP exists to stop *other sites* embedding what is ours. A foreign page that framed the
bridge would only be talking to itself, because the host answers nothing until the document echoes
the per-mount `?extch=` token.

Pinned by `TestIsolationHeaders` (the header) and e2e `extension-frame-channel.spec.ts`, which arms a
real channel through the real file and fails as "the mounted extension document never armed its
channel" if the exemption is dropped. Against a running server, without the e2e harness:
`node frontend/tests/extension-bridge-load-check.mjs`.

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

**An install that cannot produce its image installs nothing.** The image is built before the row is
written, in a temp context of the server's own making (see `AGENTS.md → Extensions`), and a
language-server entry with neither an image nor a Dockerfile is refused rather than recorded. A failure
after the checkout deletes that checkout too: a directory with no row is offered by
`GET /api/extensions/local` as installable, under the manifest it shipped with. Only a checkout the
install made is removed — a local install's folder belongs to whoever put it there.

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

## Module shape and channel handshake (moved from AGENTS.md)
### UI Extensions
UI extensions are **plain JavaScript modules — not Svelte components** (external bundles carry their own Svelte runtime → `effect_orphan`). A module default-exports `init({ component, config, ...scopeContext })` and returns a cleanup `() => void`; no Svelte imports, DOM + `createElement('style')` only. Serving, CSP, listing, install model, bundling and tests: `docs/ui-extensions.md`.

- **Source lives in `../MIKE-extensions/<type>/<name>/`;** `registry.json` is **generated** — `python3 scripts/gen-registry.py` after editing a manifest or bumping a commit (`--check` in CI).
- **The host CSP blocks inline `<script>`** in an extension's HTML — load code as external files.
- **Channel handshake (document identity):** the host mints a per-mount token, passes it as `?extch=`, and arms the channel only on the document's `exthello` echo — `origin === 'null'` + `ev.source` identify the *frame*, not the mounted document. Post nothing before arming (except `exthello-request`); a `call` from an un-armed frame is refused **without a response**; a 2nd+ `load` **disarms** and re-requests hello (never tears the frame down); **nested** iframe loads never count. `frontend/public/host-client.js` answers `exthello` on every load — a hand-rolled client that doesn't hangs at `extHost.ready()`. Test: e2e `extension-frame-channel.spec.ts` (isolated; fixture must use external scripts).
