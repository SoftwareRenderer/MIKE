package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"kanban/backend/httputil"
	"net/http"
	"strings"
	"time"

	"kanban/backend/exec"
	"kanban/backend/git"
	"kanban/backend/stream"
	"kanban/backend/types"
	"kanban/pkg/logging"
)

func (s *Server) handleThreads(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	switch r.Method {
	case http.MethodGet:
		threads, err := s.store.GetThreadsByTask(taskID)
		if err != nil {
			Fail(w, err, "get threads", "task not found")
			return
		}
		JSONResponse(w, http.StatusOK, threads)
	case http.MethodDelete:
		s.handleThreadDelete(w, r, projectID, taskID)
	default:
		httputil.MethodNotAllowed(w)
	}
}

// gitErrorDetail caps git's own output for a log line.
func gitErrorDetail(out string) string {
	out = strings.TrimSpace(out)
	if len(out) > 200 {
		return out[:200] + "…"
	}
	return out
}

// releaseThreadSnapshots unpins the worktree state a thread owns: its handoff snapshot plus the checkpoint
// each of its messages captured. Call it BEFORE deleting the thread — the message ids are what name those
// snapshots. It is safe for threads forked from this one, because a fork re-pins what it inherited under
// its own message ids (see CopyMessagesToThread).
func (s *Server) releaseThreadSnapshots(taskID, threadID string) {
	wm := s.executor.WorktreeMgr()
	var ids []string
	if msgs, err := s.store.GetTaskMessages(taskID, threadID); err == nil {
		for i := range msgs {
			if msgs[i].GitCommitHash != "" {
				ids = append(ids, msgs[i].ID)
			}
		}
	} else {
		logging.For(logging.Worktree).With(logging.TaskID, taskID).Warnf("thread=%s could not list its messages: %v", threadID, err)
	}
	s.releaseMessageSnapshots(taskID, ids)
	if err := wm.DeleteThreadSnapshot(taskID, threadID); err != nil && !errors.Is(err, git.ErrWorktreeNotFound) {
		logging.For(logging.Worktree).With(logging.TaskID, taskID).Warnf("thread=%s could not release its handoff snapshot: %v", threadID, err)
	}
}

// releaseMessageSnapshots unpins the snapshots the given messages captured. Call it with ids that were
// just deleted: an unnamed snapshot is a copy of the worktree git can never reclaim.
func (s *Server) releaseMessageSnapshots(taskID string, messageIDs []string) {
	if len(messageIDs) == 0 || s.executor == nil {
		return
	}
	if err := s.executor.WorktreeMgr().UnpinMessageSnapshots(taskID, messageIDs); err != nil && !errors.Is(err, git.ErrWorktreeNotFound) {
		logging.For(logging.Worktree).With(logging.TaskID, taskID).Warnf("could not release %d checkpoint snapshot(s): %v", len(messageIDs), err)
	}
}

func (s *Server) handleThreadDelete(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	var req struct {
		ThreadID string `json:"thread_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ThreadID == "" {
		JSONError(w, http.StatusBadRequest, "thread_id required")
		return
	}
	// Validate task exists
	task, err := s.store.GetTask(projectID, taskID)
	if err != nil {
		Fail(w, err, "get task", "task not found")
		return
	}
	// Can't delete the only thread (Main)
	if task.CurrentThreadID == req.ThreadID {
		threads, _ := s.store.GetThreadsByTask(taskID)
		if len(threads) <= 1 {
			JSONError(w, http.StatusBadRequest, "cannot delete the last thread")
			return
		}
	}

	// Acquire per-task mutex to serialize git operations on this worktree.
	// Delete may checkout a replacement branch and reset the worktree.
	defer s.lockWorktree(taskID)()

	// Re-fetch task after acquiring lock — state may have changed.
	task, err = s.store.GetTask(projectID, taskID)
	if err != nil {
		Fail(w, err, "get task", "task not found")
		return
	}

	// Release the thread's saved worktree state while its rows still exist to name it.
	s.releaseThreadSnapshots(taskID, req.ThreadID)

	// One store call: the messages and the thread row go together, and the deleted row
	// tells us which branch to clean up afterwards.
	deletedThread, err := s.store.DeleteThreadWithMessages(req.ThreadID)
	if err != nil {
		Fail(w, err, "delete thread", "thread not found")
		return
	}
	deletedBranch := ""
	if deletedThread != nil {
		deletedBranch = deletedThread.GitBranch
	}
	// If the deleted thread was the active one, switch to another
	if task.CurrentThreadID == req.ThreadID {
		remaining, _ := s.store.GetThreadsByTask(taskID)
		if len(remaining) > 0 {
			// Check out the replacement's branch BEFORE the old one is deleted — git will not remove a
			// branch that is still checked out — and record where the worktree ended up.
			targetThread, _ := s.store.GetThread(remaining[0].ID)
			branch := s.checkoutThreadBranch(projectID, taskID, task, targetThread)
			_ = s.store.SetActiveThread(remaining[0].ID)
			task.CurrentThreadID = remaining[0].ID
			if branch != "" {
				task.Branch = branch
			}
			task.UpdatedAt = time.Now().UTC()
			_ = s.store.UpdateTask(task)
			// Reset file state to match the new thread's conversation
			lastCommit := s.getLastCommitHashInThread(remaining[0].ID)
			s.resetWorktreeToCommit(taskID, lastCommit)
			s.broadcastTaskDiff(taskID)
			s.deleteThreadBranch(taskID, deletedBranch)
		}
	} else if deletedBranch != "" {
		// Not active but still has a branch — clean it up
		s.deleteThreadBranch(taskID, deletedBranch)
	}
	task, _ = s.store.GetTask(projectID, taskID)
	if task != nil {
		s.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	}
	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleThreadSwitch(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	var req struct {
		ThreadID string `json:"thread_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ThreadID == "" {
		JSONError(w, http.StatusBadRequest, "thread_id required")
		return
	}
	// Validate task exists (pre-lock check, re-fetched after lock below)
	if _, err := s.store.GetTask(projectID, taskID); err != nil {
		Fail(w, err, "get task", "task not found")
		return
	}
	// Validate thread exists and belongs to this task
	thread, err := s.store.GetThread(req.ThreadID)
	if err != nil || thread.TaskID != taskID {
		JSONError(w, http.StatusBadRequest, "thread not found")
		return
	}

	// Acquire per-task mutex to serialize git operations on this worktree.
	// Prevents concurrent stash/checkout/branch/reset operations that would
	// corrupt worktree state if interleaved.
	defer s.lockWorktree(taskID)()

	// Re-fetch task after acquiring lock — state may have changed.
	task, err := s.store.GetTask(projectID, taskID)
	if err != nil {
		Fail(w, err, "get task", "task not found")
		return
	}

	// Switching to the already-active thread (page-load with thread URL): the worktree
	// already holds this thread's content, so skip checkout/stash/restore to avoid
	// wiping uncommitted work. Just refresh the diff.
	if task.CurrentThreadID == req.ThreadID {
		s.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		s.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeThreadSwitch, Data: map[string]string{"thread_id": req.ThreadID, "task_id": taskID}})
		s.broadcastTaskDiff(taskID)
		JSONResponse(w, http.StatusOK, thread)
		return
	}

	// The agent must not be writing while its work is captured. Stopping is real, so a refusal that
	// follows has to say so rather than leave the UI showing a running task.
	wasRunning := task.ExecutionState == types.ExecStateRunning
	if wasRunning {
		_ = s.executor.StopTask(taskID)
		task.ExecutionState = types.ExecStateIdle
		task.Output = ""
	}
	// Save the current thread's worktree state, then move to the target thread's branch and restore its
	// own state there.
	if task.Branch != "" {
		oldThreadID := task.CurrentThreadID
		if oldThreadID != "" && oldThreadID != req.ThreadID {
			oldThread, _ := s.store.GetThread(oldThreadID)
			oldBranch := oldThread.GitBranch
			if oldBranch == "" {
				oldBranch = task.Branch
			}
			_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "checkout", oldBranch)
			// Save the worktree as it is so switching back can restore it — and if it cannot be saved,
			// leave both threads exactly as they are.
			stashHash, err := s.stashWorktreeState(projectID, taskID, oldThreadID, git.StashMessageForSwitch(taskID))
			if err != nil {
				s.refuseUnsavedWork(w, projectID, task, "switched", err, wasRunning)
				return
			}
			if stashHash != "" {
				_ = s.store.UpdateThreadWorktreeStashHash(oldThreadID, stashHash)
			}
		}
		// Check out the thread's branch — resolving it for Main, whose branch is slug-named for a
		// titled task — and clean the worktree. The next step discards the working state either way, so
		// there is nothing to protect by checking out unforced.
		if branch := s.checkoutThreadBranch(projectID, taskID, task, thread); branch != "" {
			task.Branch = branch
		}
		// A thread's saved worktree snapshot is its working state; without one the branch HEAD is all
		// there is (do NOT reset to the base commit — that would discard committed fork work).
		if thread.WorktreeStashHash != "" {
			if err := s.executor.WorktreeMgr().ResetWorktreeTo(taskID, thread.WorktreeStashHash); err != nil {
				logging.For(logging.Thread).With(logging.TaskID, taskID).Warnf("thread=%s could not restore its saved worktree (%.12s): %v", req.ThreadID, thread.WorktreeStashHash, err)
			}
		} else if last := s.getLastCommitHashInThread(req.ThreadID); last != "" && s.executor.WorktreeMgr().IsAncestor(taskID, "HEAD", last) {
			// Uncommitted work not yet on the branch, still sitting on top of HEAD.
			s.resetWorktreeToCommit(taskID, last)
		}
	}
	if err := s.store.SetActiveThread(req.ThreadID); err != nil {
		Fail(w, err, "set active thread", "thread not found")
		return
	}
	task.CurrentThreadID = req.ThreadID
	task.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateTask(task); err != nil {
		Fail(w, err, "update task", "task not found")
		return
	}
	// Broadcast updated task so frontend has new currentThreadID before diff loads
	s.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	// Broadcast thread_switch event to WebSocket clients
	s.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeThreadSwitch, Data: map[string]string{"thread_id": req.ThreadID, "task_id": taskID}})
	// Send fresh diff so the Changes panel reflects the new thread's branch
	s.broadcastTaskDiff(taskID)
	JSONResponse(w, http.StatusOK, thread)
}

func (s *Server) handleThreadFork(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	var req struct {
		FromThreadID  string `json:"from_thread_id"`
		FromMessageID string `json:"from_message_id"`
		NewContent    string `json:"new_content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.FromThreadID == "" || req.FromMessageID == "" {
		JSONError(w, http.StatusBadRequest, "from_thread_id and from_message_id required")
		return
	}
	// If new_content is provided, stop the current executor so the next run
	// loads the forked context instead of the stale original thread.
	if req.NewContent != "" {
		_ = s.executor.StopTask(taskID)
	}
	// Validate task exists (pre-lock check, re-fetched after lock below)
	if _, err := s.store.GetTask(projectID, taskID); err != nil {
		Fail(w, err, "get task", "task not found")
		return
	}
	// Validate source thread exists
	sourceThread, err := s.store.GetThread(req.FromThreadID)
	if err != nil || sourceThread.TaskID != taskID {
		JSONError(w, http.StatusBadRequest, "source thread not found")
		return
	}

	// Acquire per-task mutex to serialize git operations on this worktree.
	// Prevents concurrent stash/checkout/branch/reset operations that would
	// corrupt worktree state if interleaved with a switch or another fork.
	defer s.lockWorktree(taskID)()

	// Re-fetch task after acquiring lock — state may have changed.
	task, err := s.store.GetTask(projectID, taskID)
	if err != nil {
		Fail(w, err, "get task", "task not found")
		return
	}

	// Generate new thread ID and branch name
	newThreadID := types.GenerateThreadID()
	// Ensure uniqueness
	for {
		if _, err := s.store.GetThread(newThreadID); err != nil {
			break // unique
		}
		newThreadID = types.GenerateThreadID()
	}
	newBranchName := fmt.Sprintf("thread/%s", newThreadID)
	// Find the file state at the checkpoint: last commit BEFORE the checkpoint message.
	// This is the state before its tool calls ran, so the fork starts from a clean slate.
	checkpointCommit, _ := s.store.GetLastCommitHashBeforeMessage(req.FromMessageID, req.FromThreadID)
	// Determine the branch point for the fork
	branchPoint := checkpointCommit
	if branchPoint == "" {
		branchPoint = task.BaseRef
	}
	// Save current worktree state before forking. Uses labeled stash push + list
	// search to capture worktree as a commit hash saved to the CURRENT thread.
	// Must save to CurrentThreadID (not FromThreadID) because the worktree belongs
	// to the currently active thread. Forking from an inactive thread should not
	// overwrite that inactive thread's already-saved stash hash.
	if task.Branch != "" {
		_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "checkout", task.Branch)
		// Save the worktree as it is: this thread's in-flight work has to survive the rewind below, and
		// switching back has to be able to put it back. If it cannot be saved, the fork does not happen.
		stashHash, err := s.stashWorktreeState(projectID, taskID, task.CurrentThreadID, git.StashMessageForFork(taskID))
		if err != nil {
			s.refuseUnsavedWork(w, projectID, task, "forked", err, false)
			return
		}
		if stashHash != "" {
			_ = s.store.UpdateThreadWorktreeStashHash(task.CurrentThreadID, stashHash)
		}
		_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "branch", newBranchName, branchPoint)
		_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "checkout", newBranchName)
		// Update task.Branch to track the currently checked-out branch
		task.Branch = newBranchName
		// Reset worktree to checkpoint state.
		if branchPoint != "" {
			if err := s.executor.WorktreeMgr().ResetWorktreeTo(taskID, branchPoint); err != nil {
				logging.For(logging.Thread).With(logging.TaskID, taskID).Warnf("could not rewind the worktree to %.12s: %v", branchPoint, err)
			}
		} else {
			// No checkpoint — reset to base ref (clean initial state).
			baseRef := task.BaseRef
			if baseRef == "" {
				baseRef = "master"
			}
			_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "reset", "--hard", baseRef)
			_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "clean", "-fd")
		}
	}
	// Look up the checkpoint message's created_at for frontend filtering
	var checkpointMsgTime *time.Time
	if ct, err := s.store.GetMessageCreatedAt(req.FromMessageID); err == nil && ct != nil {
		checkpointMsgTime = ct
	}
	// Create new thread
	now := time.Now().UTC()
	newThread := &types.TaskThread{
		ID:                    newThreadID,
		TaskID:                taskID,
		ParentThreadID:        req.FromThreadID,
		Name:                  "",
		IsActive:              true,
		GitBranch:             newBranchName,
		CheckpointMessageID:   req.FromMessageID,
		CheckpointMessageTime: checkpointMsgTime,
		CheckpointCommitHash:  branchPoint,
		CreatedAt:             now,
	}
	// Generate thread name from new_content if provided, otherwise use "Fork #N"
	threads, _ := s.store.GetThreadsByTask(taskID)
	if req.NewContent != "" {
		newThread.Name = s.generateThreadName(r.Context(), task, req.NewContent)
	} else {
		newThread.Name = fmt.Sprintf("Fork #%d", len(threads)+1)
	}
	if err := s.store.CreateThread(newThread); err != nil {
		Fail(w, err, "create thread", "task not found")
		return
	}
	// Copy the parent conversation into the fork: whole entries up to the fork point, payloads and all.
	inherited, err := s.store.CopyMessagesToThread(req.FromThreadID, newThreadID, req.FromMessageID)
	if err != nil {
		Fail(w, err, "copy thread messages", "thread not found")
		return
	}
	// Re-pin every snapshot the fork inherited under the fork's own rows. The rows name these hashes,
	// but until now the only ref keeping them reachable belonged to the parent thread — delete the
	// parent and the fork's checkpoints become prunable, which is a restore failing much later against
	// a conversation that still claims the state exists. Best-effort: the parent's ref still pins the
	// same objects, so a failed re-pin costs durability against a future deletion, not correctness now.
	for _, cp := range inherited {
		if pinErr := s.executor.WorktreeMgr().PinTaskSnapshot(taskID, cp.MessageID, cp.CommitHash); pinErr != nil {
			logging.For(logging.Thread).With(logging.TaskID, taskID).Warnf("could not pin inherited snapshot %.12s for message %s: %v", cp.CommitHash, cp.MessageID, pinErr)
		}
	}
	// If new_content is provided, update the checkpoint message in the fork
	// to the edited content. Find the last user message in the new thread
	// (the copied checkpoint) and update its content.
	if req.NewContent != "" {
		if copiedMsgID, err := s.store.GetLastUserMessageInThread(newThreadID); err == nil && copiedMsgID != "" {
			_ = s.store.UpdateTaskMessage(copiedMsgID, req.NewContent)
		}
		// Stop the current executor so the next run loads the forked context.
		_ = s.executor.StopTask(taskID)
	}
	if err := s.store.SetActiveThread(newThreadID); err != nil {
		Fail(w, err, "set active thread", "thread not found")
		return
	}
	task.CurrentThreadID = newThreadID
	task.UpdatedAt = now
	if err := s.store.UpdateTask(task); err != nil {
		Fail(w, err, "update task", "task not found")
		return
	}
	// Broadcast updated task so frontend has new currentThreadID before diff loads
	s.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	// Broadcast thread_switch event
	s.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeThreadSwitch, Data: map[string]string{"thread_id": newThreadID, "task_id": taskID}})
	// Send fresh diff so the Changes panel reflects the forked branch
	s.broadcastTaskDiff(taskID)
	s.generateCheckpointLabelForFork(r.Context(), taskID, projectID, req.FromMessageID)
	JSONResponse(w, http.StatusCreated, newThread)
}

// generateThreadName generates a short thread label via LLM, or truncates the text.
func (s *Server) generateThreadName(ctx context.Context, task *types.Task, content string) string {
	trimmed := strings.TrimSpace(content)
	if len(trimmed) == 0 {
		return "Fork"
	}

	// Try LLM generation if config is available
	if task.LlamaServerURL != "" && task.ModelName != "" {
		messages := []types.ChatMessage{
			{Role: "system", Content: "You are a thread name generator. Given a user message, generate a very short, descriptive thread label (max 30 characters, no quotes). Return only the label, nothing else."},
			{Role: "user", Content: trimmed},
		}

		_, name, err := s.executor.ChatCompletion(ctx, task.LlamaServerURL, task.ModelName, messages, nil)
		if err == nil && strings.TrimSpace(name) != "" {
			name = strings.TrimSpace(name)
			if len(name) > 35 {
				name = name[:35]
			}
			logging.For(logging.Thread).With(logging.TaskID, task.ID).Infof("generated: %q", name)
			return name
		}
	}

	// Fallback: truncate the text
	if len(trimmed) > 35 {
		return trimmed[:35]
	}
	return trimmed
}

// checkoutThreadBranch checks out the branch a thread's work lives on and returns the branch it landed on
// ("" if it could not). A thread with no git_branch is Main, whose branch still exists (slug-named for a
// titled task), so it is resolved rather than skipped: skipping it left the worktree on the branch the
// caller was about to delete, and git will not delete a checked-out branch.
func (s *Server) checkoutThreadBranch(projectID, taskID string, task *types.Task, targetThread *types.TaskThread) string {
	if targetThread == nil {
		return ""
	}
	branch := targetThread.GitBranch
	if branch == "" {
		if task == nil {
			return ""
		}
		branch = s.executor.WorktreeMgr().ResolveMainBranch(projectID, taskID, task.Title, fmt.Sprintf("task/%s", taskID))
	}
	// Forced: the worktree holds the state of the thread being deleted, and a restored checkpoint counts
	// as uncommitted changes, which is what makes a plain checkout refuse. Callers restore the surviving
	// thread's own checkpoint straight after this.
	out, err := s.executor.WorktreeMgr().GitRunInWorktreeOutput(taskID, "checkout", "-f", branch)
	if err != nil {
		// git's own words, because "exit status 1" tells you nothing about a refused checkout.
		logging.For(logging.Thread).With(logging.TaskID, taskID).Warnf("could not check out %s: %v: %s", branch, err, gitErrorDetail(out))
		return ""
	}
	_, _ = s.executor.WorktreeMgr().GitRunInWorktreeOutput(taskID, "clean", "-fd")
	return branch
}

// deleteThreadBranch deletes a thread's git branch.
func (s *Server) deleteThreadBranch(taskID string, branchName string) {
	if branchName == "" {
		return
	}
	// A failure here is a branch that outlived its thread, so it must not be dropped silently.
	out, err := s.executor.WorktreeMgr().GitRunInWorktreeOutput(taskID, "branch", "-D", branchName)
	if err != nil {
		logging.For(logging.Thread).With(logging.TaskID, taskID).Warnf("could not delete branch %s: %v: %s", branchName, err, gitErrorDetail(out))
	}
}

// Parks the worktree's changes for threadID. A clean worktree returns "" with no error: nothing to save.
func (s *Server) stashWorktreeState(projectID, taskID, threadID, label string) (string, error) {
	wm := s.executor.WorktreeMgr()
	// The status is read here rather than through HasWorktreeChanges, which reports a worktree it cannot
	// read as clean — and an unreadable worktree is exactly what a caller must not overwrite.
	status, err := wm.GitStatus(taskID)
	if err != nil {
		return "", fmt.Errorf("read the worktree state: %w", err)
	}
	if strings.TrimSpace(status) == "" {
		return "", nil
	}
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return "", err
	}
	// A thread switch stashes the worktree, so it queues behind every other stash in the project.
	defer wm.LockProjectStashWatched(projectID, stream.LockWaitNoticeEvery,
		s.appStreamMgr.LockWaitNotifier(projectID, taskID, "stash"))()

	stashHash, err := wm.PushStash(worktreePath, label)
	if errors.Is(err, git.ErrNothingToStash) {
		// Whatever made the worktree dirty has gone in the meantime; git found nothing to take.
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if pinErr := wm.PinSnapshot(worktreePath, git.SnapshotRef(taskID, git.ThreadSnapshotOwner(threadID)), stashHash); pinErr != nil {
		// Leave the stash entry where it is: dropped and unpinned, the snapshot is git's to prune, and an
		// entry the user can see outliving its thread is the lesser evil.
		logging.For(logging.Worktree).With(logging.TaskID, taskID).Warnf("thread=%s saved %.12s but could not pin it, so it stays in the stash list: %v", threadID, stashHash, pinErr)
		return stashHash, nil
	}
	_ = wm.DropStashByHash(worktreePath, stashHash)
	return stashHash, nil
}

// refuseUnsavedWork answers a switch or fork that could not capture the worktree: no thread was changed and
// nothing was discarded, so the reply names git's reason and says the operation did not happen — the user's
// next move is to fix the complaint and click again, not to work out what a half-completed operation broke.
//
// One thing really did change: a task stopped so its work could be captured stays stopped. That is recorded
// and broadcast here, because a card still showing "running" after a refusal is a lie about the board.
func (s *Server) refuseUnsavedWork(w http.ResponseWriter, projectID string, task *types.Task, done string, cause error, wasRunning bool) {
	logging.For(logging.Thread).With(logging.TaskID, task.ID).Warnf("refused to %s: %v", done, cause)
	message := fmt.Sprintf("could not save this thread's work — nothing was %s", done)
	if detail := gitErrorDetail(cause.Error()); detail != "" {
		message += ": " + detail
	}
	if wasRunning {
		message += "; the task was stopped to save its work and did not restart"
		task.ExecutionState = types.ExecStateIdle
		task.UpdatedAt = time.Now().UTC()
		if err := s.store.UpdateTask(task); err != nil {
			logging.For(logging.Thread).With(logging.TaskID, task.ID).Warnf("refused to %s and could not record the stop: %v", done, err)
		}
		s.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	}
	JSONError(w, http.StatusConflict, message)
}

// resetWorktreeToCommit resets the worktree to a commit or snapshot.
func (s *Server) resetWorktreeToCommit(taskID, commitHash string) {
	if commitHash == "" {
		return
	}
	if err := s.executor.WorktreeMgr().ResetWorktreeTo(taskID, commitHash); err != nil {
		logging.For(logging.Worktree).With(logging.TaskID, taskID).Warnf("could not restore %.12s: %v", commitHash, err)
	}
}

// getLastCommitHashInThread finds the last commit hash in a thread.
func (s *Server) getLastCommitHashInThread(threadID string) string {
	messages, err := s.store.GetTaskMessages("", threadID)
	if err != nil || len(messages) == 0 {
		return ""
	}
	// Walk backward to find the last message with a commit hash
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].GitCommitHash != "" {
			return messages[i].GitCommitHash
		}
	}
	return ""
}

// broadcastTaskDiff sends the current git diff manifest via WebSocket.
// Sends manifest format (files map with per-file metadata) instead of legacy
// TaskDiff (diff string + untracked array) so the frontend gets staged status
// and per-file hashes in the WebSocket update.
func (s *Server) broadcastTaskDiff(taskID string) {
	task, _ := s.store.GetTaskByID(taskID)
	if task == nil {
		return
	}
	thread, _ := s.store.GetThread(task.CurrentThreadID)
	forkBranch := ""
	if thread != nil {
		forkBranch = thread.GitBranch
	}
	baseRef, _ := s.executor.WorktreeMgr().ResolveDiffBaseline(taskID, task.BaseRef, task.Branch, forkBranch)
	manifest, err := s.gitHandler.GetDiffManifest(taskID, baseRef)
	if err != nil {
		return
	}
	s.appStreamMgr.SendTaskEvent(task.ProjectID, taskID, types.StreamEvent{Type: types.WSTypeDiffChanged, Data: manifest})
}

// lockWorktree locks the per-task worktree and returns the function that releases it.
//
// Release function rather than mutex: these are injected into managers.GitHandler, and a mutex handed
// across a package boundary is a value that can be stored, copied, and unlocked by whoever holds a
// copy — see git.WorktreeManager.LockProjectStash for the same argument.
func (s *Server) lockWorktree(taskID string) func() {
	return s.gitTask.Lock(taskID)
}

// lockProject locks the per-project git operations (stash, checkout, pull, push) that touch shared
// state like .git/refs/stash, and returns the function that releases it.
func (s *Server) lockProject(projectID string) func() {
	return s.gitProject.Lock(projectID)
}

// generateCheckpointLabelForFork generates a short label for the checkpoint message
// at the fork point and sends it to the frontend via WebSocket.
func (s *Server) generateCheckpointLabelForFork(ctx context.Context, taskID, projectID, fromMessageID string) {
	task, err := s.store.GetTaskByID(taskID)
	if err != nil {
		return
	}
	// Get diff stat at the fork point
	diffStat, _ := s.executor.WorktreeMgr().GitRunInWorktreeOutput(taskID, "diff", "--stat", "HEAD")
	if diffStat == "" {
		diffStat = "no diff available"
	}
	// Get conversation up to the checkpoint message
	taskMsgs, _ := s.store.GetTaskMessages(taskID, task.CurrentThreadID)
	conversation := exec.BuildConversationFromTaskMessages(taskMsgs)
	turnText := exec.FormatConversationTurns(exec.ExtractLastTurns(conversation, 3))
	label := s.executor.GenerateCheckpointLabel(ctx, taskID, diffStat, turnText)
	if label != "" && label != "file changes" {
		s.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeCheckpointComplete, Data: map[string]string{
			"message_id": fromMessageID,
			"label":      label,
		}})
	}
}
