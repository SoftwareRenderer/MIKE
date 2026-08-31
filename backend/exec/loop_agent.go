package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"kanban/backend/agent"
	"kanban/backend/prompts"
	"kanban/backend/stream"
	"kanban/backend/types"
	pkgagent "kanban/pkg/agent"

	"github.com/google/uuid"
)

// AgentSentinelPrefix is the URL prefix for agent providers.
const AgentSentinelPrefix = "agent://"

// executeTaskWithAgent runs the task using an agent extension via the generic agent runner.
// The agent handles its own LLM calls, tool execution, loop detection, and compaction.
// Kanban only manages the subprocess lifecycle and streams events to the frontend.
// requestedModel is the model name from the task config; if set, it sends set_model before the first prompt.
func (te *TaskExecutor) executeTaskWithAgent(ctx context.Context, task *types.Task, projectID, worktreePath, serverURL, requestedModel string, userMsgChan chan stream.UserMessage, sandboxEnabled bool, initialContent string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[agent-panic] task=%s recovered from panic: %v", task.ID, r)
			te.sendAndPersistError(task.ID, fmt.Sprintf("Internal error: %v", r), "panic")
		}
	}()

	// Keep the task's accumulated session cost (from prior runs/executors) intact
	// so the badge reflects the full session cost across provider/model switches.

	sessionName := fmt.Sprintf("kanban-%s", task.ID)

	// Extract agent name from URL: "agent://pi" -> "pi"
	agentName := strings.TrimPrefix(serverURL, AgentSentinelPrefix)
	if agentName == "" || agentName == serverURL {
		te.failTask(task, projectID, "Invalid agent URL: "+serverURL, "invalid_agent_url")
		return
	}

	// Lookup agent extension via registry
	ext := te.agentRegistry.LookupByName(agentName)
	if ext == nil {
		te.failTask(task, projectID, fmt.Sprintf("Agent %q not found in extensions", agentName), "agent_not_found")
		return
	}

	var runner *agent.AgentRunner
	var err error

	if sandboxEnabled {
		// Pre-flight: check if agent binary exists in the container
		if !te.sandboxMgr.BinaryAvailable(task.ID, "pi") {
			te.failTask(task, projectID, fmt.Sprintf("Agent binary not found in sandbox container for %s", agentName), "agent_not_available")
			return
		}

		log.Printf("[agent] task=%s creating runner in container (agent=%s, session=%s)", task.ID, agentName, sessionName)
		runner, err = agent.NewAgentRunner(ctx, ext, te.sandboxMgr, task.ID, worktreePath, sessionName, true)
	} else {
		// Agent runs on the host.
		// The adapter's SubprocessConfig() sets config dir env vars from the
		// extension's sandbox_mount declaration, so no agent-specific code needed here.
		log.Printf("[agent] task=%s creating runner (cwd=%s, session=%s)", task.ID, worktreePath, sessionName)
		runner, err = agent.NewAgentRunner(ctx, ext, nil, task.ID, worktreePath, sessionName, false)
	}
	if err != nil {
		te.failTask(task, projectID, fmt.Sprintf("Failed to start agent: %v", err), "agent_start_failed")
		return
	}

	// Store runner on execution state so it survives after the event loop exits.
	// This enables on-demand compaction while the task is idle.
	te.taskMu.Lock()
	if state, exists := te.tasks[task.ID]; exists && state.ctx == ctx {
		state.agentRunner = runner
	}
	te.taskMu.Unlock()

	log.Printf("[agent] task=%s runner started (pid=%d)", task.ID, runner.PID())

	// Set the requested model (from task config) before first prompt.
	if requestedModel != "" {
		log.Printf("[agent] task=%s setting model=%s", task.ID, requestedModel)
		setModelCtx, setModelCancel := context.WithTimeout(ctx, 5*time.Second)
		runner.SetModel(setModelCtx, requestedModel)
		setModelCancel()
	}

	// Discover the active model (after set_model takes effect).
	modelCtx, modelCancel := context.WithTimeout(ctx, 5*time.Second)
	activeModel := runner.QueryModel(modelCtx)
	modelCancel()
	log.Printf("[agent] task=%s model=%s", task.ID, activeModel)

	// Capture session file path from get_state response (for session resumption on restart).
	sessionFile := runner.SessionFile()
	if sessionFile != "" {
		log.Printf("[agent] task=%s sessionFile=%s", task.ID, sessionFile)
	}

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

	te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Agent started"}})

	// Send initial prompt to agent.
	// Agent subprocesses start fresh each time. If we have a saved session file
	// from a previous run, switch to it so the agent resumes with full context.
	// Otherwise, replay conversation history as a text transcript.
	useSessionResumption := false
	if len(taskMsgs) > 1 && task.SessionID != "" {
		// Restart: try to resume the saved agent session.
		switchCtx, switchCancel := context.WithTimeout(ctx, 5*time.Second)
		if runner.SwitchSession(switchCtx, task.SessionID) {
			log.Printf("[agent] task=%s switching to session %s", task.ID, task.SessionID)
			// Wait briefly for switch to take effect
			time.Sleep(500 * time.Millisecond)
			useSessionResumption = true
		} else {
			log.Printf("[agent] task=%s switch_session failed, falling back to context replay", task.ID)
		}
		switchCancel()
	}
	// Store session file path for future restarts.
	if sessionFile != "" && task.SessionID == "" {
		task.SessionID = sessionFile
		te.storeTask(task, projectID)
		log.Printf("[agent] task=%s saved sessionFile=%s", task.ID, sessionFile)
	}

	var initialPrompt string
	if initialContent != "" {
		// Lazy-start (follow-up message)
		if useSessionResumption {
			// Agent has the session from switch_session, just send the new message.
			initialPrompt = initialContent
			log.Printf("[agent] task=%s lazy-start via session resume: sending user message", task.ID)
		} else {
			// Replay context as fallback.
			replayMsgs := taskMsgs
			if len(taskMsgs) > 0 && taskMsgs[len(taskMsgs)-1].Content == initialContent {
				replayMsgs = taskMsgs[:len(taskMsgs)-1]
			}
			if len(replayMsgs) > 0 {
				initialPrompt = buildContextReplay(replayMsgs) + "\n\n--- New message ---\n\n" + initialContent
				log.Printf("[agent] task=%s lazy-start: replaying %d messages + new message", task.ID, len(replayMsgs))
			} else {
				initialPrompt = initialContent
				log.Printf("[agent] task=%s lazy-start: sending user message as initial prompt", task.ID)
			}
		}
	} else if len(taskMsgs) > 1 {
		// Restart from done/review
		if useSessionResumption {
			// Agent has the session, send the task prompt to continue.
			initialPrompt = task.Prompt
			log.Printf("[agent] task=%s restart via session resume: sending task prompt", task.ID)
		} else {
			// Replay full conversation history.
			// The task prompt is already the first message in taskMsgs,
			// so the replay includes it plus all subsequent messages.
			initialPrompt = buildContextReplay(taskMsgs)
			log.Printf("[agent] task=%s restart: replaying %d messages", task.ID, len(taskMsgs))
		}
	} else {
		// Fresh start: agent has no session, send the task prompt.
		initialPrompt = task.Prompt
		log.Printf("[agent] task=%s fresh-start: sending task prompt", task.ID)
	}
	err = runner.Prompt(ctx, initialPrompt)
	if err != nil {
		te.failTask(task, projectID, fmt.Sprintf("Failed to send prompt to agent: %v", err), "agent_prompt_failed")
		return
	}

	// Validate agent startup: wait for status event within a timeout.
	startupCtx, startupCancel := context.WithTimeout(ctx, 30*time.Second)
	defer startupCancel()

	startupValidated := false
	for !startupValidated {
		select {
		case event, ok := <-runner.EventChan():
			if !ok {
				log.Printf("[agent] task=%s event channel closed during startup", task.ID)
				runner.Close()
				te.failTask(task, projectID, "Agent process exited unexpectedly", "agent_process_exited")
				return
			}
			log.Printf("[agent] task=%s startup event: %s", task.ID, event.Type)
			if streamEvent := toStreamEvent(event, sandboxEnabled, runner); streamEvent.Type != "" {
				te.sendAgentStreamEvent(projectID, task.ID, streamEvent)
			}
			switch event.Type {
			case pkgagent.EventTypeStatus:
				if event.Status.State == "running" {
					startupValidated = true
				}
			case pkgagent.EventTypePersist:
				// agent_end produced persist entries — startup validated
				startupValidated = true
			case pkgagent.EventTypeError:
				te.failTask(task, projectID, fmt.Sprintf("Agent error: %v", event.RawData), "agent_error")
				return
			}
		case <-startupCtx.Done():
			if startupCtx.Err() == context.DeadlineExceeded {
				log.Printf("[agent] task=%s startup timeout (no status within 30s)", task.ID)
				runner.Close()
				te.failTask(task, projectID, "Agent failed to start (timeout)", "agent_startup_timeout")
			} else {
				// Stopped by user during agent startup
				te.handleCancelledTask(task.ID, projectID)
				return
			}
		}
	}

	// Event loop
	startTime := time.Now()
	turnStartTime := startTime
	var firstTokenTime time.Time // tracks when first token arrives (prompt processing ends)
	agentEnded := false
	var stopReason string
	var totalPromptTokens, totalPredictedTokens int
	compactCh := te.getCompactCh(task.ID)

	// Query initial stats
	statsCtx, statsCancel := context.WithCancel(ctx)
	defer statsCancel()
	statsCtxInner, cancel := context.WithTimeout(statsCtx, 3*time.Second)
	initialStats := runner.QueryStats(statsCtxInner)
	cancel()
	if initialStats.ContextWindow > 0 {
		cost := initialStats.Cost
		if cost <= 0 {
			cost = runner.SessionCost()
		}
		te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStats, Data: map[string]any{
			"model_name":       runner.ModelName(),
			"prompt_tokens":    initialStats.TotalInput,
			"predicted_tokens": initialStats.TotalOutput,
			"context_used":     initialStats.ContextTokens,
			"context_limit":    initialStats.ContextWindow,
			"context_percent":  initialStats.ContextPercent,
			"cost":             cost,
		}})
	}

	// Periodically query agent session stats.
	statsTicker := time.NewTicker(5 * time.Second)
	defer statsTicker.Stop()
	go func() {
		for {
			select {
			case <-statsCtx.Done():
				return
			case <-statsTicker.C:
				statsCtxInner, cancel := context.WithTimeout(statsCtx, 3*time.Second)
				stats := runner.QueryStats(statsCtxInner)
				cancel()
				if stats.ContextWindow > 0 {
					// Use per-message cost accumulation as fallback when get_session_stats
					// returns NaN/null cost (Pi agent bug: usage.cost.total missing from entries).
					cost := stats.Cost
					if cost <= 0 {
						cost = runner.SessionCost()
					}
					te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStats, Data: map[string]any{
						"model_name":       runner.ModelName(),
						"prompt_tokens":    stats.TotalInput,
						"predicted_tokens": stats.TotalOutput,
						"context_used":     stats.ContextTokens,
						"context_limit":    stats.ContextWindow,
						"cost":             cost,
						"context_percent":  stats.ContextPercent,
					}})
				}
			}
		}
	}()

	for !agentEnded {
		select {
		case <-ctx.Done():
			log.Printf("[agent] task=%s context cancelled", task.ID)
			runner.Close()
			te.handleCancelledTask(task.ID, projectID)
			return

		case event, ok := <-runner.EventChan():
			if !ok {
				agentEnded = true
				stopReason = "process_exited"
				break
			}

			// Track first token arrival to compute prompt vs gen timing
			if event.Type == pkgagent.EventTypeToken && firstTokenTime.IsZero() {
				firstTokenTime = time.Now()
			}

			// Stream the event to frontend
			if streamEvent := toStreamEvent(event, sandboxEnabled, runner); streamEvent.Type != "" {
				te.sendAgentStreamEvent(projectID, task.ID, streamEvent)
			}

			switch event.Type {
			case pkgagent.EventTypePersist:
				// Compute prompt/gen timing for this turn
				turnEnd := time.Now()
				promptMs := int64(0)
				predictedMs := int64(0)
				if !firstTokenTime.IsZero() {
					promptMs = firstTokenTime.Sub(turnStartTime).Milliseconds()
					predictedMs = turnEnd.Sub(firstTokenTime).Milliseconds()
				} else {
					// No tokens received (e.g., error turn); use full duration as gen time
					predictedMs = turnEnd.Sub(turnStartTime).Milliseconds()
				}
				// Inject timing into persist entries so CreateTaskMessage accumulates speeds
				for i := range event.PersistEntries {
					if event.PersistEntries[i].Role == "assistant" {
						event.PersistEntries[i].PromptMs = promptMs
						event.PersistEntries[i].PredictedMs = predictedMs
					}
				}
				// Persist pre-parsed messages from adapter
				pt, ptok := te.persistAgentTurnEntries(projectID, task.ID, task.CurrentThreadID, event.PersistEntries, turnStartTime, sandboxEnabled)
				totalPromptTokens += pt
				totalPredictedTokens += ptok
				firstTokenTime = time.Time{} // reset for next turn

				// Checkpoint file changes after agent turn completes.
				checkpointMsgID := uuid.New().String()
				te.createCheckpoint(ctx, projectID, task.ID, worktreePath, task.CurrentThreadID, checkpointMsgID, nil, sandboxEnabled)

				// Determine stop reason from status event
				if event.Status.State == "idle" {
					stopReason = "stop"
				} else if event.Status.State == "running" {
					stopReason = "toolUse"
				}

				log.Printf("[agent] task=%s persist complete, stopReason=%s", task.ID, stopReason)

				// Check for queued user messages before ending.
				select {
				case um := <-userMsgChan:
					if um.ContextChanged {
						log.Printf("[agent] task=%s context changed notification, skipping", task.ID)
					} else {
						// Handle model switch if requested with this message.
						if um.ModelName != "" {
							log.Printf("[agent] task=%s model switch requested: %s", task.ID, um.ModelName)
							setModelCtx, setModelCancel := context.WithTimeout(ctx, 5*time.Second)
							runner.SetModel(setModelCtx, um.ModelName)
							setModelCancel()
						}
						log.Printf("[agent] task=%s persist: forwarding queued user message for next turn", task.ID)
						turnStartTime = time.Now()
						firstTokenTime = time.Time{}
						err := runner.Prompt(ctx, um.Content, "steer")
						if err != nil {
							log.Printf("[agent] task=%s failed to forward queued message: %v", task.ID, err)
						}
					}
				default:
					te.handleAgentTurnComplete(task, projectID, task.ID, worktreePath, runner, stopReason, &agentEnded)
					// Send complete event to clear frontend streaming state.
					// For normal idle (agentEnded=false), the loop stays alive for follow-ups.
					// For terminal states (agentEnded=true), the loop exits and sends its own complete.
					if !agentEnded {
						te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
							"state":            task.ExecutionState,
							"summary":          "Agent finished",
							"model_name":       runner.ModelName(),
							"prompt_tokens":    totalPromptTokens,
							"predicted_tokens": totalPredictedTokens,
							"cache_tokens":     0,
							"prompt_ms":        0,
							"predicted_ms":     0,
							"duration":         time.Since(turnStartTime).Milliseconds(),
						}})
					}
				}

			case pkgagent.EventTypeError:
				if event.RawData != nil {
					msg, _ := event.RawData["message"].(string)
					log.Printf("[agent] task=%s error: %s", task.ID, msg)
				}

			case pkgagent.EventTypeStatus:
				if event.Status.State == "idle" && event.Status.Activity != "" {
					stopReason = "stop"
				}
			}

		case um := <-userMsgChan:
			if um.ContextChanged {
				log.Printf("[agent] task=%s context changed notification, skipping", task.ID)
				continue
			}
			// Handle model switch if requested with this message.
			if um.ModelName != "" {
				log.Printf("[agent] task=%s model switch requested: %s", task.ID, um.ModelName)
				setModelCtx, setModelCancel := context.WithTimeout(ctx, 5*time.Second)
				runner.SetModel(setModelCtx, um.ModelName)
				setModelCancel()
			}
			log.Printf("[agent] task=%s forwarding user message (len=%d)", task.ID, len(um.Content))
			turnStartTime = time.Now()
			firstTokenTime = time.Time{}
			err := runner.Prompt(ctx, um.Content, "steer")
			if err != nil {
				log.Printf("[agent] task=%s failed to forward user message: %v", task.ID, err)
			}

		case resultCh := <-compactCh:
			log.Printf("[agent] task=%s compact requested", task.ID)
			compactCtx, compactCancel := context.WithTimeout(ctx, 120*time.Second)
			compactResult, err := runner.Compact(compactCtx)
			compactCancel()
			if err != nil {
				log.Printf("[agent] task=%s compact failed: %v", task.ID, err)
				resultCh <- &CompactResult{Error: err}
			} else if compactResult != nil {
				log.Printf("[agent] task=%s compact succeeded (summary len=%d)", task.ID, len(compactResult.Summary))
				resultCh <- &CompactResult{Summary: compactResult.Summary, Error: compactResult.Err}
			}
		}
	}

	// Task completion
	duration := time.Since(startTime)
	log.Printf("[agent] task=%s completed stopReason=%s duration=%s", task.ID, stopReason, duration)

	task, _ = te.store.GetTaskByID(task.ID)
	if task.ExecutionState == "running" {
		task.ExecutionState = "failed"
		task.Status = "review"
		task.Output = fmt.Sprintf("Agent process exited unexpectedly (stopReason=%s)", stopReason)
		te.storeTask(task, projectID)
	}

	// Accumulate session cost into project_token_stats (aggregate increment, not per-message)
	// Use SessionCost() directly — it's accumulated from message_end events and persist
	// entries, bypassing the broken get_session_stats NaN cost entirely.
	finalCost := runner.SessionCost()
	if finalCost > 0 {
		modelName := runner.ModelName()
		providerName := runner.ProviderName()
		if providerName == "" {
			providerName = "Pi Coding Agent"
		}
		te.store.AccumulateProjectStats(projectID, modelName, providerName, finalCost)
		log.Printf("[agent] task=%s accumulated session cost=%f for model=%s provider=%s", task.ID, finalCost, modelName, providerName)
	}
	// Persist session cost to the task so it's available when viewing non-streaming tasks.
	// Accumulate onto any previously carried-over cost (e.g. from a prior executor run).
	task.SessionCost += finalCost
	te.storeTask(task, projectID)

	// Broadcast complete event to clear streaming state on the frontend.
	te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
		"state":            task.ExecutionState,
		"summary":          "Agent finished",
		"model_name":       runner.ModelName(),
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
		_ = te.AutoStartNextSibling(projectID, task.ID, task.ParentTaskID)
	}
}

// handleAgentTurnComplete handles post-turn logic after an agent turn completes.
func (te *TaskExecutor) handleAgentTurnComplete(task *types.Task, projectID, taskID, worktreePath string, runner *agent.AgentRunner, stopReason string, agentEnded *bool) {
	// Check auto-commit/merge
	if task.AutoCommit {
		if task.HasMergeConflict {
			log.Printf("[agent] task=%s agent finished resolving conflicts, committing resolution", task.ID)
			// Same stage + commit steps the LLM loop runs (commitConflictResolution).
			// What deliberately differs is the stash: this flow POPS it, because the
			// agent keeps working on this turn and CommitAndMerge runs next, while the
			// LLM loop DROPS it (its pre-sync edits are already committed, so popping
			// would re-apply them as unstaged changes).
			commitOK := te.stageResolvedConflicts(task.ID, worktreePath)
			if commitOK {
				commitOK = te.commitMergeConflictResolution(task.ID, worktreePath)
			}
			if commitOK {
				log.Printf("[agent] task=%s conflict resolution committed", task.ID)
				te.worktreeMgr.GetProjectStashMu(projectID).Lock()
				_ = te.worktreeMgr.GitRunInDir(worktreePath, "stash", "pop")
				te.worktreeMgr.GetProjectStashMu(projectID).Unlock()
			}
			_ = te.store.SetTaskMergeConflict(task.ID, projectID, false)
			task.ExecutionState = "idle"
			_ = te.store.UpdateTaskExecutionState(task.ID, projectID, "idle")
		}
		commitMsg := te.GenerateCommitMessage(context.Background(), task)
		mergeErr := te.CommitAndMerge(projectID, task.ID, worktreePath, commitMsg, task.AutoMerge)
		task, _ = te.store.GetTaskByID(task.ID)
		if task.HasMergeConflict {
			conflictedFiles := GetConflictedFiles(worktreePath)
			prompt, _ := prompts.RenderMergeConflictPrompt(prompts.MergeConflictData{
				TaskBranch:      task.Branch,
				TargetBranch:    te.ResolveMergeTarget(projectID, task),
				TaskTitle:       task.Title,
				TaskDescription: task.Prompt,
				ConflictedFiles: conflictedFiles,
			})
			log.Printf("[agent] task=%s merge conflict, prompting agent to resolve", task.ID)
			err := runner.Prompt(context.Background(), prompt)
			if err != nil {
				log.Printf("[agent] task=%s failed to send merge conflict prompt: %v", task.ID, err)
				*agentEnded = true
			}
			return
		}
		if mergeErr != nil {
			log.Printf("[agent] task=%s merge failed, moving to review: %v", task.ID, mergeErr)
			task.ExecutionState = "idle"
			task.Status = "review"
			task.Output = "Merge failed: " + mergeErr.Error()
			if te.sandboxMgr != nil {
				_ = te.sandboxMgr.Stop(task.ID)
			}
			te.storeTask(task, projectID)
			*agentEnded = true
			return
		}
		if !task.AutoMerge {
			// Merge was skipped (auto_merge=false) — don't mark done
			task.ExecutionState = "idle"
			task.Status = "review"
			if te.sandboxMgr != nil {
				_ = te.sandboxMgr.Stop(task.ID)
			}
			te.storeTask(task, projectID)
			*agentEnded = true
			return
		}
		task.ExecutionState = "completed"
		task.Status = "done"
		if te.sandboxMgr != nil {
			_ = te.sandboxMgr.Stop(task.ID)
		}
		task.Output = fmt.Sprintf("Agent finished (%s)", stopReason)
		te.storeTask(task, projectID)
		*agentEnded = true
		return
	}

	if task.HasMergeConflict {
		_, task = te.commitConflictResolution(projectID, task.ID, worktreePath, task)
		if task.HasMergeConflict {
			log.Printf("[agent] task=%s merge still conflicted after agent ended", task.ID)
			task.ExecutionState = "failed"
			task.Status = "review"
			if te.sandboxMgr != nil {
				_ = te.sandboxMgr.Stop(task.ID)
			}
			task.Output = fmt.Sprintf("Agent finished but merge still conflicted (%s)", stopReason)
		} else if !task.AutoMerge {
			// Merge was skipped (auto_merge=false) — don't mark done
			task.ExecutionState = "idle"
			task.Status = "review"
			if te.sandboxMgr != nil {
				_ = te.sandboxMgr.Stop(task.ID)
			}
			task.Output = fmt.Sprintf("Agent finished (%s)", stopReason)
		} else {
			task.ExecutionState = "completed"
			task.Status = "done"
			if te.sandboxMgr != nil {
				_ = te.sandboxMgr.Stop(task.ID)
			}
			task.Output = fmt.Sprintf("Agent finished (%s)", stopReason)
		}
		te.storeTask(task, projectID)
		*agentEnded = true
		return
	}

	if stopReason == "error" {
		task.ExecutionState = "failed"
		task.Status = "review"
		task.Output = fmt.Sprintf("Agent ended with error (stopReason=%s)", stopReason)
		te.storeTask(task, projectID)
		*agentEnded = true
		return
	}

	if stopReason == "aborted" {
		task.ExecutionState = "idle"
		task.Status = "review"
		task.Output = "Agent was aborted"
		te.storeTask(task, projectID)
		*agentEnded = true
		return
	}

	task.ExecutionState = "idle"
	task.Status = "review"
	task.Output = fmt.Sprintf("Agent finished (%s)", stopReason)
	te.storeTask(task, projectID)
	// Don't set agentEnded = true — keep the event loop alive for follow-up messages.
	// The agent subprocess still has full conversation context.
	// The loop will wait for: userMsgChan (follow-up), ctx.Done (stop), or process exit.
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
}

// persistAgentTurnEntries persists pre-parsed agent turn entries to the DB.
// This is a generic function that works with any agent adapter — it only iterates
// PersistEntry objects and creates TaskMessage rows.
// The sandbox flag is injected into tool_call ToolCallsJSON so the frontend
// can display the sandbox icon correctly after page refresh (the adapter
// doesn't know about sandbox mode — this mirrors the non-agent loop).
func (te *TaskExecutor) persistAgentTurnEntries(projectID, taskID, threadID string, entries []pkgagent.PersistEntry, startTime time.Time, sandbox bool) (totalPromptTokens, totalPredictedTokens int) {
	if len(entries) == 0 {
		return 0, 0
	}

	for _, entry := range entries {
		switch entry.Role {
		case "assistant":
			te.store.CreateTaskMessage(&types.TaskMessage{
				ID:               uuid.New().String(),
				TaskID:           taskID,
				ThreadID:         threadID,
				Role:             "assistant",
				Content:          entry.Content,
				ReasoningContent: entry.ReasoningContent,
				ModelName:        entry.ModelName,
				ProviderName:     entry.ProviderName,
				PromptTokens:     entry.PromptTokens,
				PredictedTokens:  entry.PredictedTokens,
				PromptMs:         int(entry.PromptMs),
				PredictedMs:      int(entry.PredictedMs),
				Duration:         time.Since(startTime).Milliseconds(),
				CreatedAt:        time.Now().UTC(),
			})
			totalPromptTokens += entry.PromptTokens
			totalPredictedTokens += entry.PredictedTokens

		case "tool_call":
			// Inject sandbox field into ToolCallsJSON so it persists to the DB
			// and survives page refresh. The adapter doesn't know about sandbox
			// mode, so we add it here (mirrors the non-agent loop at loop.go:914).
			toolCallsJSON := entry.ToolCallsJSON
			var toolCallData map[string]any

			if toolCallsJSON != "" {
				if err := json.Unmarshal([]byte(toolCallsJSON), &toolCallData); err == nil {
					toolCallData["sandbox"] = sandbox
					if modified, err := json.Marshal(toolCallData); err == nil {
						toolCallsJSON = string(modified)
					}
				}
			}

			te.store.CreateTaskMessage(&types.TaskMessage{
				ID:         uuid.New().String(),
				TaskID:     taskID,
				ThreadID:   threadID,
				Role:       "tool_call",
				Content:    "",
				ToolCalls:  toolCallsJSON,
				ToolResult: entry.ToolResultJSON,
				CreatedAt:  time.Now().UTC(),
			})

			// Send tool_call WS event (now includes sandbox field)
			if toolCallData != nil {
				te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeToolCall, Data: toolCallData})
			}
			// Send tool_result WS event
			if entry.ToolResultJSON != "" {
				var resultData map[string]any
				if err := json.Unmarshal([]byte(entry.ToolResultJSON), &resultData); err == nil {
					te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeToolResult, Data: resultData})
				}
			}
		}
	}

	return totalPromptTokens, totalPredictedTokens
}

// buildContextReplay builds a text representation of the conversation history
// for context replay when the agent is restarted. Agent subprocesses start fresh
// each time, so we must replay the conversation for the agent to have context.
// This is a fallback when session resumption (switch_session) is not available.
func buildContextReplay(messages []types.TaskMessage) string {
	var sb strings.Builder
	sb.WriteString("<conversation_history>\n")
	sb.WriteString("The following is the conversation history. Continue from where it left off.\n\n")

	for _, msg := range messages {
		switch msg.Role {
		case "advisory":
			sb.WriteString("**Environment note**: ")
			sb.WriteString(msg.Content)
			sb.WriteString("\n\n")

		case "user":
			sb.WriteString("**User**: ")
			sb.WriteString(msg.Content)
			sb.WriteString("\n\n")

		case "assistant":
			if msg.ReasoningContent != "" {
				sb.WriteString("**Assistant (thinking)**: ")
				sb.WriteString(msg.ReasoningContent)
				sb.WriteString("\n\n")
			}
			if msg.Content != "" {
				sb.WriteString("**Assistant**: ")
				sb.WriteString(msg.Content)
				sb.WriteString("\n\n")
			}

		case "tool_call":
			// Reconstruct tool call + result from JSON
			if msg.ToolCalls != "" {
				sb.WriteString("**Tool calls**:\n")
				var toolCalls []struct {
					ID          string         `json:"id"`
					Name        string         `json:"name"`
					DisplayName string         `json:"display_name"`
					Args        map[string]any `json:"args"`
				}
				if err := json.Unmarshal([]byte(msg.ToolCalls), &toolCalls); err == nil {
					for _, tc := range toolCalls {
						name := tc.DisplayName
						if name == "" {
							name = tc.Name
						}
						sb.WriteString(fmt.Sprintf("- %s(%s)\n", name, tc.ID))
						argsStr, _ := json.Marshal(tc.Args)
						sb.WriteString(fmt.Sprintf("  args: %s\n", string(argsStr)))
					}
				}
				sb.WriteString("\n")
			}
			if msg.ToolResult != "" {
				sb.WriteString("**Tool results**:\n")
				var results []struct {
					ID      string `json:"id"`
					Output  string `json:"output"`
					Success bool   `json:"success"`
				}
				if err := json.Unmarshal([]byte(msg.ToolResult), &results); err == nil {
					for _, r := range results {
						sb.WriteString(fmt.Sprintf("- %s: ", r.ID))
						if !r.Success {
							sb.WriteString("[ERROR] ")
						}
						// Truncate long outputs
						output := r.Output
						if len(output) > 2000 {
							output = output[:2000] + "... (truncated)"
						}
						sb.WriteString(output)
						sb.WriteString("\n")
					}
				}
				sb.WriteString("\n")
			}

		case "summarization":
			sb.WriteString("**[Context was summarized]**\n")
			if msg.Content != "" {
				sb.WriteString(msg.Content)
				sb.WriteString("\n\n")
			}
		}
	}

	sb.WriteString("</conversation_history>")
	return sb.String()
}

// sendAgentStreamEvent forwards an agent event to the client
func (te *TaskExecutor) sendAgentStreamEvent(projectID, taskID string, event types.StreamEvent) {
	if data, ok := event.Data.(map[string]string); ok && event.Type == types.WSTypeError {
		te.persistAndSendError(taskID, data["message"], data["code"], false)
		return
	}
	te.appStreamMgr.SendTaskEvent(projectID, taskID, event)
}

// toStreamEvent converts a generic agent event to a Kanban StreamEvent for WebSocket broadcasting.
func toStreamEvent(event pkgagent.Event, sandbox bool, runner *agent.AgentRunner) types.StreamEvent {
	switch event.Type {
	case pkgagent.EventTypeToken:
		return types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": event.Text}}

	case pkgagent.EventTypeReasoning:
		return types.StreamEvent{Type: types.WSTypeReasoning, Data: map[string]string{"text": event.Thinking}}

	case pkgagent.EventTypeToolCall:
		return types.StreamEvent{Type: types.WSTypeToolCall, Data: map[string]any{
			"tool_id":           event.ToolCall.ID,
			"tool_name":         event.ToolCall.Name,
			"tool_display_name": event.ToolCall.DisplayName,
			"args":              event.ToolCall.Args,
			"raw_args":          event.ToolCall.RawArgs,
			"sandbox":           sandbox,
		}}

	case pkgagent.EventTypeToolResult:
		return types.StreamEvent{Type: types.WSTypeToolResult, Data: map[string]any{
			"tool_id": event.ToolResult.ID,
			"success": event.ToolResult.Success,
			"output":  event.ToolResult.Output,
			"sandbox": sandbox,
		}}

	case pkgagent.EventTypeStatus:
		state := "running"
		activity := "Agent running"
		if event.Status.State != "" {
			state = event.Status.State
		}
		if event.Status.Activity != "" {
			activity = event.Status.Activity
		}
		return types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": state, "activity": activity}}

	case pkgagent.EventTypeError:
		msg := ""
		code := "agent_error"
		if event.RawData != nil {
			if m, ok := event.RawData["message"].(string); ok {
				msg = m
			}
			if c, ok := event.RawData["code"].(string); ok {
				code = c
			}
		}
		return types.StreamEvent{Type: types.WSTypeError, Data: map[string]string{"message": msg, "code": code}}

	default:
		// Pass through unknown events as status
		return types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": fmt.Sprintf("Agent: %s", event.Type)}}
	}
}
