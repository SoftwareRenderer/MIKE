package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

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
			if err := s.store.DeleteTaskMessageByID(req.MessageID); err != nil {
				JSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			JSONResponse(w, http.StatusOK, map[string]string{"status": "deleted"})
			return
		}
		// Get the task to find its current thread
		task, _ := s.store.GetTask(projectID, taskID)
		threadID := ""
		if task != nil {
			threadID = task.CurrentThreadID
		}
		// Get the message's created_at BEFORE deletion (needed for checkpoint matching below).
		msgCreatedAt, msgTimeErr := s.store.GetMessageCreatedAt(req.MessageID)
		if err := s.store.DeleteTaskMessagesAfter(taskID, req.MessageID, threadID); err != nil {
			JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Check if the deleted message was a checkpoint for any fork threads.
		// If so, delete those orphaned fork threads and switch to parent if needed.
		// Skip during regeneration — the message is being re-sent, not permanently removed.
		if !req.Regenerate {
			s.cleanupDeletedMessageForks(projectID, taskID, task, threadID, req.MessageID, msgCreatedAt, msgTimeErr)
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
			JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		JSONResponse(w, http.StatusCreated, msg)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
		JSONError(w, http.StatusInternalServerError, err.Error())
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
				truncated := truncateBashResult(&messages[i])
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
				JSONError(w, http.StatusNotFound, err.Error())
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
				JSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if req.CommitHash != "" {
			if err := s.store.UpdateTaskMessageCommitHash(messageID, req.CommitHash); err != nil {
				JSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// cleanupDeletedMessageForks deletes orphan fork threads when a checkpoint message is deleted.
func (s *Server) cleanupDeletedMessageForks(projectID, taskID string, task *types.Task, threadID, messageID string, msgCreatedAt *time.Time, msgTimeErr error) {
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
			// Checkout the new active thread's branch and delete orphaned branches
			s.checkoutThreadBranch(taskID, targetThread)
			for _, branch := range branchesToDelete {
				s.deleteThreadBranch(taskID, branch)
			}
		} else {
			// Non-active fork deleted; just clean up its branch
			for _, branch := range branchesToDelete {
				s.deleteThreadBranch(taskID, branch)
			}
		}
		// Reset file state to match the (possibly new) active thread's conversation
		lastCommit := s.getLastCommitHashInThread(task.CurrentThreadID)
		s.resetWorktreeToCommit(taskID, lastCommit)
		s.broadcastTaskDiff(taskID)
		// Broadcast updated task so frontend has new currentThreadID before diff loads
		s.appStreamMgr.BroadcastTaskUpdated(projectID, *task)
		s.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeThreadSwitch, Data: map[string]string{"thread_id": task.CurrentThreadID, "task_id": taskID}})
	} else {
		// No fork threads affected, but messages were deleted.
		// Reset file state to match remaining conversation in active thread.
		lastCommit := s.getLastCommitHashInThread(threadID)
		s.resetWorktreeToCommit(taskID, lastCommit)
		s.broadcastTaskDiff(taskID)
	}
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

// truncateBashResult truncates bash output to 10 lines / 1KB.
func truncateBashResult(msg *types.TaskMessage) bool {
	// Check if this is a bash tool call
	var tc map[string]any
	if err := json.Unmarshal([]byte(msg.ToolCalls), &tc); err != nil {
		return false
	}
	if tc["tool_name"] != "bash" {
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
	// Check if truncation is needed
	lines := strings.Split(output, "\n")
	if len(lines) <= 10 && len(output) <= 1024 {
		return false
	}
	// Find how many lines fit within 1KB
	var bytesAt1KB int
	byteTotal := 0
	for i, line := range lines {
		sep := 0
		if i > 0 {
			sep = 1
		}
		byteTotal += len(line) + sep
		if byteTotal > 1024 {
			bytesAt1KB = i
			break
		}
		bytesAt1KB = i + 1
	}
	maxLines := 10
	if bytesAt1KB < maxLines {
		maxLines = bytesAt1KB
	}
	// Truncate
	var truncated string
	if maxLines > 1 {
		truncated = strings.Join(lines[:maxLines-1], "\n") + "\n... (truncated)"
	} else {
		truncated = output[:1024] + "... (truncated)"
	}
	result["output"] = truncated
	// Store original stats for the frontend "Show all" button
	result["original_lines"] = len(lines)
	result["original_bytes"] = len(output)
	data, err := json.Marshal(result)
	if err != nil {
		return false
	}
	msg.ToolResult = string(data)
	return true
}
