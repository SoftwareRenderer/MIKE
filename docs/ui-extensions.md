# UI Extensions — Serving, CSP and the Channel Handshake

Read this before touching `backend/server/routes.go` (the `/extensions` tree),
`frontend/public/host-client.js`, `frontend/src/lib/services/extension-manager.svelte.ts` or
`ui-injector.svelte.ts`. The hard rules live in `AGENTS.md → Extensions`.

## Module Shape

UI extensions are **plain JavaScript modules, not Svelte components**. They must not import Svelte
from the host workspace to avoid runtime mismatches (`effect_orphan`). A module must default-export
`init({ component, config, ...scopeContext })` and return a cleanup `() => void`. Use standard
DOM APIs and `createElement('style')` for UI.

## Serving and Listing

`ui.module_url` is served by the Go static handler under `/extensions/...`. This tree uses
`noListingFS` (`backend/server/routes.go`), so `/extensions/` and `/extensions/<author>/` always 404
to prevent unauthenticated directory listing. Only directories containing an `index.html` are opened.
`manifest.json` defines metadata, `ui.injection_points`, optional `config_schema`, and `priority`
(lower numbers load first).

The embedded SPA uses the same wrapper, so `GET /assets/` 404s to prevent listing all build chunks.
Individual chunks are still fetchable without a session if their name is known (`isPublicPath`).

**CSP:** `/extensions/...` uses `CORSMiddleware`, so `script-src 'self' 'nonce-…'` applies.
**Inline `<script>` in an extension's HTML is blocked**; load code via external files
(`<script src="app.js">`). Inline `<style>` is allowed (`style-src 'unsafe-inline'`).

**Isolation:** Extensions are sandboxed both by the iframe and by their own response headers.
Because the `/extensions/` tree is public and on the same origin, an extension could be reached
outside a host-mounted frame (e.g., via a direct link). To prevent this, `CORSMiddleware` appends
`sandbox allow-scripts` to the CSP of every `/extensions/` response. This gives the document an
opaque origin: it cannot access `localStorage` or `GET /api/auth/session`. `/api/preview/...` carries
the same directive under its two scripts postures; under the default no-script posture it carries a
tokenless `sandbox` instead, which refuses scripts even in a frame whose own attribute asked for
`allow-scripts` (`docs/preview.md → Postures`). The two trees state the rule twice because two
handlers serve them. Verified by `TestIsolationHeaders` (the header is sent) and
`extension-frame-channel.spec.ts` (what the browser does with it).

**The entry point is the extension's directory URL.** The host requests the directory rather than
`index.html` to avoid 301 redirects that would bypass the per-mount `?extch=` cache-busting token.
Match mounted frames by **directory** path, never by `index.html`.

**The frame mounts when the file is open, not before.** If an extension is registered at `fileEditor`,
its channel is armed only after `GET /api/fs/read` succeeds. This avoids booting frames for files
that don't exist or are too large, and avoids arming capabilities (like `editor.getContent`)
before the host knows the file is openable.

**Cross-Origin-Resource-Policy:** The mounted frame is `sandbox="allow-scripts"` without
`allow-same-origin`, so it has an opaque origin. CORP `same-origin` would block it from fetching
the bridge (`/host-client.js`). Therefore, `isOpaqueOriginAsset` (`backend/server/middleware.go`)
exempts `/extensions/`, `/api/preview/`, and `/host-client.js`. This is safe because these assets
are public and contain no user data. The host only answers requests that echo the per-mount
`?extch=` token.

## Channel Handshake

The host mints a per-mount token, passes it as `?extch=` in the iframe URL, and arms the channel only
when the document echoes it back via `exthello`. This is necessary because `ev.origin === 'null'`
only identifies the frame, not the specific document the host intended to mount.

- Nothing is posted to the frame before arming (except `exthello-request`). `call` requests from
  un-armed frames are ignored silently.
- On subsequent `load` events, the channel is **disarmed** and a new hello is requested. This allows
  legitimate reloads to re-handshake without tearing down the iframe.
- **Nested** iframe loads (e.g., in a preview) do not count toward this limit, as they belong to
  a different browsing context.
- `frontend/public/host-client.js` handles the `extch` token and answers `exthello` on every load.

Regression-tested by `frontend/tests/e2e/extension-frame-channel.spec.ts`.

## Grants, Calls and Pushed Events

`computeGrants` (`$lib/extensions/capabilities.ts`) allows a permission only if it is in the catalog,
and the channel dispatches a `call` only for a granted one. Pushed events are filtered by the same
list: `forward` drops an event whose capability is not granted, warning once per capability per mount.
This matters for the two events the host pushes itself from `FileEditor` (`editor.contentChange`,
`editor.jumpToLine`) rather than through an `onGrant` — `editor.contentChange` carries the whole file.

The channel (`$lib/extensions/channel.ts`) also grants pairs no manifest declares: `theme.update`
alongside the always-granted `theme.get`, and `editor.removeFooterButton` + `editor.footerButtonClick`
with `editor.addFooterButton`. `frontend/src/lib/extensions/__tests__/capability-consent.unit.ts` is
what fails when a new capability is neither described in `permissionDescriptions()` / classified in
`DANGEROUS_PERMISSIONS` (the two lists the install dialog reads) nor listed in the test's own
`HOST_INTERNAL_CAPABILITIES`, so a capability has to be classified before it can be granted quietly.

## Install Model

Extensions are git-pinned (`author`/`repo`/`commit`/`subdir`). The server clones the repo, checks
out the specific commit, and materializes the subdirectory into `./extensions/<author>/<name>`.
Install via Settings → Extensions → "Install from Registry".

**Source and Registry:** Source lives in `../MIKE-extensions/<type>/<name>/`. The `registry.json`
is **generated** via `python3 scripts/gen-registry.py` after any commit.

**Atomic Install:** An install only succeeds if the image is built successfully. The image is built
in a temporary context before the record is written. If the build fails, the checkout is deleted.
A directory without a corresponding record is treated as installable via `GET /api/extensions/local`.

**Repair:** Missing installations are re-materialized at startup. If `manifest.json` is missing or
unreadable, the server re-checks out the pinned commit (`backend/extensions/repair.go`). Local
installs (no `repo`/`commit`) are not re-fetched.

**Isolation:** Bundle all runtime libraries (e.g., ESM npm packages) **inside the extension folder**.
The core `frontend/` workspace must not depend on an extension's runtime.

**Tests:** Extension tests live in the extension's own `tests/` directory and use Playwright from
the frontend workspace. Shared logic should be in a plain script loaded by both the extension
and its tests.
