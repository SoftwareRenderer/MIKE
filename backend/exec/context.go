package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"kanban/backend/prompts"
	"kanban/backend/stream"
	"kanban/backend/types"
)

// summarizeResult holds the result of a context summarization operation.
type summarizeResult struct {
	newMessages         []types.ChatMessage
	newLastSummarizeIdx int
	summary             string
	oldLen              int
	promptTokens    int
	predictedTokens int
	promptMs        int64
	predictedMs     int64
}

// shouldSummarize checks if the current context (estimated from messages) is approaching the context window limit.
// Uses the same token estimation as estimatePromptTokens for consistency.
func shouldSummarize(messages []types.ChatMessage, contextWindow int) bool {
	if contextWindow <= 0 {
		return false
	}
	estimatedTokens := estimatePromptTokens(messages)
	return estimatedTokens >= contextWindow*9/10
}

// summarizeContext summarizes the conversation history to reduce context size.
// It sends a non-streaming chat completion with a summarization prompt,
// then returns a new messages array: [system, summary] + last N recent messages.
// previousSummary is the content of the last summarization checkpoint (empty if first summarization).
func (te *TaskExecutor) summarizeContext(messages []types.ChatMessage, lastSummarizeIdx int, systemPrompt, serverURL, modelName, previousSummary string) (summarizeResult, error) {
	oldLen := len(messages)

	chatHistory := formatChatHistory(messages, lastSummarizeIdx, previousSummary != "")
	summarizePrompt, err := prompts.RenderSummarizePrompt(prompts.SummarizeData{
		ChatHistory:     chatHistory,
		PreviousSummary: previousSummary,
	})
	if err != nil {
		return summarizeResult{}, fmt.Errorf("load summarize prompt: %w", err)
	}
	// The formatted chat history is embedded in the system prompt,
	// so we only need a single system message (no raw messages needed).
	summarizeMessages := []types.ChatMessage{
		{Role: "system", Content: summarizePrompt},
	}

	// Send non-streaming summarization request (concurrency handled by ChatCompletion)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	usage, summary, err := te.ChatCompletion(ctx, serverURL, modelName, summarizeMessages, 32768)
	cancel()
	if err != nil {
		return summarizeResult{}, fmt.Errorf("summarization call failed: %w", err)
	}

	// Keep last N messages for continuity (system + last ~10 messages = ~5 turns)
	keepRecent := 10
	var recent []types.ChatMessage
	if len(messages) > keepRecent+1 {
		recent = messages[len(messages)-keepRecent:]
	} else {
		recent = messages[1:] // skip system
	}

	// Build new messages: system + summary + recent
	newMessages := []types.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "assistant", Content: "[Context summary: " + summary + "]"},
	}
	newMessages = append(newMessages, recent...)

	// New lastSummarizeIdx: points to the summary message (index 1)
	newLastSummarizeIdx := 1

	return summarizeResult{
		newMessages:         newMessages,
		newLastSummarizeIdx: newLastSummarizeIdx,
		summary:             summary,
		oldLen:              oldLen,
		promptTokens:        usage.PromptTokens,
		predictedTokens:     usage.CompletionTokens,
		promptMs:            usage.PromptMs,
		predictedMs:         usage.PredictedMs,
	}, nil
}

// formatChatHistory formats the conversation into a plain-text history for the summarization prompt.
// Format:
//
//	User: <content>
//	Assistant: <content>
//	Assistant: (I used the [tool name] tool to [action], which was [successful/unsuccessful])
//
// System messages are skipped. Tool result messages are incorporated into the preceding
// assistant tool call line. Only messages from summarizeFrom onwards are included.
// When skipSummaryMsg is true, the [Context summary: ...] message at index 1 is skipped
// because it is passed separately as PreviousSummary.
func formatChatHistory(messages []types.ChatMessage, lastSummarizeIdx int, skipSummaryMsg bool) string {
	summarizeFrom := max(lastSummarizeIdx, 0)

	// Build a map of toolCallID -> output for quick lookup
	toolResults := make(map[string]string)
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" {
			toolResults[m.ToolCallID] = m.Content
		}
	}

	// Pre-allocate builder capacity
	var totalSize int
	for _, m := range messages {
		totalSize += len(m.Content) + len(m.ReasoningContent)
	}
	sb := strings.Builder{}
	sb.Grow(totalSize + len(messages)*50)
	for i := summarizeFrom; i < len(messages); i++ {
		m := messages[i]
		// Skip the previous summary message — it is passed as PreviousSummary
		if skipSummaryMsg && i == 1 && strings.HasPrefix(m.Content, "[Context summary: ") {
			continue
		}
		switch m.Role {
		case "system":
			continue
		case "tool":
			// Already incorporated into assistant line above
			continue
		case "user":
			sb.WriteString("User: ")
			sb.WriteString(m.Content)
			sb.WriteString("\n")
		case "assistant":
			if len(m.ToolCalls) > 0 {
				// Format each tool call as a descriptive line
				for _, tc := range m.ToolCalls {
					action := formatToolAction(tc.Function.Name, tc.Function.Arguments)
					status := formatToolStatus(tc.ID, toolResults)
					sb.WriteString(fmt.Sprintf("Assistant: (I used the %s tool to %s, which was %s)\n",
						tc.Function.Name, action, status))
				}
			} else if m.Content != "" {
				sb.WriteString("Assistant: ")
				sb.WriteString(m.Content)
				sb.WriteString("\n")
			}
		}
	}

	return sb.String()
}

// formatToolAction extracts a human-readable action description from a tool call.
func formatToolAction(name, arguments string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "perform an action"
	}

	switch name {
	case "read_file":
		if path, ok := args["path"].(string); ok && path != "" {
			return "read `" + path + "`"
		}
	case "write_file":
		if path, ok := args["path"].(string); ok && path != "" {
			return "write `" + path + "`"
		}
	case "edit_file":
		if path, ok := args["path"].(string); ok && path != "" {
			return "edit `" + path + "`"
		}
	case "bash":
		if cmd, ok := args["command"].(string); ok && cmd != "" {
			// Truncate long commands
			cmd = strings.TrimSpace(cmd)
			if len(cmd) > 77 {
				cmd = cmd[:74] + "..."
			}
			return "execute `" + cmd + "`"
		}
	case "task_complete":
		return "complete the task"
	}
	return "perform an action"
}

// formatToolStatus checks the tool result to determine success/failure.
func formatToolStatus(toolCallID string, toolResults map[string]string) string {
	output, ok := toolResults[toolCallID]
	if !ok {
		return "pending"
	}
	// Check for common error patterns
	lower := strings.ToLower(output)
	if strings.HasPrefix(lower, "error") || strings.HasPrefix(lower, "failed") {
		return "unsuccessful"
	}
	return "successful"
}

// shellQuote safely quotes a string for use in a shell command.
// Uses single quotes and escapes any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// isSandboxableTool returns true if the tool can run inside a sandbox.
func isSandboxableTool(name string) bool {
	switch name {
	case "read_file", "write_file", "edit_file", "bash":
		return true
	default:
		return false
	}
}

// createCheckpoint checks for file changes after tool calls and creates a git stash snapshot.
// Uses `git stash create` + `git stash store` to record a checkpoint WITHOUT removing changes
// from the working tree — files stay visible in the filesystem.
// Runs the LLM labeler in a goroutine so it does not block the main execution loop.
// ctx is used to cancel the background goroutine when the task is stopped.
func (te *TaskExecutor) createCheckpoint(ctx context.Context, projectID, taskID, worktreePath, threadID, assistantMsgID string, messages []types.ChatMessage, sandboxEnabled bool) {
	// Check for file changes
	statusOut, err := te.worktreeMgr.GitRunInWorktreeOutput(taskID, "status", "--porcelain")
	if err != nil || len(strings.TrimSpace(statusOut)) == 0 {
		return // no changes
	}

	// Send pending event to frontend
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeCheckpointPending, Data: map[string]string{"message_id": assistantMsgID}})

	// Extract turn text BEFORE spawning the goroutine so the goroutine
	// doesn't hold a reference to the full (and growing) messages slice.
	turnText := FormatConversationTurns(ExtractLastTurns(messages, 3))

	// Run stash + LLM labeler in background (non-blocking)
	go func() {
		// Check for cancellation before expensive work
		select {
		case <-ctx.Done():
			return
		default:
		}

		diffStat, err := te.worktreeMgr.GitRunInWorktreeOutput(taskID, "diff", "--stat", "HEAD")
		if err != nil {
			diffStat = "file changes"
		}

		taskForLabel, _ := te.store.GetTaskByTaskID(taskID)
		branchPrefix := ""
		if taskForLabel != nil && taskForLabel.Branch != "" {
			branchPrefix = "On " + taskForLabel.Branch + ": "
		}
		label := branchPrefix + te.GenerateCommitLabel(taskID, diffStat, turnText)

		// Check for cancellation before git operations
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Serialize stash operations per project (stash is repo-wide)
		task, _ := te.store.GetTaskByTaskID(taskID)
		projectID := ""
		if task != nil {
			projectID = task.ProjectID
		}
		var stashHash string
		var stashErr error
		if projectID != "" {
			mu := te.worktreeMgr.GetProjectStashMu(projectID)
			mu.Lock()
			// Stage changes, create stash commit (doesn't remove from working tree), store in stash list
			_ = te.worktreeMgr.GitRunInWorktree(taskID, "add", "-A")
			unstageNewBinaryFiles(te.worktreeMgr, taskID)
			stashCreateOut, createErr := te.worktreeMgr.GitRunInWorktreeOutput(taskID, "stash", "create")
			if createErr == nil {
				stashCreateHash := strings.TrimSpace(stashCreateOut)
				_ = te.worktreeMgr.GitRunInWorktree(taskID, "stash", "store", "-m", label, stashCreateHash)
				stashHash = stashCreateHash
			} else {
				stashErr = createErr
			}
			mu.Unlock()
		} else {
			// Fallback without locking (shouldn't happen)
			_ = te.worktreeMgr.GitRunInWorktree(taskID, "add", "-A")
			unstageNewBinaryFiles(te.worktreeMgr, taskID)
			stashCreateOut, createErr := te.worktreeMgr.GitRunInWorktreeOutput(taskID, "stash", "create")
			if createErr == nil {
				stashCreateHash := strings.TrimSpace(stashCreateOut)
				_ = te.worktreeMgr.GitRunInWorktree(taskID, "stash", "store", "-m", label, stashCreateHash)
				stashHash = stashCreateHash
			} else {
				stashErr = createErr
			}
		}

		if stashErr != nil {
			log.Printf("[checkpoint] task=%s stash failed: %v", taskID, stashErr)
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeCheckpointComplete, Data: map[string]string{
				"message_id":  assistantMsgID,
				"commit_hash": "",
				"label":       label,
			}})
			return
		}

		// Store stash hash on the assistant message (reuses git_commit_hash field)
		if stashHash != "" {
			_ = te.store.UpdateTaskMessageCommitHash(assistantMsgID, stashHash)
		}

		// Broadcast updated diff stats so the frontend updates staged/unstaged split
		// (git reset HEAD doesn't trigger fsnotify, so the watcher won't pick this up)
		taskForBroadcast, _ := te.store.GetTaskByTaskID(taskID)
		if taskForBroadcast != nil {
			thread, _ := te.store.GetThread(taskForBroadcast.CurrentThreadID)
			forkBranch := ""
			if thread != nil {
				forkBranch = thread.GitBranch
			}
			baseRef, _ := te.worktreeMgr.ResolveDiffBaseline(taskForBroadcast.ID, taskForBroadcast.BaseRef, taskForBroadcast.Branch, forkBranch)
			if diffResult, err := te.worktreeMgr.GetTaskDiff(taskID, baseRef, ""); err == nil && (diffResult.Diff != "" || len(diffResult.Untracked) > 0) {
				hash := stream.ComputeDiffHash(diffResult.Diff, diffResult.Untracked, diffResult.StagedHash)
				untrackedLines := 0
				for _, u := range diffResult.Untracked {
					untrackedLines += len(u.Lines)
				}
				// Send staged_paths so the frontend updates in-cache staged status
				// with zero HTTP requests. nil fileHashes avoids redundant re-fetches.
				te.appStreamMgr.BroadcastDiffStats(projectID, taskID, hash, diffResult.ShortStat, len(diffResult.Untracked), untrackedLines, nil, nil, diffResult.StagedPaths)
			}
		}

		// Send complete event to frontend
		te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeCheckpointComplete, Data: map[string]string{
			"message_id":  assistantMsgID,
			"commit_hash": stashHash,
			"label":       label,
		}})
	}()
}

// GenerateCommitLabel uses the LLM to generate a descriptive commit label from git diff stat
// and pre-formatted conversation turn text. Falls back to "{title}\n\nCo-authored-by: {model}" on any error.
// turnText is the pre-formatted conversation text (caller extracts last N turns to avoid
// passing the full conversation slice into a background goroutine).
func (te *TaskExecutor) GenerateCommitLabel(taskID, diffStat, turnText string) string {
	task, err := te.store.GetTaskByTaskID(taskID)
	if err != nil || task.LlamaServerURL == "" || task.ModelName == "" {
		return "file changes"
	}
	fallback := fmt.Sprintf("%s\n\nCo-authored-by: %s", task.Title, task.ModelName)

	labelMessages := []types.ChatMessage{
		{Role: "system", Content: "You are a commit message generator. Given a git diff --stat output and the conversation that led to the changes, generate a short, descriptive commit label (one line, max 80 characters, imperative mood, no period at end). Return only the label, nothing else."},
		{Role: "user", Content: fmt.Sprintf("Conversation context:\n\n%s\n\nGit diff --stat:\n\n%s", turnText, diffStat)},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, label, err := te.ChatCompletion(ctx, task.LlamaServerURL, task.ModelName, labelMessages)
	if err != nil {
		log.Printf("[checkpoint-label] task=%s LLM call failed: %v", taskID, err)
		return fallback
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return fallback
	}
	log.Printf("[checkpoint-label] task=%s generated: %q", taskID, label)
	return label
}

// ExtractLastTurns extracts the last N turns (user + assistant pairs, including tool results) from a conversation.
func ExtractLastTurns(messages []types.ChatMessage, n int) []types.ChatMessage {
	// Group messages into turns: each turn starts with a user message and includes
	// all following assistant/tool_call messages until the next user message.
	var turns [][]types.ChatMessage
	var current []types.ChatMessage

	for _, m := range messages {
		if m.Role == "user" && len(current) > 0 {
			turns = append(turns, current)
			current = nil
		}
		// Skip system messages for turn extraction
		if m.Role == "system" {
			continue
		}
		current = append(current, m)
	}
	if len(current) > 0 {
		turns = append(turns, current)
	}

	// Take last N turns
	if len(turns) > n {
		turns = turns[len(turns)-n:]
	}

	// Flatten back into a single slice
	var result []types.ChatMessage
	for _, turn := range turns {
		result = append(result, turn...)
	}
	return result
}

// FormatConversationTurns formats a slice of chat messages into readable text.
func FormatConversationTurns(messages []types.ChatMessage) string {
	var sb strings.Builder
	for _, m := range messages {
		switch m.Role {
		case "user":
			sb.WriteString("User: ")
			sb.WriteString(m.Content)
			sb.WriteString("\n\n")
		case "assistant":
			sb.WriteString("Assistant: ")
			if m.Content != "" {
				sb.WriteString(m.Content)
			}
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					sb.WriteString(fmt.Sprintf("[tool: %s %s]\n", tc.Function.Name, tc.Function.Arguments))
				}
			}
			sb.WriteString("\n\n")
		case "tool":
			// Tool result — include truncated output
			if m.Content != "" {
				sb.WriteString("[Tool result]")
				if len(m.Content) > 500 {
					sb.WriteString(m.Content[:500] + "...")
				} else {
					sb.WriteString(m.Content)
				}
				sb.WriteString("\n\n")
			}
		}
	}
	return sb.String()
}
