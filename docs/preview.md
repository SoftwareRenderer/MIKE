# Preview Capability — Full Detail

Read this before touching `backend/server/handler_preview.go`,
`frontend/src/lib/extensions/handlers/preview.ts`, or the `html-preview` extension. The invariants
live in `AGENTS.md → Extensions`; this file is the mechanism.

Generic host support for UI extensions that render the open file. `html-preview` is the example: an overlay with
live refresh for `.html`/`.xhtml`/`.svg`. Root-relative references are rewritten on the server as the document is
served, so each one points inside the token's worktree root and a multi-page site can navigate within that tree.

## Host API

`preview.serve(content)` → `{token, url}`, `preview.release(token)`,
`preview.navState(token)` → `{seq, path, ok, isDoc}` (the token's last committed document navigation
— the server-side truth behind the extension's in-tree allow-list) —
`frontend/src/lib/extensions/handlers/preview.ts`, exposed as `extHost.preview.*` in
`frontend/public/host-client.js`. A re-serve from the same editor implicitly releases its previous
doc.

## Go side (`backend/server/handler_preview.go`)

In-memory; 10-min idle TTL (refreshed per read); 32-doc cap (oldest evicted); 10 MB/doc; 30 s sweep.

`POST /api/preview` (session+CSRF) stores the doc: it generates the token FIRST, rewrites
root-relative refs (`src/href/action="..."`) for HTML-family content to `/api/preview/<token>/...`
(RE2 — no backrefs; patterns never consume the closing quote/paren; unquoted `url(...)` supported),
re-checks the size cap post-rewrite, and resolves the doc's **worktree root** + `dirRel`.
That root is the jail the token exposes: the project checkout plus the worktree of the task the request named,
never the workspace dir, which holds every project's task worktrees. A path in neither is a 400, not a document
with a broken root. `resolveFSFilePath` resolves symlinks too, because a link inside the checkout would otherwise
point the asset root at the rest of the host.

The token is 128 bits from `crypto/rand`, and `newPreviewToken` refuses the serve on error rather than falling
back to another token: a guessable token is an unauthenticated read of the document and of every file under its
asset root. The `project`/`path`/`task` the root is resolved from come from the host's own injection context
(FileEditor passes `filePath`/`projectId`/`taskId`), never from extension arguments — otherwise an extension
could point the server at any directory it names.

The doc serves at its own directory: `GET /api/preview/{token}/[dirRel]/` (bare token 301s there;
the token root is the worktree root, no listing; a link to the doc's own file name — the
self-reference — serves the STORED content, not the disk file).

`GET /api/preview/{token}/[path...]` is public — the token is the credential. The content type comes from the
extension, and the answer is `Cache-Control: no-store`. The document, and any HTML/SVG/XHTML document beside it,
carries its own CSP, which `CORSMiddleware` skips, so a page the previewed document navigates to is held to the
same egress rule:

- `previewDocCSPStrict` by default: no `*` in any load-type directive, and `connect-src 'none'`.
- `previewDocCSPNetwork` only when the serve request sets `allow_network`.
- Two CSP headers union to the most restrictive, so a served document cannot widen what the host set.
- `navigate-to` is left out on purpose: no browser implements it, and a top-frame navigation is already held by
  the host CSP's `frame-src 'self'` plus the load counter described under *Nav record semantics*.

`GET /api/preview/{token}/nav` (session-authed; exact route wins over `{path...}`, so a worktree-root
file literally named `nav` is shadowed — documented limitation) returns the nav record;
`DELETE /api/preview/{token}` releases early. Sibling files are worktree-root-aware with
`filepath.Clean`/`Rel` containment + `EvalSymlinks` re-containment — traversal and symlink escapes
are 404.

## Nav record semantics

A real frame navigation that is answered with an HTML-family document bumps the token's monotonic `seq` and
stores `{path, ok, isDoc}`. (Chromium marks a top-frame navigation `Sec-Fetch-Dest: document` and a subframe one
`iframe`; the preview frame is a subframe.) Nothing else records: a 404, a redirect or a css/js/img subresource
leaves the record as it was. So a navigation out of the tree, which no preview handler can serve, leaves a stale
record — and the extension treats a stale record as an escape.

## Security model

The doc loads in a nested `<iframe sandbox>` (opaque origin — bare `sandbox` = scripts OFF by
default; the extension sets `allow-scripts` only when its `allow_scripts` setting is on) inside the
extension's already-sandboxed iframe — no host cookies/DOM/Web Storage access, no top-window
navigation, no popups; `/api` mutations still need the CSRF header; no `frame-ancestors` (the doc
must be frameable). Scripts-off previews are fully static (exfil impossible — every requested URL was
author-written) so they get the permissive CSP; scripts-on get the strict zero-egress CSP unless
`allow_network` opts back into the permissive one.

**The origin is taken away by the server, not by the extension.** The preview URL is
same-origin with the application, so that `sandbox` attribute is what stands between a
previewed document and the signed-in session — and `GET /api/auth/session` answers any
cookie-carrying same-origin GET with the CSRF token, which leaves an unconfined preview
document one call from every mutating endpoint. Both policies therefore end in
`sandbox allow-scripts` (`previewDocCSPStrict`, `previewDocCSPNetwork`), so the document
is opaque-origin whichever element embeds it; the attribute the extension sets is the
second lock rather than the only one. `allow-scripts` is in both on purpose — a sandbox
source only adds restrictions, so the token costs nothing, while a bare `sandbox` would
strip scripts from the scripts-on mode. `allow-same-origin` must never appear
(`TestPreviewDocCSP_SandboxsWithoutSameOrigin` holds both halves). A browser without CSP
`sandbox` support falls back to exactly the previous behaviour, the attribute still
applying, so the directive is defense in depth and not a new dependency.

**Self-navigation — in-tree allow-list.** The extension arms a load counter at every `frame.src` set;
the first load is the doc it installed (it then syncs its baseline `seq` from `/nav` — the
`load` event fires after subresources, so nested-iframe loads at install are absorbed into the
baseline), and any load with nothing pending is a self-navigation. The extension can't read the
nested frame's URL (opaque origin), so it asks `/nav`:

- a FRESH `ok` record (`seq` above that baseline) means the server actually served an in-tree file →
  ALLOWED (the doc may be a multi-page site: `index.html` → `page2.html` → `sub/page3.html`);
- anything else (stale record, error, host call failure) → CONTAINED (frame reset to about:blank,
  token released, warning modal, overlay off).

Scripts-off has no script-driven nav at all — the bare sandbox also refuses
`<meta http-equiv="refresh">` redirects (spec-gated on the `allow-scripts` keyword); scripts-on
external navs are blocked in flight by the host `frame-src 'self'` (the blocked nav's error load then
hits the stale record → contained); same-origin OUT-of-tree navs (incl. `<meta refresh>`) commit,
leave the record stale → contained.

While the preview is on another in-tree page, live refresh is paused (re-serving would kick the user
back to the doc); returning to the doc (its dir URL or self-reference → `isDoc`) re-enables it. An
edit arriving while an allow-list decision is in flight (the frame rendered, but the outcome isn't
known yet) is HELD and re-evaluated on resolution — refreshed if it lands on the doc (incl. the
return-to-doc window, where the frame still came from a sibling), superseded by the skip if it lands
on a sibling — so a refresh can never yank the frame back mid-navigation.

**Known residual:** a scripts-on document that DYNAMICALLY loads an in-tree nested iframe after
install (invisibly bumping `seq`), then escapes same-origin, can false-allow — bounded (cross-origin
still blocked in flight; permissive posture opted into open network).

## Tests

Probes in `frontend/tests/e2e/html-preview.spec.ts` (egress listener + scripts-off/scripts-on mode
tests, incl. meta-refresh and in-tree navigation probes) and the extension's colocated iframe harness
(`MIKE-extensions/ui/html-preview/tests/`).
