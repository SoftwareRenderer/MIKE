package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"kanban/backend/llama"
	"kanban/backend/pi"
	"kanban/backend/sandbox"
	"kanban/backend/stream"
	"kanban/backend/types"

	"github.com/google/uuid"
)

// PiSentinelURL is the sentinel value for llama_server_url when a task uses Pi.
const PiSentinelURL = "pi://agent"

// executeTask runs the AI conversation loop.
func (te *TaskExecutor) executeTask(ctx context.Context, task *types.Task, projectID, projectName, worktreePath, serverURL, modelName string, userMsgChan chan stream.UserMessage, sandboxEnabled bool, networkIsolation bool, baseImage, sandboxDockerfilePath string, memoryLimit int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[panic] task=%s recovered from panic: %v", task.ID, r)
			te.sendAndPersistError(task.ID, fmt.Sprintf("Internal error: %v", r), "panic")
		}
		te.taskMu.Lock()
		delete(te.tasks, task.ID)
		te.taskMu.Unlock()
		// Don't remove the stream here — the broadcaster lives as long as clients are connected.
		// It's removed only when the task is deleted (via RemoveStream in cleanupTask).
		// Sandbox container is also kept alive until task deletion.
	}()

	// Create sandbox if enabled
	var execFn execFunc
	var readFn readFunc
	var writeFn writeFunc
	if sandboxEnabled {
		if te.sandboxMgr == nil || !te.sandboxMgr.IsAvailable() {
			task.StatusState = "failed"
			task.Status = "review"
			task.Output = "Sandbox enabled but gVisor (runsc) is not available. Install gVisor: sudo bash scripts/install-gvisor.sh"
			te.store.UpdateTask(task)
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
			te.sendAndPersistError(task.ID, "gVisor not available", "sandbox_unavailable")
			return
		}
		// Determine image to use: Dockerfile build takes precedence over pre-built image
		imageToUse := ""
		if sandboxDockerfilePath != "" {
			imageToUse = sandbox.ProjectImageName(projectName)
			if !te.sandboxMgr.ImageReadyForProject(projectName) {
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "building_image", "activity": "Building sandbox image from Dockerfile"}})
				if err := te.sandboxMgr.BuildImageWithProgress(projectName, sandboxDockerfilePath, func(line string) {
					te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeBuildProgress, Data: map[string]string{"line": line}})
				}); err != nil {
					task.StatusState = "failed"
					task.Status = "review"
					task.Output = fmt.Sprintf("Failed to build sandbox image: %v", err)
					te.store.UpdateTask(task)
					te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
					te.sendAndPersistError(task.ID, fmt.Sprintf("Image build failed: %v", err), "image_build_failed")
					return
				}
			}
		} else {
			// Pull pre-built image if not available locally
			imageToUse = baseImage
			if imageToUse == "" {
				imageToUse = sandbox.BaseImage
			}
			if !te.sandboxMgr.ImageReady(imageToUse) {
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "pulling_image", "activity": "Pulling " + imageToUse}})
				if err := te.sandboxMgr.PullImageWithProgress(imageToUse, func(line string) {
					te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeBuildProgress, Data: map[string]string{"line": line}})
				}); err != nil {
					task.StatusState = "failed"
					task.Status = "review"
					task.Output = fmt.Sprintf("Failed to pull sandbox image: %v", err)
					te.store.UpdateTask(task)
					te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
					te.sendAndPersistError(task.ID, fmt.Sprintf("Image pull failed: %v", err), "image_pull_failed")
					return
				}
			}
		}
		// Determine if agent config dir should be mounted (only for agent tasks)
		piAgentDir := ""
		if serverURL == PiSentinelURL {
			piAgentDir = te.configStore.GetAgentConfigDir()
		}
		// Reuse existing container if it's already running from a previous executor (lazy-start).
		// Only create a new container and send status if one doesn't exist.
		if !te.sandboxMgr.ContainerRunning(task.ID) {
			if err := te.sandboxMgr.Create(task.ID, worktreePath, imageToUse, networkIsolation, memoryLimit, piAgentDir); err != nil {
				task.StatusState = "failed"
				task.Status = "review"
				task.Output = fmt.Sprintf("Failed to create sandbox: %v", err)
				te.store.UpdateTask(task)
				te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
				te.sendAndPersistError(task.ID, fmt.Sprintf("Sandbox creation failed: %v", err), "sandbox_create_failed")
				return
			}
			log.Printf("[sandbox] task=%s sandbox created with image %s", task.ID, imageToUse)
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Sandbox container started"}})
		} else {
			log.Printf("[sandbox] task=%s reusing existing sandbox container", task.ID)
		}

		// Build sandbox exec function
		execFn = func(command string) (string, error) {
			return te.sandboxMgr.Exec(task.ID, command, nil)
		}
		// Build sandbox read/write functions
		readFn = func(path string) ([]byte, error) {
			out, err := te.sandboxMgr.Exec(task.ID, "cat "+shellQuote(path), nil)
			if err != nil {
				return nil, fmt.Errorf("%v: %s", err, out)
			}
			return []byte(out), nil
		}
		writeFn = func(path, content string) error {
			_, err := te.sandboxMgr.ExecWithInput(task.ID, "mkdir -p "+shellQuote(filepath.Dir(path))+" && cat > "+shellQuote(path), []byte(content), nil)
			if err != nil {
				return fmt.Errorf("write failed: %w", err)
			}
			return nil
		}
	}

	// Check if this task uses Pi agent
	if serverURL == PiSentinelURL {
		log.Printf("[exec] task=%s detected Pi agent (url=%s), calling executeTaskWithPi", task.ID, serverURL)
		te.executeTaskWithPi(ctx, task, projectID, projectName, worktreePath, userMsgChan, sandboxEnabled)
		log.Printf("[exec] task=%s executeTaskWithPi returned", task.ID)
		return
	}

	mc := &modelConfig{serverURL: serverURL, modelName: modelName}
	providerName := te.configStore.GetProviderNameByURL(serverURL)
	reasoningEffort := task.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = te.configStore.GetModelReasoningEffort(modelName)
	}
	apiKey := te.configStore.GetProviderAPIKey(serverURL)
	client := llama.NewLlamaClient(serverURL, modelName, reasoningEffort, apiKey)
	// In plan mode, restrict tools to read-only + create_task + task_complete
	var tools []types.ChatTool
	if task.PlanMode {
		tools = llama.GetPlanModeToolDefinitions()
	} else {
		tools = llama.GetToolDefinitions()
	}
	// In sandbox mode, use the container mount path so the LLM sees
	// the actual working directory inside the sandbox.
	promptPath := worktreePath
	if sandboxEnabled {
		promptPath = "/sandbox/" + filepath.Base(worktreePath)
	}
	var systemPrompt string
	if task.PlanMode {
		systemPrompt = llama.BuildPlanModeSystemPrompt(promptPath)
	} else {
		systemPrompt = llama.BuildSystemPrompt(promptPath)
	}

	taskMsgs, _ := te.store.GetTaskMessages(task.ID, task.CurrentThreadID)
	messages := []types.ChatMessage{{Role: "system", Content: systemPrompt}}
	for _, m := range taskMsgs {
		switch m.Role {
		case "user":
			// Check for image attachments and build multi-modal content
			if m.Attachments != "" {
				var atts []struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				}
				if err := json.Unmarshal([]byte(m.Attachments), &atts); err == nil {
					parts := []types.ContentPart{{Type: "text", Text: m.Content}}
					for _, att := range atts {
						if att.Data != "" {
							mediaType := att.MimeType
							if mediaType == "" {
								mediaType = "image/png"
							}
							base64Data := att.Data
							// Strip data: prefix if present
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
					}
					messages = append(messages, types.ChatMessage{Role: m.Role, ContentParts: parts})
					continue
				}
			}
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
				var resultMap map[string]any
				if err := json.Unmarshal([]byte(m.ToolResult), &resultMap); err == nil {
					output := resultMap["output"].(string)
					toolID := ""
					if m.ToolCalls != "" {
						var tcMap map[string]any
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

	var tracker failureTracker
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
	detector := newLoopDetector()
	startTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			task.StatusState = "stopped"
			task.Status = "review"
			task.Output = "Stopped by user"
			te.store.UpdateTask(task)
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "stopped", "activity": "Task stopped by user"}})
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
			te.sendAndPersistError(task.ID, "Task stopped", "stopped")
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
				newAPIKey := te.configStore.GetProviderAPIKey(newURL)
				client = llama.NewLlamaClient(newURL, newModel, newReasoningEffort, newAPIKey)
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
			detector.reset() // user input breaks any loop pattern
			messages = append(messages, types.ChatMessage{Role: "user", Content: um.Content})
			if um.ModelName != "" {
				mc.set(um.ServerURL, um.ModelName)
				newReasoningEffort := te.configStore.GetModelReasoningEffort(um.ModelName)
				newAPIKey := te.configStore.GetProviderAPIKey(um.ServerURL)
				client = llama.NewLlamaClient(um.ServerURL, um.ModelName, newReasoningEffort, newAPIKey)
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
					te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeQueue, Data: map[string]any{
						"position":               pos,
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
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "stopped", "activity": "Task stopped by user"}})
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
			te.sendAndPersistError(task.ID, "Task stopped while waiting for LLM slot", "stopped")
			return
		}
		close(queueDone)

		// Per-call context — can be cancelled independently to abort the LLM stream.
		callCtx, callCancel := context.WithCancel(ctx)

		streamDone := make(chan struct{})
		var usage *types.ChatUsage
		var streamErr error
		var lastProgressSend time.Time // throttle progress updates to ~1/2s
		var lastPredictedN int         // track predicted tokens for real-time stats events

		go func() {
			usage, streamErr = client.ChatStream(callCtx, messages, tools, func(choice types.ChatChoice) {
				if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
					reasoningContent.WriteString(*choice.Delta.ReasoningContent)
					te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeReasoning, Data: map[string]string{"text": *choice.Delta.ReasoningContent}})
				}
				if choice.Delta.Content != nil && *choice.Delta.Content != "" {
					assistantContent.WriteString(*choice.Delta.Content)
					te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": *choice.Delta.Content}})
					// Real-time phrase repetition detection
					if loopType := detector.checkStreamingText(*choice.Delta.Content); loopType == LoopPhrase {
						log.Printf("[loop] task=%s phrase repetition detected during streaming", task.ID)
						te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Repetitive response detected"}})
					}
				}
				if len(choice.Delta.ToolCalls) > 0 {
					priorLen := len(pendingToolCalls)
					pendingToolCalls = llama.MergeToolCalls(pendingToolCalls, choice.Delta.ToolCalls)
					// Send tool_call event as soon as a new tool call appears in the stream
					// so the frontend shows a spinner during LLM token generation
					for i := priorLen; i < len(pendingToolCalls); i++ {
						tc := pendingToolCalls[i]
						if tc.ID != "" && tc.Function.Name != "" {
							path := extractPathFromArgs(tc.Function.Arguments)
							// During streaming, args may be partial JSON (e.g. just "{").
							// Send raw_args and let the frontend parse when complete.
							te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeToolCall, Data: map[string]any{"tool_id": tc.ID, "tool_name": tc.Function.Name, "tool_display_name": llama.GetToolDisplayName(tc.Function.Name), "args": nil, "raw_args": tc.Function.Arguments, "path": path, "sandbox": sandboxEnabled && isSandboxableTool(tc.Function.Name)}})
						}
					}
				}
				// Throttled progress updates for write_file (1 per 2 seconds)
				if time.Since(lastProgressSend) > 2*time.Second {
					for _, tc := range pendingToolCalls {
						if tc.Function.Name == "write_file" && tc.Function.Arguments != "" {
							te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeToolProgress, Data: map[string]any{"tool_id": tc.ID, "bytes_written": len(tc.Function.Arguments), "path": extractPathFromArgs(tc.Function.Arguments)}})
							lastProgressSend = time.Now()
						}
					}
				}
				// Send stats event when predicted token count changes.
				// LLM client accumulates usage/timings from each SSE frame.
				if usage != nil {
					predictedN := usage.CompletionTokens
					if predictedN != lastPredictedN {
						lastPredictedN = predictedN
						te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStats, Data: map[string]any{
							"model_name":     activeModel,
							"prompt_tokens":  usage.PromptTokens,
							"predicted_tokens": predictedN,
							"predicted_ms":   usage.PredictedMs,
						}})
					}
				}
			})

			// Retry on transient errors (skip if cancelled by user message).
			if streamErr != nil && callCtx.Err() == nil {
				for i := range 3 {
					select {
					case <-callCtx.Done():
						close(streamDone)
						return
					case <-time.After(time.Duration(i+1) * 2 * time.Second):
					}
					usage, streamErr = client.ChatStream(callCtx, messages, tools, func(choice types.ChatChoice) {
						if choice.Delta.ReasoningContent != nil {
							reasoningContent.WriteString(*choice.Delta.ReasoningContent)
							te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeReasoning, Data: map[string]string{"text": *choice.Delta.ReasoningContent}})
						}
						if choice.Delta.Content != nil {
							te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": *choice.Delta.Content}})
						}
						// Send stats event when predicted token count changes (retry path)
						if usage != nil {
							predictedN := usage.CompletionTokens
							if predictedN != lastPredictedN {
								lastPredictedN = predictedN
								te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStats, Data: map[string]any{
									"model_name":     activeModel,
									"prompt_tokens":  usage.PromptTokens,
									"predicted_tokens": predictedN,
									"predicted_ms":   usage.PredictedMs,
								}})
							}
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
			te.gate.Release(serverURL) // Release gate immediately so other tasks can proceed
			<-streamDone               // Then wait for goroutine to finish
			task.StatusState = "stopped"
			task.Status = "review"
			task.Output = "Stopped by user"
			te.store.UpdateTask(task)
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "stopped", "activity": "Task stopped by user"}})
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
			return
		case <-streamDone:
			// Stream completed normally (or all retries exhausted)
		case um := <-userMsgChan:
			// New user message — cancel current LLM call.
			log.Printf("[cancel] task=%s user message received during streaming, cancelling", task.ID)
			detector.reset() // user input breaks any loop pattern
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
			for range 16 {
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
				// Check for image attachments
				if qm.Attachments != "" {
					var atts []struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					}
					if err := json.Unmarshal([]byte(qm.Attachments), &atts); err == nil {
						parts := []types.ContentPart{{Type: "text", Text: qm.Content}}
						for _, att := range atts {
							if att.Data != "" {
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
						}
						messages = append(messages, types.ChatMessage{Role: "user", ContentParts: parts})
						continue
					}
				}
				messages = append(messages, types.ChatMessage{Role: "user", Content: qm.Content})
				if qm.ModelName != "" {
					mc.set(qm.ServerURL, qm.ModelName)
					newReasoningEffort := te.configStore.GetModelReasoningEffort(qm.ModelName)
					newAPIKey := te.configStore.GetProviderAPIKey(qm.ServerURL)
					client = llama.NewLlamaClient(qm.ServerURL, qm.ModelName, newReasoningEffort, newAPIKey)
					providerName = te.configStore.GetProviderNameByURL(qm.ServerURL)
				}
			}

			// Notify frontend that response was cancelled.
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "cancelled", "activity": "Response cancelled, processing new message"}})

			continue
		}
		callCancel()

		if streamErr != nil {
			log.Printf("[connect] task=%s connection failed: %v, retrying...", task.ID, streamErr)
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": fmt.Sprintf("Connection failed: %v, retrying...", streamErr)}})
			time.Sleep(5 * time.Second)
			continue
		}

		// Release gate slot after LLM call (before tool execution).
		te.gate.Release(serverURL)

		assistantText := assistantContent.String()

		type toolResult struct {
			toolCallID string
			output     string
		}
		var toolResults []toolResult
		var turnFailures []turnResult

		if len(pendingToolCalls) > 0 {
			// Persist assistant message BEFORE tool calls so DB ordering matches LLM output order
			msgCountBefore := len(messages)
			promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(usage, messages[:msgCountBefore], assistantText, reasoningContent.String(), startTime)
			assistantCreatedAt := time.Now().UTC()
			assistantMsgID := uuid.New().String()
			te.store.CreateTaskMessage(&types.TaskMessage{
				ID: assistantMsgID, TaskID: task.ID, ThreadID: task.CurrentThreadID, Role: "assistant",
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
				// Send final path update before execution (full args now available)
				if tc.Function.Name == "write_file" {
					path := extractPathFromArgs(tc.Function.Arguments)
					if path == "" {
						if p, ok := args["path"].(string); ok && p != "" {
							path = p
						}
					}
					if path != "" {
						te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeToolProgress, Data: map[string]any{"tool_id": tc.ID, "bytes_written": len(tc.Function.Arguments), "path": path}})
					}
				}
				// tool_call event already sent during streaming, just execute and send result
				log.Printf("[tool] executing %s for task=%s", tc.Function.Name, task.ID)
				toolStart := time.Now()
				te.taskMu.Lock()
				state := te.tasks[task.ID]
				te.taskMu.Unlock()
				result, success := te.executeTool(tc.Function.Name, args, worktreePath, task, state, execFn, readFn, writeFn)
				toolDuration := time.Since(toolStart).Milliseconds()
				isSandboxed := sandboxEnabled && isSandboxableTool(tc.Function.Name)
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeToolResult, Data: map[string]any{"tool_id": tc.ID, "success": success, "output": result, "duration": toolDuration, "args": args, "raw_args": tc.Function.Arguments, "path": extractPathFromArgs(tc.Function.Arguments), "sandbox": isSandboxed}})
				toolResults = append(toolResults, toolResult{toolCallID: tc.ID, output: result})

				tcJSON, _ := json.Marshal(map[string]any{
					"tool_id":           tc.ID,
					"tool_name":         tc.Function.Name,
					"tool_display_name": llama.GetToolDisplayName(tc.Function.Name),
					"args":              args,
					"raw_args":          tc.Function.Arguments,
					"sandbox":           sandboxEnabled && isSandboxableTool(tc.Function.Name),
				})
				resultJSON, _ := json.Marshal(map[string]any{
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

				turnFailures = append(turnFailures, turnResult{
					signature: tc.Function.Name + ":" + tc.Function.Arguments,
					success:   success,
				})

				if tc.Function.Name == "task_complete" {
					// Extract summary from tool args
					if summary, ok := args["summary"].(string); ok && summary != "" {
						task.ChangeSummary = summary
					}
					modelName := activeModel
					if modelName == "" {
						modelName = "unknown model"
					}
					if task.AutoCommit {
						summary := task.ChangeSummary
						if summary == "" {
							// Generate a descriptive label from conversation + diff
							diffStat, err := te.worktreeMgr.GitRunInWorktreeOutput(task.ID, "diff", "--stat", "HEAD")
							if err != nil {
								diffStat = "no diff available"
							}
							summary = te.GenerateCommitLabel(task.ID, diffStat, FormatConversationTurns(ExtractLastTurns(messages, 3)))
						}
						commitMsg := fmt.Sprintf("%s, generated by %s\n\n%s", task.Title, modelName, summary)
						te.gitRun(worktreePath, "add", "-A")
						te.gitRun(worktreePath, "commit", "-m", commitMsg)
						// Merge into origin branch if merge_on_commit is enabled
						if task.MergeOnCommit {
							if err := te.mergeBranch(projectID, task.ID, task); err != nil {
								log.Printf("[auto-commit] task=%s merge failed: %v", task.ID, err)
							}
						}
						task.StatusState = "completed"
						task.Status = "done"
						if te.sandboxMgr != nil {
							_ = te.sandboxMgr.Stop(task.ID)
						}
					} else {
						task.StatusState = "awaiting_review"
						task.Status = "review"
					}
					te.store.UpdateTask(task)
					te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
						"state":                task.StatusState,
						"assistant_message_id": assistantMsgID,
						"prompt_tokens":        promptTokens, "predicted_tokens": predictedTokens,
						"cache_tokens": cacheTokens,
						"prompt_ms":    promptMs, "predicted_ms": predictedMs,
						"model_name": activeModel, "duration": time.Since(startTime).Milliseconds(),
					}})
					te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
					// Broadcast diff so the frontend updates the diff panel
					baseRef, _ := te.worktreeMgr.ResolveDiffBaseline(task.ID, task.BaseRef, task.Branch, "")
					if diffResult, err := te.worktreeMgr.GetTaskDiff(task.ID, baseRef, ""); err == nil {
						hash := stream.ComputeDiffHash(diffResult.Diff, diffResult.Untracked)
						fileHashes := diffResult.GetPerFileHashes()
						untrackedLines := 0
						for _, u := range diffResult.Untracked {
							untrackedLines += len(u.Lines)
						}
						te.appStreamMgr.BroadcastDiffStats(projectID, task.ID, hash, diffResult.ShortStat, len(diffResult.Untracked), untrackedLines, fileHashes, nil)
					}
					// If this is a subtask, auto-start the next sibling
					if task.ParentTaskID != "" {
						_ = te.AutoStartNextSibling(projectID, task.ID, task.ParentTaskID, task.Status)
					}
					return
				}
			}
			// Check consecutive identical tool call failures (turn-level).
			if count := tracker.recordTurn(turnFailures); count >= maxFailures {
				task.StatusState = "failed"
				task.Output = fmt.Sprintf("Max consecutive failed turns (%d) reached", maxFailures)
				te.store.UpdateTask(task)
				te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
				te.sendAndPersistError(task.ID, fmt.Sprintf("Max consecutive failed turns (%d) reached", maxFailures), "max_failures")
				return
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

			// Loop detection: record this turn and check for repetition
			loopType := detector.recordTurn(pendingToolCalls, assistantText)
			if loopType != LoopNone {
				if detector.hintCount >= maxLoopHints {
					task.StatusState = "failed"
					task.Output = "Model stuck in loop (max hints exhausted)"
					te.store.UpdateTask(task)
					te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
					te.sendAndPersistError(task.ID, "Model stuck in loop (max hints exhausted)", "loop_detected")
					return
				}
				// Extract target for hint message
				target := ""
				if loopType == LoopTool && len(pendingToolCalls) > 0 && pendingToolCalls[0].Function.Name != "" {
					target = extractTarget(pendingToolCalls[0].Function.Name, pendingToolCalls[0].Function.Arguments)
				}
				hint := buildHint(loopType, target)
				log.Printf("[loop] task=%s detected loop type=%d (hint %d/%d), target=%s", task.ID, loopType, detector.hintCount+1, maxLoopHints, target)
				messages = append(messages, types.ChatMessage{Role: "system", Content: hint})
				detector.hintCount++
				detector.reset()
				// Notify frontend
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Loop detected, steering agent"}})
			}

			pendingToolCalls = nil
			assistantContent.Reset()
			reasoningContent.Reset()

			// Auto-commit file changes as a checkpoint
			te.createCheckpoint(projectID, task.ID, worktreePath, task.CurrentThreadID, assistantMsgID, messages, sandboxEnabled)

			// Broadcast updated diff stats to board viewers
			thread, _ := te.store.GetThread(task.CurrentThreadID)
			forkBranch := ""
			if thread != nil {
				forkBranch = thread.GitBranch
			}
			baseRef, _ := te.worktreeMgr.ResolveDiffBaseline(task.ID, task.BaseRef, task.Branch, forkBranch)
			if diffResult, err := te.worktreeMgr.GetTaskDiff(task.ID, baseRef, ""); err == nil && (diffResult.Diff != "" || len(diffResult.Untracked) > 0) {
				hash := stream.ComputeDiffHash(diffResult.Diff, diffResult.Untracked)
				fileHashes := diffResult.GetPerFileHashes()
				untrackedLines := 0
				for _, u := range diffResult.Untracked {
					untrackedLines += len(u.Lines)
				}
				te.appStreamMgr.BroadcastDiffStats(projectID, task.ID, hash, diffResult.ShortStat, len(diffResult.Untracked), untrackedLines, fileHashes, nil)
			}
			// Continue to call LLM again with tool results — don't fall through to blocking select
			continue
		} else if assistantText != "" {
			// Text-only response resets the failure tracker
			tracker.reset()

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

			// Loop detection for text-only responses
			loopType := detector.recordTurn(nil, assistantText)
			// Also check for phrase repetition detected during streaming
			if loopType == LoopNone && detector.phraseDetected() {
				loopType = LoopPhrase
			}
			if loopType != LoopNone {
				if detector.hintCount >= maxLoopHints {
					task.StatusState = "failed"
					task.Status = "review"
					task.Output = "Model stuck in loop (max hints exhausted)"
					te.store.UpdateTask(task)
					te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
					te.sendAndPersistError(task.ID, "Model stuck in loop (max hints exhausted)", "loop_detected")
					return
				}
				hint := buildHint(loopType, "")
				log.Printf("[loop] task=%s detected loop type=%d (hint %d/%d)", task.ID, loopType, detector.hintCount+1, maxLoopHints)
				messages = append(messages, types.ChatMessage{Role: "system", Content: hint})
				detector.hintCount++
				detector.reset()
				task.StatusState = "running"
				te.store.UpdateTask(task)
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Loop detected, steering agent"}})
				continue
			}

			// Check for queued user messages before sending complete.
			select {
			case um := <-userMsgChan:
				log.Printf("[user-msg] task=%s received message after response, continuing", task.ID)
				detector.reset() // user input breaks any loop pattern
				messages = append(messages, types.ChatMessage{Role: "user", Content: um.Content})
				if um.ModelName != "" {
					mc.set(um.ServerURL, um.ModelName)
					newReasoningEffort := te.configStore.GetModelReasoningEffort(um.ModelName)
					newAPIKey := te.configStore.GetProviderAPIKey(um.ServerURL)
					client = llama.NewLlamaClient(um.ServerURL, um.ModelName, newReasoningEffort, newAPIKey)
					providerName = te.configStore.GetProviderNameByURL(um.ServerURL)
				}
				task.StatusState = "running"
				te.store.UpdateTask(task)
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Processing new message"}})
				continue
			default:
			}
			task.Status = "review"
			task.StatusState = "idle"
			te.store.UpdateTask(task)
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
			// Broadcast diff so the frontend updates the diff panel
			baseRef, _ := te.worktreeMgr.ResolveDiffBaseline(task.ID, task.BaseRef, task.Branch, "")
			if diffResult, err := te.worktreeMgr.GetTaskDiff(task.ID, baseRef, ""); err == nil {
				hash := stream.ComputeDiffHash(diffResult.Diff, diffResult.Untracked)
				fileHashes := diffResult.GetPerFileHashes()
				untrackedLines := 0
				for _, u := range diffResult.Untracked {
					untrackedLines += len(u.Lines)
				}
				te.appStreamMgr.BroadcastDiffStats(projectID, task.ID, hash, diffResult.ShortStat, len(diffResult.Untracked), untrackedLines, fileHashes, nil)
			}
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
				"state": "idle", "summary": "Agent finished responding",
				"prompt_tokens": promptTokens, "predicted_tokens": predictedTokens,
				"cache_tokens": cacheTokens,
				"prompt_ms":    promptMs, "predicted_ms": predictedMs,
				"model_name": activeModel, "duration": duration,
			}})
			// If this is a subtask, auto-start the next sibling
			if task.ParentTaskID != "" {
				_ = te.AutoStartNextSibling(projectID, task.ID, task.ParentTaskID, task.Status)
			}
		}

		// Context compression: check if we're approaching the context window limit
		if contextWindow != nil && lastCompressIdx < len(messages)-1 {
			if shouldCompress(messages, *contextWindow) {
				compressResult, err := te.compressContext(messages, lastCompressIdx, systemPrompt, serverURL, activeModel)
				if err != nil {
					log.Printf("[compress] task=%s compression failed: %v", task.ID, err)
				} else {
					messages = compressResult.newMessages
					lastCompressIdx = compressResult.newLastCompressIdx
					// Persist compression checkpoint message
					_ = te.store.CreateTaskMessage(&types.TaskMessage{
						ID:        uuid.New().String(),
						TaskID:    task.ID,
						ThreadID:  task.CurrentThreadID,
						Role:      "compression",
						Content:   compressResult.summary,
						CreatedAt: time.Now().UTC(),
					})
					te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: "compression", Data: map[string]string{"summary": compressResult.summary}})
					log.Printf("[compress] task=%s compressed context: %d messages -> %d messages", task.ID, compressResult.oldLen, len(messages))
				}
			}
		}

		// Block waiting for a user message before the next LLM call.
		// Without this, the executor would immediately loop and call the LLM again
		// with the same conversation (text-only response already added to history).
		select {
		case <-ctx.Done():
			task.StatusState = "stopped"
			task.Status = "review"
			task.Output = "Stopped by user"
			te.store.UpdateTask(task)
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "stopped", "activity": "Task stopped by user"}})
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
			te.sendAndPersistError(task.ID, "Task stopped", "stopped")
			return
		case um := <-userMsgChan:
			log.Printf("[user-msg] task=%s received message while idle, continuing", task.ID)
			detector.reset()
			messages = append(messages, types.ChatMessage{Role: "user", Content: um.Content})
			if um.ModelName != "" {
				mc.set(um.ServerURL, um.ModelName)
				newReasoningEffort := te.configStore.GetModelReasoningEffort(um.ModelName)
				newAPIKey := te.configStore.GetProviderAPIKey(um.ServerURL)
				client = llama.NewLlamaClient(um.ServerURL, um.ModelName, newReasoningEffort, newAPIKey)
				providerName = te.configStore.GetProviderNameByURL(um.ServerURL)
			}
			task.StatusState = "running"
			te.store.UpdateTask(task)
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Processing new message"}})
		}
	}
}

// sendAndPersistError sends an error event via WebSocket and persists it to the database.
func (te *TaskExecutor) sendAndPersistError(taskID, message, code string) {
	task, _ := te.store.GetTaskByTaskID(taskID)
	errMsg := &types.TaskMessage{
		ID: uuid.New().String(), TaskID: taskID, Role: "error",
		Content: fmt.Sprintf("[%s] %s", code, message), CreatedAt: time.Now(),
	}
	if task != nil {
		errMsg.ThreadID = task.CurrentThreadID
		te.appStreamMgr.SendTaskEvent(task.ProjectID, taskID, types.StreamEvent{Type: types.WSTypeError, Data: map[string]string{"message": message, "code": code}})
	}
	if err := te.store.CreateTaskMessage(errMsg); err != nil {
		log.Printf("[error-persist] task=%s failed to persist error: %v", taskID, err)
	}
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

// -- Pi Agent Execution --

// executeTaskWithPi runs the task using Pi agent via RPC mode.
// Pi handles its own LLM calls, tool execution, loop detection, and compaction.
// Kanban only manages the subprocess lifecycle and streams events to the frontend.
func (te *TaskExecutor) executeTaskWithPi(ctx context.Context, task *types.Task, projectID, projectName, worktreePath string, userMsgChan chan stream.UserMessage, sandboxEnabled bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[pi-panic] task=%s recovered from panic: %v", task.ID, r)
			te.sendAndPersistError(task.ID, fmt.Sprintf("Internal error: %v", r), "panic")
		}
		te.taskMu.Lock()
		delete(te.tasks, task.ID)
		te.taskMu.Unlock()
	}()

	sessionName := fmt.Sprintf("kanban-%s", task.ID)

	var client *pi.Client
	var err error

	if sandboxEnabled {
		// Pre-flight: check if pi binary exists in the container
		if !te.sandboxMgr.PiAvailable(task.ID) {
			task.StatusState = "failed"
			task.Status = "review"
			task.Output = "Pi Coding Agent binary not found in sandbox container. Ensure the container image includes the `pi` binary."
			te.store.UpdateTask(task)
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
			te.sendAndPersistError(task.ID, "pi binary not found in container", "pi_not_available")
			return
		}

		log.Printf("[pi] task=%s creating client in container (session=%s)", task.ID, sessionName)
		client, err = pi.NewClientInContainer(te.sandboxMgr, task.ID, sessionName)
	} else {
		// Pi runs on the host — point it at its config directory
		cwd := worktreePath
		var env []string
		agentDir := te.configStore.GetAgentConfigDir()
		if agentDir == "" {
			if homeDir, err := os.UserHomeDir(); err == nil {
				agentDir = filepath.Join(homeDir, ".pi", "agent")
			}
		}
		if agentDir != "" {
			env = append(env, "PI_CODING_AGENT_DIR="+agentDir)
		}
		log.Printf("[pi] task=%s creating client (cwd=%s, session=%s)", task.ID, cwd, sessionName)
		client, err = pi.NewClient(cwd, sessionName, env)
	}

	if err != nil {
		task.StatusState = "failed"
		task.Status = "review"
		task.Output = fmt.Sprintf("Failed to start Pi Coding Agent: %v", err)
		te.store.UpdateTask(task)
		te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		te.sendAndPersistError(task.ID, fmt.Sprintf("Pi start failed: %v", err), "pi_start_failed")
		return
	}
	defer client.Close()

	log.Printf("[pi] task=%s client started (pid=%d)", task.ID, client.PID())

	// Discover the active model
	modelCtx, modelCancel := context.WithTimeout(ctx, 5*time.Second)
	modelName := client.QueryModel(modelCtx)
	modelCancel()
	log.Printf("[pi] task=%s model=%s", task.ID, modelName)

	// Get existing messages
	taskMsgs, _ := te.store.GetTaskMessages(task.ID, task.CurrentThreadID)

	// If no messages exist yet, create the initial user message from the task prompt
	if len(taskMsgs) == 0 {
		te.store.CreateTaskMessage(&types.TaskMessage{
			ID:        uuid.New().String(),
			TaskID:    task.ID,
			ThreadID:  task.CurrentThreadID,
			Role:      "user",
			Content:   task.Prompt,
			CreatedAt: time.Now().UTC(),
		})
	}

	te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Pi Coding Agent started"}})

	// Send initial prompt to Pi
	err = client.Prompt(ctx, task.Prompt)
	if err != nil {
		task.StatusState = "failed"
		task.Status = "review"
		task.Output = fmt.Sprintf("Failed to send prompt to Pi: %v", err)
		te.store.UpdateTask(task)
		te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		te.sendAndPersistError(task.ID, fmt.Sprintf("Pi prompt failed: %v", err), "pi_prompt_failed")
		return
	}

	// Validate Pi startup: wait for agent_start event within a timeout.
	// The prompt "response" event only confirms Pi accepted the prompt.
	// agent_start confirms Pi actually began processing (connected to LLM, etc.).
	// Without this, a hung Pi process would leave the UI stuck on "Awaiting LLM..." forever.
	startupTimeout := 30 * time.Second
	startupDeadline := time.Now().Add(startupTimeout)
	startupValidated := false
	for !startupValidated {
		// Check if timeout has elapsed
		if time.Now().After(startupDeadline) {
			log.Printf("[pi] task=%s startup timeout (no agent_start within %v)", task.ID, startupTimeout)
			client.Abort(ctx)
			task.StatusState = "failed"
			task.Status = "review"
			task.Output = "Pi Coding Agent failed to start (timeout waiting for agent)"
			te.store.UpdateTask(task)
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
			te.sendAndPersistError(task.ID, "Pi failed to start within 30s", "pi_startup_timeout")
			return
		}

		remaining := startupDeadline.Sub(time.Now())
		timer := time.NewTimer(remaining)

		select {
		case event, ok := <-client.EventChan():
			timer.Stop()
			if !ok {
				// Channel closed — Pi process exited
				log.Printf("[pi] task=%s event channel closed during startup", task.ID)
				client.Abort(ctx)
				task.StatusState = "failed"
				task.Status = "review"
				task.Output = "Pi Coding Agent process exited unexpectedly"
				te.store.UpdateTask(task)
				te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
				te.sendAndPersistError(task.ID, "Pi process exited", "pi_process_exited")
				return
			}
			log.Printf("[pi] task=%s startup event: %s", task.ID, event.Type)
			if streamEvent := pi.ToStreamEvent(event, sandboxEnabled); streamEvent.Type != "" {
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, streamEvent)
			}
			switch event.Type {
			case "agent_start":
				// Pi has started processing — startup validated
				startupValidated = true
			case "agent_end":
				// Pi ended immediately after prompt — treat as failure
				task.StatusState = "failed"
				task.Status = "review"
				task.Output = "Pi Coding Agent ended immediately after starting"
				te.store.UpdateTask(task)
				te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
				te.sendAndPersistError(task.ID, "Pi ended immediately", "pi_ended_immediately")
				return
			case "response":
				if !event.Success && event.Error != "" && event.Command == "prompt" {
					// Prompt was rejected by Pi
					task.StatusState = "failed"
					task.Status = "review"
					task.Output = fmt.Sprintf("Pi rejected prompt: %s", event.Error)
					te.store.UpdateTask(task)
					te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
					te.sendAndPersistError(task.ID, event.Error, "pi_prompt_rejected")
					return
				}
				// Prompt accepted (response success) — keep waiting for agent_start
			default:
				// Other events (turn_start, message_start, etc.) — keep waiting
			}
		case <-ctx.Done():
			timer.Stop()
			task.StatusState = "stopped"
			task.Status = "review"
			task.Output = "Stopped by user"
			te.store.UpdateTask(task)
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "stopped", "activity": "Task stopped by user"}})
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
			return
		case <-timer.C:
			// Timeout — handled by the check at the top of the loop
		}
	}

	// Event loop
	startTime := time.Now()
	agentEnded := false
	var stopReason string

	for !agentEnded {
		select {
		case <-ctx.Done():
			log.Printf("[pi] task=%s context cancelled", task.ID)
			client.Abort(ctx)
			task.StatusState = "stopped"
			task.Status = "review"
			task.Output = "Stopped by user"
			te.store.UpdateTask(task)
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "stopped", "activity": "Task stopped by user"}})
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
			te.sendAndPersistError(task.ID, "Task stopped", "stopped")
			return

		case event, ok := <-client.EventChan():
			if !ok {
				// Channel closed — process exited
				agentEnded = true
				stopReason = "process_exited"
				break
			}

			// Stream the event to frontend
			if streamEvent := pi.ToStreamEvent(event, sandboxEnabled); streamEvent.Type != "" {
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, streamEvent)
			}

			switch event.Type {
			case "agent_end":
				agentEnded = true
				stopReason = event.StopReason
				log.Printf("[pi] task=%s agent_end stopReason=%s", task.ID, stopReason)

				// Persist assistant messages and tool calls from agent_end.
				// Order must match LLM path: assistant(text+reasoning) then tool_call entries,
				// so the frontend's mergeToolCallsIntoAssistant can merge them correctly.
				// Pi message format:
				//   - Assistant content blocks: {type:"text", text}, {type:"thinking", thinking},
				//     {type:"toolCall", id, name, arguments}
				//   - Tool results: separate messages with role="toolResult", toolCallId, content, isError
				if len(event.Messages) > 0 {
					// Build a map of toolCallId -> tool_result from toolResult messages
					toolResults := make(map[string]map[string]any)
					for _, msg := range event.Messages {
						role, _ := msg["role"].(string)
						if role != "toolResult" {
							continue
						}
						toolCallID, _ := msg["toolCallId"].(string)
						if toolCallID == "" {
							continue
						}
						// Extract content as string
						var resultText string
						if contentVal, ok := msg["content"].(string); ok {
							resultText = contentVal
						} else if contentArr, ok := msg["content"].([]any); ok {
							var parts []string
							for _, item := range contentArr {
								if itemMap, ok := item.(map[string]any); ok {
									if itemMap["type"] == "text" {
										if t, ok := itemMap["text"].(string); ok {
											parts = append(parts, t)
										}
									}
								}
							}
							resultText = strings.Join(parts, "\n")
						}
						isError, _ := msg["isError"].(bool)
						toolResults[toolCallID] = map[string]any{
							"output":  resultText,
							"success": !isError,
						}
					}

					for _, msg := range event.Messages {
						role, _ := msg["role"].(string)
						if role != "assistant" {
							continue
						}

						// Extract text, reasoning, and toolCall from content blocks
						var assistantText, reasoningText string
						var toolCalls []map[string]any
						if content, ok := msg["content"].([]any); ok {
							var texts []string
							for _, c := range content {
								if cm, ok := c.(map[string]any); ok {
									switch cm["type"] {
									case "text":
										if t, ok := cm["text"].(string); ok {
											texts = append(texts, t)
										}
									case "thinking":
										if t, ok := cm["thinking"].(string); ok {
											reasoningText = t
										}
									case "toolCall":
										toolCalls = append(toolCalls, cm)
									}
								}
							}
							assistantText = strings.Join(texts, "\n")
						}

						// Extract usage data if available
						var promptTokens, predictedTokens int
						if usage, ok := msg["usage"].(map[string]any); ok {
							if input, ok := usage["input"].(float64); ok {
								promptTokens = int(input)
							}
							if output, ok := usage["output"].(float64); ok {
								predictedTokens = int(output)
							}
						}

						// Persist assistant message first (even if empty — acts as anchor for tool call merge)
						te.store.CreateTaskMessage(&types.TaskMessage{
							ID:               uuid.New().String(),
							TaskID:           task.ID,
							ThreadID:         task.CurrentThreadID,
							Role:             "assistant",
							Content:          assistantText,
							ReasoningContent: reasoningText,
							ModelName:        "pi",
							ProviderName:     "Pi Coding Agent",
							PromptTokens:     promptTokens,
							PredictedTokens:  predictedTokens,
							Duration:         time.Since(startTime).Milliseconds(),
							CreatedAt:        time.Now().UTC(),
						})

						// Persist tool calls after the assistant message
						// so mergeToolCallsIntoAssistant merges them into the preceding assistant entry.
						for _, tc := range toolCalls {
							toolCallID, _ := tc["id"].(string)
							toolName, _ := tc["name"].(string)
							toolArgs, _ := tc["arguments"].(map[string]any)
							if toolCallID == "" {
								continue
							}

							// Map Pi tool names to Kanban tool names
							kanbanToolName := mapPiToolName(toolName)

							// Build raw_args for display
							rawArgs, _ := json.Marshal(toolArgs)

							// Get matching tool result
							result, hasResult := toolResults[toolCallID]

							// Build tool_call JSON (same format as LLM path)
							tcJSON, _ := json.Marshal(map[string]any{
								"tool_id":           toolCallID,
								"tool_name":         kanbanToolName,
								"tool_display_name": llama.GetToolDisplayName(kanbanToolName),
								"args":              toolArgs,
								"raw_args":          string(rawArgs),
								"sandbox":           sandboxEnabled && isSandboxableTool(kanbanToolName),
							})

							var resultJSON []byte
							if hasResult {
								resultJSON, _ = json.Marshal(result)
							} else {
								resultJSON, _ = json.Marshal(map[string]any{
									"output":  "",
									"success": true,
								})
							}

							te.store.CreateTaskMessage(&types.TaskMessage{
								ID:         uuid.New().String(),
								TaskID:     task.ID,
								ThreadID:   task.CurrentThreadID,
								Role:       "tool_call",
								Content:    "",
								ToolCalls:  string(tcJSON),
								ToolResult: string(resultJSON),
								CreatedAt:  time.Now().UTC(),
							})

							// Send tool_result WS event to finalize pending tool calls on the frontend.
							// If the streaming tool_execution_end was missing/late the frontend
							// would otherwise leave isPending=true forever (stuck spinner).
							resultOutput := ""
							resultSuccess := true
							if hasResult {
								if out, ok := result["output"].(string); ok {
									resultOutput = out
								}
								if suc, ok := result["success"].(bool); ok {
									resultSuccess = suc
								}
							}
							te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeToolResult, Data: map[string]any{
								"tool_id":  toolCallID,
								"success":  resultSuccess,
								"output":   resultOutput,
								"args":     toolArgs,
								"raw_args": string(rawArgs),
								"path":     extractPathFromArgs(string(rawArgs)),
								"sandbox":  sandboxEnabled && isSandboxableTool(kanbanToolName),
							}})
						}
					}
				}

			case "response":
				if !event.Success && event.Error != "" && event.Command == "prompt" {
					log.Printf("[pi] task=%s prompt response error: %s", task.ID, event.Error)
					te.sendAndPersistError(task.ID, event.Error, "pi_rpc_error")
				}
			}

		case um := <-userMsgChan:
			// Forward user message to Pi
			log.Printf("[pi] task=%s forwarding user message (len=%d)", task.ID, len(um.Content))
			err := client.Prompt(ctx, um.Content)
			if err != nil {
				log.Printf("[pi] task=%s failed to forward user message: %v", task.ID, err)
			}
		}
	}

	// Task completion
	duration := time.Since(startTime)
	log.Printf("[pi] task=%s completed stopReason=%s duration=%s", task.ID, stopReason, duration)

	if stopReason == "stop" || stopReason == "length" || stopReason == "process_exited" {
		if task.AutoCommit {
			// Commit changes
			summary := task.ChangeSummary
			if summary == "" {
				summary = task.Title
			}
			commitMsg := fmt.Sprintf("%s, generated by %s\n\n%s", task.Title, client.ModelName(), summary)
			te.gitRun(worktreePath, "add", "-A")
			te.gitRun(worktreePath, "commit", "-m", commitMsg)
			// Merge into origin branch if merge_on_commit is enabled
			if task.MergeOnCommit {
				if err := te.mergeBranch(projectID, task.ID, task); err != nil {
					log.Printf("[auto-commit] task=%s merge failed: %v", task.ID, err)
				}
			}
			task.StatusState = "completed"
			task.Status = "done"
			if te.sandboxMgr != nil {
				_ = te.sandboxMgr.Stop(task.ID)
			}
		} else {
			task.StatusState = "awaiting_review"
			task.Status = "review"
		}
		task.Output = fmt.Sprintf("Pi Coding Agent finished (%s)", stopReason)
		te.store.UpdateTask(task)

		// Broadcast complete event
		te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
			"state":      task.StatusState,
			"summary":    "Pi Coding Agent finished",
			"model_name": client.ModelName(),
			"duration":   duration.Milliseconds(),
		}})

		te.appStreamMgr.BroadcastTaskUpdated(projectID, task)

		// Broadcast diff
		baseRef, _ := te.worktreeMgr.ResolveDiffBaseline(task.ID, task.BaseRef, task.Branch, "")
		if diffResult, err := te.worktreeMgr.GetTaskDiff(task.ID, baseRef, ""); err == nil {
			hash := stream.ComputeDiffHash(diffResult.Diff, diffResult.Untracked)
			fileHashes := diffResult.GetPerFileHashes()
			untrackedLines := 0
			for _, u := range diffResult.Untracked {
				untrackedLines += len(u.Lines)
			}
			te.appStreamMgr.BroadcastDiffStats(projectID, task.ID, hash, diffResult.ShortStat, len(diffResult.Untracked), untrackedLines, fileHashes, nil)
		}

		// Auto-start next sibling if subtask
		if task.ParentTaskID != "" {
			_ = te.AutoStartNextSibling(projectID, task.ID, task.ParentTaskID, task.Status)
		}
	}
}
