package exec

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"kanban/backend/types"
)

// compressResult holds the result of a context compression operation.
type compressResult struct {
	newMessages        []types.ChatMessage
	newLastCompressIdx int
	summary            string
	oldLen             int
}

// shouldCompress checks if the current context (estimated from messages) is approaching the context window limit.
// Uses the same token estimation as estimatePromptTokens for consistency.
func shouldCompress(messages []types.ChatMessage, contextWindow int) bool {
	if contextWindow <= 0 {
		return false
	}
	estimatedTokens := estimatePromptTokens(messages)
	return estimatedTokens >= contextWindow*8/10
}

// compressContext summarizes the conversation history to reduce context size.
// It sends a non-streaming chat completion with a summarization prompt,
// then returns a new messages array: [system, summary] + last N recent messages.
func (te *TaskExecutor) compressContext(messages []types.ChatMessage, lastCompressIdx int, systemPrompt, serverURL, modelName string) (compressResult, error) {
	oldLen := len(messages)

	// Build the summarization request: system prompt + all messages since last compression
	compressFrom := max(lastCompressIdx, 0)

	compressMessages := []types.ChatMessage{
		{Role: "system", Content: "Summarize the key events, decisions, and lessons learned in this conversation. " +
			"Preserve technical details, file paths, code decisions, and any important context. " +
			"This summary will be used to restore context in a truncated conversation. " +
			"Be concise but thorough. Include: what was done, what files were changed, what commands were run, " +
			"what errors were encountered, and what the current state of the task is."},
	}
	// Include messages from after last compression (skip system prompt at index 0)
	for i := compressFrom; i < len(messages); i++ {
		compressMessages = append(compressMessages, messages[i])
	}

	// Send non-streaming summarization request (concurrency handled by ChatCompletion)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	_, summary, err := te.ChatCompletion(ctx, serverURL, modelName, compressMessages)
	cancel()
	if err != nil {
		return compressResult{}, fmt.Errorf("summarization call failed: %w", err)
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

	// New lastCompressIdx: points to the summary message (index 1)
	newLastCompressIdx := 1

	return compressResult{
		newMessages:        newMessages,
		newLastCompressIdx: newLastCompressIdx,
		summary:            summary,
		oldLen:             oldLen,
	}, nil
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
func (te *TaskExecutor) createCheckpoint(projectID, taskID, worktreePath, threadID, assistantMsgID string, messages []types.ChatMessage, sandboxEnabled bool) {
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
		// Get diff stat for the LLM labeler
		diffStat, err := te.worktreeMgr.GitRunInWorktreeOutput(taskID, "diff", "--stat", "HEAD")
		if err != nil {
			diffStat = "file changes"
		}

		// Generate commit label via LLM (include branch prefix for stash list filtering)
		taskForLabel, _ := te.store.GetTaskByTaskID(taskID)
		branchPrefix := ""
		if taskForLabel != nil && taskForLabel.Branch != "" {
			branchPrefix = "On " + taskForLabel.Branch + ": "
		}
		label := branchPrefix + te.GenerateCommitLabel(taskID, diffStat, turnText)

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

		// Send complete event to frontend
		te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeCheckpointComplete, Data: map[string]string{
			"message_id":  assistantMsgID,
			"commit_hash": stashHash,
			"label":       label,
		}})
	}()
}

// GenerateCommitLabel uses the LLM to generate a descriptive commit label from git diff stat
// and pre-formatted conversation turn text. Falls back to "{title}, generated by {model}" on any error.
// turnText is the pre-formatted conversation text (caller extracts last N turns to avoid
// passing the full conversation slice into a background goroutine).
func (te *TaskExecutor) GenerateCommitLabel(taskID, diffStat, turnText string) string {
	task, err := te.store.GetTaskByTaskID(taskID)
	if err != nil || task.LlamaServerURL == "" || task.ModelName == "" {
		return "file changes"
	}
	fallback := fmt.Sprintf("%s, generated by %s", task.Title, task.ModelName)

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
