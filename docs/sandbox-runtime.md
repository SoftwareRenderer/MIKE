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
  `backend/extensions/sandbox_test.go`, `backend/api/project_sandbox_settings_test.go`,
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
- Shutdown order follows from that: websocket watchers → terminal sessions → lsp → extensions → sandbox
  containers → database, so a session's shell is killed while its container can still receive it.
- Tests: `backend/sandbox/podman_wait_test.go`, `TestTerminalManagerClose`, `pkg/subproc/subproc_test.go`,
  `TestPodmanLabelNamesTheOperation`, `pkg/sandboxruntime/podmanenv_test.go`.

## A container's memory record, and what the user is told

- **Held memory is `anon + shmem` from `memory.stat`** — `sandboxruntime.CgroupMemory.HeldBytes`, the
  definition every number in this subsystem rests on. `anon` is the host-side heaps of the processes in
  the cgroup; `shmem` is shared memory, tmpfs, and **the RAM backing the gVisor guest**, because runsc
  runs the guest out of a memory file. Measured here, a container writing 500 MB *inside* the guest put
  537 MB into `shmem` and left `anon` at 17 MB — so an `anon`-only reading reports 18 MB for a sandbox
  holding half a gigabyte, which is how this was originally wrong.
  What is excluded, and why: `file` (cgroup v2 counts `shmem` inside it, so the host's page cache is
  `file − shmem` — RAM the host can take back, and a container that only made the host *read* a large
  file moves neither line), and `kernel`, two thirds of which measured as `slab_reclaimable`.
  A missing `anon` line is *no data*, never 0 MB; a missing `shmem` line is 0.
- **`memory.peak` is not a source.** It counts everything the cgroup ever touched, page cache included,
  and on this kernel it cannot be reset — a container that grew once reads its cap forever, which is
  where a trivia task came to record 4990 MB against a 1024 MB limit. The high-water is MIKE's own:
  `MemoryTracker` keeps `peak` per container, raises it from each sample and never lowers it, and
  `Forget` drops it with the container.
- **Sampling follows the container, not the task's status** (`backend/stream/sandbox_stats.go`): a task
  idle with its container alive still holds RAM the budget must see. Only a container whose ID names no
  task row (the project's `proj:` terminal container) is skipped there. Two ticks share one sampler —
  500 ms to sample (`DefaultSandboxSampleInterval`, also the budget's clock), 2 s to broadcast a value
  that changed. The final reading is taken before the container is removed, so a run's peak is its
  high-water and not its last sample.
- `oom_kill` dies with the container, so `Stop` hands `(peak, oomKills)` to
  `Store.RecordTaskSandboxMemory` before the removal: the one writer of `tasks.sandbox_memory_peak_mb`
  and `tasks.sandbox_oom_kills`. An ID naming no row (the `proj:` container, a task deleted mid-run) is a
  no-op, and a reading that produced neither number reports nothing rather than erasing a previous run.
  `UpdateTask` owns neither column but reads both back, so broadcasting the row it just wrote cannot
  clear a measurement taken during it.
- **A stored peak means *the last run*.** It is not an estimate of anything and is used by no admission
  decision. Peaks written before this definition counted page cache, so `zeroLegacySandboxPeaks` clears
  the column and any dismissal taken against it exactly once, guarded by its own `config` row (the
  statement is not idempotent in the direction that matters: a re-run would zero honest numbers).

## The memory budget: what admits a task, and what pauses one

- **One rule**, in `exec.MemoryGate`: `mikeMB + reservations + candidate ≤ capacity`. `mikeMB` is what
  MIKE itself costs — every container it started plus its own process — and capacity is
  `MIKE_MEMORY_BUDGET_PCT` (default 90) percent of installed RAM, computed once at startup in
  `backend/server` and handed to the gate, to `ConfigStore.SetMemoryBudget` for the About tab, and to the
  startup log line. **The ceiling is MIKE's share of the machine, not a margin over everyone else's
  usage**: what the rest of the host holds is in no admission sum. `/proc/meminfo` has exactly one reader
  in the tree (`meminfoMB` under `readMeminfoMB`), and `/proc/self/status` likewise (`selfRSSMB` under
  `readSelfRSSMB`).
- **`mikeMB` is the sum of three accountants** (`memoryBudget.Measure`): the task containers and the
  project-level `proj:` terminal containers (`SandboxManager.MemoryAccounting`, which iterates the same
  `active` map that holds both), the extension sandboxes
  (`ExtensionSandboxManager.MemoryAccounting`), and the server process itself (`selfProcessAccountant`,
  `VmRSS` — no cgroup, no limit, so held and charge are the same figure; an unreadable `VmRSS` adds
  nothing and warns once, not once per tick). Each container costs `max(held, cap)` —
  `sandboxruntime.ChargeMB` — so a capped container pays its cap, an uncapped one what it holds, an
  unreadable one its cap alone: "unreadable" is not "free". Caps are recorded when the container is
  created (`capsMB`, `extensionSandboxState.memLimitMB`), because the row that carried the limit can
  change while it runs.
- **`heldMB` travels with `mikeMB` for the log, not the rule.** The ceiling never reads it, but the tick
  prints the two side by side, and the gap between them is what makes a `paused (frees 2048 MB)` line
  readable when the container behind it holds 25 MB.
- **The reservation a task acquires lives only across the container's creation.** `acquireMemSlot`
  reserves the project's configured limit — the cap the container is about to get, never a mean of other
  tasks' history — and `confirmMemReservation` spends it as soon as `Create` has registered the
  container, since from then the managers' sums represent it. **A non-sandboxed run books the same number
  and keeps it to the end of the run**: its agent CLI is memory MIKE started with no cgroup to measure,
  so nothing else would ever report it. An uncapped project reserves 0 and is admitted only while the
  budget is *under* the ceiling. Consequence worth knowing: `GetRunningSandboxedTasks` lists only
  sandbox-enabled projects, so a host run's reservation is one the supervisor cannot give back — it is
  returned when the run ends, and only then.
- **An unread host is never read as an empty one.** If either `MemTotal` or `MemAvailable` is missing,
  the tick reports "not measured" and the gate admits on its reservation ledger — the pre-measurement
  behaviour — rather than admitting everything.
- **Pause is the last resort, and it picks the cheapest victim.** `MemorySupervisor` takes each live
  container's charge from `SandboxManager.Charges()` and stops the smallest, one per tick: stopping a
  task destroys its container's writable layer and the turn in flight, and the large task is the one
  likeliest to finish and free its own memory. A task with no live container is costed at the reserve the
  budget already carries, never at zero. Trigger 1 is a non-empty queue — the case a finishing task
  solves by itself. Trigger 2 is `MemAvailable` under 5 % of `MemTotal`
  (`memoryAvailableFloorPct`), which pauses with an empty queue because nothing finishing soon is the
  reason the host is tight; it logs at `Warn`, naming the task and what it waits on. Resume happens only
  when `InUse() + reserve ≤ capacity`, so a resume cannot immediately re-queue.
- **Both transitions write to the task's own history, because the paused row cannot say it.** `noteMemoryPause`
  and `noteMemoryResume` (`backend/exec/memory_supervisor.go`, on the executor side of
  `memorySupervisorActions`) record a `system` note on the task's current thread through `systemNote`
  (`backend/exec/system_note.go`), which is where the role's rule lives: a system row is display-only, since
  `groupTaskMessages` drops it when it rebuilds the conversation, and the same type carries the retry hint
  and the merge orchestrator's notes. The row's `kind` (`memory_pressure` / `memory_resumed`) is what the
  card is worded and coloured by: `systemNoteText`
  renders the sentence from `en.ts` and `severity: "warning"` selects the yellow system card, so the kind
  strings and `chat-entries.ts` must agree case for case; the stored `content` stays English because it is
  what an API client reads. Order is the rule: the pause note goes **after** `SetMemorySupervisorPaused`,
  because it promises the auto-resume that flag arms, and a failed stop or start records nothing. The same
  note corrects `tasks.output`, which the shared stop transition fills with "Paused by user" for every
  pause including this one, through `SetMemoryPressurePausedOutput` — one guarded statement whose guard is
  `execution_state = 'paused' AND memory_supervisor_paused = 1`. That guard doubles as the ownership check
  for the note: a row whose flag a start already cleared gets neither, because whoever acted next owns what
  the task says about why it stopped, and a promise to resume would describe a run that is already going.
  The `status` frame's `activity` line stays "Task paused by user": the SPA reads it only for its
  `Pulling ` prefix, and the note travels beside it.
- **Trigger 2 is the only host-side guard, and it is a thin one.** Because the admission sum ignores
  everyone else's RAM, MIKE can be admitted onto a machine that is already full; nothing in the gate
  refuses it. Below the 5 % floor the supervisor pauses, and below what the kernel will tolerate the host
  memcg killer takes a whole `runsc` sandbox rather than the over-allocating guest process (exit 128,
  `WaitPID failed: EOF`) — the failure recorded in *The cap is the enforcement point* below and in
  `docs/testing.md`. `HostMemoryTight` and admission are deliberately independent: a tight host still
  admits whatever fits MIKE's ceiling, and the supervisor is what reacts.
- **The cap is the enforcement point; a throttle line is not.** `ApplyMemoryLimit` writes `memory.max`
  and `memory.swap.max = 0`, and leaves `memory.high` at `max` (clearing one an older build may have
  left). Measured on this host: a throttle line at 90 % of the cap has nothing to reclaim for a gVisor
  guest — its RAM is charged as `shmem` and swap is off — so crossing it *stalls* rather than sheds: the
  same 512 MB allocation took 21 s with the line (2032 throttle events) and 1 s without, and the cgroup
  OOM-killed either way. (A whole-sandbox death seen alongside it under parallel load is the host memcg
  killer taking runsc, which `docs/testing.md` records for this test with or without a throttle line — the
  stall does not need to be blamed for it. The stall alone is reason enough.) What bounds the overshoot
  between samples is admission, not throttling.
- **A memory cap has a floor below which it kills the sandbox, not the process.** An idle sandbox holds
  ~23 MB for its own guest processes, and a 64 MB cap was measured taking runsc itself rather than the
  over-allocating guest process (exit 128, `WaitPID failed: EOF`, container gone, 3 runs out of 3, no host
  pressure involved). At 128 MB the guest process is OOM-killed and the container survives, which is what
  `TestMemoryLimitEnforced` asserts at that cap. A project configured with a very low limit therefore does
  not get "the runaway process dies"; it gets a dead sandbox.
- **Extension sandboxes are bounded** (`DefaultExtensionMemory` = 2048 MB, above the gopls peak of 1816
  MB measured here): an unbounded one is invisible to the budget until it has already grown. The limit
  field reads three ways — 0 is "no opinion, take the default", positive is that bound, negative is no
  bound — in the manifest and in the stored config alike, so declaring a limit is never overwritten by
  the default.
- **The shared containers count, but not against a task.** One `kanban-ext-<author>-<name>` sandbox per
  LSP extension and a project's `proj:` terminal container are real RAM, so they sit inside `mikeMB`;
  they are never inside a *task's* recorded peak, which is its own container only.
  Extension sandboxes outlive the task that started them (`ExtensionStopGracePeriod`), and a terminal
  container is released by its holders, so attributing them to a task would charge one run for another's.
- What needs a person's attention is decided once, in `frontend/src/lib/utils/memory-stats.ts`: an OOM
  kill, or a peak that reached the limit. Running near the limit is pressure, not an alert — it colours
  the bar and the shield dot orange and asks for nothing. Red is the alert, and it outlives the run
  because the numbers that produced it do.
- A dismissal is a column on the task row, not a record in the browser that clicked: one operator with a
  phone and a laptop sees one answer. `POST …/memory-alert-ack` writes it through
  `Store.DismissTaskMemoryAlert` and broadcasts the task, which is how the other devices find out — the
  same path a status change travels. The ack keeps the three numbers the alert showed and covers an
  alert when the limit matches and neither counter is worse, so a worse run announces itself again; the
  comparison is by numbers rather than a timestamp because a container's counters only rise and
  `sandbox_stats` carries no time. Known nuance: a task that hits an identical cap in a *later* run
  stays dismissed, because nothing on the row distinguishes the runs — a per-container run token, on the
  row and in the `sandbox_stats` frame, is what would re-arm it.
- Tests: `backend/sandbox/memory_peak_test.go` (incl. the high-water surviving a voluntary free, on a real
  gVisor container), `backend/sandbox/memory_accounting_test.go`,
  `backend/extensions/memory_accounting_test.go`, `pkg/sandboxruntime/memorystats_test.go`,
  `backend/exec/memory_gate_test.go`, `backend/exec/memory_supervisor_test.go`,
  `backend/server/memory_budget_test.go`, `backend/stream/sandbox_stats_test.go`,
  `backend/store/tasks_test.go`, `backend/store/migrations_test.go`,
  `backend/api/task_memory_alert_test.go`,
  `frontend/src/lib/utils/__tests__/memory-stats.unit.ts`,
  `frontend/src/lib/api/__tests__/queue-board-scope.unit.ts`,
  `frontend/src/lib/components/__tests__/{task-header-sandbox-dot,sandbox-panel-memory,card-memory-pill}.unit.svelte.ts`.
  The two history notes: `backend/store/tasks_test.go` (the output-text guard),
  `frontend/src/lib/utils/__tests__/chat-entries.unit.ts` (the wording by kind),
  `frontend/src/lib/services/__tests__/stream-handler.unit.ts` (the frame carries kind and severity),
  `frontend/src/lib/components/__tests__/chat-system-note-warning.unit.svelte.ts` (the card it draws), and
  `backend/exec/system_note_test.go` (the two record shapes).
