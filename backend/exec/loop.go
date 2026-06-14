package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"kanban/backend/llama"
	"kanban/backend/stream"
	"kanban/backend/types"

	"github.com/google/uuid"
)

// executeTask runs the AI conversation loop.
func (te *TaskExecutor) executeTask(ctx context.Context, task *types.Task, projectID, worktreePath, serverURL, modelName string, b *stream.Broadcaster, userMsgChan chan stream.UserMessage) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[panic] task=%s recovered from panic: %v", task.ID, r)
			te.sendAndPersistError(b, task.ID, fmt.Sprintf("Internal error: %v", r), "panic")
		}
		te.taskMu.Lock()
		delete(te.tasks, task.ID)
		te.taskMu.Unlock()
		te.streamMgr.Remove(task.ID)
	}()

	mc := &modelConfig{serverURL: serverURL, modelName: modelName}
	providerName := te.configStore.GetProviderNameByURL(serverURL)
	reasoningEffort := task.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = te.configStore.GetModelReasoningEffort(modelName)
	}
	client := llama.NewLlamaClient(serverURL, modelName, reasoningEffort)
	tools := llama.GetToolDefinitions()
	var systemPrompt string
	if task.PlanMode {
		systemPrompt = llama.BuildPlanModeSystemPrompt(worktreePath)
	} else {
		systemPrompt = llama.BuildSystemPrompt(worktreePath)
	}

	taskMsgs, _ := te.store.GetTaskMessages(task.ID, task.CurrentThreadID)
	messages := []types.ChatMessage{{Role: "system", Content: systemPrompt}}
	for _, m := range taskMsgs {
		switch m.Role {
		case "user":
			messages = append(messages, types.ChatMessage{Role: m.Role, Content: m.Content})
		case "assistant":
			// Merge reasoning_content into content so all models see the full assistant message
			// (reasoning_content field in requests is undefined behavior - works for Qwen, not gemma)
			content := m.Content
			if m.ReasoningContent != "" {
				content = m.ReasoningContent + "\n" + m.Content
			}
			messages = append(messages, types.ChatMessage{Role: m.Role, Content: content})
		case "tool_call":
			if m.ToolResult != "" {
				var resultMap map[string]interface{}
				if err := json.Unmarshal([]byte(m.ToolResult), &resultMap); err == nil {
					output := resultMap["output"].(string)
					toolID := ""
					if m.ToolCalls != "" {
						var tcMap map[string]interface{}
						if err2 := json.Unmarshal([]byte(m.ToolCalls), &tcMap); err2 == nil {
							if tid, ok := tcMap["tool_id"].(string); ok {
								toolID = tid
							}
						}
					}
					messages = append(messages, types.ChatMessage{Role: "tool", Content: output, ToolCallID: toolID})
				}
			}
		}
	}

	consecutiveFailures := 0
	maxFailures := task.MaxConsecutiveFailedTurns
	if maxFailures == 0 {
		maxFailures = 3
	}

	// Context tracking: load context window from provider config, discover if unknown
	var contextWindow *int
	if cw := te.configStore.GetModelContextWindow(modelName, serverURL); cw != nil {
		contextWindow = cw
	} else if discovered := te.configStore.DiscoverContextWindow(modelName, serverURL); discovered != nil {
		contextWindow = discovered
		te.configStore.SaveModelContextWindow(modelName, serverURL, *discovered)
		log.Printf("[context] discovered context window for %s: %d", modelName, *discovered)
	}
	lastCompressIdx := -1 // index in messages slice up to which content was already compressed

	var assistantContent strings.Builder
	var reasoningContent strings.Builder
	var pendingToolCalls []types.ChatToolCall
	var recentToolSignatures []string // tracks primary tool call signature per turn for loop detection
	startTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			task.StatusState = "stopped"
			task.Output = "Stopped by user"
			te.store.UpdateTask(task)
			te.boardStreamMgr.Broadcast(projectID)
			te.sendAndPersistError(b, task.ID, "Task stopped", "stopped")
			return
		default:
		}

		// Check for model/provider changes before each LLM call.
		if updatedTask, err := te.store.GetTaskByTaskID(task.ID); err == nil {
			newURL := updatedTask.LlamaServerURL
			newModel := updatedTask.ModelName
			curURL, curModel := mc.get()
			if newURL != "" && newURL != curURL || newModel != "" && newModel != curModel {
				if newURL == "" {
					newURL = curURL
				}
				if newModel == "" {
					newModel = curModel
				}
				mc.set(newURL, newModel)
				newReasoningEffort := updatedTask.ReasoningEffort
				if newReasoningEffort == "" {
					newReasoningEffort = te.configStore.GetModelReasoningEffort(newModel)
				}
				client = llama.NewLlamaClient(newURL, newModel, newReasoningEffort)
				providerName = te.configStore.GetProviderNameByURL(newURL)
				contextWindow = te.configStore.GetModelContextWindow(newModel, newURL)
				if contextWindow == nil {
					if discovered := te.configStore.DiscoverContextWindow(newModel, newURL); discovered != nil {
						contextWindow = discovered
						te.configStore.SaveModelContextWindow(newModel, newURL, *discovered)
					}
				}
				log.Printf("[model-switch] task=%s url=%s model=%s context_window=%v", task.ID, newURL, newModel, contextWindow)
			}
		}

		// Check for user messages that arrived during tool execution.
		select {
		case um := <-userMsgChan:
			log.Printf("[user-msg] task=%s received message before LLM call", task.ID)
			messages = append(messages, types.ChatMessage{Role: "user", Content: um.Content})
			if um.ModelName != "" {
				mc.set(um.ServerURL, um.ModelName)
				newReasoningEffort := te.configStore.GetModelReasoningEffort(um.ModelName)
				client = llama.NewLlamaClient(um.ServerURL, um.ModelName, newReasoningEffort)
				providerName = te.configStore.GetProviderNameByURL(um.ServerURL)
			}
		default:
		}

		// Acquire concurrency gate slot before LLM call.
		serverURL, activeModel := mc.get()
		concurrency := te.configStore.GetProviderConcurrency(serverURL)

		// Queue status polling — runs while waiting for a gate slot.
		queueDone := make(chan struct{})
		go func() {
			tick := time.NewTicker(1 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-queueDone:
					return
				case <-ctx.Done():
					return
				case <-tick.C:
					pos := te.gate.Position(serverURL)
					est := te.gate.EstimateWait(serverURL)
					b.Send(types.StreamEvent{Type: types.WSTypeQueue, Data: map[string]interface{}{
						"position":             pos,
						"estimated_wait_seconds": est,
					}})
				}
			}
		}()

		_, err := te.gate.Acquire(ctx, serverURL, concurrency)
		if err != nil {
			close(queueDone)
			// Context cancelled while waiting for slot
			task.StatusState = "stopped"
			task.Output = "Stopped by user"
			te.store.UpdateTask(task)
			te.boardStreamMgr.Broadcast(projectID)
			te.sendAndPersistError(b, task.ID, "Task stopped while waiting for LLM slot", "stopped")
			return
		}
		close(queueDone)

		var streamTokenCount int
		streamStart := time.Time{}

		// Per-call context — can be cancelled independently to abort the LLM stream.
		callCtx, callCancel := context.WithCancel(ctx)

		streamDone := make(chan struct{})
		var usage *types.ChatUsage
		var streamErr error

		go func() {
			usage, streamErr = client.ChatStream(callCtx, messages, tools, func(choice types.ChatChoice) {
				if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
					reasoningContent.WriteString(*choice.Delta.ReasoningContent)
					b.Send(types.StreamEvent{Type: types.WSTypeReasoning, Data: map[string]string{"text": *choice.Delta.ReasoningContent}})
					streamTokenCount++
					if streamStart.IsZero() {
						streamStart = time.Now()
					}
					if streamTokenCount%10 == 0 {
						elapsed := time.Since(streamStart).Milliseconds()
						b.Send(types.StreamEvent{Type: types.WSTypeStats, Data: map[string]interface{}{
							"model_name":       activeModel,
							"predicted_tokens": streamTokenCount,
							"predicted_ms":     int(elapsed),
						}})
					}
				}
				if choice.Delta.Content != nil && *choice.Delta.Content != "" {
					assistantContent.WriteString(*choice.Delta.Content)
					b.Send(types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": *choice.Delta.Content}})
					streamTokenCount++
					if streamStart.IsZero() {
						streamStart = time.Now()
					}
					if streamTokenCount%10 == 0 {
						elapsed := time.Since(streamStart).Milliseconds()
						b.Send(types.StreamEvent{Type: types.WSTypeStats, Data: map[string]interface{}{
							"model_name":       activeModel,
							"predicted_tokens": streamTokenCount,
							"predicted_ms":     int(elapsed),
						}})
					}
				}
				if len(choice.Delta.ToolCalls) > 0 {
					pendingToolCalls = llama.MergeToolCalls(pendingToolCalls, choice.Delta.ToolCalls)
				}
			})

			// Retry on transient errors (skip if cancelled by user message).
			if streamErr != nil && callCtx.Err() == nil {
				for i := 0; i < 3; i++ {
					select {
					case <-callCtx.Done():
						close(streamDone)
						return
					case <-time.After(time.Duration(i+1) * 2 * time.Second):
					}
					usage, streamErr = client.ChatStream(callCtx, messages, tools, func(choice types.ChatChoice) {
						if choice.Delta.ReasoningContent != nil {
							reasoningContent.WriteString(*choice.Delta.ReasoningContent)
							b.Send(types.StreamEvent{Type: types.WSTypeReasoning, Data: map[string]string{"text": *choice.Delta.ReasoningContent}})
						}
						if choice.Delta.Content != nil {
							b.Send(types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": *choice.Delta.Content}})
						}
					})
					if streamErr == nil {
						break
					}
				}
			}

			close(streamDone)
		}()

		// Wait for: stream complete, new user message, or task stop.
		select {
		case <-ctx.Done():
			callCancel()
			<-streamDone
			te.gate.Release(serverURL)
			task.StatusState = "stopped"
			task.Output = "Stopped by user"
			te.store.UpdateTask(task)
			te.boardStreamMgr.Broadcast(projectID)
			return
		case <-streamDone:
			// Stream completed normally (or all retries exhausted)
		case um := <-userMsgChan:
			// New user message — cancel current LLM call.
			log.Printf("[cancel] task=%s user message received during streaming, cancelling", task.ID)
			callCancel()
			// Wait for goroutine to finish (with timeout in case HTTP stream is slow to abort).
			select {
			case <-streamDone:
			case <-time.After(2 * time.Second):
				log.Printf("[cancel] task=%s streamDone timeout, forcing continue", task.ID)
			}
			te.gate.Release(serverURL)

			// Drain all queued messages from channel (non-blocking).
			var queuedMsgs []stream.UserMessage
			queuedMsgs = append(queuedMsgs, um)
			for i := 0; i < 16; i++ {
				select {
				case q := <-userMsgChan:
					queuedMsgs = append(queuedMsgs, q)
				default:
					goto drainDone
				}
			}
		drainDone:

			// Clear partial assistant state.
			assistantContent.Reset()
			reasoningContent.Reset()
			pendingToolCalls = nil

			// Add queued messages to conversation history.
			for _, qm := range queuedMsgs {
				messages = append(messages, types.ChatMessage{Role: "user", Content: qm.Content})
				if qm.ModelName != "" {
					mc.set(qm.ServerURL, qm.ModelName)
					newReasoningEffort := te.configStore.GetModelReasoningEffort(qm.ModelName)
					client = llama.NewLlamaClient(qm.ServerURL, qm.ModelName, newReasoningEffort)
					providerName = te.configStore.GetProviderNameByURL(qm.ServerURL)
				}
			}

			// Notify frontend that response was cancelled.
			b.Send(types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "cancelled", "activity": "Response cancelled, processing new message"}})

			continue
		}
		callCancel()

		if streamErr != nil {
			task.StatusState = "failed"
			task.Output = fmt.Sprintf("Connection failed: %v", streamErr)
			te.store.UpdateTask(task)
			te.boardStreamMgr.Broadcast(projectID)
			te.sendAndPersistError(b, task.ID, streamErr.Error(), "connection_failed")
			return
		}

		// Release gate slot after LLM call (before tool execution).
		te.gate.Release(serverURL)

		assistantText := assistantContent.String()

		type toolResult struct {
			toolCallID string
			output     string
		}
		var toolResults []toolResult

		if len(pendingToolCalls) > 0 {
			// Persist assistant message BEFORE tool calls so DB ordering matches LLM output order
			msgCountBefore := len(messages)
			promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(usage, messages[:msgCountBefore], assistantText, reasoningContent.String(), startTime)
			assistantCreatedAt := time.Now().UTC()
			te.store.CreateTaskMessage(&types.TaskMessage{
				ID: uuid.New().String(), TaskID: task.ID, ThreadID: task.CurrentThreadID, Role: "assistant",
				Content: assistantText, ReasoningContent: reasoningContent.String(),
				ModelName: activeModel, ProviderName: providerName,
				PromptTokens: promptTokens, PredictedTokens: predictedTokens, CacheTokens: cacheTokens,
				PromptMs: promptMs, PredictedMs: predictedMs,
				Duration: time.Since(startTime).Milliseconds(), CreatedAt: assistantCreatedAt,
			})

			for _, tc := range pendingToolCalls {
				if tc.ID == "" || tc.Function.Name == "" {
					log.Printf("[tool] skipping incomplete tool call (id=%q, name=%q)", tc.ID, tc.Function.Name)
					continue
				}
				args := parseToolArgs(tc.Function.Arguments)
				toolStart := time.Now()
				result, success := te.executeTool(tc.Function.Name, args, worktreePath, task)
				toolDuration := time.Since(toolStart).Milliseconds()
				b.Send(types.StreamEvent{Type: types.WSTypeToolCall, Data: map[string]interface{}{"tool_id": tc.ID, "tool_name": tc.Function.Name, "tool_display_name": llama.GetToolDisplayName(tc.Function.Name), "args": args, "raw_args": tc.Function.Arguments}})
				b.Send(types.StreamEvent{Type: types.WSTypeToolResult, Data: map[string]interface{}{"tool_id": tc.ID, "success": success, "output": result, "duration": toolDuration}})
				toolResults = append(toolResults, toolResult{toolCallID: tc.ID, output: result})

				tcJSON, _ := json.Marshal(map[string]interface{}{
					"tool_id":           tc.ID,
					"tool_name":         tc.Function.Name,
					"tool_display_name": llama.GetToolDisplayName(tc.Function.Name),
					"args":              args,
					"raw_args":          tc.Function.Arguments,
				})
				resultJSON, _ := json.Marshal(map[string]interface{}{
					"output":   result,
					"success":  success,
					"duration": toolDuration,
				})
				_ = te.store.CreateTaskMessage(&types.TaskMessage{
					ID:         uuid.New().String(),
					TaskID:     task.ID,
					ThreadID:   task.CurrentThreadID,
					Role:       "tool_call",
					Content:    "",
					ToolCalls:  string(tcJSON),
					ToolResult: string(resultJSON),
					CreatedAt:  time.Now().UTC(),
				})

				if !success {
					consecutiveFailures++
					if consecutiveFailures >= maxFailures {
						task.StatusState = "failed"
						task.Output = fmt.Sprintf("Max consecutive failed turns (%d) reached", maxFailures)
						te.store.UpdateTask(task)
						te.boardStreamMgr.Broadcast(projectID)
						te.sendAndPersistError(b, task.ID, fmt.Sprintf("Max consecutive failed turns (%d) reached", maxFailures), "max_failures")
						return
					}
				} else {
					consecutiveFailures = 0
				}

				if tc.Function.Name == "task_complete" {
					if task.AutoCommit {
						te.gitRun(worktreePath, "add", "-A")
						te.gitRun(worktreePath, "commit", "-m", "Task completed")
						task.StatusState = "completed"
						task.Status = "done"
					} else {
						task.StatusState = "awaiting_review"
						task.Status = "review"
					}
					te.store.UpdateTask(task)
					b.Send(types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]interface{}{
						"state": task.StatusState,
						"prompt_tokens": promptTokens, "predicted_tokens": predictedTokens,
						"cache_tokens": cacheTokens,
						"prompt_ms": promptMs, "predicted_ms": predictedMs,
						"model_name": activeModel, "duration": time.Since(startTime).Milliseconds(),
					}})
					te.boardStreamMgr.Broadcast(projectID)
					return
				}
			}
			// Build assistant message with merged reasoning + content + tool calls.
			// Including tool_calls in the message history is critical for models like gemma
			// to understand what tool was called and match it to the result.
			assistantContentFull := assistantText
			if reasoningContent.Len() > 0 {
				assistantContentFull = reasoningContent.String() + "\n" + assistantText
			}
			var assistantToolCalls []types.ToolCallMsg
			for _, tc := range pendingToolCalls {
				if tc.ID != "" && tc.Function.Name != "" {
					assistantToolCalls = append(assistantToolCalls, types.ToolCallMsg{
						ID:   tc.ID,
						Type: "function",
					})
					assistantToolCalls[len(assistantToolCalls)-1].Function.Name = tc.Function.Name
					assistantToolCalls[len(assistantToolCalls)-1].Function.Arguments = tc.Function.Arguments
				}
			}
			messages = append(messages, types.ChatMessage{
				Role:      "assistant",
				Content:   assistantContentFull,
				ToolCalls: assistantToolCalls,
			})
			for _, tr := range toolResults {
				messages = append(messages, types.ChatMessage{Role: "tool", Content: tr.output, ToolCallID: tr.toolCallID})
			}

			// Loop detection: track tool call signature and check for repetition
			if len(pendingToolCalls) > 0 && pendingToolCalls[0].Function.Name != "" {
				sig := pendingToolCalls[0].Function.Name + ":" + pendingToolCalls[0].Function.Arguments
				recentToolSignatures = append(recentToolSignatures, sig)
				if len(recentToolSignatures) >= 3 {
					last := recentToolSignatures[len(recentToolSignatures)-1]
					isLoop := true
					for i := len(recentToolSignatures) - 3; i < len(recentToolSignatures); i++ {
						if recentToolSignatures[i] != last {
							isLoop = false
							break
						}
					}
					if isLoop {
						task.StatusState = "failed"
						task.Output = "Model stuck in tool-call loop (repeated same tool call 3+ times)"
						te.store.UpdateTask(task)
						te.boardStreamMgr.Broadcast(projectID)
						te.sendAndPersistError(b, task.ID, "Model stuck in tool-call loop", "tool_loop")
						return
					}
				}
			}

			pendingToolCalls = nil
			assistantContent.Reset()
			reasoningContent.Reset()

			// Broadcast updated diff stats to board viewers
			if shortStat, untrackedN, untrackedLines, err := te.worktreeMgr.GetDiffStats(task.ID); err == nil && (shortStat != "" || untrackedN > 0) {
				te.boardStreamMgr.BroadcastDiffStats(projectID, task.ID, shortStat, untrackedN, untrackedLines)
			}
		} else if assistantText != "" {
			// Merge reasoning into content so the model sees its own thinking
			assistantContentFull := assistantText
			if reasoningContent.Len() > 0 {
				assistantContentFull = reasoningContent.String() + "\n" + assistantText
			}
			messages = append(messages, types.ChatMessage{Role: "assistant", Content: assistantContentFull})
			promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(usage, messages[:len(messages)-1], assistantText, reasoningContent.String(), startTime)
			duration := time.Since(startTime).Milliseconds()
			te.store.CreateTaskMessage(&types.TaskMessage{
				ID: uuid.New().String(), TaskID: task.ID, ThreadID: task.CurrentThreadID, Role: "assistant",
				Content: assistantText, ReasoningContent: reasoningContent.String(),
				ModelName: activeModel, ProviderName: providerName,
				PromptTokens: promptTokens, PredictedTokens: predictedTokens, CacheTokens: cacheTokens,
				PromptMs: promptMs, PredictedMs: predictedMs,
				Duration: duration, CreatedAt: time.Now().UTC(),
			})
			assistantContent.Reset()
			reasoningContent.Reset()
			// Check for queued user messages before sending complete.
			select {
			case um := <-userMsgChan:
				log.Printf("[user-msg] task=%s received message after response, continuing", task.ID)
				messages = append(messages, types.ChatMessage{Role: "user", Content: um.Content})
				if um.ModelName != "" {
					mc.set(um.ServerURL, um.ModelName)
					newReasoningEffort := te.configStore.GetModelReasoningEffort(um.ModelName)
					client = llama.NewLlamaClient(um.ServerURL, um.ModelName, newReasoningEffort)
					providerName = te.configStore.GetProviderNameByURL(um.ServerURL)
				}
				task.StatusState = "running"
				te.store.UpdateTask(task)
				b.Send(types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Processing new message"}})
				continue
			default:
			}
			task.StatusState = "idle"
			te.store.UpdateTask(task)
			te.boardStreamMgr.Broadcast(projectID)
			b.Send(types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]interface{}{
				"state": "idle", "summary": "Agent finished responding",
				"prompt_tokens": promptTokens, "predicted_tokens": predictedTokens,
				"cache_tokens": cacheTokens,
				"prompt_ms": promptMs, "predicted_ms": predictedMs,
				"model_name": activeModel, "duration": duration,
			}})
			return
		}

		// Context compression: check if we're approaching the context window limit
		if contextWindow != nil && lastCompressIdx < len(messages)-1 {
			if shouldCompress(messages, *contextWindow) {
				compressResult, err := compressContext(messages, lastCompressIdx, client, systemPrompt)
				if err != nil {
					log.Printf("[compress] task=%s compression failed: %v", task.ID, err)
				} else {
					messages = compressResult.newMessages
					lastCompressIdx = compressResult.newLastCompressIdx
					// Persist compression checkpoint message
					_ = te.store.CreateTaskMessage(&types.TaskMessage{
						ID:       uuid.New().String(),
						TaskID:   task.ID,
						ThreadID: task.CurrentThreadID,
						Role:     "compression",
						Content:  compressResult.summary,
						CreatedAt: time.Now().UTC(),
					})
					b.Send(types.StreamEvent{Type: "compression", Data: map[string]string{"summary": compressResult.summary}})
					log.Printf("[compress] task=%s compressed context: %d messages -> %d messages", task.ID, compressResult.oldLen, len(messages))
				}
			}
		}
	}
}

// sendAndPersistError sends an error event via WebSocket and persists it to the database.
func (te *TaskExecutor) sendAndPersistError(b *stream.Broadcaster, taskID, message, code string) {
	// Get the task to find its current thread
		task, _ := te.store.GetTaskByTaskID(taskID)
		errMsg := &types.TaskMessage{
			ID: uuid.New().String(), TaskID: taskID, Role: "error",
			Content: fmt.Sprintf("[%s] %s", code, message), CreatedAt: time.Now(),
		}
		if task != nil {
			errMsg.ThreadID = task.CurrentThreadID
		}
	if err := te.store.CreateTaskMessage(errMsg); err != nil {
		log.Printf("[error-persist] task=%s failed to persist error: %v", taskID, err)
	}
	b.Send(types.StreamEvent{Type: types.WSTypeError, Data: map[string]string{"message": message, "code": code}})
}

func (te *TaskExecutor) gitRun(dir string, args ...string) error {
	c := exec.Command("git", args...)
	c.Dir = dir
	_, err := c.CombinedOutput()
	return err
}

func computeStats(usage *types.ChatUsage, messages []types.ChatMessage, assistantText, reasoningContent string, startTime time.Time) (promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs int) {
	if usage != nil {
		return usage.PromptTokens, usage.CompletionTokens, usage.PromptTokensDetails.CachedTokens, int(usage.PromptMs), int(usage.PredictedMs)
	}
	durationMs := int(time.Since(startTime).Milliseconds())
	promptTokens = estimatePromptTokens(messages)
	predictedTokens = estimateTotalTokens(assistantText, reasoningContent)
	cacheTokens = 0
	promptMs = durationMs * 7 / 10
	predictedMs = durationMs * 3 / 10
	return promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs
}

// compressResult holds the result of a context compression operation.
type compressResult struct {
	newMessages       []types.ChatMessage
	newLastCompressIdx int
	summary           string
	oldLen            int
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
func compressContext(messages []types.ChatMessage, lastCompressIdx int, client *llama.LlamaClient, systemPrompt string) (compressResult, error) {
	oldLen := len(messages)

	// Build the summarization request: system prompt + all messages since last compression
	compressFrom := 0
	if lastCompressIdx > 0 {
		compressFrom = lastCompressIdx
	}

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

	// Send non-streaming summarization request
	_, summary, err := client.ChatCompletion(context.Background(), compressMessages)
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
