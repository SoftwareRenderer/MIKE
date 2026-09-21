# Git: argument construction, stash identity, transport

The rules are in `AGENTS.md → Git`. This is the mechanism behind each fence.

## One command constructor, and repo content never executes

- `git.Command(dir, args…)` / `git.CommandContext(ctx, dir, args…)` are the only way this repo starts
  git, and `TestNoGitSubprocessSpawnsOutsideGit` walks `backend/`, `pkg/` and `scripts/` to keep that
  true. Both set `Dir`, `Env: Env()` and `WaitDelay` in one place, so the allowlist and the hook fence
  cannot be forgotten at a call site. `_test.go` is exempt for a stated reason, not for churn: the
  control-case test must be able to spawn an **unfenced** git.
- `git.HookFence()` pins `core.hooksPath=/dev/null` and `core.fsmonitor=false` via
  `GIT_CONFIG_COUNT`/`KEY_n`/`VALUE_n`, appended *after* the extras so `os/exec`'s last-wins makes it
  unforgeable. `--no-verify` is not equivalent — it has no effect on `post-checkout`, which
  `git worktree add` runs. Accepted user-visible consequence: a `pre-push` hook no longer runs when
  someone clicks Push.
- The fence pins those two keys only. Repo-readable settings that name a program otherwise
  (`credential.helper`, `core.askPass`, `diff.<name>.textconv`) are deliberately left alone: a helper in
  the operator's own configuration is how a private HTTPS remote authenticates here.
  `GIT_TERMINAL_PROMPT=0` is what keeps a remote that cannot authenticate from hanging the request.
- Bounds: a **cancelled context** bounds the child, a **WaitDelay** bounds the output pipes after the
  kill, and neither substitutes for the other. `WithStash`'s bracket and the worktree watcher both
  involve processes that can outlive their caller (see `docs/locking.md`).

## The worktree layout

`<worktreesDir>/<projectID>/<taskID>`, derived in `backend/git` only: `worktreesRoot` →
`projectWorktreesDir` → `WorktreePath`, used by `CreateWorktree`, `RemoveWorktree`,
`RemoveProjectWorktrees` and `GarbageCollect`. `RemoveWorktree` takes a task's own directory;
`RemoveProjectWorktrees` takes the parent, which is what clears a directory a failed per-task removal
left behind. `--worktrees-dir` overrides `WorkspaceDir` at runtime and is not persisted to the DB
(`docs/configuration.md`).

### `.git/info/exclude`: what the repository hides from itself

Two things are MIKE's own and must never show in a project's status or be swept into a stash: the worktrees
directory when it lives inside the project, and a task's `.mike-uploads/`. Both are declared by appending a
pattern to the repository's **local** `.git/info/exclude` through `EnsureExcluded` (`backend/git/gc.go`),
which writes each pattern once and never touches the project's own `.gitignore`.

Two facts about that file, both measured:

- **It lives in the common git dir.** A checkout made by `git worktree add` has a gitdir of its own, and git
  reads `info/exclude` from the shared one only — a pattern written under the per-checkout gitdir is a silent
  no-op, which is what the first version of this did for a project that is itself a worktree.
  `repoExcludeFile` asks git instead (`rev-parse --path-format=absolute --git-common-dir`);
  `TestEnsureExcludedFollowsAGitdirPointerToTheSharedGitDir` fails against reading the `.git` file by hand.
- **Patterns are relative to the checkout root, so they stay unanchored.** A task's worktree lives *below*
  that root, so `.mike-uploads/` — which matches at any depth — is the form that reaches it, and
  `/.mike-uploads/` would match the project root alone.

Tests: `backend/git/exclude_test.go` (idempotence, the shared git dir, and what git can and cannot do to an
excluded upload: `status`, `add -A`, `stash -u`, `clean -fd`, a checkpoint restore).

### Removing a worktree: directory, prune, branch

The registration in the project's `.git/worktrees/`, not the directory, is what makes a branch
undeletable: `git branch -D` answers `cannot delete branch 'task/x' used by worktree at …` while a
registration names the branch, and a `worktree prune` run while the directory is still on disk prunes
nothing. So `RemoveWorktree` goes directory → prune → branch, and no other order deletes anything.
The branch delete runs even when the directory could not be removed, and a refusal is returned with
what git printed (`deleteBranch`) — git writes its reason to stderr, and the caller's log line is the
only place it can surface. `PruneOrphanedBranches` prunes first too, under the project's worktree
lock (the one `CreateWorktree` takes, busy-skip-and-log like `GarbageCollect`), because the branches
it is looking for are the ones a worktree removed moments ago still holds. `GarbageCollect` runs only
at startup, so nothing else sweeps: a leftover is not self-healing.

Branch names come back from `listBranchNames`/`ListProjectBranches` (`backend/git/branches.go`),
which reads `refs/heads/<prefix>*` with `for-each-ref`. `git branch --list` decorates its output —
`+ ` on a branch a linked worktree holds, `* ` on the current one — and anything compared against
that output misses both, which is how the branches the refusal produced stayed invisible to the sweep
meant to catch them. The glob is one path segment wide, the shape this repo writes (`task/<id>`,
`task/<slug>-<random6>`, `thread/<id>`), so a nested branch a person made under the same namespace is
never read as an orphan.

**Tests:** `backend/git/worktree_removal_test.go` (the three-part removal, the decorated orphan,
live branches kept), `backend/git/branch_listing_test.go` (both markers, slug prefix, one segment),
`backend/server/task_delete_branches_test.go` (single delete, Clear-all, fork thread branch).

## Stash: the bracket and the identity

- Work that must run against a temporarily clean tree goes through `WithStash`
  (`backend/git/stash_bracket.go`) — the only home for the push/run/restore bracket, which the merge
  paths take rather than writing the sequence themselves. A bracketed section begins with
  `git checkout -f`, which silently reverts tracked modifications to HEAD when the tree is dirty, so
  `WithStash` **refuses to run the work when `PushStash` fails** (`ErrNothingToStash` is the one
  tolerated failure — there was nothing to take), and `fn` returns `ErrKeepStash` to leave the entry
  stacked for the turn that later resolves a merge conflict. The stash mutex is taken around the push and
  around the restore and held across neither, so `fn` may take the merge lock.
- **A stash entry is identified by hash, never by position.** `git stash pop` pops `stash@{0}` and has no
  idea who pushed it; the stack is shared by every worktree of the project *and holds the user's own
  entries*. Measured with MIKE's own labels: A pushes, B pushes, A pops → **A's worktree receives B's work
  and A's entry is left for B**, with no error on either side.
  - `PushStash` returns the created entry's hash (diffing the list); `PopStashByHash` applies by hash and
    **leaves an entry it could not apply** rather than destroying unapplied work.
  - Callers that never saw the push find it through `RestoreStashForTask`/`DropStashForTask`, whose
    identity is the `StashMessageFor*` label because it embeds the task ID.
  - Never a bare `git stash drop` (it discards `stash@{0}`, possibly the user's) and never a fallback to
    position when a hash cannot be found.
  - `git stash push` exits 0 having created nothing on a clean tree — `PushStash` returns
    `ErrNothingToStash` when `err == nil` and nothing was created.
- Two stash uses stay outside `WithStash` deliberately: `stashWorktreeState`
  (`backend/server/handler_thread.go`) hands the hash back for a later thread switch to restore, which a
  bracket scoped to one call cannot express, and `loop_agent.go` restores by label with no push of its
  own.
- **The restore keeps the work's staged/unstaged split.** A plain `git stash apply` brings a
  modification back unstaged (a new file stays staged), and commit-and-done commits the index only, so a
  task whose own worktree was merged into — a subtask landing in its parent — loses its edits to
  existing files from its own commit and then loses the worktree. `PopStashByHash` restores with
  `--index` and falls back to a plain apply only when the refused attempt changed nothing: measured, git
  answers `conflicts in index. Try without --index.` and leaves the tree untouched, so the retry cannot
  double-apply. A retry that also fails keeps the entry on the stack and the worktree conflicted.
- **A conflicted restore is reported, with the paths read from git rather than from its wording.**
  `PopStashByHash` checks `git diff --name-only --diff-filter=U` after a failed apply and returns
  `*RestoreConflictError` (the paths and the entry's hash, classified with `errors.As`) only when the
  index is unmerged: a conflict is a file a person has to resolve, a refusal left the tree alone, and
  git's phrasing does not tell them apart. What a caller does with it is in *Commit and Done*
  (`docs/commit-and-done.md`) — the merge paths finish the task on it, which is why the git layer states
  it separately instead of returning one error for the bracket.
- Tests: `backend/git/stash_identity_test.go`, `push_stash_test.go`, `stash_bracket_test.go`,
  `stash_restore_conflict_test.go`, `backend/exec/sync_merge_test.go`
  (`…_KeepsUnstagedWorkWhenTheStashFails` — its stash-ref injection is
  used instead of an index lock because a locked index blocks the forced checkout too, so the loss never
  happens and the test passes vacuously).

## Discard: one file back to HEAD, or off disk

`POST /api/projects/{projectID}/git/discard` (`discardFile`) reverts one path in `project.Path` — the
checkout every other Review Changes button writes to.

- **HEAD decides revert from delete** (`ls-tree --name-only HEAD -- p`): the index cannot answer it, a
  staged deletion holds no entry either.
- **Revert is `checkout HEAD -- p` alone** — it writes the index along with the worktree, so the staged half
  of an edit and a staged deletion both go.
- **Delete is `reset -q HEAD -- p`, then `clean -f -q -- p`, never `os.Remove`:** the reset unstages a file
  staged as new; clean skips ignored files and directories another repository owns.
- Nothing parks the work — no stash entry, no snapshot ref — so the panel's confirmation dialog is the
  whole guard, worded from the status letter (`?`, `A`, `C`, `R` are the letters HEAD holds no path under).
- Tests: `backend/api/git_discard_test.go`, the discard block of `git-status-panel.unit.svelte.ts`,
  `review-changes-discard.spec.ts`.

## Client-supplied refs are validated, then fenced

`types.ValidateGitRef` (`backend/types/validate.go`) is deliberately tighter than `ValidateBranchName`
(no `~ ^ : ? * [ \`, space, leading `-`). `GET /api/fs` validates `?ref=` **and** passes
`--end-of-options` (git ≥ 2.24) before `<ref>:<path>` in `git show`, so a ref can never parse as an
option (`--output=/tmp/pwn` would create/truncate a file anywhere the server user can write).

## Clones speak only allowed transports

- `git.CloneArgs` puts `-c protocol.ext.allow=never` *before* `clone` (so the process parsing the URL
  gets it) and `git.CloneEnv` sets `GIT_ALLOW_PROTOCOL=http:https:git:ssh:file` +
  `GIT_TERMINAL_PROMPT=0` — enforced by git, independent of the user's `git config`.
- `git.ValidateCloneURL` mirrors it host-side: no leading `-`, no control characters, no remote-helper
  `::` transport (`ext::sh -c 'id'`), and a **case-sensitive** scheme check because git's own is
  (`HTTPS://` is refused as transport `HTTPS`).
- **Every** clone of a caller-supplied source goes through both halves — project creation
  (`api.validateGitCloneURL`) and the extension install (`extensions.CheckoutExtension`). Git's own
  refusal of `ext::` is a host default, not a defence (a host with `protocol.ext.allow=always` would run
  the payload as the server user). Use `git.CloneProtocolArgs()` for a clone that needs a bespoke flag —
  never drop the fence.
- Tests: `backend/git/clone_test.go`.

## A `commit_hash` is an object id, not a rev

`types.ValidateCommitHash` accepts 4–64 hex characters and nothing else, and is checked **where the value
is accepted** — `PATCH /projects/{id}/tasks/{id}/messages/{id}` stores `git_commit_hash` only after the
check — because that stored value is what the message-deletion path later hands to `ResetWorktreeTo`'s
`rev-parse --verify <hash>^{tree}` / `read-tree --reset -u <hash>` without re-checking it. Every hash MIKE
writes is `git stash create` output, so the rule costs no real request; without it
`{"git_commit_hash":"HEAD"}` discarded a task's work to restore a branch, and an option-shaped value
reached git as an option. Tests: `backend/server/handler_message_commit_hash_test.go`,
`backend/types/validate_test.go`.

## One slugifier for every name derived from user text

`types.Slugify` / `types.SlugifyBranchName` (`backend/types/slug.go`, `MaxBranchName` /
`BranchSlugMaxLen`) back task branch names, the branch GC and Main-thread recovery, and project IDs. The
matching *parser* `types.TaskBranchSlugPrefix` (with `TaskBranchSuffixLen`, the random tail `exec`
appends) lives beside them — a GC that parses a different shape than the executor writes deletes live
branches. Never add a second copy of either. Non-ASCII letters are dropped, not transliterated.
Tests: `backend/types/slug_test.go`, `backend/server/handler_fs_test.go`,
`backend/api/project_test.go`, `backend/extensions/registry_test.go`.

## Which git calls carry a context

`git.GitWaitDelay` is the `cmd.WaitDelay` every constructor sets. What it buys is specific: after a child is
killed, `cmd.Wait` still waits on its **output pipes**, and a child that dies holding them (or a grandchild
that inherited them) would otherwise park the caller forever — the WaitDelay closes them and lets `Wait`
return. It does **not** stop a child that never gets killed, so a context-less `git` call hangs the request
to the completion of the TCP timeout, not of MIKE's patience.

A cancelled context therefore exists only where a caller has something worth cancelling: `git clone`
(`CloneContext`, on the request context, so closing the tab stops it), pull/push (`handleGitRemote`, which
used to hold the per-project mutex for the full ssh timeout), and the extension install's
`clone`/`checkout`/`rev-parse`. The other ~61 local git call sites behind the three helpers (`gitRun`,
`gitRunInGitDir`, `gitRunOutput`) are deliberately context-less: they run against a local repository, and
threading a context through them buys nothing at the sites that never block on a network.
