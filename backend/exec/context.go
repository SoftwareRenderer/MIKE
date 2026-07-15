package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"kanban/backend/git"
	"kanban/backend/prompts"
	"kanban/backend/types"
)

// commitMessagePrompt is the system prompt for generating commit messages.
const commitMessagePrompt = "You are a commit message generator. Given a git diff --stat output and the conversation that led to the changes, generate a concise commit message. Simple changes only need a title. Titles must not have more than 80 characters. Explain key decisions ONLY IF changes include complex logic."

// buildCommitMessages creates the chat messages for commit message generation.
func buildCommitMessages(diffStat, turnText string) []types.ChatMessage {
	return []types.ChatMessage{
		{Role: "system", Content: commitMessagePrompt},
		{Role: "user", Content: fmt.Sprintf("Conversation context:\n\n%s\n\nGit diff --stat:\n\n%s", turnText, diffStat)},
	}
}

// checkpointLabelPrompt generates a short 5-word label for git stash checkpoints.
const checkpointLabelPrompt = "Generate a short label of 5 words or less describing the file changes. Use imperative mood (e.g. 'Add login validation', 'Fix race condition'). Output ONLY the label, nothing else."

func buildCheckpointLabelMessages(diffStat, turnText string) []types.ChatMessage {
	return []types.ChatMessage{
		{Role: "system", Content: checkpointLabelPrompt},
		{Role: "user", Content: fmt.Sprintf("Recent changes:\n\n%s\n\nGit diff --stat:\n\n%s", turnText, diffStat)},
	}
}

// summarizeResult holds the result of a context summarization operation.
type summarizeResult struct {
	summary         string
	oldLen          int
	promptTokens    int
	predictedTokens int
	promptMs        int64
	predictedMs     int64
}

// shouldSummarize checks if the assembled LLM context is approaching the window limit.
func shouldSummarize(messages []types.ChatMessage, contextWindow int) bool {
	if contextWindow <= 0 {
		return false
	}
	// Estimate tokens in assembled context (what the LLM actually sees)
	llmCtx := buildLLMContext(messages)
	estimatedTokens := estimatePromptTokens(llmCtx)
	return estimatedTokens >= contextWindow*9/10
}

// summarizeContext produces a compact summary of the conversation history.
// Sends a non-streaming chat completion. Does not modify the messages slice.
func (te *TaskExecutor) summarizeContext(ctx context.Context, messages []types.ChatMessage, serverURL, modelName, previousSummary string) (summarizeResult, error) {
	oldLen := len(messages)

	chatHistory := formatChatHistory(messages, previousSummary != "")
	summarizePrompt, err := prompts.RenderSummarizePrompt(prompts.SummarizeData{
		ChatHistory:     chatHistory,
		PreviousSummary: previousSummary,
	})
	if err != nil {
		return summarizeResult{}, fmt.Errorf("load summarize prompt: %w", err)
	}
	// Chat history is embedded in the system prompt.
	summarizeMessages := []types.ChatMessage{
		{Role: "system", Content: summarizePrompt},
	}

	// Non-streaming, with reasoning enabled.
	usage, summary, err := te.ChatCompletion(ctx, serverURL, modelName, summarizeMessages, &ChatCompletionOpts{ReasoningEffort: ""})
	if err != nil {
		return summarizeResult{}, fmt.Errorf("summarization call failed: %w", err)
	}

	return summarizeResult{
		summary:         summary,
		oldLen:          oldLen,
		promptTokens:    usage.PromptTokens,
		predictedTokens: usage.CompletionTokens,
		promptMs:        usage.PromptMs,
		predictedMs:     usage.PredictedMs,
	}, nil
}

// buildLLMContext filters out summarization messages (summary is in system prompt),
// assistant messages containing request_review tool calls, plus their orphaned results.
// When a summarization message exists, all messages between the system prompt and
// the last summarization are dropped — their content is captured by the summary
// in the system prompt. The system message itself is always preserved.
func buildLLMContext(messages []types.ChatMessage) []types.ChatMessage {
	// Find the last summarization message — everything between system and it is compressed.
	lastSummaryIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "summarization" {
			lastSummaryIdx = i
			break
		}
	}

	// Collect tool_call_ids from assistants with request_review — their results become orphans.
	filteredIDs := make(map[string]bool)
	for _, m := range messages {
		if m.Role == "assistant" && hasToolCall(m.ToolCalls, "request_review") {
			for _, tc := range m.ToolCalls {
				filteredIDs[tc.ID] = true
			}
		}
	}

	// Always keep the system message (index 0, contains summary in system prompt).
	// When a summary exists, skip all messages between system and post-summary.
	var result []types.ChatMessage
	if len(messages) > 0 {
		result = append(result, messages[0]) // system prompt
	}
	startIdx := 1 // skip system
	if lastSummaryIdx >= 0 {
		startIdx = lastSummaryIdx + 1 // skip pre-summary + summarization itself
	}
	for i := startIdx; i < len(messages); i++ {
		m := messages[i]
		if m.Role == "assistant" && hasToolCall(m.ToolCalls, "request_review") {
			continue
		}
		if m.Role == "tool" && filteredIDs[m.ToolCallID] {
			continue
		}
		result = append(result, m)
	}
	return result
}

// hasToolCall checks if any tool call in the slice matches the given name.
func hasToolCall(toolCalls []types.ToolCallMsg, name string) bool {
	for _, tc := range toolCalls {
		if tc.Function.Name == name {
			return true
		}
	}
	return false
}

// formatChatHistory formats conversation as plain text for the summarization prompt.
// System and tool messages are skipped. Summarization messages are skipped when
// skipSummaryMsg is true (content passed as PreviousSummary instead).
func formatChatHistory(messages []types.ChatMessage, skipSummaryMsg bool) string {
	// Map tool results for status lookup.
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
	for _, m := range messages {
		switch m.Role {
		case "system":
			continue
		case "summarization":
			if skipSummaryMsg {
				continue // passed as PreviousSummary
			}
			sb.WriteString("Assistant: [Context summary: ")
			sb.WriteString(m.Content)
			sb.WriteString("]\n")
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
	case "request_review":
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
// Runs in a goroutine so it does not block the main execution loop.
// Label generation is deferred to fork time (handleThreadFork).
// newMessages contains only messages since the last checkpoint.
func (te *TaskExecutor) createCheckpoint(ctx context.Context, projectID, taskID, worktreePath, threadID, assistantMsgID string, newMessages []types.ChatMessage, sandboxEnabled bool) {
	// Check for file changes
	statusOut, err := te.worktreeMgr.GitRunInWorktreeOutput(taskID, "status", "--porcelain")
	if err != nil || len(strings.TrimSpace(statusOut)) == 0 {
		return // no changes
	}

	// Send pending event to frontend
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeCheckpointPending, Data: map[string]string{"message_id": assistantMsgID}})

	// Run stash creation in background (non-blocking).
	// Label generation is deferred to fork time (handleThreadFork).
	go func() {
		label := git.StashMessageForCheckpoint(taskID, "", "")

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
		}})
	}()
}

// GenerateCheckpointLabel generates a short 5-word label for a git stash checkpoint.
// Falls back to the task title on any error.
func (te *TaskExecutor) GenerateCheckpointLabel(ctx context.Context, taskID, diffStat, turnText string) string {
	task, err := te.store.GetTaskByTaskID(taskID)
	if err != nil || task.LlamaServerURL == "" || task.ModelName == "" {
		return "file changes"
	}
	fallback := task.Title
	if fallback == "" {
		fallback = "file changes"
	}

	labelMessages := buildCheckpointLabelMessages(diffStat, turnText)

	maxTokens := 10
	_, label, err := te.ChatCompletion(ctx, task.LlamaServerURL, task.ModelName, labelMessages, &ChatCompletionOpts{MaxTokens: &maxTokens, ReasoningEffort: "off"})
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

// GenerateCommitMessage uses the LLM to generate a descriptive commit label from git diff stat
// and pre-formatted conversation turn text. Falls back to "{title}\n\nCo-authored-by: {model}" on any error.
func (te *TaskExecutor) GenerateCommitMessage(ctx context.Context, taskID, diffStat, turnText string) string {
	task, err := te.store.GetTaskByTaskID(taskID)
	if err != nil || task.LlamaServerURL == "" || task.ModelName == "" {
		return "file changes"
	}
	fallback := fmt.Sprintf("%s\n\nCo-authored-by: %s", task.Title, task.ModelName)

	labelMessages := buildCommitMessages(diffStat, turnText)

	maxTokens := 128
	_, label, err := te.ChatCompletion(ctx, task.LlamaServerURL, task.ModelName, labelMessages, &ChatCompletionOpts{MaxTokens: &maxTokens, ReasoningEffort: "off"})
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

// GenerateCommitMessageForTask builds a full commit message from task title, summary, and model.
// Generates one via LLM from conversation + diff. This is the single source of truth for commit
// message generation across all commit paths (auto-commit, manual commit-and-done).
func (te *TaskExecutor) GenerateCommitMessageForTask(ctx context.Context, taskID string, task *types.Task) string {
	modelName := task.ModelName
	if modelName == "" {
		modelName = "unknown model"
	}

	diffStat, _ := te.worktreeMgr.GitRunInWorktreeOutput(taskID, "diff", "--stat", "HEAD")
	if diffStat == "" {
		diffStat = "no diff available"
	}
	taskMsgs, _ := te.store.GetTaskMessages(taskID, task.CurrentThreadID)
	var conversation []types.ChatMessage
	for _, m := range taskMsgs {
		content := m.Content
		if m.ReasoningContent != "" {
			content = m.ReasoningContent + "\n" + m.Content
		}
		conversation = append(conversation, types.ChatMessage{Role: m.Role, Content: content})
	}
	commitMsg := te.GenerateCommitMessage(ctx, taskID, diffStat, FormatConversationTurns(ExtractLastTurns(conversation, 3)))
	// GenerateCommitMessage returns "file changes" as a generic fallback when LLM is unavailable.
	// Use the task title instead for a more useful commit message.
	if commitMsg == "file changes" && task.Title != "" {
		commitMsg = task.Title
	}

	return fmt.Sprintf("%s\n\nCo-authored-by: %s", commitMsg, modelName)
}

// SSEFlusher is the interface needed for SSE streaming.
type SSEFlusher interface {
	http.ResponseWriter
	http.Flusher
}

// StreamCommitMessage generates a commit message via LLM streaming and sends
// tokens as SSE events. No timeout is applied. Caller must set SSE headers first.
func (te *TaskExecutor) StreamCommitMessage(w SSEFlusher, taskID string, task *types.Task) {
	serverURL := task.LlamaServerURL
	if serverURL == "" || task.ModelName == "" {
		fallback := task.Title
		if fallback == "" {
			fallback = "file changes"
		}
		fmt.Fprintf(w, "event: done\ndata: {\"content\":%q}\n\n", fallback)
		return
	}

	diffStat, _ := te.worktreeMgr.GitRunInWorktreeOutput(taskID, "diff", "--stat", "HEAD")
	if diffStat == "" {
		diffStat = "no diff available"
	}
	taskMsgs, _ := te.store.GetTaskMessages(taskID, task.CurrentThreadID)
	var conversation []types.ChatMessage
	for _, m := range taskMsgs {
		content := m.Content
		if m.ReasoningContent != "" {
			content = m.ReasoningContent + "\n" + m.Content
		}
		conversation = append(conversation, types.ChatMessage{Role: m.Role, Content: content})
	}
	turnText := FormatConversationTurns(ExtractLastTurns(conversation, 3))
	labelMessages := buildCommitMessages(diffStat, turnText)

	var sb strings.Builder
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writeSSE := func(event, data string) {
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			cancel()
			return
		}
		w.Flush()
	}

	_, err := te.StreamCompletion(ctx, serverURL, task.ModelName, labelMessages,
		func(choice types.ChatChoice) {
			if choice.Delta.Content != nil && *choice.Delta.Content != "" {
				token := *choice.Delta.Content
				sb.WriteString(token)
				writeSSE("token", fmt.Sprintf("{\"content\":%q}", token))
			}
			if choice.FinishReason != "" {
				content := strings.TrimSpace(sb.String())
				if content == "" {
					content = task.Title
					if content == "" {
						content = "file changes"
					}
				}
				content = fmt.Sprintf("%s\n\nCo-authored-by: %s", content, task.ModelName)
				writeSSE("done", fmt.Sprintf("{\"content\":%q}", content))
			}
		},
		func(progress types.PromptProgressData) {
			total := progress.Total
			if total == 0 {
				total = 1
			}
			pct := (progress.Processed * 100) / total
			writeSSE("prompt_progress", fmt.Sprintf("{\"processed\":%d,\"total\":%d,\"percent\":%d}", progress.Processed, progress.Total, pct))
		},
	)
	if err != nil {
		log.Printf("[commit-preview-stream] task=%s LLM stream failed: %v", taskID, err)
		content := strings.TrimSpace(sb.String())
		if content == "" {
			content = task.Title
			if content == "" {
				content = "file changes"
			}
		}
		content = fmt.Sprintf("%s\n\nCo-authored-by: %s", content, task.ModelName)
		writeSSE("done", fmt.Sprintf("{\"content\":%q}", content))
	}
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
