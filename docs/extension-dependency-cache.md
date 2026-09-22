# Extension dependency caches

How an LSP extension gets its resolved dependencies mounted read-only, who grants
the host directory, and what the mount must never reach. Read before touching
`backend/extensions/sandbox.go`, `registry.go`, or the extension Edit dialog.

## Declaration and grant

- **One derivation, both sides.** `dependencyBind` (`backend/extensions/sandbox.go`) produces the `-v` spec and `-e` assignment for a create *and* the snapshot `MountsCompatible` compares against (`withDependencyMount`/`withDependencyEnv`, `DesiredDependencyMount`). Two derivations drift, and a container predating a grant would be called compatible with it — leaving the grant doing nothing forever.
- **The grant reaches that extension's sandbox only.** It mounts `ro`, and `SandboxOpts` (`backend/sandbox/sandbox.go`) has **no field for it**: tasks, sandboxed terminals and the project container never bind it. They used to, read-write, at the path where the project image keeps its own module cache (`/go/pkg/mod` in `golang:1.25`) — one install-wide text field as a host write surface for every project's code. A project that wants a task to fill a dependency directory says so in its own Sandbox Configuration.
- **Set at container creation, never per connection.** The extension's `environment.check` is an exec in that container and must be told the same thing the server is.
- **A grant is a root for one extension only.** `MountPolicyForExtension(ext)` adds it; `MountPolicy()` — what install and `PUT /api/extensions/{id}` validate against — never contains anyone's grant, so an extension cannot self-grant. `ValidateDependencyGrant` is deliberately not `MountPolicy.ValidateHostPath` (the directory lives outside that allowlist): absolute, no `..`, symlinks resolved before storing, not `/`, existing as a directory, re-checked at bind time so podman cannot recreate a deleted one root-owned.
- **Core's container path is not borrowable.** `adaptProjectMounts` drops a project mount that wants it (`isReservedContainerPath`) and `buildExtensionCreateArgs` drops a manifest mount that wants it: two `-v` specs on one path make podman refuse the create outright. `extensions.ShadowsToolState` is the single rule for "that is the server's own writable state" — a server with a read-only cache publishes nothing about a file it cannot load, so a broken file is reported clean.
- **An empty string revokes a grant** on `PUT /api/extensions/{id}/dependency-cache`, and also stops the sandbox (the mount set is fixed at creation). Reapplying the same path writes and broadcasts nothing. The Edit dialog saves the grant before `PUT /api/extensions/{id}`, so a refused path cannot leave the rest of the dialog saved.
- **Two declaration shapes, one reader.** The registry index projects the declaration up to `capabilities.lsp.dependency_cache` (`scripts/gen-registry.py`); `GET /api/extensions/local` answers with the manifest as written. `dependencyCacheDeclaration()` / `requiresSetupAfterInstall()` (`frontend/src/lib/utils/extension-permissions.ts`) are the only readers; `needsDependencyGrant(ext)` is the *installed* card's state, since only it has a stored record to be missing a grant — Browse has none, so its badge is a static span, never a button that opens nothing. A declaration is `omitempty` and a grant is never required or auto-detected — but the sandbox runs `--network none` with no cache, so without one a workspace with third-party imports cannot be resolved (gopls: `BrokenImport`, `UndeclaredImportedName`, `Unavailable`). The alternative is the project **image** carrying them, which is what `environment.check` tests.

## The `environment` declaration
**`environment` is declared; core does not enforce it yet.** `capabilities.lsp.sandbox.environment: {check, suppress_codes, note}` says how to tell whether the extension can resolve a workspace, and which diagnostic codes mean "could not load" rather than "this code is wrong". Nothing calls `ExtensionLSPManager.Environment` (`backend/lsp/environment.go`): no verdict is recorded, no finding is dropped, and the `environment` field of `GET /api/extensions` always answers healthy. What exists is sound - `check` is a bridge `bash` exec in the extension's own sandbox with cwd = that workspace's container path (`envCheckCommand` quotes argv through the lexer rules, so a manifest cannot become shell syntax), verdicts cached 60 s per workspace. **A check must be able to fail:** `go list -e -deps ./...` exits 0 on a graph it could not load, so it reported healthy from an empty module cache and a full one alike; the bundled gopls check is `GOPROXY=off go list -deps ./...` (0 with the cache mounted, 1 without). Either call it from `backend/exec/diagnostics.go` or delete the chain - do not describe it as live. `reload_files` (e.g. `go.mod`) **is** wired: a turn that changes one recycles **that workspace's** connection only, because a server over a bind mount gets no file-watch events.

## Retired knobs - do not reintroduce them
- **`read_only` on the declaration** does not exist: the mount is read-only by definition, and the mode is decided in core, not offered. A stale manifest or stored record that still carries the key decodes and the key is ignored.
- **`detect`, `sandbox_probe` and the `LSPParams.Env` wire field were removed** with the "discover the cache path" design. A discovered path needs a container to ask it in, and puts extension-supplied stdout on the host command line; the grant replaced both.

## The two fields, and why a declaration is optional but loud

The extension declares `capabilities.lsp.sandbox.dependency_cache: {container_path, env}`; the host
directory is a **human's** choice, configured in that extension's Edit dialog and stored beside the
declaration as `sandbox_config.dependency_host_path`. Two fields, because the two origins are the security
argument, and a manifest carrying it is **refused**, not stripped.

- **Never discover where the cache lives.** A discovered path needs a container to ask it in, so the first
  turn of every workspace pays for a server started to answer `go env GOMODCACHE` — and it puts
  extension-supplied stdout on the host command line.
- **Core's container path is not borrowable** (`isReservedContainerPath`), because two `-v` specs on one
  path make podman refuse the whole create.
- **A grant is revoked by an empty string** on `PUT /{id}/dependency-cache`, which also stops the sandbox
  (the mount set is fixed at creation).
- **A declaration is optional, and its absence is loud.** The sandbox runs `--network none` with no cache,
  so without a grant a workspace with third-party imports cannot be resolved and every turn argues with
  phantom errors.
- `environment: {check, suppress_codes, note}` is **declared but not enforced** — nothing calls
  `ExtensionLSPManager.Environment`, so the field always answers healthy; a check must be able to fail
  (gopls: `GOPROXY=off go list -deps ./...`). `reload_files` **is** wired.
- Tests: `dependency_manifest_test.go`, `dependency_grant_test.go`, `dependency_policy_test.go`,
  `dependency_mount_test.go`, `bundled_manifests_test.go`, `reload_env_test.go`,
  `backend/sandbox/containerenv_test.go`, `backend/exec/sandbox_create_opts_test.go`,
  `backend/term/terminal_test.go`; UI states in
  `frontend/src/lib/utils/__tests__/extension-permissions.unit.ts` and
  `frontend/src/lib/components/ConfigModal/__tests__/extensions-dependency-cache.unit.svelte.ts`.

## The second two-origins grant: an agent's configuration folder

`PUT /api/extensions/{id}/agent-mount` (`backend/agent-extensions.md → A manifest's sandbox mount is a
proposal`) is the same shape — the manifest names the container path, a person names the host path — with
three differences worth knowing when reading either one:

- **`rw` is possible**, and the mode is the person's choice; a dependency directory is always bound
  read-only, however the manifest asks.
- **It reaches task containers, not the extension's own.** An agent extension runs no container of its
  own, so the route stops nothing: the executor recreates the containers of the tasks that run the agent
  when the grant changes. The dependency grant stops one running sandbox and is applied at the next use.
- **The directory may be missing**, when something above it exists — an agent creates its own folder on
  first login — where a missing cache is refused ("create it first"), because a task's download command is
  the way a cache gets filled and there is no equivalent for a login.

Both go through the same shape rule (`checkGrantShape`), each with its own wording, and both refuse the
credential stores, the homes and the system trees: a grant is readable from a container that runs
extension-supplied or agent-supplied code, and what that code does with a file it can open is nobody's
promise. Tests: `backend/extensions/agent_mount_test.go`, `backend/extensions/dependency_grant_test.go`.
## Checking this

`go test ./backend/extensions/ ./pkg/sandboxruntime/` covers the two fields, the read-only mount and the
capability that refuses without one.
