package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"mike/backend/types"
	"mike/pkg/logging"
)

const commitMessagePrompt = "You are a commit message generator. Given the conversation that led to the changes, generate a concise commit message. Simple changes only need a title. Titles must not have more than 80 characters. Explain key decisions ONLY IF changes include complex logic."

func buildCommitMessages(turnText string) []types.ChatMessage {
	return []types.ChatMessage{
		{Role: "system", Content: commitMessagePrompt},
		{Role: "user", Content: fmt.Sprintf("Conversation context:\n\n%s", turnText)},
	}
}

// checkpointLabelPrompt is the system prompt for short git stash checkpoint labels.
const checkpointLabelPrompt = "Generate a short label of 5 words or less describing the file changes. Use imperative mood (e.g. 'Add login validation', 'Fix race condition'). Output ONLY the label, nothing else."

func buildCheckpointLabelMessages(diffStat, turnText string) []types.ChatMessage {
	return []types.ChatMessage{
		{Role: "system", Content: checkpointLabelPrompt},
		{Role: "user", Content: fmt.Sprintf("Recent changes:\n\n%s\n\nGit diff --stat:\n\n%s", turnText, diffStat)},
	}
}

// formatToolAction extracts a human-readable description from a tool call.
func formatToolAction(name, arguments string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "perform an action"
	}

	switch name {
	case types.ToolReadFile:
		if path, ok := args["path"].(string); ok && path != "" {
			return "read `" + path + "`"
		}
	case types.ToolWriteFile:
		if path, ok := args["path"].(string); ok && path != "" {
			return "write `" + path + "`"
		}
	case types.ToolEditFile:
		if path, ok := args["path"].(string); ok && path != "" {
			return "edit `" + path + "`"
		}
	case types.ToolBash:
		if cmd, ok := args["command"].(string); ok && cmd != "" {
			// Truncate long commands
			cmd = strings.TrimSpace(cmd)
			if len(cmd) > 77 {
				cmd = cmd[:74] + "..."
			}
			return "execute `" + cmd + "`"
		}
	case types.ToolRequestReview:
		if summary := reviewSummary(arguments); summary != "" {
			return "request a review: " + summary
		}
		return "request a review"
	case types.ToolCreateTask:
		return "create"
	case types.ToolEditTask:
		return "edit"
	case types.ToolLSPReferences:
		if path, ok := args["path"].(string); ok && path != "" {
			if sym, ok2 := args["symbol"].(string); ok2 && sym != "" {
				return "find references for `" + sym + "` in `" + path + "`"
			}
			return "find references in `" + path + "`"
		}
	case types.ToolLSPDefinition:
		if path, ok := args["path"].(string); ok && path != "" {
			if sym, ok2 := args["symbol"].(string); ok2 && sym != "" {
				return "find definition of `" + sym + "` in `" + path + "`"
			}
			return "find definition in `" + path + "`"
		}
	case types.ToolLSPHover:
		if path, ok := args["path"].(string); ok && path != "" {
			if sym, ok2 := args["symbol"].(string); ok2 && sym != "" {
				return "hover over `" + sym + "` in `" + path + "`"
			}
			return "get hover info in `" + path + "`"
		}
	case types.ToolLSPRename:
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

// reviewSummary extracts the summary from a request_review call.
func reviewSummary(arguments string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	summary, _ := args["summary"].(string)
	return strings.TrimSpace(summary)
}

// formatToolStatus determines success or failure from a tool result.
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

// isTaskID checks if a string matches task ID format (word-word-word).
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

// extractTaskFields extracts task_id and title from a JSON tool result.
func extractTaskFields(output string) (taskID, title string) {
	var result map[string]string
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return "", ""
	}
	return result["task_id"], result["title"]
}

// formatToolResult returns descriptive result text for a tool call.
func formatToolResult(name, toolCallID string, status string, toolResults map[string]string) string {
	if name == types.ToolCreateTask {
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
	if name == types.ToolEditTask {
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

// sandboxableTools are tools that can run inside a sandbox.
var sandboxableTools = map[string]struct{}{
	types.ToolReadFile: {}, types.ToolWriteFile: {}, types.ToolEditFile: {}, types.ToolBash: {},
}

// lspTools always run in an LSP sandbox, regardless of project settings.
var lspTools = map[string]struct{}{
	types.ToolLSPReferences: {}, types.ToolLSPDefinition: {}, types.ToolLSPHover: {}, types.ToolLSPRename: {},
}

func inToolSet(name string, set map[string]struct{}) bool {
	_, ok := set[name]
	return ok
}

func isSandboxableTool(name string) bool {
	return inToolSet(name, sandboxableTools)
}

func isLSPTool(name string) bool {
	return inToolSet(name, lspTools)
}

// createCheckpoint snapshots the worktree after a turn's tool calls.
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

		// Stage work, capture as snapshot, then unstage: this avoids stash entries and ensures untracked binaries are preserved during restore, as git stash create lacks -u.
		capture := func() (string, error) {
			_ = te.worktreeMgr.GitRunInWorktree(taskID, "add", "-A")
			out, err := te.worktreeMgr.GitRunInWorktreeOutput(taskID, "stash", "create")
			unstageNewBinaryFiles(te.worktreeMgr, taskID)
			return strings.TrimSpace(out), err
		}

		// Staging touches the shared index; projects are locked to prevent concurrent index modification.
		if projectID != "" {
			defer te.worktreeMgr.LockProjectStash(projectID)()
		}
		// Released explicitly to avoid holding the worktree lock during pinning, reducing contention with tool calls and subtask merges.
		unlockWorktree := te.LockWorktree(taskID)
		snapHash, snapErr := capture()
		unlockWorktree()

		// A snapshot must be pinned to prevent git from pruning the object, ensuring the state exists during later restore.
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

		// Database uses the hash; a ref keeps the object alive.
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

// GenerateCheckpointLabel generates a short label for a git stash checkpoint, falling back to task title.
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

// BuildConversationFromTaskMessages converts DB TaskMessages into ChatMessages for utility prompts.
func BuildConversationFromTaskMessages(taskMsgs []types.TaskMessage) []types.ChatMessage {
	return groupTaskMessages(taskMsgs)
}

// groupTaskMessages rebuilds a conversation from persisted rows, attaching tool calls to assistants.
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

// toolCallRowsByOwner maps assistant rows to their tool calls.
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

// toolCallIDFromMessage extracts tool_id from tool_calls JSON.
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

// toolResultMessage rebuilds tool role message from result.
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

// collectUniqueModels extracts unique model names from assistant messages.
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

// displayModelName shortens namespaced model name for commit trailers.
func displayModelName(name string) string {
	if i := strings.Index(name, "/"); i > 0 {
		return name[i+1:]
	}
	return name
}

// assistedByModel formats model name for Assisted-by trailer.
func assistedByModel(name string) string {
	return "MIKE:" + displayModelName(name)
}

// buildAssistedBy formats Assisted-by trailer lines.
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

// buildCommitMessageWithAssistedBy appends Assisted-by trailers to commit message body.
func buildCommitMessageWithAssistedBy(body string, models []string) string {
	trailer := buildAssistedBy(models)
	if trailer == "" {
		return body
	}
	return body + trailer
}

func hasRequestReview(m *types.ChatMessage) bool {
	for _, tc := range m.ToolCalls {
		if tc.Function.Name == types.ToolRequestReview {
			return true
		}
	}
	return false
}

func collectToolIDs(msgs []*types.ChatMessage) map[string]bool {
	ids := make(map[string]bool)
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			ids[tc.ID] = true
		}
	}
	return ids
}

// ExtractCommitContext extracts first user message and request_review turns for commit context.
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

// reviewSummaries returns summaries of all request_review calls.
func reviewSummaries(taskMsgs []types.TaskMessage) string {
	messages := groupTaskMessages(taskMsgs)
	seen := make(map[string]bool)
	var summaries []string
	for i := range messages {
		if messages[i].Role != "assistant" {
			continue
		}
		for _, tc := range messages[i].ToolCalls {
			if tc.Function.Name != types.ToolRequestReview {
				continue
			}
			summary := reviewSummary(tc.Function.Arguments)
			if summary == "" || seen[summary] {
				continue
			}
			seen[summary] = true
			summaries = append(summaries, summary)
		}
	}
	return strings.Join(summaries, "\n\n")
}

// ExtractLastTurns extracts the last N turns from a conversation.
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

// FormatConversationTurns formats chat messages into readable text for prompts.
func FormatConversationTurns(messages []types.ChatMessage) string {
	return formatTurns(messages, false)
}

// commitContextText builds context for the commit message generator.
func commitContextText(conversation []types.ChatMessage) string {
	return formatTurns(ExtractCommitContext(conversation), true)
}

// formatTurns renders messages as prompt text.
func formatTurns(messages []types.ChatMessage, consolidate bool) string {
	// Map tool IDs to results for status lookup.
	toolResults := make(map[string]string)
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" {
			toolResults[m.ToolCallID] = m.Content
		}
	}

	var sb strings.Builder
	inBlock := false
	endBlock := func() {
		if inBlock {
			sb.WriteString("\n\n")
			inBlock = false
		}
	}
	for _, m := range messages {
		switch m.Role {
		case "user":
			endBlock()
			sb.WriteString("User: ")
			sb.WriteString(strings.TrimRight(m.Content, "\n"))
			sb.WriteString("\n\n")
			continue
		case "tool":
			// Standalone tool message (no preceding assistant with ToolCalls).
			// Already incorporated into assistant line when ToolCalls are present.
			continue
		}

		if !consolidate {
			sb.WriteString("Assistant: ")
		}
		// Reasoning rides in its own field after the rebuild (groupTaskMessages keeps it there
		// so toAPIMessage can send it as reasoning_content), and this renderer reads only
		// Content, so fold the reasoning text in here.
		text := m.Content
		if m.ReasoningContent != "" {
			text = m.ReasoningContent + "\n" + m.Content
		}
		if text != "" {
			if consolidate {
				if inBlock {
					sb.WriteString("\n\n")
				} else {
					sb.WriteString("Assistant: ")
					inBlock = true
				}
			}
			sb.WriteString(strings.TrimRight(text, "\n"))
		}
		for _, tc := range m.ToolCalls {
			action := formatToolAction(tc.Function.Name, tc.Function.Arguments)
			if tc.Function.Name == types.ToolRequestReview {
				if consolidate {
					summary := reviewSummary(tc.Function.Arguments)
					if summary == "" {
						continue
					}
					if inBlock {
						sb.WriteString("\n")
					} else {
						sb.WriteString("Assistant: ")
						inBlock = true
					}
					sb.WriteString(summary)
					continue
				}
				sb.WriteString(fmt.Sprintf("\nAssistant: %s", action))
				continue
			}
			status := formatToolStatus(tc.ID, toolResults)
			result := formatToolResult(tc.Function.Name, tc.ID, status, toolResults)
			// This line carries its own label.
			if consolidate {
				if inBlock {
					sb.WriteString("\n")
				}
				inBlock = true
			}
			sb.WriteString(fmt.Sprintf("Assistant: (I used the %s tool to %s %s)",
				tc.Function.Name, action, result))
		}
		if !consolidate {
			sb.WriteString("\n\n")
		}
	}
	endBlock()
	return sb.String()
}
