# Preview Capability — Full Detail

This document describes the mechanism for UI extensions to render files in a preview window (e.g., the `html-preview` extension for HTML/SVG files). Invariants are in `AGENTS.md → Extensions`.

## Overview

The server provides a hosted preview of a file. Root-relative references (links, images) are rewritten on the server to point inside the task's worktree root, allowing multi-page sites to navigate within that tree.

## Host API

The host provides the following API via `extHost.preview.*`:
- `serve(content)` $\to$ `{token, url}`: Stores the document and returns a unique token and URL.
- `release(token)`: Explicitly removes the preview.
- `navState(token)` $\to$ `{seq, path, ok, isDoc}`: Returns the server's record of the last committed document navigation.

## Go Implementation (`backend/server/handler_preview.go`)

### Storage & Limits
Previews are stored in-memory with:
- 10-minute idle TTL.
- Maximum of 32 documents (oldest evicted).
- 10 MB limit per document.

### The Serve Route (`POST /api/preview`)
1. Generates a 128-bit token.
2. Rewrites root-relative references (e.g., `<a href="/page2.html">`) to `/api/preview/<token>/page2.html`.
3. Resolves the **worktree root** (project checkout + task worktree) as the jail for the token.
4. Assets outside this root are refused (400). Symlinks are resolved and re-checked for containment.

### Asset Delivery (`GET /api/preview/{token}/[path...]`)
This route is public; the token is the credential.
- **Content Type**: Determined by the extension.
- **Cache**: `Cache-Control: no-store`.
- **CSP**: Every document carries a strict Content Security Policy (CSP):
    - `previewDocCSPStrict` (default): No `*` in load directives; `connect-src 'none'`.
    - `previewDocCSPNetwork`: Enabled if the serve request sets `allow_network`.
- **Self-Reference**: A link to the document's own filename serves the stored content, not the disk file.

## Navigation & Security Model

### The Sandbox
Previews are rendered in a nested `<iframe sandbox>` with an **opaque origin**.
- **`allow-scripts`**: Set only if the extension's setting is enabled.
- **`allow-same-origin`**: **Never** allowed. This prevents the previewed document from accessing host cookies, DOM, or Web Storage, and stops it from calling `/api` mutations.

### In-Tree Navigation (Allow-List)
Because the origin is opaque, the extension cannot read the iframe's URL. It uses the `/nav` endpoint:
- **Fresh Record**: If the server's `seq` (sequence number) has increased, it means an in-tree file was served $\to$ **ALLOWED**.
- **Stale Record**: If the record is stale or an error occurs, the navigation is an escape $\to$ **CONTAINED** (frame reset to `about:blank`, token released).

### Live Refresh
When a file is edited, the preview is refreshed. If the user has navigated away from the primary document to a sibling file, the refresh is held until the user returns to the document, or the navigation is resolved.

## Verification

- **Backend Tests**: `go test ./backend/server/` verifies preview routes, reference rewriting, and CSP.
- **E2E Tests**: `frontend/tests/e2e/html-preview.spec.ts` tests egress, script modes, and in-tree navigation.
