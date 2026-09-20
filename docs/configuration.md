# Configuration: flags, environment, startup resolution

The rules are in `AGENTS.md → Configuration`. This is the reference: every variable, every flag,
and how a value becomes a decision.

## Resolution

Resolved **once**: a consumer receives the decision and never re-reads a variable where it is used.

- The flag → env → default ladder is resolved **once**, in `backend/startup` (`startup.Resolve` →
  immutable `startup.Config`; `startup.Flags` for the command line). A new variable is added there and
  its **decision** is injected into the consumer, never re-read where it is used.
- A malformed value fails startup naming the variable, and the refusal happens before any side effect:
  a refused boot leaves no data directory, no trace file, no TLS material.
- A set-but-empty variable means *unset*. The two readings of `MIKE_LISTEN=` are "loopback" and "every
  interface".
- An opt-in switch is the **exact value `1`** (`startup.isOne`). `true`, `yes`, `on` and ` 1` do nothing
  and get a boot warning saying so; that is why `fsutil.HostRoot` and `sandbox.DangerZone` take a bool
  instead of reading a variable.
- The one switch held to a different shape is `MIKE_SANDBOX_SKIP_STARTUP_CLEANUP`: it only ever turns a
  destructive sweep **off**, so any non-empty value suppresses it and no warning is owed (a truthy typo
  there costs a stale container, not a live sandbox).
- `main.go` and `backend/startup` are the only readers of `MIKE_*` in a boot or request path; the
  `env` archgate rule holds that. Four baselined exceptions: the encryption key, read per crypto
  operation because its PBKDF2 salt is per value, and `term`'s two reads of the *operator's own* shell
  environment, which is the sanctioned widening.
- Soft refusals the boot prints once arrive as `startup.Config.Warnings`.

## Variables with a `--flag` twin

Eight of the ten flags have an environment twin and **flags win**: `--port`/`MIKE_PORT`,
`--listen`/`MIKE_LISTEN`, `--data-dir`/`MIKE_DATA_DIR`, `--worktrees-dir`/`MIKE_WORKTREES_DIR`,
`--frontend-dist`/`MIKE_FRONTEND_DIST` (dev-only, below), `--no-tls`/`MIKE_TLS`,
`--allowed-hosts`/`MIKE_ALLOWED_HOSTS`, `--log-level`/`MIKE_LOG_LEVEL`. `--tls-cert` and `--tls-key` have
no environment spelling at all: pointing the server at someone's key file is a flag or nothing. Two of
the twins are not name-matched (`--no-tls` ↔ `MIKE_TLS`), so the list is the authority, not the rule "add
`MIKE_`".

A switch that widens a permission or points at a host path has one spelling only, so it cannot
arrive by a route the operator did not read about.

**Names this table does not carry are not live.** A name an older `.env` still carries either appears here
or does nothing at all — except `MIKE_UPDATE_CHECK`, which is read only to warn an `.env` carrying it that it
disabled nothing (`docs/auto-update.md → Who decided`).

`--worktrees-dir` (`MIKE_WORKTREES_DIR`) is a runtime override that is **not** persisted to the DB
`workspace_dir`.

## The table

| Env Var | Description |
|---------|-------------|
| `MIKE_ENCRYPTION_KEY` | Non-blank; derives the AES-256-GCM key for sensitive data. Server refuses to start without it. |
| `MIKE_LOGIN_PASSWORD` | Master password for auth/sessions. Server refuses to start without it. Only its `sha256` digest is kept in memory; a weak one (< 8 chars, or a known default) additionally refuses a non-loopback listen address. |
| `MIKE_REGISTRY_URL` | Extension registry index URL. Sourced **exclusively** from this env var (not DB-backed); falls back to `https://registry.getmike.dev/registry.json`. A bare path (no `http(s)://`/`file://` scheme) is a local file resolved against the CWD. |
| `MIKE_UPDATE_URL` | Release manifest the update check reads. Falls back to `updates.DefaultManifestURL` (`https://getmike.dev/version`); a mirror or a local fixture names itself here. Read **once** at startup and handed to the service as a value. Must be `https` (loopback may be `http`) with a host; anything else **refuses the boot** naming the variable. An archive the manifest names is fetched only from this same host. **`docs/auto-update.md`**. |
| `MIKE_DISABLE_UPDATES` | The organization-level kill switch for the server's own release updates: exactly `1` and this process will not check for, download, install or restart onto a release, and no browser is asked to opt in (About reads "Updates are managed by your organization"). `0` or unset leaves the decision to the `update_check_enabled` preference; any other value warns at boot that it **disabled nothing**. It outranks the stored preference; the ladder is in `docs/auto-update.md → Who decided: the ladder`. Applies to server releases only — the extension registry is untouched. **`docs/auto-update.md`**. |
| `MIKE_TRUST_PROXY` | Set to `1` only behind a trusted reverse proxy that strips forwarding headers. Only then are `X-Forwarded-For` (rate-limit IP) and `X-Forwarded-Proto` (HTTPS redirect) honored; unset = ignored so they can't be spoofed. |
| `MIKE_LISTEN` | Which socket the API is reachable on — it **never authorizes a `Host` header**. **Defaults to `127.0.0.1`**: the API is RCE-complete (terminal WS, git ops, file writes), so it is never public by accident. `0.0.0.0`/`::` are the explicit all-interfaces values; any other address listens on that interface only. |
| `MIKE_ALLOWED_HOSTS` | Comma-separated extra hostnames accepted in the `Host` header (DNS-rebinding pinning, `HostPinMiddleware`). Accepted without listing: `localhost`, `127.0.0.1`, `::1`, the listen host. Anything else — a proxy's public domain, `hostname.local`, a LAN IP — gets **HTTP 421** until listed here; a missing/empty `Host` is rejected too. Ports are stripped on both sides. `--allowed-hosts` **overrides** (not extends); `Serve()` logs the effective value and every rejection names both. |
| `MIKE_LOGIN_RATE_LIMIT` | Login budget per client IP per minute (default `5`). **Cannot be disabled:** `0`, negative or unparsable values fall back to `5` with a startup warning, so a typo can't silently drop brute-force protection. |
| `MIKE_EXT_MOUNT_ROOTS` | `os.PathList`-separated list of **extra** host roots an extension sandbox may bind-mount, beyond the implicit ones (workspace dir, registered project paths, extensions dir, system temp dir). Still an allowlist — anything outside it is rejected. |
| `MIKE_LOG_LEVEL` | Log verbosity: `debug`, `info` (default), `warn` or `error`. A value that is not one of those **refuses the boot** naming the variable. Levels are filtered by `pkg/logging`; see *Logging* in `AGENTS.md`. |
| `MIKE_AGENT_PATH` | Absolute path to the `sandbox-bridge` binary, tried before the executable-relative searches. Read **once** at startup and handed to the runtime as `sandboxruntime.Boot.AgentPath`; a wired path that does not exist falls back to the search instead of failing the task. |
| `MIKE_SANDBOX_SKIP_STARTUP_CLEANUP` | Suppresses the boot sweep that deletes stale `kanban-sandbox-*` / `kanban-ext-*` container families — a host-wide delete. Any non-empty value suppresses it. The server sweeps; `sandboxruntime.Boot{}` declines, so a test binary never wipes the host it runs on. |
| `MIKE_MEMORY_BUDGET_PCT` | The share of **installed RAM** the memory budget admits against (default `90`) — the ceiling every sandbox MIKE started shares, counted at each container's `max(held, cap)` plus outstanding reservations plus the server's own resident set. What the rest of the host holds is **not** in that sum, so `MemAvailable` under 5 % of RAM is what pauses tasks on a machine that is full for other reasons. A value that does not parse, or parses outside `1-100`, refuses the boot naming the variable. Env-only: there is no `--memory-budget-pct` flag to reach for. The reported ceiling is in Settings → About. Rule and consequences: `docs/sandbox-runtime.md → The memory budget`. |
| `MIKE_GC_DRY_RUN` | Set to `1` to report the branch GC's decisions without deleting anything. Exact-value-1, with the boot warning for a truthy typo. |
| `MIKE_PPROF` | Set to `1` to serve `net/http/pprof` on `localhost:6060`. A profile can contain API keys and passwords, so it is never on by default and never off the loopback interface. |
| `MIKE_FRONTEND_DIST` | **Development only.** Serve the SPA from this directory (default `./frontend/dist`) instead of the copy compiled into the binary, so `npm run build` reaches a running server with no Go rebuild. It is honoured only when the directory holds an `index.html`; otherwise the compiled-in copy answers and the startup log says which. A `-tags release` binary (`make package`, `make release`, the image) resolves this to nothing and **refuses the boot** when either spelling is given — accepted-and-ignored would read, months later, as a deployment serving its own frontend directory. |
| `MIKE_TLS` | Set to `0` to serve over plain HTTP — the env spelling of `--no-tls`, and only the exact value `0` turns it off. Development and the e2e harness; anything else keeps TLS. |
| `DANGER_ZONE` | Set to `1` to allow **disabling** sandboxing: a project may then be created with sandboxing disabled, or switched to it; without the flag both are 403. It does not gate execution. Resolved once into `startup.Config.DangerZone` and injected as `sandbox.DangerZone` into the two places that decide. Its second meaning (what a host terminal is *given*) is in `docs/confinement.md`. |
| `MIKE_ALLOW_ROOT_FS` | Set to `1` to let the **no-project** file endpoints — the directory picker, its `mkdir`, `git init`, and the add-project/clone target — reach any host path. Unset = they are jailed to `$HOME` plus the workspace and extensions directories declared at startup, and the refusal says so and names this variable. **Only the exact value `1` widens**; `true`, `yes`, `0` and a set-but-empty value all keep the jail, and a set-but-not-`1` value gets a startup warning. Never widens anything project-scoped, and the data directory is not a root either way. The decision is taken once by `startup.Resolve` and handed to `fsutil.NewHostRoot(allowRootFS, …)` (`backend/fsutil/hostroot.go`): a jail that re-read the variable per call could widen between two calls of one request, and the roots quoted in a refusal would not be the roots that refused. |

## Reachability is not the certificate

Reachability is `MIKE_LISTEN` + Host pinning (`MIKE_ALLOWED_HOSTS`), not the SAN set. Weak-password and
non-loopback startup rules, certs and distribution: `docs/tls.md`.

## Distribution

`make package` builds `dist/mike-server-<version>-<os>-<arch>.tar.gz` for `linux/amd64` and
`linux/arm64` (`scripts/package-release.sh`; `make package-amd64` builds one target), and writes
`dist/sha256sum.txt` and `dist/version.json` beside them. `<version>` is `VERSION` when that is a dotted
number and `0.0.0` when the tree names no version, so an untagged checkout never puts a commit hash in a
filename someone runs `sha256sum -c` on. `version.json` is the release manifest the update check reads; its
digests are read out of `sha256sum.txt`, so the two agree by construction (**`docs/auto-update.md`**).

Both binaries in an archive are built for that archive's `GOARCH`, including `sandbox/sandbox-bridge`
whose committed copy belongs to whoever last ran `make bridge`. `CGO_ENABLED=0` throughout — SQLite here
is modernc.org, so the binary needs no glibc match. Archives are byte-comparable between runs (sorted
names, no uid/gid, zeroed mtime, `gzip -n`).

One archive holds `mike-server`, `extensions/` (its README only; the rest of that tree is written by the
installer), `prompts/`, `sandbox/` (`sandbox-bridge` and the two Dockerfiles), the licences and a README,
under one top-level directory. That layout is what the sibling lookups require, not a convention:
`sandbox-bridge` beside the executable or under `sandbox/` (`pkg/sandboxruntime.GetAgentBinaryPath`),
`prompts/` beside the executable (`backend/prompts.resolvePromptDir`), and `extensions/` relative to the
**working directory**. Hence: extract, `cd`, run.

The SPA is compiled into `mike-server` (`assets.go`, `//go:embed all:frontend/dist`), so a package ships
no frontend directory and reads none: both binaries are built `-tags release`, which resolves
`--frontend-dist` / `MIKE_FRONTEND_DIST` to nothing and refuses the boot when either is given
(`backend/startup/frontend_release.go`). The image follows the same rule — its Go stage receives
`frontend/dist` from the web stage because that is what `go:embed` reads (`.dockerignore` keeps it out of
the build context), and the runtime stage ships no frontend directory. `make package` depends on
`make frontend` for that reason, and the committed `frontend/dist/.gitkeep` exists only so `go:embed`
has something to match on a tree that never ran npm. `all:` takes whatever else that directory holds,
so `package-release.sh` refuses to package a tree containing a `*.map` or a stray dotfile — a
sourcemap would publish the sources inside the download.

The version in the filename is stamped into the binary as well (`/api/version`), and its source is a
**git tag**: `git tag v1.2.3 && make package`. An untagged tree describes a commit hash, which is a fine
thing for a developer's binary to carry and not a thing to name a download after, so the filename falls
back to `0.0.0`; `make package VERSION=v1.2.3` overrides without a tag. Nothing in the tree holds a
version constant to bump. The script re-checks both variables it is handed — `VERSION` a dotted number,
`RELEASE_TAGS` a list of build tags — because they reach a filename, a `sed` program and `-ldflags`.

`dist/sha256sum.txt` proves an archive matches the checksum file beside it. Nothing signs the archives,
so that pair is only as trusted as whoever handed them over: the package README says so where it tells
the buyer to run `sha256sum -c`. The update check inherits that limit and says so (**`docs/auto-update.md`**).
