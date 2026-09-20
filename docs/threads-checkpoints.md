# Threads, Checkpoints & Worktree Snapshots

Read this before touching `backend/git/snapshots.go`, `backend/git/worktree.go` (restore),
`createCheckpoint` in `backend/exec/context.go`, `CopyMessagesToThread` in `backend/store/threads.go`, or
the fork/switch handlers in `backend/server/handler_thread.go`. The hard rules are the bolded claims
below — each one is a bug that happened, and the rest of the file is why they are shaped that way.

## The two layers

A task's files live in one worktree, and at any instant they are the product of two independent stores:

| Layer | Holds | Lives in |
|---|---|---|
| **Branch** | everything the agent committed | `task/<id>`, `thread/<id>` branches — the user's namespace |
| **Snapshot** | everything it did *not* commit (staged, unstaged, untracked) | one pinned commit per owning message, under `refs/mike/snap/<taskID>/<ownerID>` |

A **checkpoint** is a snapshot plus the message that caused it: `task_messages.git_commit_hash` names the
snapshot, and the ref of the same name keeps the object alive. The owning id is the assistant message id
(`createCheckpoint(taskID, assistantMsgID, hash)`) or `thread-<threadID>` for a thread handoff, where the
thread has no message to hang it on. Both are opaque owners — the ref layout does not care which.

**The invariant everything else rests on: the worktree goes back to a checkpoint by *tree*, never by
patch and never by moving `HEAD`.** The restore is `checkout -f HEAD` + `clean -fd` + `read-tree --reset -u
<snapshot>` + (if the snapshot is a 3-parent stash commit) `checkout <p3> -- .` to overlay the untracked
files git kept off that tree. Reading a tree cannot fail halfway the way `git apply` can, and the only
`git reset` in it is the no-op index refresh. Nothing rewrites a branch ref behind git's back — the only
`update-ref` calls in the tree are the snapshot refs in `git/snapshots.go` — so a branch moves only by
checkout or merge (`CreateWorktree`'s `checkout -B`, the merge path's `checkout -f <target>`). The `clean -fd` step reaches untracked files only, so a task's
`.mike-uploads/` — excluded through the repository's local `info/exclude`, see `docs/git-operations.md` —
survives a restore with its attachments in it; `TestExcludedUploadsSurviveACleanAndARestore` holds that,
including the control that the same restore does discard an ordinary untracked file.

**Deleting a thread moves the worktree off its branch before deleting that branch, and the checkout is
forced.** git refuses to delete a checked-out branch, and a checkpoint restored into the worktree counts as
uncommitted changes — so a plain `checkout` refuses, the refusal used to go unlogged, and the branch outlived
its thread. A thread with no `git_branch` is Main, whose branch still exists (slug-named for a titled task),
so it is *resolved* via `ResolveMainBranch`, not skipped; whatever the checkout lands on is written back to
`task.Branch`, because every later operation takes its branch from there.

**A thread's saved state is the branch plus the snapshot on top of it, and both must be captured before
the worktree moves on.** Switch and fork capture in that order (`stashWorktreeState`), because
`DropStashByHash` on the previous hash runs right after; the pin-before-drop ordering is load-bearing — a
snapshot must never be unpinned while a thread still names it.

**Capture is all-or-nothing, and a capture that fails stops the operation.** `stashWorktreeState` returns
an error for a state it could NOT save (index locked by another git process, unresolved conflict, `.git`
unwritable or full, no commit yet, worktree gone), and switch/fork answer 409 with git's own reason —
nothing checked out, nothing cleaned, nothing rewound. Proceeding anyway would `clean -fd` away the one
copy of work that was never saved. A clean worktree is not a failure: no error, nothing to keep. Two
details carry the same rule:

- `PushStash` identifies the entry it created by diffing the stash list across the push, never by its
  position. The stack is shared by every worktree of the project and holds the user's own entries, so
  taking the top one would save a stranger's state as this thread's work and then `DropStashByHash` it
  out of the user's list. `git stash push` exits 0 saying "No local changes to save" when the worktree
  changed under us; that is `ErrNothingToStash` (nothing to keep), not somebody else's hash.
- If the pin fails, the stash entry is deliberately left where it is. Dropped and unpinned, the snapshot
  is git's to prune; a visible entry that outlives its thread is the lesser evil.

`stashWorktreeState` reads `GitStatus` itself rather than `HasWorktreeChanges`, which reports a worktree it
cannot read as clean — and an unreadable worktree is exactly what must not be overwritten. A switch also
stops the task before capturing, and a refusal records and broadcasts that stop: a card still showing
"running" after nothing happened is a lie about the board.

**Everything that deletes a row releases the snapshot it named.** `DeleteTaskMessagesAfter` returns the ids
of the deleted rows that held a checkpoint — read out under the same predicate that deletes, because which
rows go is a `created_at`/role rule with a summarization carve-out and ties inside one turn broken by
`rowid`, so a caller must not re-derive it. Thread deletion calls `releaseThreadSnapshots` before the cascade
(which is also what frees a fork's *inherited* pins, since it re-pinned them under its own copied message
ids); task deletion sweeps the whole `refs/mike/snap/<taskID>/` prefix.

**A fork inherits whole displayed entries and re-pins the checkpoints it inherits.** Copying rows is not
copying history: the boundary is the last row of the checkpoint's *entry* (an assistant row plus the
`tool_call` rows that merge into it, which in legacy rows may have no owner at all — see
`entryCutoffLocked`), the payload columns travel with the row, and `assistant_msg_id` is remapped to the
copied assistant's new id. The copied `git_commit_hash` is not decoration: a fork of a fork resolves its
branch point by walking its own thread's messages for a hash, so the copy that stripped hashes made every
second-generation fork rewind to the task's starting commit — files rewound behind their own conversation.
Re-pinning matters for the same reason: a thread whose rows name a snapshot only the *parent's* rows named
becomes a dangling reference the day the parent is tidied.

## "New Thread" is a fork plus a regenerate

Editing a non-last user message offers **New Thread**: the fork copies the conversation up to that
message, replaces the copied user row with the edited text, stops the executor, and then the client *sends
the same text* to start the new thread. That send would look like a double submit to
`HandleSend`'s dedup — the thread's last row is already that identical user message — and a deduped send
starts nothing, which is how a new thread sat idle with its question never answered.

So the follow-up send carries `regenerate: true`, and `HandleSend` separates two things it used to conflate:
`lastRowIsThisMessage` (the row exists, do not store it again) and `isDuplicate` (this submit adds nothing,
do not start the run). A regenerate is never the latter. A plain repeated submit still is —
`TestHandleSend_DoubleSubmit_IsDeduped_AndLeavesReview` holds that, and the client swallows send errors, so
a wrong answer here is a silent dead end rather than an error message.

## Legacy rows

`git_commit_hash` values written before snapshots were refs are `git stash create` commits. Those are
commits whose tree is the working state, so the tree restore reads them exactly as it reads a pinned
snapshot — no migration. They stay alive through `refs/stash` as before, and MIKE's legacy sweep
(`DropStashEntriesForTask`, label `kanban:checkpoint:<taskID>:<idx>`) still runs at task deletion.
`git stash store` is gone from the write path, and a capture pushes then drops its entry immediately, so
nothing of MIKE's stays in `refs/stash` — that list is the user's (`git stash list` is *their* stash). The
one exception is a failed pin, which leaves the entry on purpose: it is then the only durable copy.

## Known gaps, by severity

1. **Every thread shares one worktree.** A switch is a rewind of live files under whoever is looking.
2. **Thread identity is decided in more than one place.** `thread.GitBranch` is the authority when set,
   otherwise the task's branch, otherwise `ResolveMainBranch` recovers a slug branch `task/<slug>-<6>` that
   Main lost when a fork took `task/<taskID>` away. That chain is rewritten at each site that needs it —
   `git.ResolveTaskBaseline`, `git.ResolveDiffBaseline`, `(*Server).checkoutThreadBranch` and the inline
   `thread.GitBranch` tests in `api/task.go` and `handler_message.go` — each with different fallbacks.
3. **Checkpoint identity is positional.** "The checkpoint at index N" means "the Nth message with a hash"
   in message order; the durable `threads.checkpoint_commit_hash` is written at fork time and ignored by
   the readers that matter.
4. **Two writers, one working tree.** An executor running in thread A while the user opens thread B
   switches the files underneath it mid-turn.

## Deferred work, the right shape

Deferred, not rejected: what it costs is disk (an agent's `node_modules` per thread) and the migration
below, and no day-count is recorded here because a number copied forward is one nobody re-measured. The
shape is here because building it the other way means paying twice:

- **Per-thread worktrees (gaps 1, 4 and most of 3).** Worktrees by **branch**, snapshots by **thread**,
  since several threads share one fork branch. A thread without a directory materializes one with
  `CreateWorktree(branch)` then `ResetWorktreeTo(snapshot)` — the two calls a switch already makes, aimed at
  a new directory. Everything downstream resolves paths through `WorktreePath(taskID)`, which has to become
  `WorktreePath(taskID, threadID)`: sandbox mounts, the diff baseline, orphan-worktree GC, the terminal cwd.
- **Identity resolution in one place (gaps 2 and 3).** One `ThreadContextResolver` returning
  `{branch, base, diffBaseline, snapshotHash}` per `(task, thread)`, built once from
  `task.BaseCommit`/`BaseRef`/`Branch` plus `thread.GitBranch`/`CheckpointCommitHash`, is
  the five chains above collapsed into one. A checkpoint is then addressed by its owning assistant
  message's id — `checkpoint_seq` on `task_messages` beside the hash — instead of by counting positions.

## Tests

- `backend/git/snapshots_test.go` — ref naming, pin/refuse, missing-ref detection, sweep scope, and a
  `gc --prune=now` control proving an unpinned snapshot dies and a pinned one does not.
- `backend/git/reset_snapshot_test.go` — binary snapshot restores byte-exact with `HEAD` unmoved; legacy
  stash-commit rows restore from their tree; a missing/empty snapshot is refused and changes nothing.
- `backend/git/worktree_test.go` — branch-name validation incl. the MIKE ref namespace.
- `backend/git/push_stash_test.go` — a capture returns the entry it created (not the user's entry sitting
  above it) and drops only its own; nothing to stash is `ErrNothingToStash`, never a hash; git's reason
  survives into the error.
- `backend/exec/checkpoint_snapshot_test.go` — a turn's checkpoint pins its snapshot; `git stash list`
  stays empty.
- `backend/store/threads_test.go` — entry boundary and payload fidelity across a fork copy.
- `backend/exec/fork_context_test.go` — parent and fork rebuild to the same LLM conversation.
- `backend/server/fork_snapshot_test.go` — fork of a fork keeps the checkpoint it inherited; deleting a
  turn inside a fork rolls back to the surviving checkpoint.
- `backend/server/snapshot_lifecycle_test.go` — task deletion unpins its snapshots, nobody else's, and no
  user ref.
- `backend/store/messages_test.go` — `TestDeleteTaskMessagesAfter_ReportsDeletedCheckpoints`: an earlier
  checkpoint and the surviving summarization row are not reported, the deleted turn's tool_call rows add
  nothing.
- `backend/server/fork_snapshot_test.go` — deleting a turn inside a fork releases its pin and keeps the
  surviving one; `delete_single` releases the dismissed row's pin; deleting the **active** thread removes its
  branch, forces the worktree onto the replacement's branch and updates `task.Branch`.
- `backend/server/fork_snapshot_test.go` also asserts that deleting a fork releases the pins its own rows
  held and leaves the parent's and Main's intact.
- `backend/api/checkpoint_diff_test.go` — the diff at a checkpoint lists the files it captured,
  including the binary one.
- `backend/server/thread_capture_test.go` — with git unable to write, a switch and a fork are refused and
  the work (tracked edit *and* new file) is still there, no thread changed, no stash entry appeared, no
  snapshot pinned — and once git can write again the same click works and the work comes back; a refusal
  reports and records the task stop.
- `backend/exec/duplicate_send_test.go` — a plain repeated submit is still deduped and starts nothing; a
  regenerate of the thread's last user message starts it and does not store the message twice.
- e2e: `thread-switch.spec.ts`, `thread-new-thread.spec.ts` (the fork becomes active, holds the edited
  question once, and gets an answer).
