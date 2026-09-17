# Extension lifecycle: install, image, delete, repair

The rules are in `AGENTS.md → Extensions`. Dependency-cache grants have their own file:
`docs/extension-dependency-cache.md`.

## Install is atomic and answers as a stream

- The image is built **before** `store.Create` (`installInBackground`); a `type: lsp` entry with neither
  image nor Dockerfile is refused (`installNeedsImage`); a failure after `CheckoutExtension` deletes the
  checkout through a deferred guard — a directory with no row is listed by `GET /api/extensions/local` as
  installable under the manifest it shipped with.
- A **local folder may declare `"image"`** in its manifest (`ExtensionManifest.Image`, carried by the one
  mapping `localRegistryEntry` that feeds both the browse listing and the install, since a folder has no
  registry index entry to carry one; never pulled at install, only on first sandbox start).
- `installDockerfile` treats a read failure as an error, never as "no Dockerfile". The update path refuses
  an LSP whose new commit ships neither, instead of keeping the previous commit's image under new code. A
  record that predates the rule is repaired lazily at `Create` (`persistDerivedImage` →
  `ExtensionStore.SetImageRef`).
- The three long-running operations (`POST /install`, `POST /install-local`, `POST /{id}/update`) answer
  as **SSE** streams (`streamOperation`). There is **no** build-progress route: build/update output
  reaches the browser as `build_progress` **task WebSocket** events (`types.WSTypeBuildProgress`, from
  `exec/loop_helpers.go`).
- `streamOperation` **keeps draining the operation's channel after the client goes away**: the work runs
  in its own goroutine, so a reader that stops reading strands it mid-flight — no row is ever written and
  the name stays claimed in `inFlightOps`, which answers "install already in progress" to every later
  attempt with nothing in the UI able to say why. Test: `stream_client_gone_test.go`.
- `installInBackground` emits its **terminal event last**, after the rollback of its own checkout and
  after `inFlightOps.end`, because a client told "install failed" immediately re-lists or retries: with
  the event first, `GET /api/extensions/local` offers the ghost folder the rollback has not deleted yet
  and the retry is refused as already in progress. Test: `install_report_order_test.go` (20 rounds — one
  round passes whenever the removal happens to win).
- Tests: `backend/extensions/image_build_test.go`, `local_install_image_test.go`, `fake_podman_test.go`.

## There is no default image

The image is either what the record declares or what a successful build of the extension's own Dockerfile
produces; anything else is an error from `extensionImageSource` (`backend/extensions/sandbox.go`) with no
container created. `ubuntu:latest` behind an empty `image_ref` produces the worst failure a language
server can have: a container that starts, reports running and holds no server, so the editor's LSP never
resolves and nothing says why.

## The build context is created by this package, and it is the jail

`BuildExtensionImage` (`backend/extensions/image_build.go`) stages a temp context holding the Dockerfile
and the bridge at `sandbox/sandbox-bridge`, then runs
`podman build -t <ref> -f <ctx>/Dockerfile <ctx>`. It never uses the server's working directory as the
context.

- Both published extension Dockerfiles `COPY sandbox/sandbox-bridge`, so the context must contain that
  path — and building from the CWD lets an unrelated `.dockerignore` filter the bridge out of it
  (`no items matching glob … 1 filtered out using .dockerignore`), which breaks every language server at
  once.
- The context is also the jail: a Dockerfile fetched from a registry must not be able to
  `COPY data/kanban.db` or `COPY .git`, which the repo root as context would have allowed but for ignore
  rules written for a different build.
- `TestBuildExtensionImage_RealBuildSurvivesADockerignoreBelow` holds that against real buildah.

## Deleting an extension deletes its directory

`extensions/<author>/<name>` is not a cache: UI code is served from it, agent adapters' WASM and binaries
load from it, and `GET /api/extensions/local` lists it as installable.

- The browser half is the service worker: extension assets are cached `NetworkFirst` as the offline
  fallback, so `uiInjector.registerFromExtensions` drops the `kanban-extensions` cache whenever the
  installed set moved (`services/sw-cache.ts`) — a cached body is the extension's *previous* code, running
  in a frame the host armed and granted.
- That cache covers the `/extensions/` tree and nothing else. The matchers are
  `frontend/scripts/sw-routes.ts` — workbox answers with the **first** matching route, and it reads no
  `Cache-Control` (`cacheableResponse`'s status list is the only gate), so which URLs the worker takes
  over is decided there and nowhere else. `/api/extensions/...` is not the tree: `NetworkFirst` answers
  from the stored body whenever the network is slow, and an API answer about what is installed has no
  usable old version. Tests: `src/lib/services/__tests__/sw-routes.unit.ts`.
- A deleted row with its files still present is a deleted extension that keeps executing and returns under
  its own name with the manifest it was installed with.
- `removeExtensionFiles` (`backend/extensions/remove.go`) derives the path from the **stored record**
  through `extensionDir` — never from a request field — and the delete is contained twice: lexically
  inside the extensions root, and again with symlinks resolved (an `extensions/<author>` that links
  elsewhere must not delete what it points at).
- Because every reader derives the directory that way, `install-local` **refuses** a folder it cannot read
  back: a local install is materialized in place while the record stores the *slugged* author, so an
  author folder that is not its own slug would install "successfully" and then 404 every asset it serves
  (a translation catalog, a stylesheet, a UI module) with the extension installed and enabled. An emptied
  author folder goes too.
- Removal runs **after** the row delete, so an unremovable directory cannot make an extension
  undeletable; a failure is reported as `leftover` in the response (rendered by `ExtensionsTab`) and a
  `LEFTOVER FILES` log line, because invisible files are still served.
- Updates are unaffected — `CheckoutExtension` removes the destination first.
- Test: `backend/extensions/delete_files_test.go`.

## A row that outlived its files is repaired at startup, from its own pin

A missing directory with its row present is an enabled extension that silently does nothing — a UI
extension's iframe 404s, so its document never boots or registers what it exists for, while
`GET /api/extensions` still reports it installed and enabled.

`RepairMissingFiles` (`backend/extensions/repair.go`) runs once at startup from `main.go` (off the boot
path, never joined to shutdown), derives each directory through the same `extensionDir`, reads "no
readable `manifest.json`" as missing (a half-written checkout has a directory), and re-checks out **the
record's** pinned commit via `CheckoutExtensionContext` — never a newer one, which is an update the user
asks for. A row with no `repo`+`commit` (`local` installs are materialized in place, so their files *were*
the install) or a name that is not a directory name is logged, not fetched. Detection is startup-only, so
files deleted while the server runs stay broken until the next start. Test:
`backend/extensions/repair_test.go`.

## Egress and REST surface

- `POST /{id}/proxy` is the egress path an **extension's own script** calls; a contributed tool and the
  pre-save probe reach the network through the same policy in `extensions.Egress`
  (`backend/extensions/egress.go`), which enforces the manifest's domain allowlist beside the operator's
  configured address, resolves the manifest's header templates, refuses redirects, caps the response and
  never echoes the upstream's `Content-Type` — `proxyContentType` keeps `application/json` because that is
  what the client branches on and answers `text/plain` for everything else, since the bytes are served from
  MIKE's own origin. The proxy route passes **no address grants**: loopback is reachable only for an address
  a person typed, which is `docs/extension-tools.md`.
- `POST /{id}/test-url` is a probe of an address a person is about to save: same policy, and it writes
  nothing, so it is classified as a request-scoped read (`server/sideeffects.go`).
- Routes: `/api/extensions` (list/get/update/delete), `PATCH /{id}/enabled`, `GET /registry`,
  `GET /local`, `GET /check-updates`, `GET|PUT|DELETE /{id}/storage` (the namespaced KV store behind
  `extHost.storage.*`), `PUT /api/extensions/{id}/dependency-cache`.
- Outbound HTTP goes through `backend/httputil/client.go`: `NewClient` follows redirects,
  `NewSingleAttemptClient` refuses them. The refusal is the security control — the domain allowlist is
  checked against the URL the caller asked for, so following a redirect reaches a host nothing validated.
  Don't build an `http.Client` at a call site. Test: `backend/httputil/client_test.go`.
- A call to a URL someone else chose is `httputil.DoOutbound` (`outbound.go`): it is the single place that
  builds the request, applies the caller's headers and reads the answer under `MaxUpstreamBodyBytes`. The
  `http` archgate rule is why `net/http` stays in `backend/httputil` and `backend/server`, and why a domain
  package holds a typed answer instead.
- The egress transport takes **no environment proxy** (`egressTransport` sets `Proxy = nil`): the peer of
  a proxied connection is the proxy, so the address rule would clear the proxy and say nothing about the
  extension's target. Test: `backend/httputil/egress_test.go`.

## LSP capability gates

`manifest.capabilities.lsp` — both default to enabled, both toggled per extension in Settings →
Extensions, and **independent** of each other.

- `editor_enabled` gates the *browser's* LSP features: the server only stores and serves the flag, while
  `extension-manager.getEditorLSPExtensions()` filters and `lsp-connection-manager.isLSPAvailable()`
  decides whether a channel opens. **It is not a highlighting gate**: `editor-language.resolveFile()`
  answers the file type, wire id and syntax profile from `getEnabledLSPExtensions()` (installed and
  switched on, gate or no gate), because a grammar is data the manifest shipped and the toggle is sold as
  "diagnostics, completions, go-to-definition, and hover". The gate reaches the rest of the editor as the
  single `ResolvedFile.editorLSP` field, which nothing but `isLSPAvailable` reads.
- `tool_calls_enabled` gates the *agent's* LSP tools and is enforced server-side
  (`ExtensionRegistry.IsToolCallsEnabledForFile`, checked before an LSP tool runs).

## Changelogs

An extension may ship `CHANGELOG.jsonl` next to its manifest (one JSON object per line, oldest→newest:
`{"hash", "version", "description"}`), published to `<registry-base>/<author-slug>/<name>`.
`GET /api/extensions/check-updates` returns entries newer than the installed commit (anchored on the
first hash match); no match / no file / fetch error → empty `changelog` (best-effort, never blocks update
detection). UI: `.update-changelog` in the "Updates Available" section of `ExtensionsTab.svelte`. Key
files: `backend/extensions/changelog.go`, `FetchChangelog` (`registry.go`), `HandleCheckUpdates`, e2e
`extension-changelog.spec.ts`.

## UI injection scopes

Components call `renderSlot(scope, context)` on mount. Scopes → components: `task-card`→Card,
`file-editor`→FileEditor, `project-board`→Board, `task-chat`→TaskChat, `project-sidebar`→Sidebar,
`file-browser`→dir picker modal, `create-task`→CreateTaskModal. The extension *module* contract, the CSP
and the `exthello` handshake: `docs/ui-extensions.md`.

## Key files

`backend/extensions/` — domain types, `store.go` (SQLite), `registry.go`
(FetchIndex/FetchManifest/PullImage/BuildImage), `sandbox.go` (refcount lifecycle), `handler.go` (REST),
`changelog.go`, `manifest_validate.go`. `backend/lsp/extension_manager.go` — extension-driven LSP
discovery (LSP availability = installed extensions). Frontend:
`services/extension-manager.svelte.ts`, `ui-injector.svelte.ts`, `$lib/api/extensions.ts`,
`ConfigModal/ExtensionsTab.svelte`.
