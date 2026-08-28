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
const commitMessagePrompt = "You are a commit message generator. Given the conversation that led to the changes, generate a concise commit message. Simple changes only need a title. Titles must not have more than 80 characters. Explain key decisions ONLY IF changes include complex logic."

// buildCommitMessages creates the chat messages for commit message generation.
func buildCommitMessages(turnText string) []types.ChatMessage {
	return []types.ChatMessage{
		{Role: "system", Content: commitMessagePrompt},
		{Role: "user", Content: fmt.Sprintf("Conversation context:\n\n%s", turnText)},
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

// defaultSummarizeThreshold is the auto-summarization threshold (percent of the
// context window) used when no explicit config value is set.
const defaultSummarizeThreshold = 90

// shouldSummarize checks if the LLM context usage is approaching the window limit.
// thresholdPct is the percent of the context window at which auto-summarization
// triggers (1-100).
func shouldSummarize(usedTokens, contextWindow, thresholdPct int) bool {
	if contextWindow <= 0 {
		return false
	}
	if thresholdPct <= 0 || thresholdPct > 100 {
		thresholdPct = defaultSummarizeThreshold
	}
	return usedTokens >= contextWindow*thresholdPct/100
}

// contextTokensUsed returns the real context token usage from the most recent LLM
// call (prompt + completion tokens, matching what the model will consume next).
// This is the same value the frontend displays as the context-usage percentage.
// Falls back to a heuristic estimate when the LLM didn't report usage.
func contextTokensUsed(lc *loopContext) int {
	if lc != nil && lc.usage != nil && lc.usage.PromptTokens > 0 {
		return lc.usage.PromptTokens + lc.usage.CompletionTokens
	}
	return estimatePromptTokens(buildLLMContext(lc.messages))
}

// summarizeContext produces a compact summary of the conversation history.
// Sends a non-streaming chat completion. Does not modify the messages slice.
func (te *TaskExecutor) summarizeContext(ctx context.Context, messages []types.ChatMessage, serverURL, modelName, previousSummary string) (summarizeResult, error) {
	oldLen := len(messages)

	chatHistory := formatChatHistory(messages, previousSummary != "")
	systemPrompt, userPrompt, err := prompts.RenderSummarizePrompt(prompts.SummarizeData{
		ChatHistory:     chatHistory,
		PreviousSummary: previousSummary,
	})
	if err != nil {
		return summarizeResult{}, fmt.Errorf("load summarize prompt: %w", err)
	}
	// Conversation data (previous summary + chat history) goes in the user
	// message; some chat templates reject system-only requests.
	summarizeMessages := []types.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	// Non-streaming, with reasoning enabled.
	usage, summary, err := te.ChatCompletion(ctx, serverURL, modelName, summarizeMessages, &ChatCompletionOpts{ReasoningEffort: ""})
	if err != nil {
		return summarizeResult{}, fmt.Errorf("summarization call failed: %w", err)
	}
	// An empty summary must be rejected: buildLLMContext drops all messages
	// before the summarization entry, so installing one erases the conversation.
	if strings.TrimSpace(summary) == "" {
		return summarizeResult{}, fmt.Errorf("summarization produced an empty summary")
	}

	res := summarizeResult{
		summary: summary,
		oldLen:  oldLen,
	}
	if usage != nil {
		res.promptTokens = usage.PromptTokens
		res.predictedTokens = usage.CompletionTokens
		res.promptMs = usage.PromptMs
		res.predictedMs = usage.PredictedMs
	}
	return res, nil
}

// buildLLMContext filters out summarization messages (summary is in system prompt).
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
		result = append(result, m)
	}
	return result
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
					result := formatToolResult(tc.Function.Name, tc.ID, status, toolResults)
					sb.WriteString(fmt.Sprintf("Assistant: (I used the %s tool to %s %s)\n",
						tc.Function.Name, action, result))
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
		if summary, ok := args["summary"].(string); ok && summary != "" {
			return "request a review: " + summary
		}
		return "request a review"
	case "create_task":
		return "create"
	case "edit_task":
		return "edit"
	case "lsp_references":
		if path, ok := args["path"].(string); ok && path != "" {
			if sym, ok2 := args["symbol"].(string); ok2 && sym != "" {
				return "find references for `" + sym + "` in `" + path + "`"
			}
			return "find references in `" + path + "`"
		}
	case "lsp_definition":
		if path, ok := args["path"].(string); ok && path != "" {
			if sym, ok2 := args["symbol"].(string); ok2 && sym != "" {
				return "find definition of `" + sym + "` in `" + path + "`"
			}
			return "find definition in `" + path + "`"
		}
	case "lsp_hover":
		if path, ok := args["path"].(string); ok && path != "" {
			if sym, ok2 := args["symbol"].(string); ok2 && sym != "" {
				return "hover over `" + sym + "` in `" + path + "`"
			}
			return "get hover info in `" + path + "`"
		}
	case "lsp_rename":
		if path, ok := args["path"].(string); ok && path != "" {
			if sym, ok2 := args["symbol"].(string); ok2 && sym != "" {
				if nn, ok3 := args["new_name"].(string); ok3 && nn != "" {
					return "rename `" + sym + "` to `" + nn + "` in `" + path + "`"
				}
				return "rename `" + sym + "` in `" + path + "`"
			}
			return "rename a symbol in `" + path + "`"
		}
	}
	return "perform an action"
}

// formatToolStatus checks the tool result to determine success/failure.
func formatToolStatus(toolCallID string, toolResults map[string]string) string {
	output, ok := toolResults[toolCallID]
	if !ok {
		return "pending"
	}
	// Check for common error patterns.
	// Tool errors are wrapped as "tool <name> failed: <msg>" by executeTool.
	lower := strings.ToLower(output)
	if strings.HasPrefix(lower, "error") || strings.HasPrefix(lower, "failed") || strings.HasPrefix(lower, "tool ") {
		return "unsuccessful"
	}
	return "successful"
}

// isTaskID checks if a string matches the task ID format (word-word-word).
func isTaskID(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return true
}

// extractTaskFields parses a JSON tool result and extracts task_id and title fields.
// Returns empty strings if parsing fails or fields are missing.
func extractTaskFields(output string) (taskID, title string) {
	var result map[string]string
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return "", ""
	}
	return result["task_id"], result["title"]
}

// formatToolResult returns the descriptive result text for a tool call.
// For create_task: returns "task ID: [id] - [title]" on success (extracted from JSON result),
// "but was unsuccessful. Error: [msg]" on failure, or "a task" when result is unknown.
// For edit_task: returns "task ID: [id] - [title]" on success (extracted from JSON result),
// "but was unsuccessful. Error: [msg]" on failure.
// For other tools: returns "which was [status]".
func formatToolResult(name, toolCallID string, status string, toolResults map[string]string) string {
	if name == "create_task" {
		if status == "successful" {
			taskID, title := extractTaskFields(toolResults[toolCallID])
			if isTaskID(taskID) {
				if title != "" {
					return "task ID: " + taskID + ", title: " + title
				}
				return "task ID: " + taskID
			}
			return "a task"
		}
		if status == "unsuccessful" {
			return "but was unsuccessful. Error: " + strings.TrimSpace(toolResults[toolCallID])
		}
		return "a task"
	}
	if name == "edit_task" {
		if status == "successful" {
			taskID, title := extractTaskFields(toolResults[toolCallID])
			if isTaskID(taskID) {
				if title != "" {
					return "task ID: " + taskID + ", title: " + title
				}
				return "task ID: " + taskID
			}
			return "which was " + status
		}
		if status == "unsuccessful" {
			return "but was unsuccessful. Error: " + strings.TrimSpace(toolResults[toolCallID])
		}
		return "which was " + status
	}
	return "which was " + status
}

// sandboxableTools are the tools that can run inside a sandbox.
var sandboxableTools = map[string]struct{}{
	"read_file": {}, "write_file": {}, "edit_file": {}, "bash": {},
}

// lspTools are the tools that run inside an LSP extension sandbox.
// These are always sandboxed regardless of the project's sandbox setting.
var lspTools = map[string]struct{}{
	"lsp_references": {}, "lsp_definition": {}, "lsp_hover": {}, "lsp_rename": {},
}

func inToolSet(name string, set map[string]struct{}) bool {
	_, ok := set[name]
	return ok
}

// isSandboxableTool returns true if the tool can run inside a sandbox.
func isSandboxableTool(name string) bool {
	return inToolSet(name, sandboxableTools)
}

// isLSPTool returns true for tools that run inside an LSP extension sandbox.
func isLSPTool(name string) bool {
	return inToolSet(name, lspTools)
}

// createCheckpoint checks for file changes after tool calls and creates a git stash snapshot.
// Uses `git stash create` + `git stash store` to record a checkpoint WITHOUT removing changes
// from the working tree — files stay visible in the filesystem.
// Runs in a goroutine; acquires WorktreeMu to block the next turn's tool execution
// until the snapshot is complete, preventing the checkpoint from capturing changes
// from a subsequent turn.
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

	// Run stash creation in background.
	// Acquires WorktreeMu to serialize against the next turn's tool calls,
	// so the checkpoint captures only the changes from the current turn.
	// Label generation is deferred to fork time (handleThreadFork).
	go func() {
		label := git.StashMessageForCheckpoint(taskID, "")

		// Check for cancellation before git operations
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Serialize stash operations per project (stash is repo-wide)
		task, _ := te.store.GetTaskByID(taskID)
		projectID := ""
		if task != nil {
			projectID = task.ProjectID
		}
		var stashHash string
		var stashErr error
		if projectID != "" {
			stashMu := te.worktreeMgr.GetProjectStashMu(projectID)
			stashMu.Lock()
			// Acquire worktree mutex to block the next turn's tool calls.
			// This ensures git add -A captures only the current turn's changes.
			te.GetWorktreeMu(taskID).Lock()
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
			te.GetWorktreeMu(taskID).Unlock()
			stashMu.Unlock()
		} else {
			// Fallback without stash locking (shouldn't happen)
			te.GetWorktreeMu(taskID).Lock()
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
			te.GetWorktreeMu(taskID).Unlock()
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
	task, err := te.store.GetTaskByID(taskID)
	if err != nil {
		return "file changes"
	}
	serverURL, modelName, _ := te.utilityTarget(task)
	if serverURL == "" || modelName == "" {
		return "file changes"
	}
	fallback := task.Title
	if fallback == "" {
		fallback = "file changes"
	}

	labelMessages := buildCheckpointLabelMessages(diffStat, turnText)

	maxTokens := 10
	_, label, err := te.ChatCompletion(ctx, serverURL, modelName, labelMessages, &ChatCompletionOpts{MaxTokens: &maxTokens, ReasoningEffort: "off"})
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

// generateCommitMessageLLM uses the LLM to generate a descriptive commit message
// from pre-formatted conversation turn text. Falls back to
// "{title}\n\nAssisted-by: {model}" on any error.
func (te *TaskExecutor) generateCommitMessageLLM(ctx context.Context, task *types.Task, turnText string) string {
	serverURL, modelName, _ := te.utilityTarget(task)
	if serverURL == "" || modelName == "" {
		return "file changes"
	}
	fallback := fmt.Sprintf("%s\n\nAssisted-by: %s", task.Title, assistedByModel(modelName))

	labelMessages := buildCommitMessages(turnText)

	maxTokens := 128
	_, label, err := te.ChatCompletion(ctx, serverURL, modelName, labelMessages, &ChatCompletionOpts{MaxTokens: &maxTokens, ReasoningEffort: "off"})
	if err != nil {
		log.Printf("[commit-message] task=%s LLM call failed: %v", task.ID, err)
		return fallback
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return fallback
	}
	log.Printf("[commit-message] task=%s generated: %q", task.ID, label)
	return label
}

// BuildConversationFromTaskMessages converts DB TaskMessages into ChatMessages.
// It extracts ToolCalls from tool_call messages and merges them into the preceding assistant
// message, so FormatConversationTurns can render descriptive tool-use lines.
// Used by commit message generation and checkpoint label generation.
func BuildConversationFromTaskMessages(taskMsgs []types.TaskMessage) []types.ChatMessage {
	var conversation []types.ChatMessage
	for _, m := range taskMsgs {
		if m.Role == "tool_call" {
			// Merge tool call info into the preceding assistant message
			if m.ToolCalls != "" && len(conversation) > 0 && conversation[len(conversation)-1].Role == "assistant" {
				var tcMap map[string]any
				if err := json.Unmarshal([]byte(m.ToolCalls), &tcMap); err == nil {
					toolID, _ := tcMap["tool_id"].(string)
					toolName, _ := tcMap["tool_name"].(string)
					rawArgs, _ := tcMap["raw_args"].(string)
					if toolID != "" && toolName != "" {
						conversation[len(conversation)-1].ToolCalls = append(conversation[len(conversation)-1].ToolCalls, types.ToolCallMsg{
							ID:   toolID,
							Type: "function",
							Function: struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							}{Name: toolName, Arguments: rawArgs},
						})
					}
				}
			}
			// Also add a tool result message if available (for status lookup)
			if m.ToolResult != "" {
				var resultMap map[string]any
				if err := json.Unmarshal([]byte(m.ToolResult), &resultMap); err == nil {
					output, _ := resultMap["output"].(string)
					toolID := ""
					if m.ToolCalls != "" {
						var tcMap map[string]any
						if err2 := json.Unmarshal([]byte(m.ToolCalls), &tcMap); err2 == nil {
							toolID, _ = tcMap["tool_id"].(string)
						}
					}
					conversation = append(conversation, types.ChatMessage{Role: "tool", Content: output, ToolCallID: toolID})
				}
			}
			continue
		}
		content := m.Content
		if m.ReasoningContent != "" {
			content = m.ReasoningContent + "\n" + m.Content
		}
		conversation = append(conversation, types.ChatMessage{Role: m.Role, Content: content})
	}
	return conversation
}

// collectUniqueModels extracts unique model names from task messages, preserving
// the order of first appearance. Only non-empty model names from assistant messages
// are collected (user/tool/summarization messages don't carry model attribution).
func collectUniqueModels(taskMsgs []types.TaskMessage) []string {
	seen := make(map[string]bool)
	var models []string
	for _, m := range taskMsgs {
		if m.Role != "assistant" || m.ModelName == "" {
			continue
		}
		if !seen[m.ModelName] {
			seen[m.ModelName] = true
			models = append(models, m.ModelName)
		}
	}
	return models
}

// displayModelName shortens a namespaced model name for commit trailers:
// "orgname/model" → "model". Non-namespaced names are returned unchanged.
func displayModelName(name string) string {
	if i := strings.Index(name, "/"); i > 0 {
		return name[i+1:]
	}
	return name
}

// assistedByModel formats a model name for an Assisted-by trailer:
// the org prefix is dropped and the MIKE: brand is prepended, e.g.
// "openai/gpt-4o" → "MIKE:gpt-4o".
func assistedByModel(name string) string {
	return "MIKE:" + displayModelName(name)
}

// buildAssistedBy formats one or more Assisted-by trailer lines.
// Returns an empty string if no models are provided.
// Output: "\n\nAssisted-by: model1\nAssisted-by: model2"
func buildAssistedBy(models []string) string {
	if len(models) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\nAssisted-by: ")
	for i, m := range models {
		if i > 0 {
			sb.WriteString("\nAssisted-by: ")
		}
		sb.WriteString(assistedByModel(m))
	}
	return sb.String()
}

// buildCommitMessageWithAssistedBy appends Assisted-by trailer(s) to a commit message body.
// Each unique model gets its own Assisted-by line.
func buildCommitMessageWithAssistedBy(body string, models []string) string {
	trailer := buildAssistedBy(models)
	if trailer == "" {
		return body
	}
	return body + trailer
}

// GenerateCommitMessage builds a full commit message from task title, summary, and model.
// Generates one via LLM from conversation + diff. This is the single source of truth for commit
// message generation across all commit paths (auto-commit, manual commit-and-done).
// If multiple models contributed to the task, each gets its own Assisted-by line.
func (te *TaskExecutor) GenerateCommitMessage(ctx context.Context, task *types.Task) string {
	taskMsgs, _ := te.store.GetTaskMessages(task.ID, task.CurrentThreadID)
	conversation := BuildConversationFromTaskMessages(taskMsgs)
	commitMsg := te.generateCommitMessageLLM(ctx, task, FormatConversationTurns(ExtractCommitContext(conversation)))
	// generateCommitMessageLLM returns "file changes" as a generic fallback when LLM is unavailable.
	// Use the task title instead for a more useful commit message.
	if commitMsg == "file changes" && task.Title != "" {
		commitMsg = task.Title
	}

	// Collect all unique models that contributed to this task.
	models := collectUniqueModels(taskMsgs)
	// If no models found in messages, fall back to the task's current model.
	if len(models) == 0 {
		if task.ModelName != "" {
			models = []string{task.ModelName}
		} else {
			models = []string{"unknown model"}
		}
	}

	return buildCommitMessageWithAssistedBy(commitMsg, models)
}

// SSEFlusher is the interface needed for SSE streaming.
type SSEFlusher interface {
	http.ResponseWriter
	http.Flusher
}

// StreamCommitMessage generates a commit message via LLM streaming and sends
// tokens as SSE events. No timeout is applied. Caller must set SSE headers first.
// If multiple models contributed to the task, each gets its own Assisted-by line.
func (te *TaskExecutor) StreamCommitMessage(w SSEFlusher, taskID string, task *types.Task) {
	serverURL, modelName, _ := te.utilityTarget(task)
	if serverURL == "" || modelName == "" {
		fallback := task.Title
		if fallback == "" {
			fallback = "file changes"
		}
		fmt.Fprintf(w, "event: done\ndata: {\"content\":%q}\n\n", fallback)
		return
	}

	taskMsgs, _ := te.store.GetTaskMessages(taskID, task.CurrentThreadID)
	conversation := BuildConversationFromTaskMessages(taskMsgs)
	turnText := FormatConversationTurns(ExtractCommitContext(conversation))
	labelMessages := buildCommitMessages(turnText)

	// Collect all unique models that contributed to this task.
	models := collectUniqueModels(taskMsgs)
	if len(models) == 0 {
		models = []string{task.ModelName}
	}

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

	_, err := te.StreamCompletion(ctx, serverURL, modelName, labelMessages,
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
				content = buildCommitMessageWithAssistedBy(content, models)
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
		content = buildCommitMessageWithAssistedBy(content, models)
		writeSSE("done", fmt.Sprintf("{\"content\":%q}", content))
	}
}

// hasRequestReview checks if an assistant message contains a request_review tool call.
func hasRequestReview(m *types.ChatMessage) bool {
	for _, tc := range m.ToolCalls {
		if tc.Function.Name == "request_review" {
			return true
		}
	}
	return false
}

// collectToolIDs extracts tool call IDs from a set of assistant messages.
func collectToolIDs(msgs []*types.ChatMessage) map[string]bool {
	ids := make(map[string]bool)
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			ids[tc.ID] = true
		}
	}
	return ids
}

// ExtractCommitContext extracts the first user message, ALL assistant messages that
// contain a request_review tool call, and the last relevant assistant message from a
// conversation, along with their tool results.
func ExtractCommitContext(messages []types.ChatMessage) []types.ChatMessage {
	var firstUser *types.ChatMessage
	var requestReviews []*types.ChatMessage // all request_review assistant messages, chronological

	for i := range messages {
		m := &messages[i]
		if m.Role == "system" {
			continue
		}
		if m.Role == "user" && firstUser == nil {
			firstUser = m
		}
		if m.Role == "assistant" {
			if hasRequestReview(m) {
				requestReviews = append(requestReviews, m)
			}
		}
	}

	// Collect assistant messages to include (deduped, chronological).
	// requestReviews is already chronological and every entry precedes lastAssistant,
	// so appending lastAssistant last preserves order.
	var assistants []*types.ChatMessage
	seen := make(map[*types.ChatMessage]bool)
	for _, m := range requestReviews {
		if !seen[m] {
			seen[m] = true
			assistants = append(assistants, m)
		}
	}

	toolIDSet := collectToolIDs(assistants)

	var result []types.ChatMessage
	if firstUser != nil {
		result = append(result, *firstUser)
	}
	for _, m := range assistants {
		result = append(result, *m)
	}
	// Include tool result messages for the included assistants' tool calls.
	for i := range messages {
		m := &messages[i]
		if m.Role == "tool" && toolIDSet[m.ToolCallID] {
			result = append(result, *m)
		}
	}
	return result
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
	// Map tool IDs to results for status lookup.
	toolResults := make(map[string]string)
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" {
			toolResults[m.ToolCallID] = m.Content
		}
	}

	var sb strings.Builder
	for _, m := range messages {
		switch m.Role {
		case "user":
			sb.WriteString("User: ")
			sb.WriteString(strings.TrimRight(m.Content, "\n"))
			sb.WriteString("\n\n")
		case "assistant":
			sb.WriteString("Assistant: ")
			if m.Content != "" {
				sb.WriteString(strings.TrimRight(m.Content, "\n"))
			}
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					action := formatToolAction(tc.Function.Name, tc.Function.Arguments)
					status := formatToolStatus(tc.ID, toolResults)
					result := formatToolResult(tc.Function.Name, tc.ID, status, toolResults)
					if tc.Function.Name == "request_review" {
						sb.WriteString(fmt.Sprintf("\nAssistant: %s", action))
					} else {
						sb.WriteString(fmt.Sprintf("\nAssistant: (I used the %s tool to %s %s)",
							tc.Function.Name, action, result))
					}

				}
			}
			sb.WriteString("\n\n")
		case "tool":
			// Standalone tool message (no preceding assistant with ToolCalls).
			// Already incorporated into assistant line when ToolCalls are present.
			continue
		}
	}
	return sb.String()
}
