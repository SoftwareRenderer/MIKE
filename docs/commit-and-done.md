# Commit and Move to Done

What the click does, what a merge conflict does to it, and which files never get
committed. Read before touching `HandleCommitAndDone` (`backend/api/task.go`) or the
merge/commit half of `backend/exec/merge.go`.

### Commit and Move to Done — unstaged/untracked files
- "Commit and move to done" (`HandleCommitAndDone`, `backend/api/task.go`) commits **only staged** changes. Untracked (never-staged) files must never be copied into the project's main checkout or merged — they stay in the worktree and are discarded when the task is deleted.
- **A conflicted worktree is handed to the model, never refused.** `git add` is git's signal that a conflict was resolved, so `StageIndexedFiles` (`backend/git/worktree.go`) never selects an unmerged path and returns the ones it left alone: selecting them would commit the conflict markers, merge them to the target branch and mark the task Done. `HandleCommitAndDone` flags the task and enters the path a merge conflict already takes — `ArmCommitAndDone`, the conflict prompt, In Progress — so the resolving turn finishes this click. Any other commit failure ends the request through `errs.Operation`, because the merge that follows would delete the worktree holding the uncommitted work. Tests: `TestHandleCommitAndDone_HandsWorktreeConflictToTheResolver`, `…_ReportsACommitThatGitRefused`, `…_CommitsTheParentsOwnWorkAfterASubtaskMerge`.
- The merge-conflict resolution commit `commitConflictResolution` (`backend/exec/merge.go`) stages with `git add -u` (tracked only), **NOT** `git add -A`.
- **A merge conflict interrupts the click; it does not cancel it.** The click asked for commit + merge + Done, so the turn that finishes resolving merges into the target branch **whatever `auto_merge` says** (`auto_merge=false` is why the user clicked it) and marks the task Done.
- **Only the click knows it was the click.** A conflict found at task start means "keep working on the task", so `ArmCommitAndDone` / `takeCommitAndDone` (`backend/exec/merge.go`) records which conflict the click produced — **in memory**, so a merge nobody asked for never comes back. The record is spent by the resolution it was waiting on, dropped when the user takes over (`HandleSend`, the `ask_question` stop), and kept across a resolution that is *still* conflicted. **Nothing is marked Done that did not reach the target branch:** `conflictResolution.finishesTask()` is the only road to Done from a resolution, so a refused or failed merge is Review with `Merge failed: …`.
- When the worktrees dir lives **inside** the project, `CreateWorktree` (`backend/git/worktree.go`, `ensureWorktreesExcluded`) idempotently appends it to the repo's local `.git/info/exclude` so worktree content never shows in project `git status` or gets swept into a project-level `git stash -u`. Local-only, never committed.
- **A merge that landed finishes the click, whatever came back afterwards.** The merge parks the project
  checkout's own uncommitted work before it moves that checkout (`docs/git-operations.md → Stash`), and
  putting that work back can conflict with what the merge brought in. `mergeInto` then returns
  `errMergedWithConflictedRestore`, and `MergedDelivered` is the one predicate every Done decision
  applies — the work reached the target branch, so the task is Done, and what is left is the project's
  working copy. One Warn and one thread message name the file
  and the stash entry that still holds the person's version, because nothing on the card says it.
  Such a path never raises `has_merge_conflict`: the resolver answers a conflict by editing the file and
  running `git add`, which is git's signal that the conflict is resolved, and these are the person's own
  words. A merge that failed *and* conflicted on the restore is not this — `mergeInto` classifies on
  whether the merge landed, never on what the joined error contains.
- **Tests:** `task_test.go` (`TestHandleCommitAndDone_ExcludesUnstagedFiles`,
  `TestHandleCommitAndDone_ArmsClickForConflictResolution`,
  `TestHandleCommitAndDone_DoneWhenOnlyTheProjectOwnsTheConflict`), `backend/exec/merge_restore_test.go`
  (`TestMergedDeliveredReadsTheMergeNotTheRestore`), `merge_conflict_resolution_test.go` (`TestCommitConflictResolution_DoesNotSweepUntrackedFiles`, `…_CommitAndDoneClickMergesWithAutoMergeOff`, `…_NoClickNoMergeWithAutoMergeOff`, `…_StillConflictedKeepsTheClick`), `commit_and_done_test.go`, `backend/git/worktree_test.go` (`TestCreateWorktree_ExcludesWorktreesDirFromProjectStatus`, `…_NoExcludeWhenWorktreesOutsideProject`), e2e `commit-and-done-unstaged.spec.ts`.
### Where the work lands — the base branch

- **A base branch belongs to a root task.** `base_ref` is the branch a person chose for a task that has
  no parent. A task with a parent merges into that parent's branch instead, so it owns no base branch.
- **One helper owns the rule.** `types.ClearSubtaskBaseBranch` (`backend/types/task_defaults.go`) drops
  `base_ref` and the `base_commit` pinned from it whenever a row ends up with a parent. The task API's
  create and update handlers and `handleEditTask` (`backend/exec/tool_board.go`) each call it once the
  parent is settled.
- **`create_task` hands a created root task the base branch of the task that created it**, so a
  plan-mode run set to `test` decomposes onto `test`. Only a root originating task passes one on: a
  subtask's `base_ref` is its parent's branch, never a person's choice.
- **The reported target follows the same rule.** `ResolveMergeTarget` (`backend/exec/merge.go`) answers
  a child with its parent's branch, or with the project's origin branch until that parent has one, and
  never with the child's own `base_ref`.
- **Tests:** `backend/types/task_defaults_test.go` (`TestClearSubtaskBaseBranch`),
  `backend/exec/create_task_test.go` (`TestHandleCreateTask_RootInheritsOriginBaseBranch`,
  `…_SubtaskGetsNoBaseBranch`, `…_RootFromSubtaskOriginGetsNoBaseBranch`,
  `…_SkipsOriginBaseBranchThatIsNotABranchName`), `backend/exec/edit_task_test.go`
  (`TestHandleEditTask_NestingClearsBaseBranch`, `…_TitleOnlyEditKeepsBaseBranch`),
  `backend/exec/subtask_merge_test.go` (`TestResolveMergeTarget_ChildNeverAnswersItsOwnBaseRef`),
  `backend/api/task_test.go` (`TestTaskHandler_POST_DropsBaseBranchSentWithAParent`,
  `TestTaskHandler_PUT_NestingClearsBaseBranch`).

## Checking this

`go test ./backend/exec/` covers the arming rule and what is allowed to reach Done.
`go test ./backend/types/ ./backend/api/` covers who may hold a base branch.
`cd frontend && npx playwright test commit-and-done-unstaged` drives the click itself.
