package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"mike/backend/llama"
	"mike/backend/types"
	"mike/pkg/logging"
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

// formatToolAction extracts a human-readable action description from a tool call.
func formatToolAction(name, arguments string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "perform an action"
	}

	switch name {
	case llama.ToolReadFile:
		if path, ok := args["path"].(string); ok && path != "" {
			return "read `" + path + "`"
		}
	case llama.ToolWriteFile:
		if path, ok := args["path"].(string); ok && path != "" {
			return "write `" + path + "`"
		}
	case llama.ToolEditFile:
		if path, ok := args["path"].(string); ok && path != "" {
			return "edit `" + path + "`"
		}
	case llama.ToolBash:
		if cmd, ok := args["command"].(string); ok && cmd != "" {
			// Truncate long commands
			cmd = strings.TrimSpace(cmd)
			if len(cmd) > 77 {
				cmd = cmd[:74] + "..."
			}
			return "execute `" + cmd + "`"
		}
	case llama.ToolRequestReview:
		if summary, ok := args["summary"].(string); ok && summary != "" {
			return "request a review: " + summary
		}
		return "request a review"
	case llama.ToolCreateTask:
		return "create"
	case llama.ToolEditTask:
		return "edit"
	case llama.ToolLSPReferences:
		if path, ok := args["path"].(string); ok && path != "" {
			if sym, ok2 := args["symbol"].(string); ok2 && sym != "" {
				return "find references for `" + sym + "` in `" + path + "`"
			}
			return "find references in `" + path + "`"
		}
	case llama.ToolLSPDefinition:
		if path, ok := args["path"].(string); ok && path != "" {
			if sym, ok2 := args["symbol"].(string); ok2 && sym != "" {
				return "find definition of `" + sym + "` in `" + path + "`"
			}
			return "find definition in `" + path + "`"
		}
	case llama.ToolLSPHover:
		if path, ok := args["path"].(string); ok && path != "" {
			if sym, ok2 := args["symbol"].(string); ok2 && sym != "" {
				return "hover over `" + sym + "` in `" + path + "`"
			}
			return "get hover info in `" + path + "`"
		}
	case llama.ToolLSPRename:
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

// formatToolResult returns the descriptive result text for a tool call. create_task and
// edit_task report "task ID: [id] - [title]" on success (read out of the JSON result),
// "but was unsuccessful. Error: [msg]" on failure, and create_task says "a task" when the
// result is unknown. Every other tool reports "which was [status]".
func formatToolResult(name, toolCallID string, status string, toolResults map[string]string) string {
	if name == llama.ToolCreateTask {
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
	if name == llama.ToolEditTask {
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
	llama.ToolReadFile: {}, llama.ToolWriteFile: {}, llama.ToolEditFile: {}, llama.ToolBash: {},
}

// lspTools always run in an LSP extension sandbox, whatever the project's sandbox setting.
var lspTools = map[string]struct{}{
	llama.ToolLSPReferences: {}, llama.ToolLSPDefinition: {}, llama.ToolLSPHover: {}, llama.ToolLSPRename: {},
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

// createCheckpoint snapshots the worktree after a turn's tool calls
func (te *TaskExecutor) createCheckpoint(ctx context.Context, projectID, taskID, assistantMsgID string) {
	// Check for file changes
	statusOut, err := te.worktreeMgr.GitRunInWorktreeOutput(taskID, "status", "--porcelain")
	if err != nil || len(strings.TrimSpace(statusOut)) == 0 {
		return // no changes
	}

	// Send pending event to frontend
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeCheckpointPending, Data: map[string]string{"message_id": assistantMsgID}})

	// In the background, so a slow git never delays the turn.
	go func() {
		// Check for cancellation before git operations
		select {
		case <-ctx.Done():
			return
		default:
		}

		task, _ := te.store.GetTaskByID(taskID)
		projectID := ""
		if task != nil {
			projectID = task.ProjectID
		}

		// Stage the turn's work, then capture it: no stash entry, no reset.
		capture := func() (string, error) {
			_ = te.worktreeMgr.GitRunInWorktree(taskID, "add", "-A")
			unstageNewBinaryFiles(te.worktreeMgr, taskID)
			out, err := te.worktreeMgr.GitRunInWorktreeOutput(taskID, "stash", "create")
			return strings.TrimSpace(out), err
		}

		// Staging touches the index, which every worktree of the repository shares. A task with no
		// project has no project lock to take.
		if projectID != "" {
			defer te.worktreeMgr.LockProjectStash(projectID)()
		}
		// Released explicitly rather than deferred: the original scope ended at the capture, and
		// holding the task worktree lock across the pinning and everything after it would widen a
		// lock that tool calls, subtask merges and this checkpoint path all contend on.
		unlockWorktree := te.LockWorktree(taskID)
		snapHash, snapErr := capture()
		unlockWorktree()

		// A hash alone is not a checkpoint: git may prune an object nothing references, and the restore
		// would fail much later against a conversation that still claims the state exists. So a failed
		// pin is a failed checkpoint.
		if snapErr == nil && snapHash != "" {
			if pinErr := te.worktreeMgr.PinTaskSnapshot(taskID, assistantMsgID, snapHash); pinErr != nil {
				snapErr = pinErr
			}
		}

		if snapErr != nil {
			logging.For(logging.Worktree).With(logging.TaskID, taskID).Warnf("snapshot failed: %v", snapErr)
			te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeCheckpointComplete, Data: map[string]string{
				"message_id":  assistantMsgID,
				"commit_hash": "",
			}})
			return
		}

		// The hash stays the database's handle (task_messages.git_commit_hash); the ref is what keeps
		// the object it names alive for the life of the task.
		if snapHash != "" {
			_ = te.store.UpdateTaskMessageCommitHash(assistantMsgID, snapHash)
		}

		// Send complete event to frontend
		te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeCheckpointComplete, Data: map[string]string{
			"message_id":  assistantMsgID,
			"commit_hash": snapHash,
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
		logging.For(logging.Generate).With(logging.TaskID, taskID).Warnf("LLM call failed: %v", err)
		return fallback
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return fallback
	}
	logging.For(logging.Generate).With(logging.TaskID, taskID).Infof("generated: %q", label)
	return label
}

// BuildConversationFromTaskMessages converts DB TaskMessages into ChatMessages for the
// utility prompts (commit messages, checkpoint labels, thread fork titles). It rebuilds
// the same conversation the LLM sees — see groupTaskMessages.
func BuildConversationFromTaskMessages(taskMsgs []types.TaskMessage) []types.ChatMessage {
	return groupTaskMessages(taskMsgs)
}

// Rebuilds a conversation from persisted rows: a tool_call rides with the assistant row that issued it.
func groupTaskMessages(taskMsgs []types.TaskMessage) []types.ChatMessage {
	toolRows, totalToolRows := toolCallRowsByOwner(taskMsgs)
	messages := make([]types.ChatMessage, 0, len(taskMsgs))
	attached := 0
	for i, m := range taskMsgs {
		if m.IsLoopHint {
			continue
		}
		switch m.Role {
		case "tool_call":
			// Emitted with its assistant message, never here.
			continue
		case "user":
			if parts, ok := types.ContentParts(m.Content, m.Attachments); ok {
				messages = append(messages, types.ChatMessage{Role: m.Role, ContentParts: parts})
				continue
			}
			messages = append(messages, types.ChatMessage{Role: m.Role, Content: m.Content})
		case "advisory", "summarization":
			// Advisories are kept after a restart because the model still needs them (the
			// merge-conflict instructions are the case that matters); summarization rows ride
			// along and are filtered by buildLLMContext, whose system prompt carries the summary.
			messages = append(messages, types.ChatMessage{Role: m.Role, Content: m.Content})
		case "assistant":
			owned := toolRows[i]
			attached += len(owned)
			msg := types.ChatMessage{Role: m.Role, Content: m.Content}
			if m.ReasoningContent != "" {
				msg.ReasoningContent = m.ReasoningContent
			}
			for _, tc := range owned {
				toolID, toolName, rawArgs := parseToolCallRow(tc)
				if toolID == "" || toolName == "" {
					continue
				}
				msg.ToolCalls = append(msg.ToolCalls, types.ToolCallMsg{ID: toolID, Type: "function"})
				msg.ToolCalls[len(msg.ToolCalls)-1].Function.Name = toolName
				msg.ToolCalls[len(msg.ToolCalls)-1].Function.Arguments = rawArgs
			}
			messages = append(messages, msg)
			for _, tc := range owned {
				if result, ok := toolResultMessage(tc); ok {
					messages = append(messages, result)
				}
			}
		}
	}

	if unattached := totalToolRows - attached; unattached > 0 {
		logging.For(logging.Exec).Infof("dropped %d tool_call rows whose assistant message is not in this thread", unattached)
	}
	return messages
}

// toolCallRowsByOwner maps each assistant row's position to the tool_call rows belonging to it, and
// returns the tool_call total so the caller can log what it could not attach. A row written before
// the assistant_msg_id column existed names no owner, so it attaches to the nearest preceding assistant.
func toolCallRowsByOwner(taskMsgs []types.TaskMessage) (map[int][]types.TaskMessage, int) {
	idxByAssistantID := make(map[string]int)
	total := 0
	for i, m := range taskMsgs {
		switch m.Role {
		case "assistant":
			if m.ID != "" {
				idxByAssistantID[m.ID] = i
			}
		case "tool_call":
			total++
		}
	}

	byAssistantIdx := make(map[int][]types.TaskMessage)
	lastAssistantIdx := -1
	for i, m := range taskMsgs {
		switch m.Role {
		case "assistant":
			lastAssistantIdx = i
		case "tool_call":
			ownerIdx := lastAssistantIdx
			if m.AssistantMsgID != "" {
				// An owner id that names no row in this thread attaches to nothing: guessing
				// would stitch a tool call onto a turn that never made it.
				idx, ok := idxByAssistantID[m.AssistantMsgID]
				if !ok {
					continue
				}
				ownerIdx = idx
			}
			if ownerIdx < 0 {
				continue
			}
			byAssistantIdx[ownerIdx] = append(byAssistantIdx[ownerIdx], m)
		}
	}
	return byAssistantIdx, total
}

// parseToolCallRow reads the tool invocation stored on a tool_call row.
func parseToolCallRow(m types.TaskMessage) (toolID, toolName, rawArgs string) {
	if m.ToolCalls == "" {
		return "", "", ""
	}
	var tcMap map[string]any
	if err := json.Unmarshal([]byte(m.ToolCalls), &tcMap); err != nil {
		return "", "", ""
	}
	toolID, _ = tcMap["tool_id"].(string)
	toolName, _ = tcMap["tool_name"].(string)
	rawArgs, _ = tcMap["raw_args"].(string)
	return toolID, toolName, rawArgs
}

// toolCallIDFromMessage returns the tool_id recorded in a task message's
// tool_calls JSON, or "" when the message carries none. The id is what ties a
// "tool" role reply back to the assistant's call, so a missing one degrades to
// an unlinked result rather than dropping the result.
func toolCallIDFromMessage(m types.TaskMessage) string {
	if m.ToolCalls == "" {
		return ""
	}
	var tc map[string]any
	if err := json.Unmarshal([]byte(m.ToolCalls), &tc); err != nil {
		return ""
	}
	id, _ := tc["tool_id"].(string)
	return id
}

// toolResultMessage rebuilds the "tool" role message that answers a tool call
// from the task message holding its result. ok is false when there is no
// tool_result or it does not parse, so callers skip the message instead of
// appending an empty result the model would read as a successful no-op.
func toolResultMessage(m types.TaskMessage) (types.ChatMessage, bool) {
	if m.ToolResult == "" {
		return types.ChatMessage{}, false
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(m.ToolResult), &result); err != nil {
		return types.ChatMessage{}, false
	}
	output, _ := result["output"].(string)
	return types.ChatMessage{Role: "tool", Content: output, ToolCallID: toolCallIDFromMessage(m)}, true
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

// hasRequestReview checks if an assistant message contains a request_review tool call.
func hasRequestReview(m *types.ChatMessage) bool {
	for _, tc := range m.ToolCalls {
		if tc.Function.Name == llama.ToolRequestReview {
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

// ExtractCommitContext returns the first user message and every assistant message that
// contains a request_review call, plus those assistants' tool results. Nothing else: working
// turns are already visible in the diff, so the commit generator gets the task prompt plus
// what the agent claimed it had done when it asked for review. A conversation with no
// request_review yields only the task prompt.
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
			// Reasoning rides in its own field after the rebuild (groupTaskMessages keeps it
			// there so toAPIMessage can send it as reasoning_content); this renderer only reads
			// Content, so the text it used to get pre-merged is folded in here.
			text := m.Content
			if m.ReasoningContent != "" {
				text = m.ReasoningContent + "\n" + m.Content
			}
			if text != "" {
				sb.WriteString(strings.TrimRight(text, "\n"))
			}
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					action := formatToolAction(tc.Function.Name, tc.Function.Arguments)
					status := formatToolStatus(tc.ID, toolResults)
					result := formatToolResult(tc.Function.Name, tc.ID, status, toolResults)
					if tc.Function.Name == llama.ToolRequestReview {
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
