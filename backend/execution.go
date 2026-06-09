package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// TaskExecutor handles task execution endpoints and orchestrates AI task execution.
type TaskExecutor struct {
	store          *Store
	configStore    *ConfigStore
	boardStreamMgr *BoardStreamManager
	worktreeMgr    *WorktreeManager
	streamMgr      *StreamManager
	taskMu         sync.Mutex
	tasks          map[string]*executionState
}

type executionState struct {
	ctx         context.Context
	cancel      context.CancelFunc
	userMsgChan chan UserMessage
}

// modelConfig holds mutable model/provider settings for an executor.
type modelConfig struct {
	mu        sync.Mutex
	serverURL string
	modelName string
}

func (mc *modelConfig) get() (string, string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	return mc.serverURL, mc.modelName
}

func (mc *modelConfig) set(serverURL, modelName string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.serverURL = serverURL
	mc.modelName = modelName
}

// sendAndPersistError sends an error event via WebSocket and persists it to the database.
func (te *TaskExecutor) sendAndPersistError(b *broadcaster, taskID, message, code string) {
	// Persist error message to task_messages
	errMsg := &TaskMessage{
		ID:        uuid.New().String(),
		TaskID:    taskID,
		Role:      "error",
		Content:   fmt.Sprintf("[%s] %s", code, message),
		CreatedAt: time.Now(),
	}
	if err := te.store.CreateTaskMessage(errMsg); err != nil {
		log.Printf("[error-persist] task=%s failed to persist error: %v", taskID, err)
	}
	// Send WebSocket event
	b.send(StreamEvent{Type: WSTypeError, Data: map[string]string{"message": message, "code": code}})
}

// parseModelMetadata extracts optional model metadata from a user message.
// Format: JSON line with model_name/llama_server_url followed by \n and the actual content.
func parseModelMetadata(msg string) (content, modelName, serverURL string) {
	idx := strings.Index(msg, "\n")
	if idx <= 0 {
		return msg, "", ""
	}
	head := msg[:idx]
	content = msg[idx+1:]
	var meta map[string]string
	if err := json.Unmarshal([]byte(head), &meta); err != nil {
		// Not a metadata prefix — return the full message as content
		return msg, "", ""
	}
	return content, meta["model_name"], meta["llama_server_url"]
}

// wordRe matches words (sequences of non-whitespace chars) for token estimation.
var wordRe = regexp.MustCompile(`\S+`)

// estimateTokensFromText provides a rough token count from text content.
// Uses ~1.3 tokens per word as a heuristic (typical for English text with GPT-style tokenizers).
func estimateTokensFromText(text string) int {
	words := wordRe.FindAllString(text, -1)
	// ~1.3 tokens per word on average
	return int(float64(len(words)) * 1.3)
}

// estimatePromptTokens estimates total prompt tokens from conversation messages.
func estimatePromptTokens(messages []ChatMessage) int {
	total := 0
	for _, m := range messages {
		total += estimateTokensFromText(m.Content)
		total += estimateTokensFromText(m.ReasoningContent)
	}
	return total
}

// estimateTotalTokens estimates total output tokens from content and reasoning.
func estimateTotalTokens(content, reasoning string) int {
	return estimateTokensFromText(content) + estimateTokensFromText(reasoning)
}

// NewTaskExecutor creates a new TaskExecutor.
func NewTaskExecutor(store *Store, configStore *ConfigStore, boardStreamMgr *BoardStreamManager) *TaskExecutor {
	te := &TaskExecutor{
		store:          store,
		configStore:    configStore,
		boardStreamMgr: boardStreamMgr,
		streamMgr:      NewStreamManager(store),
		tasks:          make(map[string]*executionState),
	}
	te.worktreeMgr = NewWorktreeManager(store, configStore)
	// Wire up lazy-start: when a user message arrives and no executor is running,
	// start one automatically so the agent always responds.
	te.streamMgr.SetLazyStartHandler(te.handleLazyStart)
	return te
}

// HandleStart starts a task's AI execution.
func (te *TaskExecutor) HandleStart(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	log.Printf("[start] task=%s project=%s", taskID, projectID)
	if err := te.StartTask(projectID, taskID); err != nil {
		log.Printf("[start] task=%s error: %v", taskID, err)
		if strings.Contains(err.Error(), "already running") {
			jsonError(w, http.StatusConflict, err.Error())
		} else {
			jsonError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	log.Printf("[start] task=%s started", taskID)
	te.boardStreamMgr.Broadcast(projectID)
	task, _ := te.store.GetTask(projectID, taskID)
	jsonResponse(w, http.StatusOK, task)
}

// HandleStop stops a running task.
func (te *TaskExecutor) HandleStop(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := te.StopTask(taskID); err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	te.boardStreamMgr.Broadcast(projectID)
	task, _ := te.store.GetTask(projectID, taskID)
	jsonResponse(w, http.StatusOK, task)
}

// HandleRetry retries a failed task.
func (te *TaskExecutor) HandleRetry(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := te.RetryTask(projectID, taskID); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	te.boardStreamMgr.Broadcast(projectID)
	task, _ := te.store.GetTask(projectID, taskID)
	jsonResponse(w, http.StatusOK, task)
}

// HandleResume resumes a paused task (same as retry for now).
func (te *TaskExecutor) HandleResume(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	te.HandleRetry(w, r, projectID, taskID)
}

// HandleStream handles WebSocket stream for task output.
func (te *TaskExecutor) HandleStream(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	te.streamMgr.HandleStream(w, r, taskID)
}

// RemoveStream removes and closes the broadcaster for a task. Called during task deletion.
func (te *TaskExecutor) RemoveStream(taskID string) {
	te.streamMgr.Remove(taskID)
}

// ResetForTest stops all running executors and clears state. Used by e2e tests.
func (te *TaskExecutor) ResetForTest() {
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	for taskID, state := range te.tasks {
		state.cancel()
		delete(te.tasks, taskID)
	}
	te.streamMgr.ResetForTest()
}

// handleLazyStart is called when a user message arrives for a task with no active executor.
// modelName and serverURL are optional overrides from the user's model selection.
func (te *TaskExecutor) handleLazyStart(projectID, taskID, content, modelName, serverURL string) {
	log.Printf("[lazy-start] task=%s project=%s (override: model=%s, url=%s)", taskID, projectID, modelName, serverURL)

	te.taskMu.Lock()
	if _, exists := te.tasks[taskID]; exists {
		te.taskMu.Unlock()
		return
	}
	te.taskMu.Unlock()

	task, err := te.store.GetTaskByTaskID(taskID)
	if err != nil {
		log.Printf("[lazy-start] task=%s get error: %v", taskID, err)
		return
	}
	if task.StatusState == "completed" {
		return
	}

	// Update task status to running
	task.StatusState = "running"
	task.Status = "in_progress"
	_ = te.store.UpdateTask(task)
	te.boardStreamMgr.Broadcast(projectID)

	// Resolution: message override > task override
	if serverURL == "" {
		serverURL = task.LlamaServerURL
	}
	if serverURL == "" {
		return
	}

	if modelName == "" {
		modelName = task.ModelName
	}
	log.Printf("[lazy-start] task=%s url=%s model=%s", taskID, serverURL, modelName)

	branchName := fmt.Sprintf("task/%s", task.ID)

	wtPath, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branchName)
	if err != nil {
		log.Printf("[lazy-start] task=%s worktree error: %v", taskID, err)
		return
	}
	log.Printf("[lazy-start] task=%s worktree=%s", taskID, wtPath)

	b := te.streamMgr.GetOrCreate(taskID)
	b.send(StreamEvent{Type: WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Agent responding"}})

	ctx, cancel := context.WithCancel(context.Background())
	userMsgChan := make(chan UserMessage, 16)
	te.taskMu.Lock()
	te.tasks[taskID] = &executionState{ctx: ctx, cancel: cancel, userMsgChan: userMsgChan}
	te.taskMu.Unlock()

	b.SetUserMessageHandler(func(um UserMessage) {
		select {
		case userMsgChan <- um:
		default:
		}
	})

	log.Printf("[lazy-start] task=%s starting executor", taskID)
	go te.executeTaskWithInitialMessage(ctx, task, projectID, wtPath, serverURL, modelName, b, userMsgChan, content)
}

// executeTaskWithInitialMessage starts the executor for a lazy-start scenario.
// The initial message is already persisted in task_messages by the WebSocket handler,
// so executeTask will load it from there. No need to modify task.Prompt.
func (te *TaskExecutor) executeTaskWithInitialMessage(ctx context.Context, task *Task, projectID, worktreePath, serverURL, modelName string, b *broadcaster, userMsgChan chan UserMessage, initialMessage string) {
	te.executeTask(ctx, task, projectID, worktreePath, serverURL, modelName, b, userMsgChan)
}

// StartTask begins AI execution for a task.
func (te *TaskExecutor) StartTask(projectID, taskID string) error {
	task, err := te.store.GetTask(projectID, taskID)
	if err != nil {
		return err
	}
	if task.StatusState == "running" {
		te.taskMu.Lock()
		_, activelyRunning := te.tasks[taskID]
		te.taskMu.Unlock()
		if !activelyRunning {
			// Stale state — server likely crashed while task was running.
			// Reset to idle so the user can retry.
			log.Printf("[start] task=%s stale running state detected, resetting to idle", taskID)
			task.StatusState = "idle"
			task.Status = "todo"
			if err := te.store.UpdateTask(task); err != nil {
				return fmt.Errorf("reset stale state: %w", err)
			}
		} else {
			return fmt.Errorf("task already running")
		}
	}

	serverURL := task.LlamaServerURL
	if serverURL == "" {
		return fmt.Errorf("llama server URL not configured")
	}

	modelName := task.ModelName
	branchName := fmt.Sprintf("task/%s", task.ID)

	worktreePath, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branchName)
	if err != nil {
		return fmt.Errorf("create worktree: %w", err)
	}

	task.Branch = branchName
	task.StatusState = "running"
	task.Status = "in_progress"
	if err := te.store.UpdateTask(task); err != nil {
		return err
	}

	// Persist the initial prompt as a user message so it appears in chat history.
	// Only persist if there are no existing messages (avoids duplicates on retry).
	existingMsgs, _ := te.store.GetTaskMessages(taskID)
	if len(existingMsgs) == 0 {
		_ = te.store.CreateTaskMessage(&TaskMessage{
			ID:        generateUUID(),
			TaskID:    taskID,
			Role:      "user",
			Content:   task.Prompt,
			CreatedAt: time.Now().UTC(),
		})
	}

	b := te.streamMgr.GetOrCreate(taskID)
	b.send(StreamEvent{Type: WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Starting task execution"}})

	ctx, cancel := context.WithCancel(context.Background())
	userMsgChan := make(chan UserMessage, 16)
	te.taskMu.Lock()
	te.tasks[taskID] = &executionState{ctx: ctx, cancel: cancel, userMsgChan: userMsgChan}
	te.taskMu.Unlock()

	b.SetUserMessageHandler(func(um UserMessage) {
		select {
		case userMsgChan <- um:
		default:
		}
	})

	go te.executeTask(ctx, task, projectID, worktreePath, serverURL, modelName, b, userMsgChan)
	return nil
}

// StopTask cancels a running task.
func (te *TaskExecutor) StopTask(taskID string) error {
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	state, ok := te.tasks[taskID]
	if !ok {
		return fmt.Errorf("task not running")
	}
	state.cancel()
	delete(te.tasks, taskID)
	return nil
}

// RetryTask retries a failed task.
func (te *TaskExecutor) RetryTask(projectID, taskID string) error {
	task, err := te.store.GetTask(projectID, taskID)
	if err != nil {
		return err
	}
	task.StatusState = "idle"
	te.store.UpdateTask(task)
	return te.StartTask(projectID, taskID)
}

// executeTask runs the AI conversation loop.
// Supports mid-conversation model/provider switching via modelConfig.
func (te *TaskExecutor) executeTask(ctx context.Context, task *Task, projectID, worktreePath, serverURL, modelName string, b *broadcaster, userMsgChan chan UserMessage) {
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
	client := NewLlamaClient(serverURL, modelName, reasoningEffort)
	tools := GetToolDefinitions()
	var systemPrompt string
	if task.PlanMode {
		systemPrompt = BuildPlanModeSystemPrompt(worktreePath)
	} else {
		systemPrompt = BuildSystemPrompt(worktreePath)
	}

	// Load conversation from task_messages (deletion-aware, replaces conversation_history).
	// task_messages already includes the initial prompt and all follow-ups as separate entries.
	taskMsgs, _ := te.store.GetTaskMessages(task.ID)
	messages := []ChatMessage{{Role: "system", Content: systemPrompt}}
	for _, m := range taskMsgs {
		switch m.Role {
		case "user", "assistant":
			messages = append(messages, ChatMessage{Role: m.Role, Content: m.Content})
		case "tool_call":
			// Map tool_call entry to a tool result message for LLM context.
			// Parse tool_calls JSON to get tool_id, tool_result JSON to get output.
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
					messages = append(messages, ChatMessage{Role: "tool", Content: output, ToolCallID: toolID})
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
	var pendingToolCalls []ChatToolCall
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
		// Re-read task from store to pick up any updates (e.g., from model selector).
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
				client = NewLlamaClient(newURL, newModel, newReasoningEffort)
				log.Printf("[model-switch] task=%s url=%s model=%s", task.ID, newURL, newModel)
			}
		}

		// Real-time stats tracking
		var streamTokenCount int
		streamStart := time.Time{}

		// Capture current model for this iteration (may have changed above)
		_, activeModel := mc.get()

		usage, err := client.ChatStream(ctx, messages, tools, func(choice ChatChoice) {
			if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
				reasoningContent.WriteString(*choice.Delta.ReasoningContent)
				b.send(StreamEvent{Type: WSTypeReasoning, Data: map[string]string{"text": *choice.Delta.ReasoningContent}})
				streamTokenCount++
				if streamStart.IsZero() {
					streamStart = time.Now()
				}
				// Send real-time stats every 10 tokens (including reasoning)
				if streamTokenCount%10 == 0 {
					elapsed := time.Since(streamStart).Milliseconds()
					b.send(StreamEvent{Type: WSTypeStats, Data: map[string]interface{}{
						"model_name":       activeModel,
						"predicted_tokens": streamTokenCount,
						"predicted_ms":     int(elapsed),
					}})
				}
			}
			if choice.Delta.Content != nil && *choice.Delta.Content != "" {
				assistantContent.WriteString(*choice.Delta.Content)
				b.send(StreamEvent{Type: WSTypeToken, Data: map[string]string{"text": *choice.Delta.Content}})
				streamTokenCount++
				if streamStart.IsZero() {
					streamStart = time.Now()
				}
				// Send real-time stats every 10 tokens
				if streamTokenCount%10 == 0 {
					elapsed := time.Since(streamStart).Milliseconds()
					b.send(StreamEvent{Type: WSTypeStats, Data: map[string]interface{}{
						"model_name":       activeModel,
						"predicted_tokens": streamTokenCount,
						"predicted_ms":     int(elapsed),
					}})
				}
			}
			if len(choice.Delta.ToolCalls) > 0 {
				pendingToolCalls = MergeToolCalls(pendingToolCalls, choice.Delta.ToolCalls)
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
				usage, err = client.ChatStream(ctx, messages, tools, func(choice ChatChoice) {
					if choice.Delta.ReasoningContent != nil {
						reasoningContent.WriteString(*choice.Delta.ReasoningContent)
						b.send(StreamEvent{Type: WSTypeReasoning, Data: map[string]string{"text": *choice.Delta.ReasoningContent}})
					}
					if choice.Delta.Content != nil {
						b.send(StreamEvent{Type: WSTypeToken, Data: map[string]string{"text": *choice.Delta.Content}})
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

		// toolResult holds the output of a single tool execution for feedback to the LLM.
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
				b.send(StreamEvent{Type: WSTypeToolCall, Data: map[string]interface{}{"tool_id": tc.ID, "tool_name": tc.Function.Name, "args": args, "raw_args": tc.Function.Arguments}})
				b.send(StreamEvent{Type: WSTypeToolResult, Data: map[string]interface{}{"tool_id": tc.ID, "success": success, "output": result, "duration": toolDuration}})
				// Collect result for feedback to the LLM
				toolResults = append(toolResults, toolResult{toolCallID: tc.ID, output: result})
				// Persist tool call as a separate message so it survives page refresh
				tcJSON, _ := json.Marshal(map[string]interface{}{
					"tool_id":   tc.ID,
					"tool_name": tc.Function.Name,
					"args":      args,
					"raw_args":  tc.Function.Arguments,
				})
				resultJSON, _ := json.Marshal(map[string]interface{}{
					"output":  result,
					"success": success,
					"duration": toolDuration,
				})
				_ = te.store.CreateTaskMessage(&TaskMessage{
					ID:         generateUUID(),
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
					promptTokens, predictedTokens, promptMs, predictedMs := 0, 0, 0, 0
					if usage != nil {
						promptTokens = usage.PromptTokens
						predictedTokens = usage.CompletionTokens
						promptMs = usage.PromptMs
						predictedMs = usage.PredictedMs
					} else {
						promptTokens = estimatePromptTokens(messages)
						predictedTokens = estimateTotalTokens(assistantText, reasoningContent.String())
						promptMs = int(time.Since(startTime).Milliseconds() * 7 / 10)
						predictedMs = int(time.Since(startTime).Milliseconds() * 3 / 10)
					}
					b.send(StreamEvent{Type: WSTypeComplete, Data: map[string]interface{}{"state": task.StatusState, "summary": summary, "prompt_tokens": promptTokens, "predicted_tokens": predictedTokens, "prompt_ms": promptMs, "predicted_ms": predictedMs, "model_name": activeModel, "duration": time.Since(startTime).Milliseconds()}})
					te.boardStreamMgr.Broadcast(projectID)
					return
				}
			}
			msgCountBefore := len(messages)
			messages = append(messages, ChatMessage{Role: "assistant", Content: assistantText, ReasoningContent: reasoningContent.String()})
			// Feed back each tool result with its tool_call_id so the LLM can see the actual output
			for _, tr := range toolResults {
				messages = append(messages, ChatMessage{Role: "tool", Content: tr.output, ToolCallID: tr.toolCallID})
			}
			// Compute stats from usage (fall back to estimation if LLM didn't provide usage)
			promptTokens, predictedTokens, promptMs, predictedMs := 0, 0, 0, 0
			durationMs := time.Since(startTime).Milliseconds()
			if usage != nil {
				promptTokens, predictedTokens = usage.PromptTokens, usage.CompletionTokens
				promptMs, predictedMs = usage.PromptMs, usage.PredictedMs
			} else {
				promptTokens = estimatePromptTokens(messages[:msgCountBefore])
				predictedTokens = estimateTotalTokens(assistantText, reasoningContent.String())
				// Estimate timing: assume ~70% of time is prompt processing, ~30% is generation
				promptMs = int(durationMs * 7 / 10)
				predictedMs = int(durationMs * 3 / 10)
			}
			// Persist assistant message with stats
			te.store.CreateTaskMessage(&TaskMessage{
				ID:               generateUUID(),
				TaskID:           task.ID,
				Role:             "assistant",
				Content:          assistantText,
				ReasoningContent: reasoningContent.String(),
				ModelName:        activeModel,
				PromptTokens:     promptTokens,
				PredictedTokens:  predictedTokens,
				PromptMs:         promptMs,
				PredictedMs:      predictedMs,
				Duration:         time.Since(startTime).Milliseconds(),
				CreatedAt:        time.Now().UTC(),
			})
			pendingToolCalls = nil
			assistantContent.Reset()
			reasoningContent.Reset()
		} else if assistantText != "" {
			messages = append(messages, ChatMessage{Role: "assistant", Content: assistantText, ReasoningContent: reasoningContent.String()})
			// Compute stats from usage (fall back to estimation if LLM didn't provide usage)
			promptTokens, predictedTokens, promptMs, predictedMs := 0, 0, 0, 0
			if usage != nil {
				promptTokens, predictedTokens = usage.PromptTokens, usage.CompletionTokens
				promptMs, predictedMs = usage.PromptMs, usage.PredictedMs
			} else {
				promptTokens = estimatePromptTokens(messages[:len(messages)-1])
				predictedTokens = estimateTotalTokens(assistantText, reasoningContent.String())
				promptMs = int(time.Since(startTime).Milliseconds() * 7 / 10)
				predictedMs = int(time.Since(startTime).Milliseconds() * 3 / 10)
			}
			duration := time.Since(startTime).Milliseconds()
			// Persist assistant message with stats
			te.store.CreateTaskMessage(&TaskMessage{
				ID:               generateUUID(),
				TaskID:           task.ID,
				Role:             "assistant",
				Content:          assistantText,
				ReasoningContent: reasoningContent.String(),
				ModelName:        activeModel,
				PromptTokens:     promptTokens,
				PredictedTokens:  predictedTokens,
				PromptMs:         promptMs,
				PredictedMs:      predictedMs,
				Duration:         duration,
				CreatedAt:        time.Now().UTC(),
			})
			assistantContent.Reset()
			reasoningContent.Reset()
			// LLM responded with text but no tools - signal completion so UI stops streaming
			task.StatusState = "idle"
			te.store.UpdateTask(task)
			te.boardStreamMgr.Broadcast(projectID)
			b.send(StreamEvent{Type: WSTypeComplete, Data: map[string]interface{}{"state": "idle", "summary": "Agent finished responding", "prompt_tokens": promptTokens, "predicted_tokens": predictedTokens, "prompt_ms": promptMs, "predicted_ms": predictedMs, "model_name": activeModel, "duration": duration}})
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

func (te *TaskExecutor) executeTool(name string, args map[string]interface{}, worktreePath string, task *Task) (string, bool) {
	switch name {
	case "read_file":
		path, _ := args["path"].(string)
		fullPath := filepath.Join(worktreePath, filepath.Clean("/"+path))
		if !strings.HasPrefix(fullPath, worktreePath) {
			return "Error: path escape", false
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		return string(data), true
	case "write_file":
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		fullPath := filepath.Join(worktreePath, filepath.Clean("/"+path))
		if !strings.HasPrefix(fullPath, worktreePath) {
			return "Error: path escape", false
		}
		os.MkdirAll(filepath.Dir(fullPath), 0o755)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		return fmt.Sprintf("Wrote %d bytes", len(content)), true
	case "execute_command":
		cmd, _ := args["command"].(string)
		if isDangerousCommand(cmd) {
			return "Error: command blocked", false
		}
		timeout := int64(5)
		if t, ok := args["timeout"].(float64); ok {
			timeout = int64(t)
		}
		if timeout > 300 {
			timeout = 300
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, "bash", "-c", cmd)
		c.Dir = worktreePath
		out, err := c.CombinedOutput()
		if err != nil {
			return fmt.Sprintf("Failed: %v\n%s", err, out), false
		}
		return string(out), true
	case "task_complete":
		return "Task complete", true
	default:
		return fmt.Sprintf("Unknown tool: %s", name), false
	}
}

func (te *TaskExecutor) gitRun(dir string, args ...string) error {
	c := exec.Command("git", args...)
	c.Dir = dir
	_, err := c.CombinedOutput()
	return err
}

func parseToolArgs(s string) map[string]interface{} {
	result := make(map[string]interface{})
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" {
		return result
	}
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		// Some LLMs send arguments as a JSON-encoded string (double-encoded).
		// e.g. "{\"command\":\"ls\"}" instead of {"command":"ls"}
		var innerStr string
		if err2 := json.Unmarshal([]byte(s), &innerStr); err2 == nil && innerStr != "" {
			if err3 := json.Unmarshal([]byte(innerStr), &result); err3 == nil {
				return result
			}
		}
		// Some LLMs prepend an extra '{' to the arguments.
		// e.g. '{{"command":"ls"}' instead of {"command":"ls"}
		if len(s) > 1 && s[0] == '{' && s[1] == '{' {
			trimmed := s[1:]
			if err4 := json.Unmarshal([]byte(trimmed), &result); err4 == nil {
				return result
			}
		}
		log.Printf("[tool] failed to parse args: %v (raw: %q)", err, s)
		return map[string]interface{}{"raw": s}
	}
	return result
}

func isDangerousCommand(cmd string) bool {
	dangerous := []string{"sudo", "mkfs", "dd if=/dev/", ":(){ :|: };:", "chmod -R 777 /"}
	cmd = strings.TrimSpace(cmd)
	for _, d := range dangerous {
		if strings.Contains(cmd, d) {
			return true
		}
	}
	return strings.HasPrefix(cmd, "rm -rf /") || strings.HasPrefix(cmd, "rm -rf /*")
}