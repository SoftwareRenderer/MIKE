# Confinement: paths, environments, sizes

The rules are in `AGENTS.md → Confinement`. These are the mechanisms, and they hold for **non-sandboxed**
projects too because the server process enforces them.

## Where a task runs is one column

- `project.sandbox_enabled` is the whole answer: **on** means every LLM-initiated action
  (`read_file`/`write_file`/`edit_file`/`bash`, the LSP tools, the agent adapter) runs in the container
  and a missing bridge is an error rather than a host fallback. The **off** case runs those actions on the
  host, because turning a project's sandbox off *is* the operator's decision about that project.
- `DANGER_ZONE=1` is the permission to make that decision — creating a project with sandboxing disabled,
  or switching one to unsandboxed, is 403 without it (`cannot create project with sandbox disabled — set
  DANGER_ZONE=1 to allow`, checked in `backend/api/project.go` against the injected
  `sandbox.DangerZone` — never a `Getenv` at the gate). It never gates execution.
- The flag has a second, deliberate meaning: it decides what a host terminal is *given*
  (`term.hostShellEnv`) — `os.Environ()` with it, because that terminal is the operator's own shell and a
  scrubbed one cannot run their ssh-agent/registry logins. `agent.SafeAgentEnv` without it, because a
  project row is not a grant of `MIKE_ENCRYPTION_KEY` to a shell.
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
  a workspace root would allow one project-scoped read, write or mkdir to reach any task on the host.
- `?project=` and `?task=` arrive uncorrelated, so `requireTaskOwnership` ties them
  (`store.GetTask(project.ID, taskID)`) before either is honoured. `resolveWorktreeForFile` calls it too,
  because the preview asset root is derived from the same resolution and a preview token is read without
  a session.
- Both jails go through `fsutil.Contained`, which needs the lexical path *and* the symlink-resolved path
  inside a root: a task with an rw bind mount can plant `link -> /etc` in its own worktree and a lexical
  check reads through it. The preview asset root calls it too.
- **The jail names a file, and the open is checked against that name.** `fsutil.Contained` answers about
  a name; opening it is a second syscall, and the task owns the directory the name sits in. So the name is
  `Lstat`ed first and the opened file compared with that — `fsutil.OpenVerified`, refusing with
  `ErrSwapped`, or `OpenChecked` where nothing else reads that stat. It refuses a name that is not a
  regular file (`ErrNotRegular`) rather than opening it, because opening a FIFO waits for whoever writes
  to it. A *link* at a name `Contained` returned is named `ErrSwapped` rather than an irregular file — a
  jail never resolves to a link, so that is a swap, and the log line is the only place an attempt shows up.
  Surfaces: the working-tree read, a preview asset,
  the untracked content of a diff, `GET /projects/…/git/diff/file`, and the LSP document sync's read.
  `openJailedFile` is the same rule with the response mapping on top, which `/api/fs/raw` and
  `/api/fs/download` already used. A tree walked a name at a time is the same race at every entry: the
  folder download's archive (`writeTarOne`, whose header comes from the walk's own lstat) and the host
  paste (`copyFile`), both in `backend/fsutil/`. A swapped name there is how a file outside the project
  reaches the browser, or lands inside it where the task can read it. Neither route has a container branch:
  a sandboxed project's worktree is a host directory the container bind-mounts, so these are host reads of
  a tree the task writes — `TestFSRoutesOfASandboxedProjectActOnTheHostBytes` pins that.
  Tests: `TestOpenVerifiedRefusesANameThatChangedUnderTheCheck`, `TestUntrackedReadTargetHandsBackTheFileItChecked`,
  `TestFSReadRefusesANameThatIsNotARegularFile`, `TestReadDocForSyncRefusesANameThatIsNotAFile`,
  `TestOpenCheckedCallsALinkAtTheNameASwapNotAnIrregularFile`,
  `TestWriteTarOneRefusesANameTheWalkNoLongerRecognises`,
  `TestCopyEntryRefusesANameTheWalkNoLongerRecognises`.
- **A container spelling is translated before the jail looks at it.** `validateFSPath` turns
  `/home/sandbox/<project>/…` into the host file it stands for — `sandbox.ContainerToHost` against the tree
  the request names, `project.Path` when it named no task — and jails *that*. The spelling is not a host
  path, and a checkout can sit at `/home/sandbox/<its own name>`, where the two spellings are one string
  and an answer returned unresolved never had its links read. A project with no container, another
  project's mount and a lookalike mount name take the same refusal: none of them names a file here.
- **The host→container half refuses instead of guessing.** `fsSandboxPath` converts against the root with
  its symlinks resolved — the root `fsutil.Contained` accepted the path under — and
  `sandbox.HostToContainer` answers `(string, bool)`. A `Rel` between two different trees does not fail, it
  steps out with `..`, and `filepath.Join` cleans that away into a sibling of the mount. A path outside the
  tree the request named yields no container path and the handler answers `fsutil.ErrOutside`, like any
  other containment refusal.
  Tests: `TestValidateFSPathMountShapedHostPathStillResolvesLinks`,
  `TestFSSandboxPathRoundTripsThroughSymlinkedProjectPath`, `TestHostContainerRoundTrip`.
- **Whether a `filepath.Rel` landed inside its root is `fsutil.RelInsideRoot` / `RelBelowRoot`**, never a
  hand-written `..` test: the two differ by `"."`, the root itself, and the loose `HasPrefix(rel, "..")`
  also refuses the legal name `..foo`. Lexical half only — a jail adds `Contained` for the symlink half.
- **A write goes through `fsWriteTarget` then `writeContainedFile`** instead of repeating the steps:
  `fsWorktreeRoot` → `requireSandboxAvailable` → `ContainerToHost` → `validateFSPath` are each the jail
  for the next, so a handler that drops one — or passes `""` where the worktree root belongs — writes
  outside the project and still compiles.
- **`GET /api/fs/raw` takes a repository-relative path, so it jails the join.** The diff surfaces hold
  paths relative to the repository root and pass those. The handler refuses an absolute or `..`-stepping
  path, joins what is left to `fsWorktreeRoot`'s tree, and hands the result to the same `validateFSPath`.
  Its own rule is what a response may *be* — one map of image extensions, which is the permission and the
  `Content-Type` at once — so a request cannot aim the route at a file that would run in the browser.
  Two things follow from reading a file a task is alive next to: the name is `lstat`ed and compared with
  the opened file, because the task owns the directory the check just resolved, and `ref` reads from
  **`project.Path`** — the tree the request names is the project's, not the task's, since worktrees share
  one object database and a done task has no worktree left to name. `docs/api-contract.md → Raw file bytes`.
- **A download jails every path of the batch it names.** `GET /api/fs/download` takes a repeated
  `?path=`: the host or container paths the listing handed the browser. Each one is settled through
  `fsRequestScope` then `fsScopeTarget` before any of them opens, so a selection whose last row leaves
  the project streams nothing. A `.git` row is refused — the listing hides one, so reaching it takes a
  hand-built URL — and so is a path above the folder the first path sits in, whose archive name would
  walk out of the folder that was clicked. Inside the archive `fsutil.walkTree` holds the rules, which
  is the walk a copy takes: symlinks are written as links and never read through, and irregular files
  are skipped rather than opened. `docs/api-contract.md → Downloading a file or a folder`.
- **A containment refusal answers `fsutil.ErrOutside` and nothing else**, because every file handler
  answers with `err.Error()` and `fsutil.Contained` names both the path it refused and the path a
  symlink resolved to. The two paths go to the log. A repository can carry a symlink, so repeating its
  target would let the requester read the server's directory layout out of a 400.
- **A task's uploaded file is a host write into that task's worktree** (`exec.WriteTaskFile`, landing in
  `.mike-uploads/`), so it is not the path above — there is no container to translate a guest path through.
  It runs the same `fsutil.Contained` check against the worktree root before the bytes go, because the task's
  own actions can plant a symlink at that name, and writes `0600` into a `0700` directory.
  `docs/sandbox-bridge.md`.
- **The file jail is two halves so a batch can share one of them.** `fsRequestScope` settles what the
  *request* decides (`fsWorktreeRoot` → `requireSandboxAvailable`), `fsScopeTarget` settles what one
  *path* needs (`ContainerToHost` → `validateFSPath`), and `fsWriteTarget` is the two in order for a write
  with a single path. `POST /api/fs/delete|copy|move|rename` (`handler_fs_ops.go`) resolves **every** path of
  the batch against the one `fsScope` before anything is removed or written, so a selection whose last row is
  outside the project moves nothing, and no path can be checked against a tree other than the one the request
  named. These four run on the host filesystem for a sandboxed project too — the bind mount's bytes are there,
  exactly as `POST /api/fs/write` — which is why the translation, not the request body, decides the host path.
- **Two refusals live inside the jail and are still refused**: any `.git` component
  (`fsutil.ErrGitPath` — including a worktree's `.git` *file*, whose removal no in-worktree command can undo)
  and a tree root or its ancestor (`fsutil.ErrProtectedPath`: the project's checkout and the task's own
  worktree, directories this server created and tears down elsewhere — `docs/commit-and-done.md`). Both are
  answered **per item**, beside the path they happened to, because a batch is a person's selection and one
  wrong row should not cost the rest of it. Only a containment failure, which is the jail rather than one
  row's bad luck, fails the whole request. A nested `.git` directory is skipped by both the size walk and the
  copy itself, so a folder that happens to hold a repository does not drag it along.
- **Every bound runs before the bytes** (`backend/fsutil/ops.go`): a copy walks its tree and refuses past
  `CopyMaxBytes` (64 MiB), `CopyMaxEntries` (5000) or `CopyMaxDepth` (32), the copy loop re-counts as it writes
  (the walk is the refusal, the running total is the bound against a tree a task is still writing into), and a
  copy that fails removes what it wrote, so a refused copy leaves no half-tree behind. The request's own bound
  sits beside it: at most `maxFSOpsBatch` (100) paths, and the `name copy`, `name copy2` search stops at
  `UniqueNameMaxTries` (200). Without them a paste of `node_modules` is unbounded work inside one HTTP request.
- **The one file route with no size refusal is the one that captures nothing.** `GET /api/fs/download`
  answers a folder with a tar.gz written straight to the response through one 32 KiB buffer, and the
  compression middleware passes it through. The tree's size decides how long that takes, not how much the
  server holds. What the request keeps is `maxFSOpsBatch`, which bounds its shape. A person asking for
  their own tree is asking for the bytes.
- Tests: `backend/fsutil/ops_test.go`, `backend/server/handler_fs_ops_test.go`, beside the ones above.

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
  `readDocForSync` runs it through `fsutil.Contained` and then opens the file that check named. Without
  the first half a `..`-escaping `path` on an LSP tool call or a diagnostics pass reads any file the
  server user can open. Without the second half, the text sent to a language server inside the task's own
  container is whatever the task put at the name afterwards.
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
`key -> ~/.ssh/id_rsa` exactly the way it lists a file. Its answer is **the open file**, not a path: the
listing's path and the content read are separate steps, and the worktree belongs to the task. Both reads
are size-capped at `maxDiffFileBytes`; past the cap the cached window plus `truncated` is the answer.
Tests: `backend/api/git_diff_file_path_test.go`, `git_diff_file_full_test.go`,
`git_diff_size_cap_test.go`, `backend/git/untracked_symlink_test.go`.

## Nothing is read or captured unbounded

- `MaxBodyMiddleware(httputil.MaxRequestBodyBytes)` in `Server.middlewareChain` bounds every non-GET
  body (GET is skipped so the WebSocket upgrades stay upgrades): a declared length over the cap is
  refused up front, a chunked one by the wrapped reader. Most handlers (`git`, `fs`, `task`, `thread`)
  decode `r.Body` directly rather than through `httputil.DecodeJSON`, which has its own limit and answers
  **413** rather than "invalid request body" when the body was simply big.
- Outbound reads are capped too: an extension's answer (`httputil.ReadUpstream`, applied by
  `httputil.DoOutbound`) buffers a bounded response and answers it with a type of MIKE's own
  (`proxyContentType`), never the upstream's. A contributed tool's answer is bounded twice over — the
  manifest's own `max_results` / `max_snippet_chars`, then `toolresult.MaxCaptureBytes` on the rendered
  text, the same number a command's output is held to (`docs/extension-tools.md`). An MCP tool's answer is
  bounded the same way, and one server-sent event is refused past `httputil.MaxUpstreamBodyBytes` rather
  than buffered (`docs/mcp.md`).
- A subprocess's output is bounded where it is captured, not where it is shown: `toolresult.Capture`
  (`backend/toolresult/capture.go`) is the writer a sandboxed `Exec` hands its command and
  `streamLineWriter` does the same for the host bash path, both at `MaxCaptureBytes` with a trailing
  marker naming what was dropped. `toolresult.Truncate` runs *after* the whole output is in memory, and
  `find /` would have cost the server the run. A capture writer always reports the full byte count: a
  short one makes `io.Copy` stop and lose the rest silently.
- `GET /api/fs/read` checks size **before** reading — `os.Lstat` for the working tree,
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
  `os.Environ()` leaked `MIKE_ENCRYPTION_KEY` via `/proc/self/environ` to an
  extension-supplied binary, so never `cmd.Env = os.Environ()` here; add a variable only if an agent
  genuinely needs it. The pairing code is no exception in the other direction: it is memory-only, so it
  is never in this process's environment to inherit.
- The six proxy variables every family needs are one set — `subproc.IsProxyEnv` — because the reason is the
  same for all three: a private remote or registry behind an egress proxy stops resolving without them.
  A proxy variable one family reads and the others do not (`NO_PROXY_HOSTS`, podman) belongs in that
  family's own list, not in the shared one.
- `os.Environ()` survives in exactly one sanctioned place: `term.hostShellEnv` under `DANGER_ZONE=1`,
  because there the shell is the operator's own.
- Tests: `backend/agent/env_test.go`, `backend/git/env_test.go`, `pkg/sandboxruntime/podmanenv_test.go`.

## The git author identity

`git.SetAuthorIdentity` / `git.AuthorIdentity` (`backend/git/worktree.go`) are process-wide and
accessor-only: there are deliberately no exported `GitAuthorName`/`GitAuthorEmail` variables, because the
add-project flow writes the identity while tasks are committing. Making it per-`WorktreeManager` is still
open.
## The address a person typed is a network grant

Two origins of an outbound address exist, and they are not equally trusted. An address this server chose —
an update manifest, a configured provider — goes out through the ordinary client. An address a **person
typed** is the permission to reach it, and it is also a risk: a typo is a dial into this host.

- An extension contributed tool may contact only the origin stored in the `config_schema` property of type
  `"url"` its `request.url` names (`docs/extension-tools.md`).
- An MCP server may be dialled only at the URL its row stores (`MCPServer.URL`), which
  `types.ValidateMCPServer` has already reduced to an http or https origin with no credentials in it.
- Both go out through `httputil.NewExtensionEgressClientGranting`, whose widening is the same for both and
  narrower than it looks: **loopback only**, for the typed host alone, with the link-local metadata address
  and every other non-public range still refused even when named. Redirects are refused outright, so one
  registered address cannot become a proxy into the local network. The grant is per client, which is why a
  granted call gets its own transport with keep-alives off.
- Neither runs in a task container, so a project's Network Isolation says nothing about either. The Sandbox
  Configuration states that, naming the extensions and the MCP servers the project enabled
  (`frontend/src/lib/utils/server-tool-network.ts`).
- Tests: `backend/httputil/client_test.go` and `egress_test.go` (the grant, the refusal, the metadata
  address refused while named), `backend/mcp/era_test.go` (the same rule through an MCP dial),
  `backend/mcp/probe_test.go` (the Test action dials what the form holds and nothing else).

## Checking this

`go test ./backend/api/ ./backend/fsutil/ ./backend/sandbox/ ./backend/server/` covers the jails, the
two-way path translation and the caps. `make check-race` runs the same packages with the race detector,
which is where a jail that holds only under concurrency shows it.
