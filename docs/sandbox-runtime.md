# Sandbox runtime: boot, containers, podman arguments

The rules are in `AGENTS.md → Sandboxing`. This is how the runtime implements them, and what was
measured on a real host.

## Boot configuration is a single injected value

- `sandboxruntime.Boot{SweepStaleContainersAtBoot, AgentPath}` is created once in `server.NewServer` from
  `startup.Config` and passed to `NewSandboxManager`, `NewExtensionSandboxManager`,
  `NewRegistryClientWithBoot` and `sandboxruntime.NewRuntimeWithBoot` — the last is the **only** Runtime
  constructor, ensuring there is no boot-less form.
- A default (zero) `Boot` value disables the host sweep, so a test binary or an ad-hoc caller cannot delete a live
  server's containers by forgetting a variable.
- An image build needs the bridge path too (the registry client stages it into the build context), which
  is why the boot reaches `NewRegistryClientWithBoot` and not only the two managers.
- Tests: `pkg/sandboxruntime/runtime_test.go`
  (`TestBootDeclinesTheHostSweepByDefault`, `TestGetAgentBinaryPathPrefersTheWiredPath`).

## Two availability probes, one sweep

- `sandboxruntime.GVisorAvailable(tag)` is used by both `SandboxManager.checkAvailable` and the extension
  manager's probe — they must be consistent to ensure both task and extension sandboxes agree about whether
  the host can run gVisor. It is a **presence** probe: `runsc` in PATH, `runsc --version`, `podman` in PATH.
  Product availability stays with it, because a startup that started a container to find out would put a
  container launch on every boot.
- `Runtime.ProbeSandbox(ctx, image)` is the second question — **did a gVisor container run?** — and answers
  it with one throwaway container: `GVisorCreateArgs`, `--network none`, no mount, no capability, `start -a`
  of `/bin/true`, removed in every path. The container is named `mike-sandbox-probe-<pid>`, outside the
  `kanban-sandbox-` family the boot sweep and the e2e teardown act on by name. An image the host does not
  have is reported as usable: the store answered, so the sandbox works and the missing image is someone
  else's failure (`ImageAvailabilityError`). Presence-only would miss the measured broken host: runsc
  installs fine on a nested/overlayfs root whose podman storage cannot initialise, and that host refuses
  every create with exit status 125. `scripts/sandboxprobe` exposes it as a command and
  `backend/testutil/sandboxcheck.Require(t, image)` as the test skip; `docs/testing.md → Go unit test
  environment` owns when a test may use them.
- `Runtime.CleanupStaleContainers(tag, nameFilter)` is the shared startup sweep that is best-effort and
  bounded to 15 seconds (prefixes `kanban-sandbox-` / `kanban-ext-`); neither manager should re-implement the
  logic for listing, parsing, and removing containers.
- `staleContainerNames` does not remove containers that are **running**: the sweep removes leftovers,
  never a live sandbox. If a container survives after its task row is gone, it survives a restart, which is why
  task and project deletion are the primary ways to stop containers (`docs/storage.md`,
  `AGENTS.md → Deletion`).
- `backend/sandbox`'s `TestMain` calls its own `cleanupStaleTestContainers`, which uses `podman rm -f` on every
  `kanban-sandbox-*` on the host, whether running or not — so a live server's task and project sandboxes are
  deleted by `go test ./backend/sandbox/` even when it runs no test at all. Running this package's tests
  beside a live server is therefore destructive. Scoping the sweep to a test-owned prefix would be the fix,
  but has not been implemented because not every test names its container `test-*`.

## The container environment

`containerEnvArgs` (`backend/sandbox/sandbox.go`) turns the project's Sandbox Configuration envs plus an
agent extension's declared `sandbox_env` into `-e` flags, project config appended last so it wins a
clash.

- **A flag like `-e NAME` without a value is not supported.** Every assignment comes from
  `types.SandboxEnvArgs([]types.SandboxEnv)` (`backend/types/env.go`), which can only emit `KEY=VALUE`
  and errors on a malformed key, and `Create` refuses the container on one. `podman -e NAME` copies the
  *client's* value of `NAME` into the container, so a project that declared a variable's name without a
  value would export whatever the server had.
- **The `exec` command does not accept its own environment flags.** The container create is the only place
  an `-e` is emitted: past the container name podman parses no more flags, so an exec-time `-e` names the program to run
  (`execCommandArgs`).
- Invent no other variable: no `PATH` (an `-e` overrides the image's `ENV PATH`, which dropped a
  custom image's tooling) and no container-wide `TERM` (the terminal sets `TERM` on its own
  `podman exec` in `shellExecArgs` — it belongs to the TTY session, not the sandbox).
- Tests: `backend/sandbox/containerenv_test.go`, `backend/sandbox/hostenv_test.go`. The last runs a real
  gVisor container and asserts a host variable reaches it **only** if allowlisted to the podman client —
  the probe variable must be one a host could set, or the test passes vacuously.

## Podman argument construction

`ValidateContainerPath`, `ValidateCapability`/`ValidateCapabilities`, `GVisorIsolationForCaps` and the
`GVisorCreateArgs`/`AppendCapabilities` pair live in `pkg/sandboxruntime`, next to
`ValidateProjectName`, and every caller that builds container arguments goes through them:
`extensions/mount_policy.go` calls them, `sandbox.Create` refuses before the container exists, and
`types.ValidateSandboxMount`/`ValidateSandboxAdditions` check a project's Sandbox Configuration on
create and `PUT` (400 naming the field).

- `GVisorCreateArgs` is the standard configuration that both builders use as a starting point (byte-identical up to their
  own `--workdir` / `--network none`). `AppendCapabilities` is the only way to append a `--cap-add`:
  it runs `GVisorIsolationForCaps` (runsc **and** `--cap-drop ALL`) first and refuses the whole grant
  otherwise, so no ordering mistake can reach podman.
- The name format is determined by the caller and is handled differently by design: a task rejects its whole
  capability list (a project row is operator configuration), while an extension sandbox ignores a single
  malformed name and grants the rest.
- Because a project row can predate the rule, `Create` refuses again at container creation rather than
  trusting stored config.
- **`--image-volume ignore` is in the shared preface.** An image that declares `VOLUME` makes podman create
  an anonymous volume for every container built from it, and `rm -f` leaves that volume behind. Each leftover
  spends one of podman's 2048 volume locks permanently; at 2048 the host refuses every create, including
  those of projects that declared no volume. MIKE owns no volume: every mount is a bind through
  `BindMountSpec`, and an explicit bind survives `ignore`.
- **`podman build` takes no `--runtime` flag** (`backend/extensions/image_build.go`), so an extension
  Dockerfile runs on the host runtime with podman's default caps — the one place "gVisor runs it" is
  not true.
- Tests: `pkg/sandboxruntime/podman_args_test.go`, `backend/sandbox/create_args_test.go`,
  `backend/extensions/sandbox_test.go`, `backend/api/project_sandbox_settings_test.go`,
  `TestTaskSandboxIsolatesWhatACapabilityGrantAssumes`, `TestBuildExtensionCreateArgs_CapabilityPosture`,
  `TestATaskContainerFromAnImageThatDeclaresAVolumeLeaksNone`,
  and each builder's `…BeginWithTheSharedPreface` test, which fails the day one re-inlines the posture.

### A task worktree's git pointer is pinned read-only

- **Every task container must have the pin established to start.** `sandboxruntime.WorktreeGitPointerMount` returns the
  `-v` pair that mounts `<worktree>/.git` read-only at `<mountPath>/.git`, and `sandbox.Create` appends
  it after the worktree's own mount. A container that cannot establish it is rejected.
- The worktree is bind-mounted `rw` and a linked worktree's `.git` is a plain file inside it, so the
  guest can rewrite it. That file names the gitdir every later **host** git command reads, and git
  executes configuration: `filter.<name>.clean` runs when git compares a file to the index, which is
  what the editor's gutter marks and the diff panel ask for. No configuration key closes that hole,
  because the config that executes is the one the pointer names — `git.ExecutionFence` holds what the
  environment can pin instead (`docs/git-operations.md → One command constructor`).
- **If the git pointer is missing or is not a regular file, the container is rejected** rather than a mount skipped:
  a task worktree is always a linked worktree, so a directory with no pointer is not the shape `Create`
  was asked to sandbox.
- **Project containers do not receive a pin.** Their mount is the checkout itself and the terminal around it is a
  person's own, where `git commit` writes into the repository's `.git` directory by design. `IsProjectKey`
  is the switch.
- `sandboxruntime.VerifyWorktreeGitPointer` is the pre-flight companion to the mount: the pointer's
  `gitdir:` must name a directory **under** `<projectGitDir>/worktrees/` once cleaned. Text that keeps
  the admin prefix and walks out of it with `..` names a directory git reaches after its own cleaning.
  The check is omitted if `<projectPath>/.git` is not a directory — a project that is itself a
  worktree or submodule names its gitdir in a file, so the expectation does not exist for it — and the
  pin still applies there.
- Tests: `backend/sandbox/git_pointer_test.go`, and `TestWorktreeGitPointerMountIsReadOnly`,
  `TestWorktreeGitPointerMountRefusesWithoutAPointer`, `TestVerifyWorktreeGitPointer` in
  `pkg/sandboxruntime/podman_args_test.go`.

### Capabilities are judged by shape, not name

`capabilityPattern` (`pkg/sandboxruntime/podman_args.go`) accepts `^[A-Za-z][A-Za-z0-9_]*$` and nothing
else, so a capability is a word and cannot be anything else.

- The structural validation is the primary defense. `--privileged`, `--cap-add` and `SYS_ADMIN ALL` are each *text a person
  or a manifest put in the capability field*, and each reaches podman as an argument to `--cap-add`, which
  no runtime makes safe. A name list would not help: the request is rejected because the value carries a flag, a space
  or a separator.
- `ValidateCapabilities` reports the first value it cannot accept, naming it, so the operator sees which
  entry was refused rather than a list of accepted ones.
- What a name *means* is decided elsewhere: `AppendCapabilities` refuses the grant unless the isolation
  posture is already there (`docs/sandbox-runtime.md → Podman argument construction`). The runtime is what the
  safety rests on: under `runsc`, a guest holding `SYS_ADMIN` still cannot write through a read-only bind mount,
  see a host PID, or bind-mount the host tree. A denylist of names was measured away as not being that boundary.
- Tests: `TestValidateCapabilityRule` (the table carries `--privileged`, `--cap-add`, `-v` and
  `SYS_ADMIN,ALL`), `TestGVisorIsolationForCaps`, `TestAppendCapabilitiesRefusesAnUnisolatedGrant`.

## What a task container binds besides its worktree

Three reads a task cannot do through the bridge still need a mount. All three are `ro`, and none of them
is configurable — what a person can widen is `SandboxMounts`, declared per project in its Sandbox
Configuration.

- `<project.Path>/.git` at its own absolute path, so git can resolve refs and worktree metadata from the
  worktree's pointer file.
- The bundled skill packages at `sandbox.SkillsMount`, from `sandbox.SkillsDir()` (`prompts.SkillsDir()`).
- The project's own skill packages at `sandbox.ProjectSkillsMount`, from `SandboxOpts.ProjectSkillsDir`.
  `ProjectOpts` fills it with `prompts.ProjectSkillsDir(project.Path)` — the same call the prompt builder
  makes, because the bind and the listing are one contract. `docs/skills.md → What must stay true`.
- Both skills binds are built by `readOnlyBindArg`, which emits **no mount** when the directory is
  missing: an optional directory nobody created is not a container failure. A directory that appears
  after the container exists is not bound into it — the container is created at run start and stopped at
  run end, so the gap is inside one run.
- Tests: `backend/sandbox/projectskills_test.go`, `backend/sandbox/git_pointer_test.go`.

## Extension sandbox mounts

`backend/extensions/mount_policy.go`:

- Host mounts must resolve inside the policy's roots: workspace dir, registered project paths,
  extensions dir, system temp dir, `MIKE_EXT_MOUNT_ROOTS`.
- **Only the extensions dir, the temp dir and the `MIKE_EXT_MOUNT_ROOTS` extras may be mounted
  read-write.** The workspace dir, the registered checkouts and anything borrowed from a project's
  Sandbox Configuration are forced to be read-only (`ro`). `MountPolicy.ForceReadOnly` does that
  regardless of what the manifest asks.
  Manifests are provided as text from the registry and the workspace holds every task's worktree.
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
- An agent extension's mounts come from the grant a person saved for it (`docs/agent-extensions.md`),
  never from its manifest and never from a project's Sandbox Configuration, so `sandbox.Create` runs each
  of them through `types.ValidateSandboxMount` beside the project's declared mounts, before the
  availability probe. An entry with one end missing is skipped: it builds no mount. A granted directory
  that does not exist yet is created here (0755, the same as a project's own mount), which is what makes
  an `rw` grant writable inside the guest — no `:U` option and no userns mapping is involved, and the
  directory ends up owned by the server's user, which is what a rootless container's root maps to.
- Tests: `backend/extensions/mount_read_only_test.go`, `backend/sandbox/create_args_test.go`.

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
- Shutdown order follows from that rule — a session's shell dies while its container can still receive it
  — and the stage list itself is `docs/lifecycle.md → Shutdown Stage List`.
- Tests: `backend/sandbox/podman_wait_test.go`, `TestTerminalManagerClose`, `pkg/subproc/subproc_test.go`,
  `TestPodmanLabelNamesTheOperation`, `pkg/sandboxruntime/podmanenv_test.go`.

## A project image builds inside a sandbox container

Why the build is a container and not a host `podman build`: `docs/adr/0002-…-over-a-commit-subtree.md`.
This section is the mechanism.

- **The context is a subtree of a commit, never a working tree.** `StageBuildContext` runs
  `git archive <commit> -- <root>` and writes the entries into the build's scratch directory, so untracked
  files — `.env`, a private key, whatever a person was mid-edit on — cannot be build input. The commit is
  `HEAD` of the project checkout, read once, and the Dockerfile is read from that same commit
  (`CommitFile`), not from disk. Symlinks, devices, fifos and hard links in the tree are refused rather than
  followed, and both the entry count (`fsutil.CopyMaxEntries`) and the byte total are bounded before a byte
  is written.
- **The default context is the directory holding the Dockerfile.** That is the smallest subtree that
  answers `COPY`, so a `COPY` of anything above it is refused with its line number, its source, and the
  field that would widen it: `sandbox_build_context`. The field holds a repository-relative path, stored in
  one spelling by `NormalizeBuildContextValue`. It never holds a host path. `/etc`, `~`, `$HOME`, a drive
  letter and any `..` segment are refused at the API, before a container exists.
- **The container is one-shot and its posture is narrower than a task container's.** Its grant is
  `--cap-drop ALL` plus `sandboxruntime.BuilderGrant`: the set the nested builder hands each `RUN` step. It
  reads **none** of the project's Capabilities — that row is about the container the agent runs in, and a build
  is not the agent. The project's memory limit and environment do come from the project. Every inherited mount
  is **read-only**, even a row that says `rw`. No port is published.
- **A build has a network, and it does not read `sandbox_network_isolation`.** That switch is about the agent:
  can the thing editing my code phone home. A Dockerfile installing its dependencies is MIKE's own plumbing
  doing what a person wrote, so coupling the two made the switch mean "your builds cannot install anything".
  The cost is this, and it is not repeated in Settings: an isolated project still has build-time egress. What
  stays bounded is what a build can *read*. Measured 2026-10-30 through the product's own arguments, a `RUN` step fetched a
  public page. A Go test reaches no network, so the permanent test
  (`TestBuildReachesTheNetworkEvenForAnIsolatedProject`) asserts the route exists even for an isolated project.
  A guest reaches the internet but **not the host's loopback**: the gateway is the CNI address, so a build
  cannot call MIKE's own API.
- **Three binds:** the staged context read-only at `/mike-ctx`, the
  output directory read-write at `/mike-out`, the base archive read-only at `/mike-base`. None of the three
  reaches a `RUN` step: the step's filesystem is the image being built, not the container running the builder,
  so a `RUN` that writes to a bind path fails with "nonexistent directory" — measured. The handoff is
  one-way in both directions: `RUN` reads the context only through what a `COPY` put in the image, and writes
  only into the image.
- **The grant is fixed, and it is tested as a whole.** `BuilderGrant` is the fourteen names the nested builder
  asks for. `BuildGrant` — `SETUID`, `SETGID`, `SETFCAP` — is the measured minimum inside it: each of the three
  is necessary, every pair failing at the `/proc/<pid>/uid_map` or `gid_map` write.

  Adding privileges is **not** monotone, and what breaks is a set with a *hole* in it. Docker's recommended
  defaults build, with or without `CHOWN`. A wide grant including `SYS_ADMIN` builds **until `CHOWN` is taken
  back out of it**, and then extracting the base layer fails at `lchown /etc/shadow`: a grant wide enough moves
  buildah off the user-namespace path, and that path needs `CHOWN`. Measured 2026-10-31, the recommended
  defaults **minus `KILL` and `NET_BIND_SERVICE`** fail at `setting "RLIMIT_NOFILE" limit to
  soft=1048576,hard=1048576 (was soft=524288,hard=524288): operation not permitted`. Denied the set it wants,
  buildah takes the path that also raises a hard limit, and a rootless gVisor guest cannot raise one without
  `CAP_SYS_RESOURCE`. `podman create` says which names it refused, and a successful create
  swallows the line.

  So a candidate grant is tested whole, never derived from a set that worked, and the shipped answer is a named
  constant nobody assembles. A build has no capability list to widen. A `RUN` step needing a privilege outside
  the set is a build this venue declines rather than one the list grows to cover, and `buildFailureHint`
  says exactly that instead of handing over buildah's line.
  `TestARunStepNeedingMoreThanTheBuildGrantSaysSo` holds the refusal through the product's own argument path.
  `make build-probe` re-runs the whole ladder and reports every grant it tried, its decisive stage measuring
  the shipped set; `pkg/sandboxruntime/buildprobe.go` holds the sets and the reading beside it.
- **A build books its memory the way a task turn does.** The amount is the limit the container is created
  with, and a project with no limit books nothing — one rule, not a second ladder. It is taken before
  anything is staged and released when the build ends. The route in is an injected `MemoryAdmission`, because
  a build belongs to no task and the supervisor that charges a live container to the budget never sees it.
- **One preparation per image runs at a time, and whoever arrives mid-build waits for it.** The in-flight
  table in `backend/sandbox/image.go` is keyed by the resolved image ref. All three ways an image gets made
  reach it: a task's `EnsureImageForProject`, a terminal's, and the Build button's `BuildProjectImage`.
  Two builds of one image would book the memory twice and retag the same name over each other. Joining is
  decided **before the host is asked whether the image exists**: a rebuild leaves the bytes it is replacing in
  place, and a container started off those is what the wait exists to prevent. A waiter hears what the
  preparation announced and the lines after it, and takes its answer: a failed build is reported to it, not
  retried by it, and nothing is cached, so a later caller tries again. `buildProjectImageDirect` is the only
  builder, and only the holder of a key may call it — a builder that consulted the table would wait on itself.
  `TestEnsureImageForProjectWaitsForTheImagePreparationAlreadyRunning`,
  `TestMakeProjectImageAnnouncesThePhaseItWorksOn`,
  `TestBuildProjectImageJoinsTheBuildAlreadyRunning` and `TestImageOpJoinersShareTheInFlightResult` hold it.
- **The builder is nested buildah, and MIKE models no instruction.** Inside the guest: storage `vfs`, root
  and runroot under `/var/tmp/mike-build` (**not** `/tmp`, which is guest tmpfs — a `vfs` layer there fails
  its xattr reads), `bud --isolation chroot --pull=never`, flags before the context.
- **The file is read for its image refs and judged for nothing else.** `backend/sandbox/dockerfile.go`
  answers one question: which images must the host fetch. Every stage's base, and every `COPY --from` naming
  an image, deduped in file order — the guest never pulls, so a ref missed is a build that dies pulling it.
  The Dockerfile is untrusted text that builds inside gVisor, so MIKE does not read which commands it runs and
  does not ask a person to bless them: containment is the control. Everything the parser does not read is the
  builder's, and the file reaches the builder unchanged.
- **The nested builder image is fetched by the host, the way a base is.** `runBuildContainer` creates its
  container with `--pull never`, so that image cannot arrive any other way: `ensureBuilderImage` fetches it
  before the create and says so in the stream. `docs/docker.md → Limits an operator meets` for what that costs.
- **The instruction whitelist was measured away.** An earlier version refused instructions, flags and path
  shapes it had not read. The builder jails those shapes itself. It resolves a `COPY /etc/passwd` and a
  `--mount=type=bind,source=…` *under the context directory*, clamps an escaping `COPY ../x` inside it, gives a
  `--mount=type=secret` no secret store and `--network=host` no interface, and builds `ARG`, `SHELL`, `VOLUME`,
  `HEALTHCHECK`, `RUN --mount=type=cache` and multi-stage files. So the whitelist bought refusals
  and a false story about where the safety lives.
  `TestBuildPassesThroughWhatTheVenueAlreadyJails` and
  `TestBuildRunsAMultiStageDockerfileOverTwoImages` hold the two halves.
- **A Dockerfile names images by registry spelling, so it cannot name one off this machine's store.** A short
  ref is qualified the way Docker qualifies it: `docker.io/library/…`. The local-store lookup
  (`ResolveImageRef`) is deliberately not applied to it, because that lookup exists for *stored* settings,
  where a short name really is a local build. Applying it to repository text let a tracked `FROM my-runner:1`
  copy any image on the host into the project — measured, no network involved. An image in the host's store is
  bytes no sandboxed process can reach. `TestBuildCannotNameAHostLocalImageByShortName` holds the rule. An
  explicit `localhost/my-runner:1` still builds, since that line is something a person reviewed.
- **Two refusals stay, each because MIKE would otherwise say something false.** `--platform`: the host
  pulls, so the build gets this host's architecture. `# escape=` with a non-default character: it moves where
  one instruction ends, so MIKE would read a different file than the builder does.
- **A base a `$variable` names is resolved, not refused.** MIKE hands the builder no `--build-arg` of its
  own, so a build arg's only possible value is the `ARG` default written in the same file. Measured: a stage
  that re-declares the arg bare sees that default with nothing passed in. So the parser reads the same line the
  builder reads, and fetches what it names. The archive arrives tagged with the qualified ref and with the
  file's own spelling, so the guest's second expansion finds it under `--pull=never` and nothing needs
  pinning. `TestBuildResolvesAnArgNamedBaseInASandbox` proves the two readings agree.
- **An `ARG` names a base only from above the first `FROM`** — that is the builder's scoping, measured on
  both engines. A default written below the `FROM` is empty inside the stage, for anything but a bare
  re-declaration, and it can never name the image a stage builds on. An `ARG` written with no `=` has no
  value, and a base naming it is refused by name — MIKE cannot fetch what has none. Defaults expand lazily,
  bounded, and never from the environment.
- **The artifact is pushed out as bytes; `podman commit` is never the path.** The guest's writes are
  invisible to the host's storage because `GVisorCreateArgs` sets `overlay2=root:self`, so a host-side commit
  would produce an image that silently omits every `RUN` step. The guest runs
  `buildah push … docker-archive:/mike-out/image.tar`; the host runs `podman load -i` on that file.
- **The load is ordered so a refusal costs the project nothing.** The build record is read off the loaded
  image *before* anything is retagged onto the project's name, so an image no MIKE build produced never
  holds it. The builder's tag is then dropped by **name** with `podman untag`. The image the build replaced
  is dropped **by id**, and skipped when that id is the one that just landed: a build whose base and steps
  changed nothing loads to its predecessor's id. `images -q` prints a bare hex id and
  `image inspect` prints the same id prefixed `sha256:`, so the comparison is `sameImageID`, and an exact
  string compare there reports one image as two and deletes the project's new image. Every removal is
  allowed to fail with a `Warn`, and the function's last act is to confirm the project's name resolves to
  the image it just built — with `--pull=never` on container creation, a lost name is a task that cannot
  start. `podman load` is also where the `mike.base-image` digests become durable: they are the answer to
  "which bytes", which a floating tag cannot give.
- **The base image arrives as an archive.** The guest is never the one that fetches, so the host exports
  the resolved `FROM` ref with `podman save` into `/mike-base` and the guest imports it. A base the host does
  not have is pulled by the **host** through the ordinary qualified-ref path and handed in; the guest never
  pulls, and a base the host cannot fetch is a refusal naming the ref.
- **The build's cache key is `(commit, context tree OID, Dockerfile bytes, base refs)`**, stamped on the
  image as the `mike.build-cache-key` label and read back by `ImageCarriesBuild`. A tag that moves on the
  registry does not change that key. `mike.base-image` records the digests the build actually used, which is
  the only answer to "which bytes": MIKE pulls a base it does not already have, so a floating tag means "this
  host's copy" until something refreshes it.
- **Nothing survives a build.** One owner, one deferred block: the container and the scratch directory go
  on every exit, and a refusal counts as an exit. The archive is deleted as soon as it is loaded, and on a
  failed load too, which leaves the previous image in place. `kanban-build-*` is a boot-swept family and
  `SweepScratch` clears the scratch root at boot. That root is the **worktrees root**, never the data
  directory: a build must not be able to write beside the database or the TLS keys. A refusal leaves the same
  empty scratch root a success does, which
  `TestBuildRemovesItsContainerAndScratchOnEveryExit` checks. A cleanup failure is a `Warn` naming the
  leftover, never a fatal.
- **The output is watched, not trusted.** `watchDir` samples `/mike-out` and stops the build past the cap;
  the in-guest store is uncapped by design (tmpfs rejected it), so the archive is the number supervised.
- Tests: `backend/sandbox/build_e2e_test.go` runs a real build and checks three things — committed bytes
  reach the image, a `RUN` step's output survives the load, nothing is left behind. The gate-side tests are
  `backend/sandbox/dockerfile_test.go`, `backend/sandbox/buildcontext_test.go` and
  `backend/sandbox/build_test.go`. `make build-probe` (`scripts/sandboxprobe --build`) re-measures the grant
  and sweeps its own `mike-build-probe-*` family, since it deliberately lives outside the product's sweep.

## A container's memory record, and what the user is told

- **Held memory is `anon + shmem` from `memory.stat`** — `sandboxruntime.CgroupMemory.HeldBytes`, the
  definition every number in this subsystem rests on. `shmem` carries the gVisor guest's RAM, so an
  `anon`-only reading reports a fraction of what a sandbox holds. `file` and `kernel` are excluded, and a
  missing `anon` line is *no data*, never 0 MB; a missing `shmem` line is 0.
- **`memory.peak` is not a source.** The high-water is MIKE's own: `MemoryTracker` keeps `peak` per
  container, raises it from each sample, never lowers it, and `Forget` drops it with the container.
- **Sampling follows the container, not the task's status** (`backend/stream/sandbox_stats.go`): a task idle
  with its container alive still holds RAM the budget must see. Only a container whose ID names no task row
  (the project's `proj:` terminal container) is skipped. Two ticks share one sampler — 500 ms to sample
  (`DefaultSandboxSampleInterval`, also the budget's clock), 2 s to broadcast a value that changed. The final
  reading is taken before the container is removed, so a run's peak is its high-water and not its last sample.
- `oom_kill` dies with the container, so `Stop` hands `(peak, oomKills)` to `Store.RecordTaskSandboxMemory`
  before the removal: the one writer of `tasks.sandbox_memory_peak_mb` and `tasks.sandbox_oom_kills`. An ID
  naming no row is a no-op, and a reading that produced neither number reports nothing rather than erasing a
  previous run. `UpdateTask` owns neither column but reads both back, so broadcasting the row it just wrote
  cannot clear a measurement taken during it (`docs/storage.md → The schema is created whole`).
- **A stored peak means *the last run*.** It estimates nothing and is used by no admission decision.
- **What needs a person's attention is decided once**, in `frontend/src/lib/utils/memory-stats.ts`: an OOM
  kill, or a peak that reached the limit. Running near the limit is pressure, not an alert — it colours the
  bar and the shield dot orange and asks for nothing. Red outlives the run because the numbers that produced
  it do.
- **A dismissal is a column on the task row**, not a record in the browser that clicked: `POST
  …/memory-alert-ack` through `Store.DismissTaskMemoryAlert`, then the task broadcast. It is the same path a
  status change travels, so one operator with a phone and a laptop sees one answer. The ack keeps the three
  numbers the alert showed and covers an alert when the limit matches and neither counter is worse, so a
  worse run announces itself again. The comparison is by numbers and not a timestamp: a container's counters
  only rise and `sandbox_stats` carries no time. A task that hits an identical cap in a later run stays
  dismissed — nothing on the row distinguishes the runs, and a per-container run token would re-arm it.
- Tests: `backend/sandbox/memory_peak_test.go`, `backend/sandbox/memory_accounting_test.go`,
  `backend/extensions/memory_accounting_test.go`, `pkg/sandboxruntime/memorystats_test.go`,
  `backend/stream/sandbox_stats_test.go`, `backend/api/task_memory_alert_test.go`,
  `frontend/src/lib/utils/__tests__/memory-stats.unit.ts`,
  `frontend/src/lib/components/__tests__/{task-header-sandbox-dot,sandbox-panel-memory,card-memory-pill}.unit.svelte.ts`.

## The memory budget: what admits a task, what pauses one, and what it can take back

- **One rule**, in `exec.MemoryGate`: `mikeMB + reservations + candidate ≤ capacity`. `mikeMB` is what MIKE
  itself costs — every container it started plus its own process. Capacity is
  `MIKE_MEMORY_BUDGET_PCT` (default 90) percent of **installed** RAM, computed once at startup in
  `backend/server` and handed to the gate, to `ConfigStore.SetMemoryBudget` for the About tab and to the
  startup log line. `/proc/meminfo` has exactly one reader in the tree (`meminfoMB` under `readMeminfoMB`),
  and `/proc/self/status` likewise (`selfRSSMB` under `readSelfRSSMB`).
- **`mikeMB` is the sum of three accountants** (`memoryBudget.Measure`). It counts task containers and the
  project-level `proj:` terminal containers (`SandboxManager.MemoryAccounting`, over the `active` map
  that holds both), the extension sandboxes (`ExtensionSandboxManager.MemoryAccounting`), and the server
  process itself (`selfProcessAccountant`, `VmRSS` — no cgroup, so held and charge are one figure). An
  unreadable `VmRSS` adds nothing and warns once, not once per tick. Each container costs `max(held, cap)`
  (`sandboxruntime.ChargeMB`): a capped container pays its cap, an uncapped one what it holds, an unreadable
  one its cap alone — "unreadable" is not "free". Caps are recorded at container creation (`capsMB`,
  `extensionSandboxState.memLimitMB`), because the row that carried the limit can change while it runs.
- **`heldMB` travels with `mikeMB` for the log, not the rule.** The gap between them is what makes a
  `paused (frees 2048 MB)` line readable when the container behind it holds 25 MB.
- **The reservation a task acquires lives only across the container's creation.** `acquireMemSlot` reserves
  the project's configured limit — the cap the container is about to get, never a mean of other tasks'
  history — and `confirmMemReservation` spends it once `Create` has registered the container. **A
  non-sandboxed run books the same number and keeps it to the end of the run**: its agent CLI is memory MIKE
  started with no cgroup to measure. An uncapped project reserves 0 and is admitted only while the budget is
  *under* the ceiling. `GetRunningSandboxedTasks` lists only sandbox-enabled projects, so a host run's
  reservation is one the supervisor cannot give back — it returns when the run ends, and only then.
- **An unread host is never read as an empty one.** If `MemTotal` or `MemAvailable` is missing, the tick
  reports "not measured" and the gate admits on its reservation ledger rather than admitting everything.
- **Pause is the last resort and picks the cheapest victim**, one per tick; a task with no live container is
  costed at the reserve the budget already carries, never at zero. Trigger 1 is a non-empty queue; trigger 2
  is `MemAvailable` under 5 % of `MemTotal` (`memoryAvailableFloorPct`), which pauses with an empty queue and
  logs at `Warn`, naming the task and what it waits on. Resume happens only when
  `InUse() + reserve ≤ capacity`, so a resume cannot immediately re-queue.
- **A queue with nothing in flight is the wedge.** Every megabyte the budget counts can belong to a
  container of a task waiting on a person. Their reservations went back when they went idle, no user action
  is coming, and the pause pool (`inFlightStates`) has no victim.
- **The supervisor can take such a container back, and `reclaim_idle_sandbox` decides whether it may.**
  The preference lives in Settings → Task and is off unless stored true.
  `ConfigStore.GetReclaimIdleSandbox` is read on the tick that decides, so the setting needs no restart.
- **It runs only after the in-flight pause branch declined for want of a victim**, one container per tick,
  the idle-longest first. A candidate comes off the supervisor's clock alone: idle past
  `defaultIdleSandboxGrace` (5 min) and held by no terminal session.
- **`MemorySupervisor.reclaimIdleSandbox` picks and `TaskExecutor.reclaimSandbox` releases.** The action
  reads the row before podman is touched: a start that landed in between refuses the release, and so does a
  charge with no task row behind it. A refused candidate does not spend the tick — the next idle one is
  asked. A task that lost that race is healed by `recoverBridgeForTask`, which creates the container from
  scratch when it is gone.
- **What is taken back is the container only, and it is silent.** `status`, `execution_state` and
  `memory_supervisor_paused` keep their values: a pause would arm the auto-resume and start a turn nobody
  asked for. No note is recorded, so the task reads exactly as it did.
- **`Stop` still writes the run's final peak**, so the card's memory figures survive the release.
  `sandbox_stats` follows containers, so the panel falls back to that recorded peak, as it does after any
  stop.
- **What the task loses is what lived inside the container** — installed packages, `$HOME`, `/tmp`, a
  daemon it started. Forwarded host ports are ephemeral per create, so a published port gets a new URL. The
  worktree, its `.mike-uploads`, git state and checkpoints are bind-mounted and unaffected.
- **The idle clock is the supervisor's own observation, not a column.** `updated_at` moves on unrelated
  writes, and `moved_at` predates every turn since the card last changed column. Neither one answers "how
  long has this container been waiting".
- **`idleSince` stamps a container the first tick that sees it not in flight**, and keeps stamping while the
  preference is off. So switching the preference on asks which containers have waited over five minutes,
  instead of waiting five more.
- **The clock drops a container that goes away**, which is what makes a recreated one a new arrival. A
  restart restarts the clock; that errs towards leaving a container up.
- **A live terminal holds its container** (`TerminalManager.HasSession`, wired in `backend/server`). The
  shell inside belongs to the person who opened it, and a closed tab is not permission to kill it.
- **A charge with no task row is nobody's to take** — the project's `proj:` terminal container stays up.
- Tests: `backend/exec/memory_supervisor_test.go` (the order, the grace, the untouched row, the hold, the
  clock), `backend/term/terminal_test.go`, `backend/config/config_test.go` (off unless stored). Which runs
  are pauseable at all is `types.ExecutionState.InFlight`, whose SQL spelling `store.inFlightStates` is
  held to the same set by `TestInFlightStatesSQLIsTheGoSet` in `backend/store/tasks_test.go`.
- **Both transitions write to the task's own history, because the paused row cannot say it.**
  `noteMemoryPause` / `noteMemoryResume` (`backend/exec/memory_supervisor.go`) record a `system` note on the
  current thread through `systemNote`, whose rule lives with the role: a system row is display-only, since
  `groupTaskMessages` drops it when it rebuilds the conversation. The row's `kind` (`memory_pressure` /
  `memory_resumed`) is what the card is worded and coloured by — `systemNoteText` renders the sentence from
  `en.ts` and `severity: "warning"` selects the yellow card, so the kind strings and `chat-entries.ts` must
  agree case for case; the stored `content` stays English because an API client reads it. **Order is the
  rule:** the pause note goes *after* `SetMemorySupervisorPaused`, because it promises the auto-resume that
  flag arms, and a failed stop or start records nothing. The same note corrects `tasks.output` through
  `SetMemoryPressurePausedOutput`, one guarded statement whose guard is
  `execution_state = 'paused' AND memory_supervisor_paused = 1` — that guard doubles as the ownership check,
  because whoever acted next owns what the task says about why it stopped. The `status` frame's `activity`
  line stays "Task paused by user": the SPA reads it only for its `Pulling ` prefix.
- **`HostMemoryTight` and admission are deliberately independent**, which costs an admission that a tight
  host will go on to pause. Below
  the 5 % floor the supervisor pauses; below what the kernel tolerates the host memcg killer takes a whole
  `runsc` sandbox rather than the over-allocating guest process (the failure `docs/testing.md → Tests that
  fail only under load` records).
- **Extension sandboxes are bounded** (`DefaultExtensionMemory`, above any measured language-server peak):
  an unbounded one is invisible to the budget until it has already grown. The limit field reads three ways —
  0 is "no opinion, take the default", positive is that bound, negative is no bound — in the manifest and in
  the stored config alike, so declaring a limit is never overwritten by the default.
- **The shared containers count, but not against a task.** One `kanban-ext-<author>-<name>` sandbox per LSP
  extension and a project's `proj:` terminal container sit inside `mikeMB` and never inside a *task's*
  recorded peak, which is its own container only. Extension sandboxes outlive the task that started them
  (`ExtensionStopGracePeriod`) and a terminal container is released by its holders, so attributing them to a
  task would charge one run for another's.
- Tests: `backend/exec/memory_gate_test.go`, `backend/exec/memory_supervisor_test.go`,
  `backend/server/memory_budget_test.go`, `backend/store/tasks_test.go`, `backend/store/schema_test.go`.

## The cap is the enforcement point, and it has a floor

- `ApplyMemoryLimit` writes `memory.max` and `memory.swap.max = 0`, and leaves `memory.high` at `max`
  (clearing one an older build may have left). Nothing bounds the overshoot between samples except admission.
- `types.DefaultSandboxMemoryLimitMB` is the cap `Store.CreateProject` gives a row that arrives without
  one, and it resolves it onto the caller's struct too, so the created project answers with what was
  stored. A stored 0 means no cap; `frontend/src/lib/utils/memory-stats.ts` mirrors the number.
- A cap set far below 128 MB does not mean "the runaway process dies"; it gets a dead sandbox. A project
  configured that low is answered by the container's exit, and `TestMemoryLimitEnforced` holds the floor.
- Tests: `TestMemoryLimitEnforced` (asserts the process-dies-not-the-sandbox behaviour at 128 MB),
  `backend/sandbox/memory_peak_test.go`.
