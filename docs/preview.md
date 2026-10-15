# Preview Capability — Full Detail

This document describes the mechanism for UI extensions to render files in a preview window (e.g., the `html-preview` extension for HTML/SVG files). Invariants are in `AGENTS.md → Extensions`.

## Overview

The server provides a hosted preview of a file. Root-relative references (links, images) are rewritten on the server to point inside the task's worktree root, allowing multi-page sites to navigate within that tree.

## Host API

The host provides the following API via `extHost.preview.*`:
- `serve(content, scripts, network)` $\to$ `{token, url}`: Stores the document and returns a unique token and URL.
  The call is dispatched as one of the three serve capabilities, and that capability is the posture
  (§The Serve Route). `scripts` off dispatches `preview.serve` whatever `network` asks for, so a caller
  cannot buy a script-capable policy by asking for the network one.
- `release(token)`: Explicitly removes the preview.
- `navState(token)` $\to$ `{seq, path, ok, isDoc}`: Returns the server's record of the last committed document navigation. `ok` is `seq > 0` — a derived key, not a stored flag.

## Go Implementation (`backend/server/handler_preview.go`)

### Storage & Limits
Previews are stored in-memory with:
- 10-minute idle TTL, refreshed by every read.
- 60-minute absolute age, which no amount of reading renews: a read costs nothing but the URL, so the
  idle rule alone lets whoever holds a leaked token hold it forever. The sweeper runs every 30s, so a
  document lives up to half a minute past the cap.
- Maximum of 32 documents (oldest evicted).
- 10 MB limit per document.

### The Serve Route (`POST /api/preview`)
1. Generates a 128-bit token.
2. Rewrites root-relative references (e.g., `<a href="/page2.html">`) to `/api/preview/<token>/page2.html`.
3. Resolves the **worktree root** (project checkout + task worktree) as the jail for the token.
4. Assets outside this root are refused (400). Symlinks are resolved and re-checked for containment.

The `posture` field carries the capability the host dispatched. It is the only thing that decides the
document's policy, and an unrecognised value — an absent field, a stale bundle's `allow_network`, a
forged string — serves the narrowest posture, so the field itself grants nothing. Which capability an
extension may dispatch is gated by its manifest grant (`frontend/src/lib/extensions/channel.ts`),
which is what the operator consented to at install.

### Asset Delivery (`GET /api/preview/{token}/[path...]`)
This route is public; the token is the credential. What one token opens is therefore a read of the
tree under the document's root, limited to the file types a browser embeds — not a read of one
document.
- **Content Type**: Determined by the extension.
- **Cache**: `Cache-Control: no-store`.
- **Type allow-list**: only the types a browser embeds are served (HTML family, script, style, image,
  font, media). An unmapped extension answers 404; `.git` is refused by path segment. Without this a
  token is an unauthenticated read of every byte under its root, `.env` among them. Refusing a type
  costs no browsing feature, because of the read rule below.
- **Expiry**: a token the store has forgotten answers `{"error":"preview not found"}`; one past
  `previewMaxAge` answers `{"error":"preview expired"}`. Both are 404 and both are dead — the
  distinction is the only thing a caller could act on, re-serving in one case and warning in the other.
- **CSP**: every document carries one of the three postures' policies (§Postures).
- **Self-Reference**: A link to the document's own filename serves the stored content, not the disk file.

### Postures
Every document — the served one and any sibling page navigated to — carries exactly one policy, chosen
by the posture the serve dispatched:

| Posture | Capability | Policy |
|---|---|---|
| `no-script` (default) | `preview.serve` | `previewDocCSPNoScript`: `script-src 'none'`, `connect-src 'none'`, tokenless `sandbox`; wildcards on `style-src`/`img-src`/`font-src`/`media-src`/`frame-src`, so the page renders like a browser with JavaScript off |
| `scripts` | `preview.serveScripts` | `previewDocCSPStrict`: `'self'`/`data:`/`blob:` only, `connect-src 'none'`, `frame-src 'none'`, `sandbox allow-scripts` |
| `network` | `preview.serveNetwork` | `previewDocCSPNetwork`: the permissive policy, scripts and egress both open |

No policy pairs script execution with an egress channel except the network one, which exists only behind
its manifest grant. The `no-script` wildcards are the load channel a scriptless render needs: they can
fetch a URL the file's author wrote, and nothing a script invented. `style-src` carries `'unsafe-inline'`
because inline CSS is the author's own, and a remote stylesheet is loadable anyway.

## Navigation & Security Model

### A previewed page can embed, it cannot read
No response under `/api/preview/` carries `Access-Control-Allow-Origin`, in any value, and the frame's
origin is opaque. So `fetch`, `XMLHttpRequest` and a JSON module import of a sibling file always fail:
the bytes are delivered to the page as a load and are never readable by its own code. A page that
loads its own data does not work in the preview, and it renders its own error rather than a refusal.
What does work is every plain load: `<script src>`, `<link>`, `<img>`, `<video>`, `@font-face`,
in-tree navigation and `?v=` cache-busters.

The value that would change this is `Access-Control-Allow-Origin: null` — what an opaque origin names
itself with — and it is refused on purpose; `docs/api-contract.md → Response headers` keeps that argument
and its gate. Only a separate origin for previews would give a page its own files without a CORS grant, so
that is a different design rather than a header. `GET /api/preview/{token}/{path...}` logs the sibling it
served at `Debug`, which is the only place an operator can see that a load happened at all.

The `img-src`/`frame-src`/`style-src` wildcards of the render posture are an outbound channel for URLs
the previewed file already contains: loading one tells its server that this preview was opened. That is
the accepted cost of rendering remote assets without scripts, and it is bounded by the token's lifetime.

### The Sandbox
Previews are rendered in a nested `<iframe sandbox>` with an **opaque origin**.
- **`allow-scripts`**: Set on the frame only if the extension's setting is enabled. The CSP `sandbox`
  directive is the ceiling: a document served a tokenless `sandbox` runs no scripts even in a frame
  whose own attribute asks for `allow-scripts`.
- **`allow-same-origin`**: **Never** allowed, from either source. This prevents the previewed document
  from accessing host cookies, DOM, or Web Storage, and stops it from calling `/api` mutations.

### In-Tree Navigation (Allow-List)
Because the origin is opaque, the extension cannot read the iframe's URL. It asks
`GET /api/preview-nav/{token}` (session-authed, and outside the public `/api/preview/` tree so no
previewed file can carry that name). This is the extension's own warning, not a boundary: an
out-of-tree navigation sends this server no request, so the token's scope and lifetime are what bound
it, and the watchdog can only stop drawing the page.
- **Fresh Record**: If the server's `seq` (sequence number) has increased, it means an in-tree file was served $\to$ **ALLOWED**.
- **Stale Record**: If the record is stale or an error occurs, the navigation is an escape $\to$ **CONTAINED** (frame reset to `about:blank`, token released).

### Live Refresh
When a file is edited, the preview is refreshed. If the user has navigated away from the primary document to a sibling file, the refresh is held until the user returns to the document, or the navigation is resolved.

## Verification

- **Backend Tests**: `go test ./backend/server/` verifies preview routes, reference rewriting, and CSP.
- **E2E Tests**: `frontend/tests/e2e/html-preview.spec.ts` tests egress, script modes, and in-tree navigation.
