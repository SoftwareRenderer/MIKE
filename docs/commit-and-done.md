# Commit and Move to Done

What the click does, how merge conflicts are handled, and which files are excluded from commits. Read before editing `HandleCommitAndDone` (`backend/api/task.go`) or the merge/commit logic in `backend/exec/merge.go`.

### Commit and Move to Done — unstaged/untracked files
- **Only staged changes are committed.** "Commit and move to done" (`HandleCommitAndDone`, `backend/api/task.go`) ignores untracked files. These files stay in the worktree and are discarded when the task is deleted.
- **Conflicts are handed to the model.** If a worktree has conflicts, it is not refused. `StageIndexedFiles` (`backend/git/worktree.go`) ignores unmerged paths to prevent committing conflict markers. `HandleCommitAndDone` then arms the task via `ArmCommitAndDone` and prompts the model to resolve the conflicts.
- **Review summaries provide context.** The model uses `request_review` summaries to resolve conflicts. `ReviewContextFor` (`backend/exec/loop_helpers.go`) extracts these summaries for `prompts/merge_conflict.tmpl`. If no reviews exist, the task's title and description are used.
- **Conflict resolution uses `git add -u`.** The commit that resolves a merge conflict (`commitConflictResolution` in `backend/exec/merge.go`) stages tracked files only, not untracked files.
- **Conflicts interrupt, but do not cancel, the click.** If a merge conflict occurs, the process pauses. Once resolved, the system merges into the target branch regardless of the `auto_merge` setting.
- **Click state is tracked in memory.** The server uses `ArmCommitAndDone` (`backend/exec/merge.go`) to remember that a merge was triggered by a user click. A task is marked Done only if it successfully reaches the target branch.
- **Worktree exclusion.** If the worktrees directory is inside the project, `CreateWorktree` (`backend/git/worktree.go`) adds it to `.git/info/exclude` so it does not appear in `git status` or `git stash -u`.
- **Successful merges mark the task Done.** If the merge succeeds, the task is marked Done even if restoring the project's uncommitted work later causes conflicts. In this case, the system issues a warning, but the task remains Done.
- **Interruptions defer the commit.** If a turn is interrupted (e.g., by `request_review`), the commit and merge are deferred until the turn that finishes the work.
- **Assisted-by trailer.** The `Assisted-by` line in commit messages is added only if `AssistedByEnabled` (`backend/types/domain.go`) is true and the project has a configured answer in its settings. Each contributing model gets its own line, named as it is configured — `buildAssistedBy` (`backend/exec/context.go`) keeps a `prefix/model` id whole, because the prefix is what tells two provider models apart.
- **The commit message is written from the thread's review summaries.** `ExtractCommitContext` (`backend/exec/context.go`) hands the generator the first user message plus every `request_review` turn. The working turns between them are not sent. A thread with no review has no summary, so its whole text is sent instead: every user and assistant message, and no tool call.

- **A merge the person's own checkout blocks is answered 409, in words they can act on.** The click's merge
  parks the project's in-progress work, and that bracket can refuse twice. The checkout can already hold
  conflict markers, so nothing can be parked (`git.PushConflictError`), or the parked work can come back
  refused rather than conflicted (`git.RestoreRefusedError`). Both become `errs.Conflict` naming what to
  open: the conflicted files and whose checkout holds them, or the stash entry
  (`StashMessageForAutoMerge`) still holding their work. Any other merge failure is
  `errs.Operation("merge <task branch> into <target>", cause)` — the browser reads MIKE's sentence and the
  log keeps git's. One place writes them: `parkedWorkRefusal` and `mergeFault` in `backend/exec/merge.go`,
  and `httputil.Fail` stays the only place a status is chosen. `mergeFault` never re-writes a merge that
  delivered, because `errs.Operation` unwraps only to its class and would hide the sentinel
  `MergedDelivered` reads.
- **A refused restore leaves the task out of Done.** The merge landed, but the person's copy of a file is
  still only in the stash. The click names the stranded work, and the task waits in Review.

**Tests:** `task_test.go`, `backend/exec/merge_restore_test.go`, `backend/exec/merge_classification_test.go`, `merge_conflict_resolution_test.go`, `commit_and_done_test.go`, `backend/git/worktree_test.go`, `backend/git/push_stash_test.go`, and e2e `commit-and-done-unstaged.spec.ts`.

### Where the work lands — the base branch

- **Root tasks own the base branch.** `base_ref` is the branch chosen for a root task. Child tasks merge into their parent's branch and do not have their own `base_ref`.
- **Subtask base branches are cleared.** `types.ClearSubtaskBaseBranch` (`backend/types/task_defaults.go`) removes the `base_ref` and `base_commit` whenever a task is assigned a parent.
- **Inheritance during creation.** A root task created via `create_task` inherits the base branch of the task that created it (e.g., a plan-mode run on `test` decomposes into `test`).
- **Merge target resolution.** `ResolveMergeTarget` (`backend/exec/merge.go`) ensures child tasks merge into their parent's branch (or the project's origin branch) and never their own `base_ref`.

**Tests:** `backend/types/task_defaults_test.go`, `backend/exec/create_task_test.go`, `backend/exec/edit_task_test.go`, `backend/exec/subtask_merge_test.go`, and `backend/api/task_test.go`.

## Checking this

- `go test ./backend/exec/` verifies the arming rule, the Done conditions, and which turns the commit message is written from.
- `go test ./backend/types/ ./backend/api/` verifies base branch ownership.
- `cd frontend && npx playwright test commit-and-done-unstaged` tests the UI interaction.
