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
- **Assisted-by trailer.** The `Assisted-by` line in commit messages is added only if `AssistedByEnabled` (`backend/types/domain.go`) is true and the project has a configured answer in its settings.

**Tests:** `task_test.go`, `backend/exec/merge_restore_test.go`, `merge_conflict_resolution_test.go`, `commit_and_done_test.go`, `backend/git/worktree_test.go`, and e2e `commit-and-done-unstaged.spec.ts`.

### Where the work lands — the base branch

- **Root tasks own the base branch.** `base_ref` is the branch chosen for a root task. Child tasks merge into their parent's branch and do not have their own `base_ref`.
- **Subtask base branches are cleared.** `types.ClearSubtaskBaseBranch` (`backend/types/task_defaults.go`) removes the `base_ref` and `base_commit` whenever a task is assigned a parent.
- **Inheritance during creation.** A root task created via `create_task` inherits the base branch of the task that created it (e.g., a plan-mode run on `test` decomposes into `test`).
- **Merge target resolution.** `ResolveMergeTarget` (`backend/exec/merge.go`) ensures child tasks merge into their parent's branch (or the project's origin branch) and never their own `base_ref`.

**Tests:** `backend/types/task_defaults_test.go`, `backend/exec/create_task_test.go`, `backend/exec/edit_task_test.go`, `backend/exec/subtask_merge_test.go`, and `backend/api/task_test.go`.

## Checking this

- `go test ./backend/exec/` verifies the arming rule and Done conditions.
- `go test ./backend/types/ ./backend/api/` verifies base branch ownership.
- `cd frontend && npx playwright test commit-and-done-unstaged` tests the UI interaction.
