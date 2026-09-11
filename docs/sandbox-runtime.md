# Sandbox runtime: boot, containers, podman arguments

The rules are in `AGENTS.md → Sandboxing`. This is how the runtime implements them, and what was
measured on a real host.

## Boot decisions are one injected value

- `sandboxruntime.Boot{SweepStaleContainersAtBoot, AgentPath}` is built once in `server.NewServer` from
  `startup.Config` and handed to `NewSandboxManager`, `NewExtensionSandboxManager`,
  `NewRegistryClientWithBoot` and `sandboxruntime.NewRuntimeWithBoot` — the last is the **only** Runtime
  constructor, so there is no boot-less form to forget.
- The zero `Boot` **declines** the host sweep, so a test binary or an ad-hoc caller cannot delete a live
  server's containers by forgetting a variable.
- An image build needs the bridge path too (the registry client stages it into the build context), which
  is why the boot reaches `NewRegistryClientWithBoot` and not only the two managers.
- Tests: `pkg/sandboxruntime/runtime_test.go`
  (`TestBootDeclinesTheHostSweepByDefault`, `TestGetAgentBinaryPathPrefersTheWiredPath`).

## One probe, one sweep

- `sandboxruntime.GVisorAvailable(tag)` backs both `SandboxManager.checkAvailable` and the extension
  manager's probe — they must agree, or a task sandbox and an extension sandbox disagree about whether
  the host can run gVisor at all.
- `Runtime.CleanupStaleContainers(tag, nameFilter)` is the shared best-effort, 15 s-bounded startup sweep
  (prefixes `kanban-sandbox-` / `kanban-ext-`); neither manager may re-implement the list/parse/remove
  loop.
- `staleContainerNames` skips containers that are **running**: the sweep removes leftovers, never a live
  sandbox. That is why a container that outlives its task row survives a restart, and why task and
  project deletion are the things that must stop containers (`docs/storage.md`,
  `AGENTS.md → Deletion`).
- `backend/sandbox`'s `TestMain` calls its own `cleanupStaleTestContainers`, which `podman rm -f`s every
  `kanban-sandbox-*` on the host, running or not — so a live server's task and project sandboxes are
  deleted by `go test ./backend/sandbox/` even when it runs no test at all. Running this package's tests
  beside a live server is therefore destructive. Scoping the sweep to a test-owned prefix is the fix and
  is not taken, because not every test names its container `test-*`.

## The container environment

`containerEnvArgs` (`backend/sandbox/sandbox.go`) turns the project's Sandbox Configuration envs plus an
agent extension's declared `sandbox_env` into `-e` flags, project config appended last so it wins a
clash.

- **A bare `-e NAME` is unrepresentable.** Every assignment comes from
  `types.SandboxEnvArgs([]types.SandboxEnv)` (`backend/types/env.go`), which can only emit `KEY=VALUE`
  and errors on a malformed key, so `Exec`/`ExecWithInput`/`execInternal` take `[]types.SandboxEnv`
  rather than `[]string`. `podman -e NAME` copies the *client's* value of `NAME` into the container, so
  a project that declared a variable's name without a value exported whatever the server had.
- `Create` refuses the container on a malformed declaration, and `Exec` validates before the container
  lookup so the cause is named.
- Invent nothing else: no hardcoded `PATH` (an `-e` overrides the image's `ENV PATH`, which dropped a
  custom image's tooling) and no container-wide `TERM` (the terminal sets `TERM` on its own
  `podman exec`, `shellExecArgs`: it belongs to the TTY session, not the sandbox).
- Tests: `backend/sandbox/containerenv_test.go`, `backend/sandbox/hostenv_test.go`. The last runs a real
  gVisor container and asserts a host variable reaches it **only** if allowlisted to the podman client —
  the probe variable must be one a host could set, or the test passes vacuously.

## Podman argument construction

`ValidateContainerPath`, `ValidateCapability`/`ValidateCapabilities`, `GVisorIsolationForCaps` and the
`GVisorCreateArgs`/`AppendCapabilities` pair live in `pkg/sandboxruntime`, next to
`ValidateProjectName`, and every caller that builds container arguments goes through them:
`extensions/mount_policy.go` delegates, `sandbox.Create` refuses before the container exists, and
`types.ValidateSandboxMount`/`ValidateSandboxAdditions` check a project's Sandbox Configuration on
create and `PUT` (400 naming the field).

- `GVisorCreateArgs` is the one copy of the posture both builders begin from (byte-identical up to their
  own `--workdir` / `--network none`), and `AppendCapabilities` is the only way to append a `--cap-add`:
  it runs `GVisorIsolationForCaps` (runsc **and** `--cap-drop ALL`) first and refuses the whole grant
  otherwise, so no ordering mistake can reach podman.
- Name shape stays the caller's and is answered differently on purpose: a task refuses its whole
  capability list (a project row is operator configuration), an extension sandbox skips the one
  malformed name and grants the rest.
- Because a project row can predate the rule, `Create` refuses again at container creation rather than
  trusting stored config.
- **`podman build` takes no `--runtime` flag** (`backend/extensions/image_build.go`), so an extension
  Dockerfile runs on the host runtime with podman's default caps — the one place "gVisor runs it" is
  not true.
- Tests: `pkg/sandboxruntime/podman_args_test.go`, `backend/sandbox/create_args_test.go`,
  `backend/extensions/sandbox_test.go`, `backend/managers/project_sandbox_settings_test.go`,
  `TestTaskSandboxIsolatesWhatACapabilityGrantAssumes`, `TestBuildExtensionCreateArgs_CapabilityPosture`,
  and each builder's `…BeginWithTheSharedPreface` test, which fails the day one re-inlines the posture.

### There is no capability denylist, and what replaced it

Measured on runsc release-20260721.0: a guest with `SYS_ADMIN` cannot write through an `ro` bind mount
(its remount flips its own mount table to `rw` and the gofer still refuses), sees only its own PIDs, and
its `mount --bind / x` exposes its own overlay, not the host tree. So a capability is judged by its
**shape** — it lands in a guest that started from `--cap-drop ALL`, which makes `SYS_ADMIN` as harmless
a name as `CHOWN` — while `--privileged` is an *argument* and is refused at install and on
`PUT /api/extensions/{id}`.

## Extension sandbox mounts

`backend/extensions/mount_policy.go`:

- Host mounts must resolve inside the policy's roots: workspace dir, registered project paths,
  extensions dir, system temp dir, `MIKE_EXT_MOUNT_ROOTS`.
- **Only the extensions dir, the temp dir and the `MIKE_EXT_MOUNT_ROOTS` extras may be mounted
  read-write.** The workspace dir, the registered checkouts and anything borrowed from a project's
  Sandbox Configuration come out `ro` however the manifest asks (`MountPolicy.ForceReadOnly`): a
  manifest is registry text and the workspace holds every task's worktree.
- Container paths must match `^/[A-Za-z0-9._/-]+$` — `:` or `,` would rewrite the podman mount spec.
- Enforced on `PUT /api/extensions/{id}` **and** at install time: a manifest that self-grants a bad
  sandbox is rejected, not persisted.
- `ExtensionSandboxManager.MountPolicy()` adds the host paths every project declares
  (`ListProjectSandboxMountPaths`) as roots and pins anything under them — plus the workspace dir and
  every registered project path — to `ro`. `withExtraRoots` carries those pins, so widening the root set
  for one handler cannot un-pin another root. `MountPolicyForExtension` adds a grant only for its own
  extension, so an extension cannot self-grant one, and the policy that install and `PUT` validate
  against never contains anyone's grant (but does contain the borrowed roots, which is why the read-only
  pin, not the root list, is what stops a manifest turning one into a write). `adaptProjectMounts`
  mirrors the *union* over projects (global paths like `/go` for everyone, task-root-relative ones only
  for their owning workspace), so a new project changes the mount set once instead of on every switch.
- Tests: `backend/extensions/mount_read_only_test.go`.

## Podman subprocesses

- Every podman subprocess comes from `sandboxruntime.PodmanCommand`/`PodmanCommandContext`, which set
  `Env: PodmanEnv()` and `cmd.WaitDelay` (`PodmanWaitDelay`, 10 s) together.
  `TestPodmanCommandIsTheOnlySpawnPoint` walks the tree and fails on any other podman spawn, because a
  spawn outside it has neither the WaitDelay nor the scrubbed environment. It bites because the bridge
  connection's LSP child inherits those very pipes.
- Where no caller can cancel, `subproc.Watch` reports instead of bounding: 30 s for container
  management, 2 min for `pull`, 5 min for `build`. The long-lived bridge `podman exec` is not watched,
  because a connection open for hours is not a slow operation. A wedged rootless podman holding its own
  database lock is otherwise a caller that never returns with nothing in the log naming what it waits
  on. This is the same instrument the git-family locks use: report, keep waiting. No podman operation
  has a deadline.
- Shutdown order follows from that: websocket watchers → terminal sessions → lsp → extensions → sandbox
  containers → database, so a session's shell is killed while its container can still receive it.
- Tests: `backend/sandbox/podman_wait_test.go`, `TestTerminalManagerClose`, `pkg/subproc/subproc_test.go`,
  `TestPodmanLabelNamesTheOperation`, `pkg/sandboxruntime/podmanenv_test.go`.
