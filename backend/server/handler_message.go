package server

import (
	"encoding/json"
	"kanban/backend/httputil"
	"net/http"
	"time"

	"github.com/google/uuid"

	"kanban/backend/toolresult"
	"kanban/backend/types"
)

func (s *Server) handleTaskMessages(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method == http.MethodDelete {
		var req struct {
			MessageID    string `json:"message_id"`
			DeleteSingle bool   `json:"delete_single"`
			Regenerate   bool   `json:"regenerate"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MessageID == "" {
			JSONError(w, http.StatusBadRequest, "message_id required")
			return
		}
		// If delete_single is true, only remove that one message (e.g., dismissing an error).
		if req.DeleteSingle {
			// Read the row first: it may be the one naming a checkpoint, and once it is gone nothing
			// in the database says so.
			var released []string
			if msg, err := s.store.GetTaskMessageByID(req.MessageID); err == nil && msg != nil && msg.GitCommitHash != "" {
				released = []string{msg.ID}
			}
			if err := s.store.DeleteTaskMessageByID(req.MessageID); err != nil {
				Fail(w, err, "delete message", "message not found")
				return
			}
			s.releaseMessageSnapshots(taskID, released)
			// Notify running executor to reset loop detector and re-sync messages.
			// Only when actively running — waking an idle executor triggers an unwanted LLM call.
			if s.executor != nil {
				task, _ := s.store.GetTask(projectID, taskID)
				if task != nil && task.ExecutionState == types.ExecStateRunning {
					s.executor.NotifyContextChanged(taskID)
				}
			}
			JSONResponse(w, http.StatusOK, map[string]string{"status": "deleted"})
			return
		}
		// Get the message's role, thread_id, and created_at BEFORE deletion.
		msg, msgErr := s.store.GetTaskMessageByID(req.MessageID)
		msgRole := ""
		msgThreadID := ""
		if msgErr == nil && msg != nil {
			msgRole = msg.Role
			msgThreadID = msg.ThreadID
		}
		msgCreatedAt, msgTimeErr := s.store.GetMessageCreatedAt(req.MessageID)
		// Use the message's own thread_id so deletion targets the correct thread
		// even if the task's active thread has switched since the message was created.
		deletedCheckpoints, err := s.store.DeleteTaskMessagesAfter(taskID, req.MessageID, msgThreadID)
		if err != nil {
			Fail(w, err, "delete messages", "message not found")
			return
		}
		// Release the file states those turns pinned. The delete returns the ids, because only it knows
		// which rows actually went.
		s.releaseMessageSnapshots(taskID, deletedCheckpoints)
		// Fetch task early to check execution state for executor notification.
		task, _ := s.store.GetTask(projectID, taskID)
		// Notify running executor to reset loop detector and re-sync messages.
		// Skip during regeneration — the regenerate message itself triggers a re-sync.
		// Skip when executor is idle — waking it would trigger an unwanted LLM call.
		// (The non-blocking select before each LLM call in the active loop already
		// picks up context changes via NotifyContextChanged when the agent is running.)
		if !req.Regenerate && s.executor != nil && task != nil && task.ExecutionState == types.ExecStateRunning {
			s.executor.NotifyContextChanged(taskID)
		}
		// Clear buffered task events so reconnecting clients don't replay
		// stale events (e.g., summarization for a deleted message, or old
		// turn deltas/tool_calls on regenerate).
		s.appStreamMgr.ClearTaskBuffer(projectID, taskID)
		if !req.Regenerate && msgRole != "summarization" {
			s.cleanupDeletedMessageForks(projectID, taskID, task, msgThreadID, req.MessageID, msgCreatedAt, msgTimeErr, msgRole)
		} else {
			s.broadcastTaskDiff(taskID)
		}
		JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if r.Method == http.MethodPost {
		var msg types.TaskMessage
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			JSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if msg.Role == "" || msg.Content == "" {
			JSONError(w, http.StatusBadRequest, "role and content required")
			return
		}
		// Only user-initiated roles may be created over HTTP
		switch msg.Role {
		case "user", "error":
		default:
			JSONError(w, http.StatusBadRequest, "invalid role: "+msg.Role)
			return
		}
		const maxMessageContentBytes = 1 << 20 // 1MB
		if len(msg.Content) > maxMessageContentBytes {
			JSONError(w, http.StatusRequestEntityTooLarge, "message too large (max 1MB)")
			return
		}
		msg.TaskID = taskID
		msg.ID = uuid.New().String()
		msg.CreatedAt = time.Now().UTC()
		// Set thread_id from task's current thread if not provided
		if msg.ThreadID == "" {
			task, _ := s.store.GetTask(projectID, taskID)
			if task != nil {
				msg.ThreadID = task.CurrentThreadID
			}
		}
		if err := s.store.CreateTaskMessage(&msg); err != nil {
			Fail(w, err, "create message", "task not found")
			return
		}
		JSONResponse(w, http.StatusCreated, msg)
		return
	}
	if r.Method != http.MethodGet {
		httputil.MethodNotAllowed(w)
		return
	}
	// Get the task to find its current thread
	task, taskErr := s.store.GetTask(projectID, taskID)
	threadID := ""
	if taskErr == nil && task != nil {
		threadID = task.CurrentThreadID
	}
	messages, err := s.store.GetTaskMessages(taskID, threadID)
	if err != nil {
		Fail(w, err, "get messages", "task not found")
		return
	}
	// Suppress write_file content args, reasoning_content, and bash tool results unless full=1
	if r.URL.Query().Get("full") != "1" {
		for i := range messages {
			if messages[i].ToolCalls != "" {
				messages[i].ToolCalls = redactWriteFileContent(messages[i].ToolCalls)
			}
			if messages[i].ReasoningContent != "" {
				suppressed := shouldSuppressReasoning(
					messages[i].ReasoningContent,
					messages[i].Content,
					messages[i].ToolCalls,
				)
				if suppressed {
					messages[i].ReasoningContent = "[HIDDEN]"
					messages[i].HasReasoning = true
				}
			}
			if messages[i].Role == "tool_call" && messages[i].ToolResult != "" {
				truncated := truncateToolResult(&messages[i])
				if truncated {
					messages[i].ResultTruncated = true
				}
			}
		}
	}
	JSONResponse(w, http.StatusOK, messages)
}

func (s *Server) handleTaskMessageByID(w http.ResponseWriter, r *http.Request) {
	messageID := r.PathValue("messageID")
	if r.Method == http.MethodGet {
		// Return full message (with unsuppressed reasoning_content and tool_calls) when full=1
		if r.URL.Query().Get("full") == "1" {
			msg, err := s.store.GetTaskMessageByID(messageID)
			if err != nil {
				Fail(w, err, "get message", "message not found")
				return
			}
			JSONResponse(w, http.StatusOK, msg)
			return
		}
		// Check if deleting this message would delete a fork branch.
		// Used by the frontend to show a warning in the delete dialog.
		checkpointThreads, _ := s.store.GetThreadsByCheckpointMessageID(messageID)
		if len(checkpointThreads) == 0 {
			if msgTime, err := s.store.GetMessageCreatedAt(messageID); err == nil && msgTime != nil {
				checkpointThreads, _ = s.store.GetThreadsByCheckpointMessageTime(msgTime.Format(time.RFC3339))
			}
		}
		willDeleteBranch := false
		var branchName string
		if len(checkpointThreads) > 0 {
			willDeleteBranch = true
			if checkpointThreads[0].GitBranch != "" {
				branchName = checkpointThreads[0].GitBranch
			}
		}
		JSONResponse(w, http.StatusOK, map[string]any{
			"will_delete_branch": willDeleteBranch,
			"branch_name":        branchName,
		})
		return
	}
	if r.Method == http.MethodPatch {
		var req struct {
			Content    string `json:"content"`
			CommitHash string `json:"git_commit_hash"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			JSONError(w, http.StatusBadRequest, "invalid request")
			return
		}
		if req.Content != "" {
			if err := s.store.UpdateTaskMessage(messageID, req.Content); err != nil {
				Fail(w, err, "update message", "message not found")
				return
			}
		}
		if req.CommitHash != "" {
			// This hash is later handed to reset-worktree, so a forged one stored here
			// reaches git just as surely as one sent to that route.
			if err := types.ValidateCommitHash(req.CommitHash); err != nil {
				JSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			if err := s.store.UpdateTaskMessageCommitHash(messageID, req.CommitHash); err != nil {
				Fault(w, "update message commit hash", err)
				return
			}
		}
		JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	httputil.MethodNotAllowed(w)
}

// cleanupDeletedMessageForks deletes orphan fork threads when a checkpoint message is deleted.
// Skips worktree reset if the deleted message was a summary (summary doesn't change files).
func (s *Server) cleanupDeletedMessageForks(projectID, taskID string, task *types.Task, threadID, messageID string, msgCreatedAt *time.Time, msgTimeErr error, msgRole string) {
	// Match by message ID first (direct match), then by created_at time (for copied messages
	// in fork threads that have new IDs but preserved timestamps).
	checkpointThreads, _ := s.store.GetThreadsByCheckpointMessageID(messageID)
	if len(checkpointThreads) == 0 && msgTimeErr == nil && msgCreatedAt != nil {
		checkpointThreads, _ = s.store.GetThreadsByCheckpointMessageTime(msgCreatedAt.Format(time.RFC3339))
	}
	if len(checkpointThreads) > 0 {
		activeWasDeleted := false
		// Collect branches to delete before removing threads
		var branchesToDelete []string
		for _, th := range checkpointThreads {
			if th.IsActive {
				activeWasDeleted = true
			}
			if th.GitBranch != "" {
				branchesToDelete = append(branchesToDelete, th.GitBranch)
			}
			// Release its worktree state while its rows are still there to say which snapshots those were.
			s.releaseThreadSnapshots(taskID, th.ID)
			_ = s.store.DeleteThread(th.ID)
		}
		if activeWasDeleted {
			// Switch to the parent thread of the deleted active thread.
			// Walk up the parent chain in case parents were also deleted.
			allThreads, _ := s.store.GetThreadsByTask(taskID)
			threadMap := make(map[string]types.TaskThread)
			for _, t := range allThreads {
				threadMap[t.ID] = t
			}
			var targetThread *types.TaskThread
			candidate := checkpointThreads[0].ParentThreadID
			for candidate != "" {
				parent, ok := threadMap[candidate]
				if !ok {
					// Parent was also deleted; walk up further
					candidate = parent.ParentThreadID
					continue
				}
				// Found a surviving parent
				_ = s.store.SetActiveThread(parent.ID)
				task.CurrentThreadID = parent.ID
				task.UpdatedAt = time.Now().UTC()
				_ = s.store.UpdateTask(task)
				targetThread = &parent
				candidate = "" // break
			}
			if candidate != "" {
				// No surviving parent found; fall back to root/Main thread
				for _, t := range allThreads {
					if t.Name == "Main" || t.ParentThreadID == "" {
						_ = s.store.SetActiveThread(t.ID)
						task.CurrentThreadID = t.ID
						task.UpdatedAt = time.Now().UTC()
						_ = s.store.UpdateTask(task)
						targetThread = &t
						break
					}
				}
			}
			// Check out the new active thread's branch BEFORE the orphaned ones go (git will not delete a
			// checked-out branch), and record where the worktree ended up.
			if branch := s.checkoutThreadBranch(projectID, taskID, task, targetThread); branch != "" && task != nil && task.Branch != branch {
				task.Branch = branch
				_ = s.store.UpdateTask(task)
			}
			for _, branch := range branchesToDelete {
				s.deleteThreadBranch(taskID, branch)
			}
		} else {
			// Non-active fork deleted; just clean up its branch
			for _, branch := range branchesToDelete {
				s.deleteThreadBranch(taskID, branch)
			}
		}
		// Reset file state to match the (possibly new) active thread's conversation.
		// Skip for summary deletion — summary doesn't change files.
		if msgRole != "summarization" {
			lastCommit := s.resolveRollbackCommit(task, task.CurrentThreadID)
			s.resetWorktreeToCommit(taskID, lastCommit)
		}
		s.broadcastTaskDiff(taskID)
		// Broadcast updated task so frontend has new currentThreadID before diff loads
		s.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		s.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeThreadSwitch, Data: map[string]string{"thread_id": task.CurrentThreadID, "task_id": taskID}})
	} else {
		// No fork threads affected, but messages were deleted.
		// Reset file state to match remaining conversation. Skip for summary deletion.
		if msgRole != "summarization" {
			lastCommit := s.resolveRollbackCommit(task, threadID)
			s.resetWorktreeToCommit(taskID, lastCommit)
		}
		s.broadcastTaskDiff(taskID)
	}
}

// resolveRollbackCommit returns the commit the worktree is reset to after messages are deleted
// from a thread, so the file state reflects the remaining conversation. In order: the last
// commit hash among the remaining messages; the thread's checkpoint_commit_hash, when the
// deleted range took every checkpoint with it; the task's base_commit, when no checkpoint remains.
func (s *Server) resolveRollbackCommit(task *types.Task, threadID string) string {
	if c := s.getLastCommitHashInThread(threadID); c != "" {
		return c
	}
	if th, err := s.store.GetThread(threadID); err == nil && th != nil && th.CheckpointCommitHash != "" {
		return th.CheckpointCommitHash
	}
	if task != nil && task.BaseCommit != "" {
		return task.BaseCommit
	}
	return ""
}

// redactWriteFileContent redacts the content field from write_file tool calls.
func redactWriteFileContent(toolCallsJSON string) string {
	var tc map[string]any
	if err := json.Unmarshal([]byte(toolCallsJSON), &tc); err != nil {
		return toolCallsJSON
	}
	if tc["tool_name"] != "write_file" {
		return toolCallsJSON
	}
	if args, ok := tc["args"].(map[string]any); ok {
		if _, hasContent := args["content"]; hasContent {
			args["content"] = "[REDACTED]"
		}
	}
	if _, hasRawArgs := tc["raw_args"]; hasRawArgs {
		tc["raw_args"] = "[REDACTED]"
	}
	redacted, err := json.Marshal(tc)
	if err != nil {
		return toolCallsJSON
	}
	return string(redacted)
}

// shouldSuppressReasoning returns true if reasoning should be collapsed to save bandwidth.
func shouldSuppressReasoning(reasoningContent, content, toolCalls string) bool {
	// Auto-expand conditions (keep reasoning visible):
	// 1. Short reasoning (<=200 chars) with no regular content
	if len(reasoningContent) <= 200 && content == "" {
		return false
	}
	// 2. Has tool calls and no regular content
	if toolCalls != "" && content == "" {
		return false
	}
	// Otherwise, suppress
	return true
}

// truncateToolResult truncates bash and read_file output to 10 lines / 1KB.
func truncateToolResult(msg *types.TaskMessage) bool {
	// Check if this is a tool with displayable output
	var tc map[string]any
	if err := json.Unmarshal([]byte(msg.ToolCalls), &tc); err != nil {
		return false
	}
	toolName := tc["tool_name"].(string)
	if toolName != "bash" && toolName != "read_file" {
		return false
	}
	// Parse tool_result
	var result map[string]any
	if err := json.Unmarshal([]byte(msg.ToolResult), &result); err != nil {
		return false
	}
	output, ok := result["output"].(string)
	if !ok {
		return false
	}
	// Delegate truncation to the shared helper
	preview, originalLines, originalBytes, truncated := toolresult.Truncate(output)
	if !truncated {
		return false
	}
	result["output"] = preview
	result["original_lines"] = originalLines
	result["original_bytes"] = originalBytes
	data, err := json.Marshal(result)
	if err != nil {
		return false
	}
	msg.ToolResult = string(data)
	return true
}
