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
	reasoningEffort := te.configStore.GetModelReasoningEffort(modelName)
	client := llama.NewLlamaClient(serverURL, modelName, reasoningEffort)
	tools := llama.GetToolDefinitions()
	var systemPrompt string
	if task.PlanMode {
		systemPrompt = llama.BuildPlanModeSystemPrompt(worktreePath)
	} else {
		systemPrompt = llama.BuildSystemPrompt(worktreePath)
	}

	taskMsgs, _ := te.store.GetTaskMessages(task.ID)
	messages := []types.ChatMessage{{Role: "system", Content: systemPrompt}}
	for _, m := range taskMsgs {
		switch m.Role {
		case "user", "assistant":
			messages = append(messages, types.ChatMessage{Role: m.Role, Content: m.Content})
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

	var assistantContent strings.Builder
	var reasoningContent strings.Builder
	var pendingToolCalls []types.ChatToolCall
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
				newReasoningEffort := te.configStore.GetModelReasoningEffort(newModel)
				client = llama.NewLlamaClient(newURL, newModel, newReasoningEffort)
				log.Printf("[model-switch] task=%s url=%s model=%s", task.ID, newURL, newModel)
			}
		}

		var streamTokenCount int
		streamStart := time.Time{}
		_, activeModel := mc.get()

		usage, err := client.ChatStream(ctx, messages, tools, func(choice types.ChatChoice) {
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

		if err != nil {
			for i := 0; i < 3; i++ {
				select {
				case <-ctx.Done():
					task.StatusState = "stopped"
					task.Output = "Stopped by user"
					te.store.UpdateTask(task)
					te.boardStreamMgr.Broadcast(projectID)
					return
				case <-time.After(time.Duration(i+1) * 2 * time.Second):
				}
				usage, err = client.ChatStream(ctx, messages, tools, func(choice types.ChatChoice) {
					if choice.Delta.ReasoningContent != nil {
						reasoningContent.WriteString(*choice.Delta.ReasoningContent)
						b.Send(types.StreamEvent{Type: types.WSTypeReasoning, Data: map[string]string{"text": *choice.Delta.ReasoningContent}})
					}
					if choice.Delta.Content != nil {
						b.Send(types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": *choice.Delta.Content}})
					}
				})
				if err == nil {
					break
				}
			}
			if err != nil {
				task.StatusState = "failed"
				task.Output = fmt.Sprintf("Connection failed: %v", err)
				te.store.UpdateTask(task)
				te.boardStreamMgr.Broadcast(projectID)
				te.sendAndPersistError(b, task.ID, err.Error(), "connection_failed")
				return
			}
		}

		assistantText := assistantContent.String()

		type toolResult struct {
			toolCallID string
			output     string
		}
		var toolResults []toolResult

		if len(pendingToolCalls) > 0 {
			for _, tc := range pendingToolCalls {
				if tc.ID == "" || tc.Function.Name == "" {
					log.Printf("[tool] skipping incomplete tool call (id=%q, name=%q)", tc.ID, tc.Function.Name)
					continue
				}
				args := parseToolArgs(tc.Function.Arguments)
				toolStart := time.Now()
				result, success := te.executeTool(tc.Function.Name, args, worktreePath, task)
				toolDuration := time.Since(toolStart).Milliseconds()
				b.Send(types.StreamEvent{Type: types.WSTypeToolCall, Data: map[string]interface{}{"tool_id": tc.ID, "tool_name": tc.Function.Name, "args": args, "raw_args": tc.Function.Arguments}})
				b.Send(types.StreamEvent{Type: types.WSTypeToolResult, Data: map[string]interface{}{"tool_id": tc.ID, "success": success, "output": result, "duration": toolDuration}})
				toolResults = append(toolResults, toolResult{toolCallID: tc.ID, output: result})

				tcJSON, _ := json.Marshal(map[string]interface{}{
					"tool_id":   tc.ID,
					"tool_name": tc.Function.Name,
					"args":      args,
					"raw_args":  tc.Function.Arguments,
				})
				resultJSON, _ := json.Marshal(map[string]interface{}{
					"output":   result,
					"success":  success,
					"duration": toolDuration,
				})
				_ = te.store.CreateTaskMessage(&types.TaskMessage{
					ID:         uuid.New().String(),
					TaskID:     task.ID,
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
					summary := args["summary"].(string)
					if task.AutoCommit {
						te.gitRun(worktreePath, "add", "-A")
						te.gitRun(worktreePath, "commit", "-m", summary)
						task.StatusState = "completed"
						task.Status = "done"
					} else {
						task.StatusState = "awaiting_review"
						task.Status = "review"
					}
					task.Output = summary
					te.store.UpdateTask(task)
					promptTokens, predictedTokens, promptMs, predictedMs := computeStats(usage, messages, assistantText, reasoningContent.String(), startTime)
					b.Send(types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]interface{}{
						"state": task.StatusState, "summary": summary,
						"prompt_tokens": promptTokens, "predicted_tokens": predictedTokens,
						"prompt_ms": promptMs, "predicted_ms": predictedMs,
						"model_name": activeModel, "duration": time.Since(startTime).Milliseconds(),
					}})
					te.boardStreamMgr.Broadcast(projectID)
					return
				}
			}
			msgCountBefore := len(messages)
			messages = append(messages, types.ChatMessage{Role: "assistant", Content: assistantText, ReasoningContent: reasoningContent.String()})
			for _, tr := range toolResults {
				messages = append(messages, types.ChatMessage{Role: "tool", Content: tr.output, ToolCallID: tr.toolCallID})
			}
			promptTokens, predictedTokens, promptMs, predictedMs := computeStats(usage, messages[:msgCountBefore], assistantText, reasoningContent.String(), startTime)
			te.store.CreateTaskMessage(&types.TaskMessage{
				ID: uuid.New().String(), TaskID: task.ID, Role: "assistant",
				Content: assistantText, ReasoningContent: reasoningContent.String(),
				ModelName: activeModel, PromptTokens: promptTokens, PredictedTokens: predictedTokens,
				PromptMs: promptMs, PredictedMs: predictedMs,
				Duration: time.Since(startTime).Milliseconds(), CreatedAt: time.Now().UTC(),
			})
			pendingToolCalls = nil
			assistantContent.Reset()
			reasoningContent.Reset()
		} else if assistantText != "" {
			messages = append(messages, types.ChatMessage{Role: "assistant", Content: assistantText, ReasoningContent: reasoningContent.String()})
			promptTokens, predictedTokens, promptMs, predictedMs := computeStats(usage, messages[:len(messages)-1], assistantText, reasoningContent.String(), startTime)
			duration := time.Since(startTime).Milliseconds()
			te.store.CreateTaskMessage(&types.TaskMessage{
				ID: uuid.New().String(), TaskID: task.ID, Role: "assistant",
				Content: assistantText, ReasoningContent: reasoningContent.String(),
				ModelName: activeModel, PromptTokens: promptTokens, PredictedTokens: predictedTokens,
				PromptMs: promptMs, PredictedMs: predictedMs,
				Duration: duration, CreatedAt: time.Now().UTC(),
			})
			assistantContent.Reset()
			reasoningContent.Reset()
			task.StatusState = "idle"
			te.store.UpdateTask(task)
			te.boardStreamMgr.Broadcast(projectID)
			b.Send(types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]interface{}{
				"state": "idle", "summary": "Agent finished responding",
				"prompt_tokens": promptTokens, "predicted_tokens": predictedTokens,
				"prompt_ms": promptMs, "predicted_ms": predictedMs,
				"model_name": activeModel, "duration": duration,
			}})
			return
		}

		if len(messages) > 100 {
			task.StatusState = "idle"
			task.Output = "Conversation limit reached"
			te.store.UpdateTask(task)
			te.boardStreamMgr.Broadcast(projectID)
			return
		}
	}
}

// sendAndPersistError sends an error event via WebSocket and persists it to the database.
func (te *TaskExecutor) sendAndPersistError(b *stream.Broadcaster, taskID, message, code string) {
	errMsg := &types.TaskMessage{
		ID: uuid.New().String(), TaskID: taskID, Role: "error",
		Content: fmt.Sprintf("[%s] %s", code, message), CreatedAt: time.Now(),
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

func computeStats(usage *types.ChatUsage, messages []types.ChatMessage, assistantText, reasoningContent string, startTime time.Time) (promptTokens, predictedTokens, promptMs, predictedMs int) {
	if usage != nil {
		return usage.PromptTokens, usage.CompletionTokens, usage.PromptMs, usage.PredictedMs
	}
	durationMs := int(time.Since(startTime).Milliseconds())
	promptTokens = estimatePromptTokens(messages)
	predictedTokens = estimateTotalTokens(assistantText, reasoningContent)
	promptMs = durationMs * 7 / 10
	predictedMs = durationMs * 3 / 10
	return promptTokens, predictedTokens, promptMs, predictedMs
}
