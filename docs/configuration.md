# Configuration: flags, environment, startup resolution

The rules are in `AGENTS.md → Configuration`. This is the reference: every variable, every flag,
and how a value becomes a decision.

## Resolution

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

Nine have one, named after them, and **flags win**: `--port`/`MIKE_PORT`, `--listen`,
`--data-dir`/`MIKE_DATA_DIR`, `--no-tls`, `--tls-cert`/`--tls-key`, `--allowed-hosts`,
`--worktrees-dir`, `--log-level`/`MIKE_LOG_LEVEL`. A switch that widens a permission or points at a host
path has one spelling only, so it cannot arrive by a route the operator did not read about.

`--worktrees-dir` (`MIKE_WORKTREES_DIR`) is a runtime override that is **not** persisted to the DB
`workspace_dir`.

## The table

| Env Var | Description |
|---------|-------------|
| `MIKE_ENCRYPTION_KEY` | Non-blank; derives the AES-256-GCM key for sensitive data. Server refuses to start without it. |
| `MIKE_LOGIN_PASSWORD` | Master password for auth/sessions. Server refuses to start without it. Only its `sha256` digest is kept in memory; a weak one (< 8 chars, or a known default) additionally refuses a non-loopback listen address. |
| `MIKE_REGISTRY_URL` | Extension registry index URL. Sourced **exclusively** from this env var (not DB-backed); falls back to `https://getmike.dev/extensions/registry.json`. A bare path (no `http(s)://`/`file://` scheme) is a local file resolved against the CWD. |
| `MIKE_TRUST_PROXY` | Set to `1` only behind a trusted reverse proxy that strips forwarding headers. Only then are `X-Forwarded-For` (rate-limit IP) and `X-Forwarded-Proto` (HTTPS redirect) honored; unset = ignored so they can't be spoofed. |
| `MIKE_LISTEN` | Which socket the API is reachable on — it **never authorizes a `Host` header**. **Defaults to `127.0.0.1`**: the API is RCE-complete (terminal WS, git ops, file writes), so it is never public by accident. `0.0.0.0`/`::` are the explicit all-interfaces values; any other address listens on that interface only. (Renamed from `--bind`/`MIKE_BIND` — the old names do nothing; a stale `MIKE_BIND=0.0.0.0` comes up loopback-only.) |
| `MIKE_ALLOWED_HOSTS` | Comma-separated extra hostnames accepted in the `Host` header (DNS-rebinding pinning, `HostPinMiddleware`). Accepted without listing: `localhost`, `127.0.0.1`, `::1`, the listen host. Anything else — a proxy's public domain, `hostname.local`, a LAN IP — gets **HTTP 421** until listed here; a missing/empty `Host` is rejected too. Ports are stripped on both sides. `--allowed-hosts` **overrides** (not extends); `Serve()` logs the effective value and every rejection names both. |
| `MIKE_LOGIN_RATE_LIMIT` | Login budget per client IP per minute (default `5`). **Cannot be disabled:** `0`, negative or unparsable values fall back to `5` with a startup warning, so a typo can't silently drop brute-force protection. |
| `MIKE_EXT_MOUNT_ROOTS` | `os.PathList`-separated list of **extra** host roots an extension sandbox may bind-mount, beyond the implicit ones (workspace dir, registered project paths, extensions dir, system temp dir). Still an allowlist — anything outside it is rejected. |
| `MIKE_LOG_LEVEL` | Log verbosity: `debug`, `info` (default), `warn` or `error`. A value that is not one of those **refuses the boot** naming the variable. Levels are filtered by `pkg/logging`; see *Logging* in `AGENTS.md`. |
| `MIKE_AGENT_PATH` | Absolute path to the `sandbox-bridge` binary, tried before the executable-relative searches. Read **once** at startup and handed to the runtime as `sandboxruntime.Boot.AgentPath`; a wired path that does not exist falls back to the search instead of failing the task. |
| `MIKE_SANDBOX_SKIP_STARTUP_CLEANUP` | Suppresses the boot sweep that deletes stale `kanban-sandbox-*` / `kanban-ext-*` container families — a host-wide delete. Any non-empty value suppresses it. The server sweeps; `sandboxruntime.Boot{}` declines, so a test binary never wipes the host it runs on. |
| `MIKE_GC_DRY_RUN` | Set to `1` to report the branch GC's decisions without deleting anything. Exact-value-1, with the boot warning for a truthy typo. |
| `MIKE_PPROF` | Set to `1` to serve `net/http/pprof` on `localhost:6060`. A profile can contain API keys and passwords, so it is never on by default and never off the loopback interface. |
| `MIKE_FRONTEND_DIST` | Where the built SPA is served from (default `./frontend/dist`). Env-only: there is no `--frontend-dist` flag to reach for. |
| `MIKE_TLS` | Set to `0` to serve over plain HTTP — the env spelling of `--no-tls`, and only the exact value `0` turns it off. Development and the e2e harness; anything else keeps TLS. |
| `DANGER_ZONE` | Set to `1` to allow **disabling** sandboxing: a project may then be created with sandboxing disabled, or switched to it; without the flag both are 403. It does not gate execution. Resolved once into `startup.Config.DangerZone` and injected as `sandbox.DangerZone` into the two places that decide. Its second meaning (what a host terminal is *given*) is in `docs/confinement.md`. |
| `MIKE_ALLOW_ROOT_FS` | Set to `1` to let the **no-project** file endpoints — the directory picker, its `mkdir`, `git init`, and the add-project/clone target — reach any host path. Unset = they are jailed to `$HOME` plus the workspace and extensions directories declared at startup, and the refusal says so and names this variable. **Only the exact value `1` widens**; `true`, `yes`, `0` and a set-but-empty value all keep the jail, and a set-but-not-`1` value gets a startup warning. Never widens anything project-scoped, and the data directory is not a root either way. The decision is taken once by `startup.Resolve` and handed to `fsutil.NewHostRoot(allowRootFS, …)` (`backend/fsutil/hostroot.go`): a jail that re-read the variable per call could widen between two calls of one request, and the roots quoted in a refusal would not be the roots that refused. |

## Reachability is not the certificate

Reachability is `MIKE_LISTEN` + Host pinning (`MIKE_ALLOWED_HOSTS`), not the SAN set. Weak-password and
non-loopback startup rules, certs and distribution: `docs/tls.md`.
