# Configuration: flags, environment, startup resolution

The rules are in `AGENTS.md → Configuration`. This is the reference for every variable, flag, and the resolution process.

## Resolution

Configuration is resolved **once** at startup. Consumers receive the final decision and do not re-read variables.

- **Resolution Order:** Flag $\rightarrow$ Environment $\rightarrow$ Default. All values are resolved in `backend/startup` and injected as immutable values into consumers.
- **Failure:** Malformed values cause startup to fail immediately, naming the variable. No side effects (e.g., creating data directories or TLS material) occur before this check.
- **Empty Values:** Set-but-empty variables are treated as unset.
- **Opt-in Switches:** These must be exactly `1`. Other truthy values (e.g., `true`, `yes`) are ignored and trigger a boot warning.
- **Sandbox Cleanup:** `MIKE_SANDBOX_SKIP_STARTUP_CLEANUP` suppresses the stale container sweep; any non-empty value disables the sweep.
- **Access:** Only `main.go` and `backend/startup` read `MIKE_*` variables. Exceptions include the git author identity and the operator's shell environment.
- **Warnings:** Soft refusals are printed once and stored in `startup.Config.Warnings`.

## Variables with a `--flag` twin

Seven of the ten flags have environment twins; **flags win**:
- `--port`/`MIKE_PORT`
- `--listen`/`MIKE_LISTEN`
- `--data-dir`/`MIKE_DATA_DIR`
- `--worktrees-dir`/`MIKE_WORKTREES_DIR`
- `--frontend-dist`/`MIKE_FRONTEND_DIST` (dev-only)
- `--allowed-hosts`/`MIKE_ALLOWED_HOSTS`
- `--log-level`/`MIKE_LOG_LEVEL`

The following have no environment twin:
- `--tls-cert`, `--tls-key`
- `--reset-passkeys` (one-shot operation; deletes credentials)

Switches that widen permissions or point to host paths have only one spelling to ensure the operator is aware of them. `--worktrees-dir` is a runtime override and is not persisted to the database.

## The table

| Env Var | Description |
|---------|-------------|
| `MIKE_ENCRYPTION_KEY` | Required. Combined with the `installation_salt` (written on first boot), it derives the AES-256-GCM data key once per process using argon2id. If the key is incorrect, startup fails and names the affected providers. |
| `MIKE_LOGIN_RATE_LIMIT` | Ceremony and pairing-code budget per client IP per minute (default `5`). Cannot be disabled; invalid values fall back to `5`. |
| `MIKE_REGISTRY_URL` | Extension registry index URL. Falls back to `https://registry.getmike.dev/registry.json`. Bare paths are resolved as local files relative to the CWD. |
| `MIKE_UPDATE_URL` | Release manifest URL. Falls back to `https://getmike.dev/download/version.json`. Must use `https` (except loopback) and include a host. **`docs/auto-update.md`**. |
| `MIKE_DISABLE_UPDATES` | Exactly `1` disables all release updates (checks, downloads, and installs). Overrides stored preferences. **`docs/auto-update.md`**. |
| `MIKE_TRUST_PROXY` | Exactly `1` enables `X-Forwarded-For` and `X-Forwarded-Proto` headers. Use only behind a trusted reverse proxy. |
| `MIKE_RESTRICT_MCP` | Exactly `1` freezes the MCP server registry; no servers can be added, edited, or removed via Settings. **`docs/mcp.md`**. |
| `MIKE_RESTRICT_STAT_RESET` | Exactly `1` prevents resetting cumulative token counters via API or Settings. |
| `MIKE_LISTEN` | Socket address for the API. Defaults to `127.0.0.1`. Use `0.0.0.0` or `::` for all interfaces. |
| `MIKE_ALLOWED_HOSTS` | Extra comma-separated hostnames accepted in the `Host` header. `localhost`, `127.0.0.1`, `::1` and the listen host are accepted without being listed, so unset means loopback-only; the container entrypoint defaults it to `localhost`. Rejections result in HTTP 421. **`docs/tls.md`**. |
| `MIKE_EXT_MOUNT_ROOTS` | `os.PathList` of extra host roots that extension sandboxes may bind-mount. |
| `MIKE_LOG_LEVEL` | Log verbosity: `debug`, `info` (default), `warn`, or `error`. Invalid values cause startup to fail. |
| `MIKE_AGENT_PATH` | Absolute path to the `sandbox-bridge` binary. Tried before executable-relative searches. |
| `MIKE_SANDBOX_SKIP_STARTUP_CLEANUP` | Any non-empty value suppresses the boot sweep that deletes stale `kanban-sandbox-*` and `kanban-ext-*` containers. |
| `MIKE_MEMORY_BUDGET_PCT` | Percentage of installed RAM (default `90`) available for all sandboxes. Invalid values (outside `1-100`) cause startup to fail. **`docs/sandbox-runtime.md`**. |
| `MIKE_GC_DRY_RUN` | Exactly `1` reports branch GC decisions without deleting data. |
| `MIKE_PPROF` | Exactly `1` serves `net/http/pprof` on `localhost:6060`. |
| `MIKE_FRONTEND_DIST` | **Dev only.** Serve SPA from this directory (default `./frontend/dist`) instead of the embedded copy. Refused in `-tags release` binaries. |
| `DANGER_ZONE` | Exactly `1` allows disabling sandboxing for projects. Does not gate execution. |
| `MIKE_ALLOW_ROOT_FS` | Exactly `1` allows no-project file endpoints (e.g., directory picker) to reach any host path. Otherwise jailed to `$HOME` and workspace/extensions dirs. |

## Reachability is not the certificate

Reachability is determined by `MIKE_LISTEN` and Host pinning (`MIKE_ALLOWED_HOSTS`), not the SAN set. See `docs/tls.md` for certificates and `docs/passkeys.md` for credentials.

## Distribution

`make package` creates `dist/mike-server-<version>-<os>-<arch>.tar.gz` and associated checksums/metadata. The version is derived from the git tag or the `VERSION` variable.

**Binary Properties:**
- Built with `CGO_ENABLED=0` (uses modernc.org SQLite).
- Archives are byte-comparable: sorted names, zeroed owner and group, no gzip timestamp, and every
  member's mtime pinned to the release instant. `docs/auto-update.md → The release instant`.
- The SPA is embedded into the binary via `go:embed`. In `-tags release` builds, `MIKE_FRONTEND_DIST` is disabled.
- The prompt templates are embedded the same way and the shipped `prompts/` directory still answers for
  its own names, so an install that loses a template still renders. `docs/i18n.md → Prompt templates`.

**Archive Layout:**
- `mike-server` (executable)
- `extensions/` (README only)
- `prompts/` (the templates a task is prompted from, and `skills/` — the skill packages it is offered)
- `sandbox/` (`sandbox-bridge`)
- `examples/` (the sample sandbox Dockerfiles; the server reads neither of them)
- Licences and README.

Extract, `cd` into the directory, and run.

The version is available via `/api/version`. Checksums are provided in `dist/sha256sum.txt`. See **`docs/auto-update.md`** for details on the update check.
