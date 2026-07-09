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

	"kanban/backend/git"
	"kanban/backend/llama"
	"kanban/backend/pi"
	"kanban/backend/prompts"
	"kanban/backend/stream"
	"kanban/backend/types"

	"github.com/google/uuid"
)

// executeTaskWithPi runs the task using Pi agent via RPC mode.
// Pi handles its own LLM calls, tool execution, loop detection, and compaction.
// Kanban only manages the subprocess lifecycle and streams events to the frontend.
func (te *TaskExecutor) executeTaskWithPi(ctx context.Context, task *types.Task, projectID, projectName, worktreePath string, userMsgChan chan stream.UserMessage, summarizeCh chan chan error, sandboxEnabled bool, initialContent string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[pi-panic] task=%s recovered from panic: %v", task.ID, r)
			te.sendAndPersistError(task.ID, fmt.Sprintf("Internal error: %v", r), "panic")
		}
		// Only remove from tasks map if we're still the active executor
		// (prevents old executor from deleting a replacement during type switch)
		te.taskMu.Lock()
		if state, exists := te.tasks[task.ID]; exists && state.ctx == ctx {
			delete(te.tasks, task.ID)
		}
		te.taskMu.Unlock()
	}()

	sessionName := fmt.Sprintf("kanban-%s", task.ID)

	var client *pi.Client
	var err error

	if sandboxEnabled {
		// Pre-flight: check if pi binary exists in the container
		if !te.sandboxMgr.PiAvailable(task.ID) {
			task.ExecutionState = "failed"
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
		task.ExecutionState = "failed"
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

	// Send initial prompt to Pi.
	// For lazy-start (executor switch), use the user's message. For normal start, use task prompt.
	initialPrompt := task.Prompt
	if initialContent != "" {
		initialPrompt = initialContent
		log.Printf("[pi] task=%s lazy-start: sending user message as initial prompt", task.ID)
	}
	err = client.Prompt(ctx, initialPrompt)
	if err != nil {
		task.ExecutionState = "failed"
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
			task.ExecutionState = "failed"
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
				task.ExecutionState = "failed"
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
				task.ExecutionState = "failed"
				task.Status = "review"
				task.Output = "Pi Coding Agent ended immediately after starting"
				te.store.UpdateTask(task)
				te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
				te.sendAndPersistError(task.ID, "Pi ended immediately", "pi_ended_immediately")
				return
			case "response":
				if !event.Success && event.Error != "" && event.Command == "prompt" {
					// Prompt was rejected by Pi
					task.ExecutionState = "failed"
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
			task.ExecutionState = "stopped"
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
	var totalPromptTokens, totalPredictedTokens int // accumulated across all assistant messages

	for !agentEnded {
		select {
		case <-ctx.Done():
			log.Printf("[pi] task=%s context cancelled", task.ID)
			client.Abort(ctx)
			task.ExecutionState = "stopped"
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
						totalPromptTokens += promptTokens
						totalPredictedTokens += predictedTokens

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
							kanbanToolName := pi.MapPiToolName(toolName)

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

							// Send tool_call WS event so the frontend creates the tool call entry.
							// Pi may not send streaming tool_execution_start events, so the frontend
							// might not have a pending entry to update. The frontend's dedup logic
							// (by tool_id) will skip duplicates if streaming already created it.
							te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeToolCall, Data: map[string]any{
								"tool_id":           toolCallID,
								"tool_name":         kanbanToolName,
								"tool_display_name": llama.GetToolDisplayName(kanbanToolName),
								"args":              toolArgs,
								"raw_args":          string(rawArgs),
								"sandbox":           sandboxEnabled && isSandboxableTool(kanbanToolName),
							}})

							// Send tool_result WS event to finalize the tool call on the frontend.
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

				// Check for queued user messages before ending.
				// If a follow-up was sent while Pi was processing, forward it
				// with steer behavior to start the next turn.
				select {
				case um := <-userMsgChan:
					log.Printf("[pi] task=%s agent_end: forwarding queued user message for next turn", task.ID)
					err := client.Prompt(ctx, um.Content, "steer")
					if err != nil {
						log.Printf("[pi] task=%s failed to forward queued message: %v", task.ID, err)
					}
					// Don't set agentEnded — continue the event loop for the next turn
				default:
					// No queued messages — check if auto-commit/merge is needed.
					// Perform merge inside the event loop so Pi can resolve conflicts with tools.
					if task.AutoCommit {
						// If task has a merge conflict, Pi resolved it by editing files.
						// Backend commits the resolution and continues the merge.
						if task.HasMergeConflict {
							log.Printf("[pi] task=%s Pi finished resolving conflicts, committing resolution", task.ID)
							// Stage only the conflicted files (not all changes — user may have unstaged files)
							conflictedFiles := GetResolvedConflictedFiles(worktreePath)
							commitOK := true
							if len(conflictedFiles) > 0 {
								addArgs := append([]string{"add"}, conflictedFiles...)
								if err := te.gitRun(worktreePath, addArgs...); err != nil {
									log.Printf("[pi] task=%s git add failed: %v", task.ID, err)
									commitOK = false
								}
							}
							if commitOK {
								if err := te.gitRun(worktreePath, append(git.GitAuthorFlags(), "commit", "-m", "Resolve merge conflicts")...); err != nil {
									log.Printf("[pi] task=%s git commit failed: %v", task.ID, err)
									commitOK = false
								}
							}
							if commitOK {
								log.Printf("[pi] task=%s conflict resolution committed", task.ID)
								// Restore user's unstaged changes (stashed during sync merge)
								te.worktreeMgr.GetProjectStashMu(projectID).Lock()
								_ = te.worktreeMgr.GitRunInDir(worktreePath, "stash", "pop")
								te.worktreeMgr.GetProjectStashMu(projectID).Unlock()
							}
							_ = te.store.SetTaskMergeConflict(task.ID, projectID, false)
							task.ExecutionState = "idle"
							_ = te.store.UpdateTaskExecutionState(task.ID, projectID, "idle")
						}
						commitMsg := te.GenerateCommitMessageForTask(task.ID, task)
						te.CommitAndMerge(projectID, task.ID, worktreePath, commitMsg, task.AutoMerge)
						// Reload task — CommitAndMerge may have set merge conflict flag
						task, _ = te.store.GetTaskByTaskID(task.ID)
						if task.HasMergeConflict {
							// Merge conflict — prompt Pi to resolve with tools
							conflictedFiles := GetConflictedFiles(worktreePath)
							prompt, pErr := prompts.RenderMergeConflictPrompt(prompts.MergeConflictData{
								TaskBranch:      task.Branch,
								TargetBranch:    task.BaseRef,
								TaskTitle:       task.Title,
								TaskDescription: task.Prompt,
								ConflictedFiles: conflictedFiles,
							})
							if pErr != nil {
								log.Printf("[pi] task=%s failed to render merge conflict prompt: %v", task.ID, pErr)
							}
							log.Printf("[pi] task=%s merge conflict, prompting Pi to resolve", task.ID)
							err := client.Prompt(ctx, prompt)
							if err != nil {
								log.Printf("[pi] task=%s failed to send merge conflict prompt: %v", task.ID, err)
								agentEnded = true
							}
							// Don't set agentEnded — continue the event loop for Pi to resolve
							break
						}
						task.ExecutionState = "completed"
						task.Status = "done"
						if te.sandboxMgr != nil {
							_ = te.sandboxMgr.Stop(task.ID)
						}
						task.Output = fmt.Sprintf("Pi Coding Agent finished (%s)", stopReason)
						te.store.UpdateTask(task)
						agentEnded = true
					} else if task.HasMergeConflict {
						// Task has a merge conflict (e.g., from HandleCommitAndDone).
						// Pi resolved conflicts — commit resolution, finish sync merge,
						// then attempt the final merge.
						commitOK := true
						if isMergeInProgress(worktreePath) {
							// Sync-merge conflict: worktree has MERGE_HEAD with conflict markers.
							// Stage the resolved conflicted files.
							conflictedFiles := GetResolvedConflictedFiles(worktreePath)
							if len(conflictedFiles) > 0 {
								addArgs := append([]string{"add"}, conflictedFiles...)
								if err := te.gitRun(worktreePath, addArgs...); err != nil {
									log.Printf("[pi] task=%s git add failed: %v", task.ID, err)
									commitOK = false
								}
							}
						} else {
							// Final-merge conflict: worktree has no MERGE_HEAD.
							// Stage the LLM's file edits (it modified task branch to be merge-compatible).
							checkCmd := exec.Command("git", "diff", "--quiet")
							checkCmd.Dir = worktreePath
							if checkCmd.Run() == nil {
								log.Printf("[pi] task=%s no changes to commit for conflict resolution", task.ID)
								commitOK = false
							}
							if commitOK {
								if err := te.gitRun(worktreePath, "add", "-A"); err != nil {
									log.Printf("[pi] task=%s git add -A failed: %v", task.ID, err)
									commitOK = false
								}
							}
						}
						if commitOK {
							if err := te.gitRun(worktreePath, append(git.GitAuthorFlags(), "commit", "-m", "Resolve merge conflicts")...); err != nil {
								log.Printf("[pi] task=%s git commit failed: %v", task.ID, err)
								commitOK = false
							}
						}
						if commitOK {
							log.Printf("[pi] task=%s conflict resolution committed", task.ID)
							// Restore user's unstaged changes (stashed during sync merge)
							te.worktreeMgr.GetProjectStashMu(projectID).Lock()
							_ = te.worktreeMgr.GitRunInDir(worktreePath, "stash", "pop")
							te.worktreeMgr.GetProjectStashMu(projectID).Unlock()
						}
						_ = te.store.SetTaskMergeConflict(task.ID, projectID, false)

						if commitOK {
							if mergeErr := te.MergeBranch(projectID, task.ID, task); mergeErr != nil {
								log.Printf("[pi] task=%s final merge failed: %v", task.ID, mergeErr)
								task, _ = te.store.GetTaskByTaskID(task.ID)
							}
						}

						task, _ = te.store.GetTaskByTaskID(task.ID)
						if task.HasMergeConflict {
							log.Printf("[pi] task=%s merge still conflicted after agent ended", task.ID)
							task.ExecutionState = "failed"
							task.Status = "review"
							if te.sandboxMgr != nil {
								_ = te.sandboxMgr.Stop(task.ID)
							}
							task.Output = fmt.Sprintf("Pi Coding Agent finished but merge still conflicted (%s)", stopReason)
						} else {
							task.ExecutionState = "completed"
							task.Status = "done"
							if te.sandboxMgr != nil {
								_ = te.sandboxMgr.Stop(task.ID)
							}
							task.Output = fmt.Sprintf("Pi Coding Agent finished (%s)", stopReason)
						}
						te.store.UpdateTask(task)
						agentEnded = true
					} else if stopReason == "error" {
						task.ExecutionState = "failed"
						task.Status = "review"
						task.Output = fmt.Sprintf("Pi Coding Agent ended with error (stopReason=%s)", stopReason)
						te.store.UpdateTask(task)
						agentEnded = true
					} else if stopReason == "aborted" {
						task.ExecutionState = "stopped"
						task.Status = "review"
						task.Output = "Pi Coding Agent was aborted"
						te.store.UpdateTask(task)
						agentEnded = true
					} else {
						task.ExecutionState = "awaiting_review"
						task.Status = "review"
						task.Output = fmt.Sprintf("Pi Coding Agent finished (%s)", stopReason)
						te.store.UpdateTask(task)
						agentEnded = true
					}
				}

			case "response":
				if !event.Success && event.Error != "" && event.Command == "prompt" {
					log.Printf("[pi] task=%s prompt response error: %s", task.ID, event.Error)
					te.sendAndPersistError(task.ID, event.Error, "pi_rpc_error")
				}
			}

		case um := <-userMsgChan:
			// Forward user message to Pi with steer behavior.
			// "steer" queues the message while the agent is running;
			// it is delivered after the current turn's tool calls finish.
			log.Printf("[pi] task=%s forwarding user message (len=%d)", task.ID, len(um.Content))
			err := client.Prompt(ctx, um.Content, "steer")
			if err != nil {
				log.Printf("[pi] task=%s failed to forward user message: %v", task.ID, err)
			}
		}
	}

	// Task completion
	duration := time.Since(startTime)
	log.Printf("[pi] task=%s completed stopReason=%s duration=%s", task.ID, stopReason, duration)

	// Reload task — state may have been set inside the event loop (agent_end handler).
	// Only set state here if the loop exited abnormally (ctx cancelled, process exited).
	task, _ = te.store.GetTaskByTaskID(task.ID)
	if task.ExecutionState == "running" || task.ExecutionState == "idle" {
		// Loop exited without agent_end setting state (e.g., process crash)
		task.ExecutionState = "failed"
		task.Status = "review"
		task.Output = fmt.Sprintf("Pi Coding Agent process exited unexpectedly (stopReason=%s)", stopReason)
		te.store.UpdateTask(task)
	}

	// Always broadcast complete event to clear streaming state on the frontend.
	// The frontend's complete handler calls closeCurrentAssistant() which clears
	// the isStreaming flag on the assistant entry (removes the blinking cursor).
	te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
		"state":            task.ExecutionState,
		"summary":          "Pi Coding Agent finished",
		"model_name":       client.ModelName(),
		"prompt_tokens":    totalPromptTokens,
		"predicted_tokens": totalPredictedTokens,
		"cache_tokens":     0,
		"prompt_ms":        0,
		"predicted_ms":     0,
		"duration":         duration.Milliseconds(),
	}})

	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)

	// Auto-start next sibling if subtask
	if task.ParentTaskID != "" {
		_ = te.AutoStartNextSibling(projectID, task.ID, task.ParentTaskID, task.Status)
	}
}
