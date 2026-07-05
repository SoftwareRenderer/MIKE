package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"kanban/backend/types"
)

func (s *Server) handleThreads(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	switch r.Method {
	case http.MethodGet:
		threads, err := s.store.GetThreadsByTask(taskID)
		if err != nil {
			JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		JSONResponse(w, http.StatusOK, threads)
	case http.MethodDelete:
		s.handleThreadDelete(w, r, projectID, taskID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
		JSONError(w, http.StatusNotFound, "task not found")
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
	// Delete messages for this thread
	_, err = s.store.Exec(`DELETE FROM task_messages WHERE thread_id = ?`, req.ThreadID)
	if err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Get the thread's git branch before deleting (needed for branch cleanup)
	deletedThread, _ := s.store.GetThread(req.ThreadID)
	deletedBranch := ""
	if deletedThread != nil {
		deletedBranch = deletedThread.GitBranch
	}
	// Delete the thread
	_, err = s.store.Exec(`DELETE FROM task_threads WHERE id = ?`, req.ThreadID)
	if err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// If the deleted thread was the active one, switch to another
	if task.CurrentThreadID == req.ThreadID {
		remaining, _ := s.store.GetThreadsByTask(taskID)
		if len(remaining) > 0 {
			_ = s.store.SetActiveThread(remaining[0].ID)
			task.CurrentThreadID = remaining[0].ID
			task.UpdatedAt = time.Now().UTC()
			_ = s.store.UpdateTask(task)
			// Checkout the new active thread's branch and delete orphaned branch
			targetThread, _ := s.store.GetThread(remaining[0].ID)
			s.checkoutThreadBranch(taskID, targetThread)
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
		s.appStreamMgr.BroadcastTaskUpdated(projectID, *task)
	}
	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleThreadSwitch(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
		JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	// Validate thread exists and belongs to this task
	thread, err := s.store.GetThread(req.ThreadID)
	if err != nil || thread.TaskID != taskID {
		JSONError(w, http.StatusBadRequest, "thread not found")
		return
	}
	// Stop executor if running
	if task.ExecutionState == "running" {
		_ = s.executor.StopTask(taskID)
	}
	// Stash current worktree state before switching threads.
	// Uses stash (not commit) to avoid polluting the git log.
	if task.Branch != "" {
		oldThreadID := task.CurrentThreadID
		if oldThreadID != "" && oldThreadID != req.ThreadID {
			oldThread, _ := s.store.GetThread(oldThreadID)
			oldBranch := oldThread.GitBranch
			if oldBranch == "" {
				oldBranch = task.Branch
			}
			_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "checkout", oldBranch)
			_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "stash", "push", "-u", "-m", fmt.Sprintf("switch-%s", taskID))
		}
		// Checkout target thread's branch (or task's original branch if none)
		targetBranch := thread.GitBranch
		if targetBranch == "" {
			targetBranch = fmt.Sprintf("task/%s", taskID)
		}
		_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "checkout", targetBranch)
		task.Branch = targetBranch
		// Reset worktree to the target thread's last checkpoint (stash entry).
		// Checkpoints are stored as git stash entries, not branch commits.
		// The stash label includes the branch name: "On <branch>: <description>"
		stashList, _ := s.executor.WorktreeMgr().GitRunInWorktreeOutput(taskID, "stash", "list")
		targetPrefix := "On " + targetBranch + ":"
		foundCheckpoint := false
		for line := range strings.SplitSeq(stashList, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, targetPrefix) {
				// Found the latest stash for this branch — extract the stash ref
				// Format: "stash@{0}: On thread/jade-honor: Create count_to_3.txt"
				stashRef := strings.Split(line, ":")[0] // "stash@{0}"
				s.executor.WorktreeMgr().ResetWorktreeTo(taskID, strings.TrimSpace(stashRef))
				foundCheckpoint = true
				break
			}
		}
		if !foundCheckpoint {
			// No checkpoint for this thread — reset to base ref (clean initial state).
			// Do NOT use HEAD — branch history may be corrupted by old stash operations.
			baseRef := task.BaseRef
			if baseRef == "" {
				baseRef = "master"
			}
			_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "reset", "--hard", baseRef)
			_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "clean", "-fd")
		}
	}
	// Set new active thread
	if err := s.store.SetActiveThread(req.ThreadID); err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Update task's current_thread_id
	task.CurrentThreadID = req.ThreadID
	task.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateTask(task); err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Broadcast updated task so frontend has new currentThreadID before diff loads
	s.appStreamMgr.BroadcastTaskUpdated(projectID, *task)
	// Broadcast thread_switch event to WebSocket clients
	s.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeThreadSwitch, Data: map[string]string{"thread_id": req.ThreadID, "task_id": taskID}})
	// Send fresh diff so the Changes panel reflects the new thread's branch
	s.broadcastTaskDiff(taskID)
	JSONResponse(w, http.StatusOK, thread)
}

func (s *Server) handleThreadFork(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	// Validate task exists
	task, err := s.store.GetTask(projectID, taskID)
	if err != nil {
		JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	// Validate source thread exists
	sourceThread, err := s.store.GetThread(req.FromThreadID)
	if err != nil || sourceThread.TaskID != taskID {
		JSONError(w, http.StatusBadRequest, "source thread not found")
		return
	}
	// Generate new thread ID and branch name
	newThreadID := types.GenerateThreadID()
	newBranchName := fmt.Sprintf("thread/%s", newThreadID)
	// Find the file state at the checkpoint: last commit BEFORE the checkpoint message.
	// This is the state before its tool calls ran, so the fork starts from a clean slate.
	checkpointCommit, _ := s.store.GetLastCommitHashBeforeMessage(req.FromMessageID, req.FromThreadID)
	// Determine the branch point for the fork
	branchPoint := checkpointCommit
	if branchPoint == "" {
		branchPoint = task.BaseRef
	}
	// Save current worktree state before forking, so uncommitted changes
	// aren't lost when we switch back to the old thread. Uses stash (not commit)
	// to avoid polluting the git log with internal snapshot commits.
	// The fork branch starts clean from the checkpoint commit — do NOT pop
	// the stash, or old thread's uncommitted files would leak into the fork.
	if task.Branch != "" {
		_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "checkout", task.Branch)
		_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "stash", "push", "-u", "-m", fmt.Sprintf("fork-%s", taskID))
		_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "branch", newBranchName, branchPoint)
		_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "checkout", newBranchName)
		// Update task.Branch to track the currently checked-out branch
		task.Branch = newBranchName
		// Reset worktree to the checkpoint state.
		// branchPoint is a stash hash (from git stash create) or a branch ref.
		// ResetWorktreeTo handles both cases.
		if branchPoint != "" {
			s.executor.WorktreeMgr().ResetWorktreeTo(taskID, branchPoint)
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
		newThread.Name = s.generateThreadName(task, req.NewContent)
	} else {
		newThread.Name = fmt.Sprintf("Fork #%d", len(threads)+1)
	}
	if err := s.store.CreateThread(newThread); err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Copy messages from source thread up to and including the checkpoint.
	if err := s.store.CopyMessagesToThread(req.FromThreadID, newThreadID, req.FromMessageID); err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
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
	// Set new thread as active
	if err := s.store.SetActiveThread(newThreadID); err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Update task's current_thread_id
	task.CurrentThreadID = newThreadID
	task.UpdatedAt = now
	if err := s.store.UpdateTask(task); err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Broadcast updated task so frontend has new currentThreadID before diff loads
	s.appStreamMgr.BroadcastTaskUpdated(projectID, *task)
	// Broadcast thread_switch event
	s.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeThreadSwitch, Data: map[string]string{"thread_id": newThreadID, "task_id": taskID}})
	// Send fresh diff so the Changes panel reflects the forked branch
	s.broadcastTaskDiff(taskID)
	JSONResponse(w, http.StatusCreated, newThread)
}

// generateThreadName generates a short thread label via LLM, or truncates the text.
func (s *Server) generateThreadName(task *types.Task, content string) string {
	trimmed := strings.TrimSpace(content)
	if len(trimmed) == 0 {
		return "Fork"
	}

	// Try LLM generation if config is available
	if task.LlamaServerURL != "" && task.ModelName != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		messages := []types.ChatMessage{
			{Role: "system", Content: "You are a thread name generator. Given a user message, generate a very short, descriptive thread label (max 30 characters, no quotes). Return only the label, nothing else."},
			{Role: "user", Content: trimmed},
		}

		_, name, err := s.executor.ChatCompletion(ctx, task.LlamaServerURL, task.ModelName, messages)
		if err == nil && strings.TrimSpace(name) != "" {
			name = strings.TrimSpace(name)
			if len(name) > 35 {
				name = name[:35]
			}
			log.Printf("[thread-name] task=%s generated: %q", task.ID, name)
			return name
		}
	}

	// Fallback: truncate the text
	if len(trimmed) > 35 {
		return trimmed[:35]
	}
	return trimmed
}

// checkoutThreadBranch checks out the target thread's git branch.
func (s *Server) checkoutThreadBranch(taskID string, targetThread *types.TaskThread) {
	if targetThread == nil || targetThread.GitBranch == "" {
		return
	}
	_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "checkout", targetThread.GitBranch)
}

// deleteThreadBranch deletes a thread's git branch.
func (s *Server) deleteThreadBranch(taskID string, branchName string) {
	if branchName == "" {
		return
	}
	_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "branch", "-D", branchName)
}

// resetWorktreeToCommit resets the worktree to a commit or stash hash.
func (s *Server) resetWorktreeToCommit(taskID, commitHash string) {
	if commitHash == "" {
		return
	}
	_ = s.executor.WorktreeMgr().ResetWorktreeTo(taskID, commitHash)
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

// broadcastTaskDiff sends the current git diff via WebSocket.
func (s *Server) broadcastTaskDiff(taskID string) {
	task, _ := s.store.GetTaskByTaskID(taskID)
	if task == nil {
		return
	}
	thread, _ := s.store.GetThread(task.CurrentThreadID)
	forkBranch := ""
	if thread != nil {
		forkBranch = thread.GitBranch
	}
	baseRef, _ := s.executor.WorktreeMgr().ResolveDiffBaseline(taskID, task.BaseRef, task.Branch, forkBranch)
	taskDiff, err := s.executor.WorktreeMgr().GetTaskDiff(taskID, baseRef, "")
	if err != nil {
		return
	}
	s.appStreamMgr.SendTaskEvent(task.ProjectID, taskID, types.StreamEvent{Type: types.WSTypeDiffChanged, Data: taskDiff})
}
