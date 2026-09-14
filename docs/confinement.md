# Confinement: paths, environments, sizes

The rules are in `AGENTS.md → Confinement`. These are the mechanisms, and they hold for **non-sandboxed**
projects too because the server process enforces them.

## Where a task runs is one column

- `project.sandbox_enabled` is the whole answer: **on** means every LLM-initiated action
  (`read_file`/`write_file`/`edit_file`/`bash`, the LSP tools, the agent adapter) runs in the container
  and a missing bridge is an error rather than a host fallback; **off** means those actions run on the
  host, because turning a project's sandbox off *is* the operator's decision about that project.
- `DANGER_ZONE=1` is the permission to make that decision — creating a project with sandboxing disabled,
  or switching one to unsandboxed, is 403 without it (`cannot create project with sandbox disabled — set
  DANGER_ZONE=1 to allow`, checked in `backend/api/project.go` against the injected
  `sandbox.DangerZone` — never a `Getenv` at the gate). It never gates execution.
- The flag has a second, deliberate meaning: it decides what a host terminal is *given*
  (`term.hostShellEnv`) — `os.Environ()` with it, because that terminal is the operator's own shell and a
  scrubbed one cannot run their ssh-agent/registry logins; `agent.SafeAgentEnv` without it, because a
  project row is not a grant of `MIKE_LOGIN_PASSWORD` and `MIKE_ENCRYPTION_KEY` to a shell.
- `toolEnv.hostGuard` (`backend/exec/tool_dispatch.go`) refuses `read_file`/`write_file`/`edit_file`/`bash`
  when a sandboxed task's sandbox callback is missing: a missing bridge is an outage, not a licence to
  work on the host behind the container. The LSP tools resolve their symbol through the same reader
  (`toolEnv.reader`), never a direct `os.ReadFile` of the worktree path. Nothing jails an *unsandboxed*
  task: its worktree join is the host, so `..` and absolute paths go where they name, by design.
- Tests: `TestSandboxedTaskWithoutBridgeRefusesEveryHostTool` (one table, every tool),
  `backend/term/host_shell_env_test.go`, `backend/exec/sandbox_file_access_test.go`,
  `backend/api/project_test.go`.

## File endpoints: the project plus the task the request names

`backend/server/handler_fs.go`:

- `validateFSPath` takes the resolved worktree root (`fsWorktreeRoot`) and allows exactly that directory
  and the project checkout — **never the workspace dir**, which holds every project's task worktrees, so
  a workspace root would let one project-scoped read, write or mkdir reach any task on the host.
- `?project=` and `?task=` arrive uncorrelated, so `requireTaskOwnership` ties them
  (`store.GetTask(project.ID, taskID)`) before either is honoured. `resolveWorktreeForFile` calls it too,
  because the preview asset root is derived from the same resolution and a preview token is read without
  a session.
- Both jails go through `fsutil.Contained`, which requires the lexical path *and* the symlink-resolved
  path to sit inside a root — a task with an rw bind mount can plant `link -> /etc` in its own worktree,
  and a lexical prefix check reads right through it. The preview asset root uses the same
  `fsutil.Contained` call rather than resolving both sides by hand.
- **A write goes through `fsWriteTarget` then `writeContainedFile`** instead of repeating the steps:
  `fsWorktreeRoot` → `requireSandboxAvailable` → `ContainerToHost` → `validateFSPath` are each the jail
  for the next, so a handler that drops one — or passes `""` where the worktree root belongs — writes
  outside the project and still compiles.
- **`GET /api/fs/raw` takes a repository-relative path, so it jails the join.** The diff surfaces hold
  paths relative to the repository root and pass those; the handler refuses an absolute or `..`-stepping
  path, joins what is left to `fsWorktreeRoot`'s tree, and hands the result to the same `validateFSPath`.
  Its own rule is what a response may *be* — one map of image extensions, which is the permission and the
  `Content-Type` at once — so a request cannot aim the route at a file that would run in the browser.
  Two things follow from reading a file a task is alive next to: the name is `lstat`ed and compared with
  the opened file, because the task owns the directory the check just resolved, and `ref` reads from
  **`project.Path`** — the tree the request names is the project's, not the task's, since worktrees share
  one object database and a done task has no worktree left to name. `docs/api-contract.md → Raw file bytes`.
- **A containment refusal answers `fsutil.ErrOutside` and nothing else**, because every file handler
  answers with `err.Error()` and `fsutil.Contained` names both the path it refused and the path a
  symlink resolved to. The two paths go to the log. A repository can carry a symlink, so repeating its
  target would let the requester read the server's directory layout out of a 400.
- **A task's uploaded file is a host write into that task's worktree** (`exec.WriteTaskFile`, landing in
  `.mike-uploads/`), so it is not the path above — there is no container to translate a guest path through.
  It runs the same `fsutil.Contained` check against the worktree root before the bytes go, because the task's
  own actions can plant a symlink at that name, and writes `0600` into a `0700` directory.
  `docs/sandbox-bridge.md`.

## The no-project host jail

`GET /api/fs`, `mkdir` and `init-git` carry **no** `project` — the directory picker, and with it adding a
project or cloning into a path. `fsutil.HostRoot` (`backend/fsutil/hostroot.go`) jails them to `$HOME`
plus the directories the operator declared at startup (workspace dir, extensions dir), or to any host
path with `MIKE_ALLOW_ROOT_FS=1`.

- It is built **once** in `NewServer` and injected into `api.NewProjectHandler`, so the picker and
  project create cannot disagree about what was declared.
- **The two workspace rules are not one:** the workspace dir is never a root for a project-scoped
  request, and it **is** a root for the no-project jail, where there is no project to protect against.
- Deliberately absent from the roots: the data directory (it holds `kanban.db` and `data/tls/*.key`, so
  browsing it is a private-key read) and the registered project paths (human-typed locations in the DB;
  including them would let one session browse every path this install ever touched).
- The jail uses `os.Root` for open/stat — `..` and a symlink resolving outside are refused in the kernel,
  with no check-then-use window. Creation goes through a per-component `Lstat`+`Mkdir` walk rather than
  `(*os.Root).MkdirAll`, because os.Root refuses every path involving a symlink and its `*PathError` is
  not the jail's 403; the walk refuses with `ErrHostRoot` and names the link. Reads fall back to
  `fsutil.Contained` on that same refusal, which is what keeps an inside-root link
  (`~/go -> ~/storage/go`) browsable.
- A clone refusal happens in `prepareCloneTarget`, **before** any byte is fetched, so a refused request
  leaves no partial checkout. The jail reaches the picker only through a refusal: `GET /api/home` answers
  the home it opens at and nothing about the jail.
- Tests: `backend/fsutil/hostroot_test.go`, `backend/server/handler_fs_hostroot_test.go`,
  `backend/server/server_hostroot_test.go`, `backend/api/project_hostroot_test.go`,
  `backend/server/handler_fs_sandbox_test.go`, `backend/server/handler_fs_upload_test.go`.

## Two host reads outside the tool path

- `ensureDocSync` (`backend/lsp/extension_manager.go`) reads the file it is about to
  `didOpen`/`didChange` **with the server's own privileges** — the one host read left after `hostGuard`
  refused the tool's own — and its `filePath` is a tool argument joined to the workspace, so
  `docSyncPath` runs it through `fsutil.Contained` before `os.ReadFile`. Without it a `..`-escaping
  `path` on an LSP tool call or a diagnostics pass reads any file the server user can open.
- A project's `sandbox_dockerfile_path` is read at build time the same way and is checked **where it is
  accepted** (`checkHostPath` on create and `PUT`), against the jail the Dockerfile picker browses, for
  the same reason as `types.ValidateCommitHash`: the stored value is what the later read trusts and never
  re-checks.
- Tests: `backend/lsp/docsync_test.go`, `backend/api/project_dockerfile_jail_test.go`.

## The git side's two working-tree reads

`repoFilePath` (`backend/api/git.go`) jails `GET /projects/{id}/git/diff/file`, whose working-tree
read follows a link committed into the repo even though `git diff` itself answers from the object
database. `git.UntrackedReadTarget` jails the `?full=1` re-read behind
`GET /projects/{id}/tasks/{id}/diff/file`, whose path came from `git status` — which lists
`key -> ~/.ssh/id_rsa` exactly the way it lists a file. Both reads are size-capped at
`maxDiffFileBytes`; past the cap the cached window plus `truncated` is the answer.
Tests: `backend/api/git_diff_file_path_test.go`, `git_diff_file_full_test.go`,
`git_diff_size_cap_test.go`, `backend/git/untracked_symlink_test.go`.

## Nothing is read or captured unbounded

- `MaxBodyMiddleware(httputil.MaxRequestBodyBytes)` in `Server.middlewareChain` bounds every non-GET
  body (GET is skipped so the WebSocket upgrades stay upgrades): a declared length over the cap is
  refused up front, a chunked one by the wrapped reader. Most handlers (`git`, `fs`, `task`, `thread`)
  decode `r.Body` directly rather than through `httputil.DecodeJSON`, which has its own limit and answers
  **413** rather than "invalid request body" when the body was simply big.
- Outbound reads are capped too: the extension proxy (`maxProxyResponseBytes`) buffers a bounded response
  and answers it with a type of MIKE's own (`proxyContentType`), never the upstream's.
- A subprocess's output is bounded where it is captured, not where it is shown: `toolresult.Capture`
  (`backend/toolresult/capture.go`) is the writer a sandboxed `Exec` hands its command and
  `streamLineWriter` does the same for the host bash path, both at `MaxCaptureBytes` with a trailing
  marker naming what was dropped. `toolresult.Truncate` runs *after* the whole output is in memory, and
  `find /` would have cost the server the run. A capture writer always reports the full byte count: a
  short one makes `io.Copy` stop and lose the rest silently.
- `GET /api/fs/read` checks size **before** reading — `os.Stat` for the working tree,
  `git cat-file -s` (`gitBlobSize`) for `?ref=` — so a request for a 4 GB file costs one subprocess
  instead of 4 GB.
- What crosses the sandbox bridge is bounded the same way: `protocol.MaxFramePayload` is checked before
  `ReadFrame` allocates from the declared length, `protocol.MaxReadBytes` is enforced in the guest *and*
  on the host's chunk collector (a stale bridge must not be able to stream past it), and a sandboxed
  `bash` captures through `toolresult.Capture` exactly like the host path. Numbers:
  `docs/sandbox-bridge.md`.
- Tests: `backend/server/body_limit_test.go` (drives `middlewareChain`, because an unwired middleware
  protects nothing), `backend/httputil/decode_json_test.go`, `backend/extensions/proxy_limit_test.go`,
  `backend/toolresult/capture_test.go`, `backend/exec/bash_stream_cap_test.go`,
  `backend/server/handler_fs_size_test.go`, `pkg/bridge/protocol/frame_limit_test.go`,
  `pkg/bridge/agent/committed_bridge_test.go`, `backend/exec/output_limits_test.go`.

## Subprocess environments: exactly three allowlists

Each belongs to one subprocess family, and each returns the **whole** environment because assigning a
bare list to `cmd.Env` *replaces* rather than extends; extras are appended last (`os/exec` keeps the last
occurrence).

- `git.Env` — `HOME`/`SSH_AUTH_SOCK`/`GIT_*`/`LC_*`/proxy/trust-store, because git needs the operator's
  own configuration and credentials to reach their remotes.
- `sandboxruntime.PodmanEnv` — the XDG/DBUS trio rootless podman genuinely reads, plus `CONTAINERS_*`.
- `agent.SafeAgentEnv` (`backend/agent/env.go`) — the only source of `cmd.Env` for an adapter's
  subprocess (`runner.go`) and its `--list-models` probe (`loader.go`):
  `PATH`/`HOME`/user/term/locale/proxy/trust-store (+ `LC_*`) plus the adapter's own extras.
  `os.Environ()` leaked `MIKE_LOGIN_PASSWORD` and `MIKE_ENCRYPTION_KEY` via `/proc/self/environ` to an
  extension-supplied binary, so never `cmd.Env = os.Environ()` here; add a variable only if an agent
  genuinely needs it.
- `os.Environ()` survives in exactly one sanctioned place: `term.hostShellEnv` under `DANGER_ZONE=1`,
  because there the shell is the operator's own.
- Tests: `backend/agent/env_test.go`, `backend/git/env_test.go`, `pkg/sandboxruntime/podmanenv_test.go`,
  `pkg/shlex/shlex_test.go`.

## The git author identity

`git.SetAuthorIdentity` / `git.AuthorIdentity` (`backend/git/worktree.go`) are process-wide and
accessor-only: there are deliberately no exported `GitAuthorName`/`GitAuthorEmail` variables, because the
add-project flow writes the identity while tasks are committing. Making it per-`WorktreeManager` is still
open.
