package exec

import (
	"context"
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

// broadcastDiffIfGitCmd looks up the execution state and broadcasts a diff
// if the task has an active executor. Used after tool execution when a git
// command may have changed the working tree state.
func (te *TaskExecutor) broadcastDiffIfGitCmd(projectID, taskID string, task *types.Task) {
	te.taskMu.Lock()
	state := te.tasks[taskID]
	te.taskMu.Unlock()
	if state != nil {
		te.checkAndBroadcastDiff(projectID, taskID, task, state, "")
	}
}

// checkAndBroadcastDiff computes the task diff and broadcasts stats only if the hash
// differs from the last broadcast. This avoids duplicate broadcasts when fsnotify
// already caught the same change. If forkBranch is provided, it resolves the thread
// to get the fork branch for diff baseline.
func (te *TaskExecutor) checkAndBroadcastDiff(projectID, taskID string, task *types.Task, state *executionState, forkBranch string) {
	baseRef, _ := te.worktreeMgr.ResolveDiffBaseline(task.ID, task.BaseRef, task.Branch, forkBranch)
	diffResult, err := te.worktreeMgr.GetTaskDiff(taskID, baseRef, "")
	if err != nil || (diffResult.Diff == "" && len(diffResult.Untracked) == 0) {
		return
	}
	hash := stream.ComputeDiffHash(diffResult.Diff, diffResult.Untracked, diffResult.StagedHash)
	// Skip if hash unchanged — fsnotify already broadcast this state
	if hash == state.lastDiffHash {
		return
	}
	state.lastDiffHash = hash
	fileHashes := diffResult.GetPerFileHashes()
	untrackedLines := 0
	for _, u := range diffResult.Untracked {
		untrackedLines += len(u.Lines)
	}
	filesN := len(diffResult.Tracked) + len(diffResult.Untracked)
	te.appStreamMgr.BroadcastDiffStats(projectID, taskID, hash, diffResult.ShortStat, filesN, len(diffResult.Untracked), untrackedLines, fileHashes, nil, diffResult.StagedPaths)
}

// extractPrevSummary returns the content of the most recent summarization message.
func extractPrevSummary(messages []types.ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "summarization" {
			return messages[i].Content
		}
	}
	return ""
}

// applySummarization runs summarization, appends the summary message, and broadcasts the event.
// Summary is injected into the system prompt on the next LLM call.
func (te *TaskExecutor) applySummarization(ctx context.Context, task *types.Task, projectID, taskID string, messages []types.ChatMessage, serverURL, modelName string, resultCh chan error) []types.ChatMessage {
	prevSummary := extractPrevSummary(messages)
	sumResult, err := te.summarizeContext(ctx, messages, serverURL, modelName, prevSummary)
	if err != nil {
		log.Printf("[summarize] task=%s summarization failed: %v", taskID, err)
		if resultCh != nil {
			resultCh <- err
		}
		return messages
	}

	// Persist to DB.
	summaryMsgID := uuid.New().String()
	summaryCreatedAt := time.Now().UTC()
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
		CreatedAt:       summaryCreatedAt,
	})

	// Append to in-memory messages.
	messages = append(messages, types.ChatMessage{
		Role:    "summarization",
		Content: sumResult.summary,
	})

	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: "summarization", Data: map[string]any{
		"id":               summaryMsgID,
		"summary":          sumResult.summary,
		"created_at":       summaryCreatedAt.Format(time.RFC3339),
		"prompt_tokens":    sumResult.promptTokens,
		"predicted_tokens": sumResult.predictedTokens,
		"prompt_ms":        sumResult.promptMs,
		"predicted_ms":     sumResult.predictedMs,
		"model_name":       modelName,
	}})
	log.Printf("[summarize] task=%s summarized context (messages preserved, LLM context compressed)", taskID)

	if resultCh != nil {
		resultCh <- nil
	}
	return messages
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

// handleLoopDetected processes a detected loop: increments the consecutive loop counter,
// checks if max consecutive loops are reached (returns true if task should terminate),
// injects a hint message, and notifies the frontend.
// If target is non-empty, it is included in the hint (used for tool loops).
// If ideationMode is true, loop hints are suppressed (no hint injection, no streak tracking).
// Plan mode also suppresses hints — reading files and making statements is normal planning behavior.
// On valid turn (via recordTurn), loopStreak is reset to 0.
func handleLoopDetected(te *TaskExecutor, task *types.Task, projectID, taskID string, detector *loopDetector, loopType LoopType, target string, messages *[]types.ChatMessage, ideationMode bool) bool {
	// In ideation/plan mode, suppress all loop hints — let the LLM freely explore / plan
	if ideationMode || task.IsPlanMode() {
		detector.reset()
		return false // continue without intervention
	}

	detector.loopStreak++
	if detector.loopStreak >= maxLoopHints {
		task.ExecutionState = "failed"
		task.Status = "review"
		task.Output = "Model stuck in loop (max hints exhausted)"
		te.store.UpdateTask(task)
		te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		te.sendAndPersistError(taskID, "Model stuck in loop (max hints exhausted)", "loop_detected")
		return true // terminate
	}

	hint := buildHint(loopType, target)
	log.Printf("[loop] task=%s detected loop type=%d (streak %d/%d), target=%s", taskID, loopType, detector.loopStreak, maxLoopHints, target)
	hintIdx := len(*messages)
	*messages = append(*messages, types.ChatMessage{Role: "user", Content: hint})
	detector.recordHintIndex(hintIdx)
	// Persist the hint as a visible user message so it appears in the chat UI
	te.store.CreateTaskMessage(&types.TaskMessage{
		ID: uuid.New().String(), TaskID: taskID, ThreadID: task.CurrentThreadID, Role: "user",
		Content: hint, CreatedAt: time.Now().UTC(),
	})
	// Preserve loopStreak across hint cycles so consecutive loops accumulate.
	// A valid turn (via recordTurn) resets it to 0.
	streak := detector.loopStreak
	detector.reset()
	detector.loopStreak = streak
	// Broadcast hint to frontend in real-time
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeHint, Data: map[string]any{
		"content":    hint,
		"created_at": time.Now().UTC().Format(time.RFC3339),
	}})
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
