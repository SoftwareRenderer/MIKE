package exec

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	"kanban/backend/llama"
	"kanban/backend/stream"
	"kanban/backend/types"

	"github.com/google/uuid"
)

// handleStopped sets the task to stopped state, broadcasts the update, and optionally
// persists an error message. Used by all ctx.Done and cancellation paths.
func (te *TaskExecutor) handleStopped(task *types.Task, projectID, taskID string, output, code string, persistError bool) {
	task.ExecutionState = "stopped"
	task.Status = "review"
	task.Output = output
	te.store.UpdateTask(task)
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "stopped", "activity": "Task stopped by user"}})
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	if persistError {
		te.sendAndPersistError(taskID, output, code)
	}
}

// modelSwitchResult holds the outputs of applyModelSwitch.
type modelSwitchResult struct {
	client        *llama.LlamaClient
	providerName  string
	contextWindow *int
}

// applyModelSwitch creates a new LlamaClient for the given serverURL/modelName,
// updates the modelConfig, and optionally persists the override to the task DB row.
// If discoverContext is true, it attempts to discover and save the context window.
func (te *TaskExecutor) applyModelSwitch(mc *modelConfig, task *types.Task, serverURL, modelName string, persistToDB, discoverContext bool) modelSwitchResult {
	reasoningEffort := task.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = te.configStore.GetModelReasoningEffort(modelName)
	}
	apiKey := te.configStore.GetProviderAPIKey(serverURL)
	client := llama.NewLlamaClient(serverURL, modelName, reasoningEffort, apiKey)
	providerName := te.configStore.GetProviderNameByURL(serverURL)

	var contextWindow *int
	if discoverContext {
		contextWindow = te.configStore.GetModelContextWindow(modelName, serverURL)
		if contextWindow == nil {
			if discovered := te.configStore.DiscoverContextWindow(modelName, serverURL); discovered != nil {
				contextWindow = discovered
				te.configStore.SaveModelContextWindow(modelName, serverURL, *discovered)
				log.Printf("[context] discovered context window for %s: %d", modelName, *discovered)
			}
		}
	}

	mc.set(serverURL, modelName)
	if persistToDB {
		task.LlamaServerURL = serverURL
		task.ModelName = modelName
		task.ReasoningEffort = reasoningEffort
		_ = te.store.UpdateTask(task)
	}

	return modelSwitchResult{
		client:        client,
		providerName:  providerName,
		contextWindow: contextWindow,
	}
}

// broadcastDiffStats fetches the task diff and broadcasts stats to all connected clients.
// If forkBranch is provided, it resolves the thread to get the fork branch for diff baseline.
func (te *TaskExecutor) broadcastDiffStats(projectID, taskID string, task *types.Task, forkBranch string) {
	baseRef, _ := te.worktreeMgr.ResolveDiffBaseline(task.ID, task.BaseRef, task.Branch, forkBranch)
	diffResult, err := te.worktreeMgr.GetTaskDiff(taskID, baseRef, "")
	if err != nil || (diffResult.Diff == "" && len(diffResult.Untracked) == 0) {
		return
	}
	hash := stream.ComputeDiffHash(diffResult.Diff, diffResult.Untracked)
	fileHashes := diffResult.GetPerFileHashes()
	untrackedLines := 0
	for _, u := range diffResult.Untracked {
		untrackedLines += len(u.Lines)
	}
	te.appStreamMgr.BroadcastDiffStats(projectID, taskID, hash, diffResult.ShortStat, len(diffResult.Untracked), untrackedLines, fileHashes, nil)
}

// extractPrevSummary extracts the previous summary text from messages[1] if it exists
// in the format "[Context summary: ...]".
func extractPrevSummary(messages []types.ChatMessage) string {
	if len(messages) > 1 {
		prev := messages[1].Content
		if strings.HasPrefix(prev, "[Context summary: ") && strings.HasSuffix(prev, "]") {
			return prev[len("[Context summary: ") : len(prev)-1]
		}
	}
	return ""
}

// applySummarization runs context summarization: extracts previous summary, calls
// summarizeContext, persists the summary message, and broadcasts the event.
// Returns the new messages slice and lastSummarizeIdx on success.
// If resultCh is non-nil, sends nil (success) or err through it (manual summarize path).
func (te *TaskExecutor) applySummarization(task *types.Task, projectID, taskID string, messages []types.ChatMessage, lastSummarizeIdx int, systemPrompt, serverURL, modelName string, resultCh chan error) ([]types.ChatMessage, int) {
	prevSummary := extractPrevSummary(messages)
	sumResult, err := te.summarizeContext(messages, lastSummarizeIdx, systemPrompt, serverURL, modelName, prevSummary)
	if err != nil {
		log.Printf("[summarize] task=%s summarization failed: %v", taskID, err)
		if resultCh != nil {
			resultCh <- err
		}
		return messages, lastSummarizeIdx
	}

	messages = sumResult.newMessages
	lastSummarizeIdx = sumResult.newLastSummarizeIdx

	// Persist summary with stats
	summaryMsgID := uuid.New().String()
	_ = te.store.CreateTaskMessage(&types.TaskMessage{
		ID:              summaryMsgID,
		TaskID:          taskID,
		ThreadID:        task.CurrentThreadID,
		Role:            "summarization",
		Content:         sumResult.summary,
		ModelName:       modelName,
		PromptTokens:    sumResult.promptTokens,
		PredictedTokens: sumResult.predictedTokens,
		PromptMs:        int(sumResult.promptMs),
		PredictedMs:     int(sumResult.predictedMs),
		CreatedAt:       time.Now().UTC(),
	})
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: "summarization", Data: map[string]any{
		"id":               summaryMsgID,
		"summary":          sumResult.summary,
		"created_at":       time.Now().UTC().Format(time.RFC3339),
		"prompt_tokens":    sumResult.promptTokens,
		"predicted_tokens": sumResult.predictedTokens,
		"prompt_ms":        sumResult.promptMs,
		"predicted_ms":     sumResult.predictedMs,
		"model_name":       modelName,
	}})
	log.Printf("[summarize] task=%s summarized context: %d messages -> %d messages", taskID, sumResult.oldLen, len(messages))

	if resultCh != nil {
		resultCh <- nil
	}
	return messages, lastSummarizeIdx
}

// userMessageToChatMessage converts a UserMessage (with optional image attachments)
// into a ChatMessage. If attachments are present, builds multi-modal ContentParts.
func userMessageToChatMessage(um stream.UserMessage) types.ChatMessage {
	if um.Attachments == "" {
		return types.ChatMessage{Role: "user", Content: um.Content}
	}

	var atts []struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	}
	if err := json.Unmarshal([]byte(um.Attachments), &atts); err != nil {
		return types.ChatMessage{Role: "user", Content: um.Content}
	}

	parts := []types.ContentPart{{Type: "text", Text: um.Content}}
	for _, att := range atts {
		if att.Data == "" {
			continue
		}
		mediaType := att.MimeType
		if mediaType == "" {
			mediaType = "image/png"
		}
		base64Data := att.Data
		if idx := strings.Index(base64Data, ","); idx >= 0 {
			base64Data = base64Data[idx+1:]
		}
		parts = append(parts, types.ContentPart{
			Type: "image",
			ImageSource: &struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			}{Type: "base64", MediaType: mediaType, Data: base64Data},
		})
	}
	return types.ChatMessage{Role: "user", ContentParts: parts}
}

// handleLoopDetected processes a detected loop: checks if max hints are exhausted
// (returns true if task should terminate), injects a hint message, and notifies the frontend.
// If target is non-empty, it is included in the hint (used for tool loops).
func handleLoopDetected(te *TaskExecutor, task *types.Task, projectID, taskID string, detector *loopDetector, loopType LoopType, target string, messages *[]types.ChatMessage) bool {
	if detector.hintCount >= maxLoopHints {
		task.ExecutionState = "failed"
		task.Status = "review"
		task.Output = "Model stuck in loop (max hints exhausted)"
		te.store.UpdateTask(task)
		te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		te.sendAndPersistError(taskID, "Model stuck in loop (max hints exhausted)", "loop_detected")
		return true // terminate
	}

	hint := buildHint(loopType, target)
	log.Printf("[loop] task=%s detected loop type=%d (hint %d/%d), target=%s", taskID, loopType, detector.hintCount+1, maxLoopHints, target)
	*messages = append(*messages, types.ChatMessage{Role: "system", Content: hint})
	detector.hintCount++
	detector.reset()
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Loop detected, steering agent"}})
	return false // continue
}

// persistAssistantMessage creates a task message for an assistant response with stats.
func persistAssistantMessage(te *TaskExecutor, task *types.Task, assistantMsgID string, assistantText, reasoningContent, activeModel, providerName string, usage *types.ChatUsage, messages []types.ChatMessage, startTime time.Time) (promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs int) {
	promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs = computeStats(usage, messages, assistantText, reasoningContent, startTime)
	_ = te.store.CreateTaskMessage(&types.TaskMessage{
		ID: assistantMsgID, TaskID: task.ID, ThreadID: task.CurrentThreadID, Role: "assistant",
		Content: assistantText, ReasoningContent: reasoningContent,
		ModelName: activeModel, ProviderName: providerName,
		PromptTokens: promptTokens, PredictedTokens: predictedTokens, CacheTokens: cacheTokens,
		PromptMs: promptMs, PredictedMs: predictedMs,
		Duration: time.Since(startTime).Milliseconds(), CreatedAt: time.Now().UTC(),
	})
	return promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs
}
