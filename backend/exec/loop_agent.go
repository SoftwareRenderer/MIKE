package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"kanban/backend/agent"
	"kanban/backend/extensions"
	"kanban/backend/git"
	"kanban/backend/prompts"
	"kanban/backend/stream"
	"kanban/backend/types"
	pkgagent "kanban/pkg/agent"

	"github.com/google/uuid"
	"kanban/pkg/logging"
)

// executeTaskWithAgent runs the task using an agent extension via the generic agent runner.
// The agent handles its own LLM calls, tool execution, loop detection, and compaction.
// Kanban only manages the subprocess lifecycle and streams events to the frontend.
// requestedModel is the model name from the task config; if set, it sends set_model before the first prompt.
func (te *TaskExecutor) executeTaskWithAgent(ctx context.Context, task *types.Task, projectID, worktreePath, serverURL, requestedModel string, userMsgChan chan stream.UserMessage, sandboxEnabled bool, initialContent string) {
	defer func() {
		if r := recover(); r != nil {
			logging.For(logging.Recovery).With(logging.TaskID, task.ID).Errorf("recovered from panic: %v", r)
			te.sendAndPersistError(task.ID, fmt.Sprintf("Internal error: %v", r), "panic")
		}
	}()

	// Keep the task's accumulated session cost (from prior runs/executors) intact
	// so the badge reflects the full session cost across provider/model switches.

	sessionName := fmt.Sprintf("kanban-%s", task.ID)

	// Extract agent name from URL: "agent://pi" -> "pi"
	agentName, ok := types.ParseAgentURL(serverURL)
	if !ok {
		te.failTask(task, projectID, "Invalid agent URL: "+serverURL, "invalid_agent_url")
		return
	}

	// Lookup agent extension via registry
	ext := te.agentRegistry.LookupByName(agentName)
	if ext == nil {
		te.failTask(task, projectID, fmt.Sprintf("Agent %q not found in extensions", agentName), "agent_not_found")
		return
	}

	// The adapter is what knows which binary the agent runs, so it is loaded and asked before
	// anything goes looking for that binary in the container.
	prepared, err := te.prepareAgent(ctx, ext, task.ID, worktreePath, sessionName, sandboxEnabled)
	if err != nil {
		te.failTask(task, projectID, err.Error(), agentStartErrorCode(err))
		return
	}
	logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("creating runner (agent=%s, binary=%s, sandbox=%v, cwd=%s)", agentName, prepared.Binary(), sandboxEnabled, worktreePath)
	runner, err := prepared.Start(te.sandboxMgr, task.ID)
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

	logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("runner started (pid=%d)", runner.PID())

	// Set the requested model (from task config) before first prompt.
	if requestedModel != "" {
		logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("setting model=%s", requestedModel)
		setModelCtx, setModelCancel := context.WithTimeout(ctx, 5*time.Second)
		runner.SetModel(setModelCtx, requestedModel)
		setModelCancel()
	}

	// Discover the active model (after set_model takes effect).
	modelCtx, modelCancel := context.WithTimeout(ctx, 5*time.Second)
	activeModel := runner.QueryModel(modelCtx)
	modelCancel()
	logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("model=%s", activeModel)

	// Capture session file path from get_state response (for session resumption on restart).
	sessionFile := runner.SessionFile()
	if sessionFile != "" {
		logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("sessionFile=%s", sessionFile)
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
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("switching to session %s", task.SessionID)
			// Wait briefly for switch to take effect
			time.Sleep(500 * time.Millisecond)
			useSessionResumption = true
		} else {
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Warnf("switch_session failed, falling back to context replay")
		}
		switchCancel()
	}
	// Store session file path for future restarts.
	if sessionFile != "" && task.SessionID == "" {
		task.SessionID = sessionFile
		te.storeTask(task, projectID)
		logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("saved sessionFile=%s", sessionFile)
	}

	var initialPrompt string
	if initialContent != "" {
		// Lazy-start (follow-up message)
		if useSessionResumption {
			// Agent has the session from switch_session, just send the new message.
			initialPrompt = initialContent
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("lazy-start via session resume: sending user message")
		} else {
			// Replay context as fallback.
			replayMsgs := taskMsgs
			if len(taskMsgs) > 0 && taskMsgs[len(taskMsgs)-1].Content == initialContent {
				replayMsgs = taskMsgs[:len(taskMsgs)-1]
			}
			if len(replayMsgs) > 0 {
				initialPrompt = buildContextReplay(replayMsgs) + "\n\n--- New message ---\n\n" + initialContent
				logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("lazy-start: replaying %d messages + new message", len(replayMsgs))
			} else {
				initialPrompt = initialContent
				logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("lazy-start: sending user message as initial prompt")
			}
		}
	} else if len(taskMsgs) > 1 {
		// Restart from done/review
		if useSessionResumption {
			// Agent has the session, send the task prompt to continue.
			initialPrompt = task.Prompt
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("restart via session resume: sending task prompt")
		} else {
			// Replay full conversation history.
			// The task prompt is already the first message in taskMsgs,
			// so the replay includes it plus all subsequent messages.
			initialPrompt = buildContextReplay(taskMsgs)
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("restart: replaying %d messages", len(taskMsgs))
		}
	} else {
		// Fresh start: agent has no session, send the task prompt.
		initialPrompt = task.Prompt
		logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("fresh-start: sending task prompt")
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
				logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("event channel closed during startup")
				runner.Close()
				te.failTask(task, projectID, "Agent process exited unexpectedly", "agent_process_exited")
				return
			}
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("startup event: %s", event.Type)
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
				logging.For(logging.Agent).With(logging.TaskID, task.ID).Errorf("startup timeout (no status within 30s)")
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

	// sendAgentStats publishes a session-stats event. Every field is a cumulative
	// session total, except cost: some agents report no cost in stats, so it falls back
	// to the per-message accumulation.
	sendAgentStats := func(stats pkgagent.SessionStatsData) {
		if stats.ContextWindow <= 0 {
			return
		}
		cost := stats.Cost
		if cost <= 0 {
			cost = runner.SessionCost()
		}
		te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStats, Data: map[string]any{
			"model_name":         runner.ModelName(),
			"prompt_tokens":      stats.TotalInput,
			"predicted_tokens":   stats.TotalOutput,
			"context_used":       stats.ContextTokens,
			"context_limit":      stats.ContextWindow,
			"context_percent":    stats.ContextPercent,
			"cost":               cost,
			"cache_read_tokens":  stats.TotalCacheRead,
			"cache_write_tokens": stats.TotalCacheWrite,
		}})
	}

	// sendAgentComplete clears the frontend's streaming state. Its token figures are
	// per-turn sums, and a persist entry carries no cache count, so cache_tokens stays 0
	// here — cumulative cache numbers belong in the stats event.
	sendAgentComplete := func(state types.ExecutionState, promptTokens, predictedTokens int, turnDuration time.Duration) {
		te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
			"state":            state,
			"summary":          "Agent finished",
			"model_name":       runner.ModelName(),
			"prompt_tokens":    promptTokens,
			"predicted_tokens": predictedTokens,
			"cache_tokens":     0,
			"prompt_ms":        0,
			"predicted_ms":     0,
			"duration":         turnDuration.Milliseconds(),
		}})
	}

	// Query initial stats
	statsCtx, statsCancel := context.WithCancel(ctx)
	defer statsCancel()
	statsCtxInner, cancel := context.WithTimeout(statsCtx, 3*time.Second)
	sendAgentStats(runner.QueryStats(statsCtxInner))
	cancel()

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
				sendAgentStats(runner.QueryStats(statsCtxInner))
				cancel()
			}
		}
	}()

	for !agentEnded {
		select {
		case <-ctx.Done():
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("context cancelled")
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

				logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("persist complete, stopReason=%s", stopReason)

				// Check for queued user messages before ending.
				select {
				case um := <-userMsgChan:
					if um.ContextChanged {
						logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("context changed notification, skipping")
					} else {
						// Handle model switch if requested with this message.
						if um.ModelName != "" {
							logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("model switch requested: %s", um.ModelName)
							setModelCtx, setModelCancel := context.WithTimeout(ctx, 5*time.Second)
							runner.SetModel(setModelCtx, um.ModelName)
							setModelCancel()
						}
						logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("persist: forwarding queued user message for next turn")
						turnStartTime = time.Now()
						firstTokenTime = time.Time{}
						err := runner.Prompt(ctx, um.Content, "steer")
						if err != nil {
							logging.For(logging.Agent).With(logging.TaskID, task.ID).Warnf("failed to forward queued message: %v", err)
						}
					}
				default:
					te.handleAgentTurnComplete(task, projectID, task.ID, worktreePath, runner, stopReason, &agentEnded)
					// Send complete event to clear frontend streaming state.
					// For normal idle (agentEnded=false), the loop stays alive for follow-ups.
					// For terminal states (agentEnded=true), the loop exits and sends its own complete.
					if !agentEnded {
						sendAgentComplete(task.ExecutionState, totalPromptTokens, totalPredictedTokens, time.Since(turnStartTime))
					}
				}

			case pkgagent.EventTypeError:
				if event.RawData != nil {
					msg, _ := event.RawData["message"].(string)
					logging.For(logging.Agent).With(logging.TaskID, task.ID).Warnf("error: %s", msg)
				}

			case pkgagent.EventTypeStatus:
				if event.Status.State == "idle" && event.Status.Activity != "" {
					stopReason = "stop"
				}
			}

		case um := <-userMsgChan:
			if um.ContextChanged {
				logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("context changed notification, skipping")
				continue
			}
			// Handle model switch if requested with this message.
			if um.ModelName != "" {
				logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("model switch requested: %s", um.ModelName)
				setModelCtx, setModelCancel := context.WithTimeout(ctx, 5*time.Second)
				runner.SetModel(setModelCtx, um.ModelName)
				setModelCancel()
			}
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("forwarding user message (len=%d)", len(um.Content))
			turnStartTime = time.Now()
			firstTokenTime = time.Time{}
			err := runner.Prompt(ctx, um.Content, "steer")
			if err != nil {
				logging.For(logging.Agent).With(logging.TaskID, task.ID).Warnf("failed to forward user message: %v", err)
			}

		case resultCh := <-compactCh:
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("compact requested")
			compactCtx, compactCancel := context.WithTimeout(ctx, 120*time.Second)
			compactResult, err := runner.Compact(compactCtx)
			compactCancel()
			if err != nil {
				logging.For(logging.Agent).With(logging.TaskID, task.ID).Warnf("compact failed: %v", err)
				resultCh <- &CompactResult{Error: err}
			} else if compactResult != nil {
				logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("compact succeeded (summary len=%d)", len(compactResult.Summary))
				resultCh <- &CompactResult{Summary: compactResult.Summary, Error: compactResult.Err}
			}
		}
	}

	// Task completion
	duration := time.Since(startTime)
	logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("completed stopReason=%s duration=%s", stopReason, duration)

	task, _ = te.store.GetTaskByID(task.ID)
	if task.ExecutionState == types.ExecStateRunning {
		task.ExecutionState = types.ExecStateFailed
		task.Status = types.StatusReview
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
		logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("accumulated session cost=%f for model=%s provider=%s", finalCost, modelName, providerName)
	}
	// Persist session cost to the task so it's available when viewing non-streaming tasks.
	// Accumulate onto any previously carried-over cost (e.g. from a prior executor run).
	task.SessionCost += finalCost
	te.storeTask(task, projectID)

	// Broadcast complete event to clear streaming state on the frontend.
	sendAgentComplete(task.ExecutionState, totalPromptTokens, totalPredictedTokens, duration)

	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)

	// Auto-start next sibling if subtask
	if task.ParentTaskID != "" {
		_ = te.AutoStartNextSibling(projectID, task.ID, task.ParentTaskID)
	}
}

// agentBinaries is the container-side question the agent pre-flight asks. *sandbox.SandboxManager
// answers it for real; a test answers it to record which name was asked about, because which name
// is asked about is the whole content of the pre-flight.
type agentBinaries interface {
	BinaryAvailable(taskID, binary string) bool
}

// errAgentBinaryNotAvailable marks the refusal that a task's sandbox container does not hold the
// binary its agent adapter names. It keeps its own code all the way to the browser, where a missing
// binary reads differently from an agent that failed to start.
var errAgentBinaryNotAvailable = errors.New("agent binary not available in the sandbox container")

// agentStartErrorCode picks the code a start that never reached the event loop reports.
func agentStartErrorCode(err error) string {
	if errors.Is(err, errAgentBinaryNotAvailable) {
		return "agent_not_available"
	}
	return "agent_start_failed"
}

// binaryProber answers the container question for this executor: whatever was injected, else the
// task sandbox manager, else nothing - and "nothing" is not "available".
func (te *TaskExecutor) binaryProber() agentBinaries {
	if te.agentBinaries != nil {
		return te.agentBinaries
	}
	if te.sandboxMgr != nil {
		return te.sandboxMgr
	}
	return nil
}

// prepareAgent loads an agent extension's adapter and, for a sandboxed task, checks that the
// container holds the binary that adapter named. The order is the point: the adapter is the only
// place that name exists, so the container cannot be asked about it beforehand.
func (te *TaskExecutor) prepareAgent(ctx context.Context, ext *extensions.Extension, taskID, worktreePath, sessionName string, sandboxEnabled bool) (*agent.Prepared, error) {
	prepared, err := agent.PrepareAgent(ctx, ext, worktreePath, sessionName, sandboxEnabled)
	if err != nil {
		return nil, err
	}
	if !sandboxEnabled {
		return prepared, nil
	}

	prober := te.binaryProber()
	if prober == nil {
		prepared.Close()
		return nil, fmt.Errorf("agent %q runs in a sandbox container and this server has none", ext.Name)
	}
	if !prober.BinaryAvailable(taskID, prepared.Binary()) {
		prepared.Close()
		return nil, fmt.Errorf("agent %q runs %q, which is not available in the sandbox container: %w",
			ext.Name, prepared.Binary(), errAgentBinaryNotAvailable)
	}
	return prepared, nil
}

// handleAgentTurnComplete handles post-turn logic after an agent turn completes.
func (te *TaskExecutor) handleAgentTurnComplete(task *types.Task, projectID, taskID, worktreePath string, runner *agent.AgentRunner, stopReason string, agentEnded *bool) {
	// Check auto-commit/merge
	if task.AutoCommit {
		// A commit-and-done click merges whatever auto_merge says: read it here, consume it after.
		commitDone := task.HasMergeConflict && te.CommitAndDonePending(task.ID)
		if task.HasMergeConflict {
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("agent finished resolving conflicts, committing resolution")
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
				logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("conflict resolution committed")
				// Restored by label, because this runs long after the sync-merge that pushed - see
				// NewestStashHashForMessage.
				//
				// Released explicitly, not deferred: a lock held past this block would be held across the merges below,
				// which take the merge lock, and merge must come before the stash lock, never after.
				unlockStash := te.worktreeMgr.LockProjectStash(projectID)
				if _, err := te.worktreeMgr.RestoreStashForTask(worktreePath, git.StashMessageForSyncMerge(task.ID)); err != nil {
					logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("restoring the sync-merge stash: %v", err)
				}
				unlockStash()
			}
			_ = te.store.SetTaskMergeConflict(task.ID, projectID, false)
			task.ExecutionState = types.ExecStateIdle
			_ = te.store.UpdateTaskExecutionState(task.ID, projectID, "idle")
		}
		commitMsg := te.GenerateCommitMessage(context.Background(), task)
		mergeErr := te.CommitAndMerge(projectID, task.ID, worktreePath, commitMsg, task.AutoMerge || commitDone)
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
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("merge conflict, prompting agent to resolve")
			err := runner.Prompt(context.Background(), prompt)
			if err != nil {
				logging.For(logging.Agent).With(logging.TaskID, task.ID).Warnf("failed to send merge conflict prompt: %v", err)
				*agentEnded = true
			}
			return
		}
		// The conflict is over, so the click is spent - merge failure included.
		commitDone = te.takeCommitAndDone(task.ID, mergeErr == nil)
		if mergeErr != nil {
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Warnf("merge failed, moving to review: %v", mergeErr)
			task.ExecutionState = types.ExecStateIdle
			task.Status = types.StatusReview
			task.Output = "Merge failed: " + mergeErr.Error()
			if te.sandboxMgr != nil {
				_ = te.sandboxMgr.Stop(task.ID)
			}
			te.storeTask(task, projectID)
			*agentEnded = true
			return
		}
		if !task.AutoMerge && !commitDone {
			// Nothing asked for this merge (auto_merge=false, no click) - don't mark done
			task.ExecutionState = types.ExecStateIdle
			task.Status = types.StatusReview
			if te.sandboxMgr != nil {
				_ = te.sandboxMgr.Stop(task.ID)
			}
			te.storeTask(task, projectID)
			*agentEnded = true
			return
		}
		task.ExecutionState = types.ExecStateCompleted
		task.Status = types.StatusDone
		if te.sandboxMgr != nil {
			_ = te.sandboxMgr.Stop(task.ID)
		}
		task.Output = fmt.Sprintf("Agent finished (%s)", stopReason)
		te.storeTask(task, projectID)
		*agentEnded = true
		return
	}

	if task.HasMergeConflict {
		res := te.commitConflictResolution(projectID, task.ID, worktreePath, task)
		task = res.task
		if task.HasMergeConflict {
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("merge still conflicted after agent ended")
			task.ExecutionState = types.ExecStateFailed
			task.Status = types.StatusReview
			if te.sandboxMgr != nil {
				_ = te.sandboxMgr.Stop(task.ID)
			}
			task.Output = fmt.Sprintf("Agent finished but merge still conflicted (%s)", stopReason)
		} else if res.finishesTask() {
			task.ExecutionState = types.ExecStateCompleted
			task.Status = types.StatusDone
			if te.sandboxMgr != nil {
				_ = te.sandboxMgr.Stop(task.ID)
			}
			task.Output = fmt.Sprintf("Agent finished (%s)", stopReason)
		} else {
			// No merge, or a failed one: the work is not in the target branch.
			task.ExecutionState = types.ExecStateIdle
			task.Status = types.StatusReview
			if te.sandboxMgr != nil {
				_ = te.sandboxMgr.Stop(task.ID)
			}
			if res.mergeErr != nil {
				task.Output = "Merge failed: " + res.mergeErr.Error()
			} else {
				task.Output = fmt.Sprintf("Agent finished (%s)", stopReason)
			}
		}
		te.storeTask(task, projectID)
		*agentEnded = true
		return
	}

	if stopReason == "error" {
		task.ExecutionState = types.ExecStateFailed
		task.Status = types.StatusReview
		task.Output = fmt.Sprintf("Agent ended with error (stopReason=%s)", stopReason)
		te.storeTask(task, projectID)
		*agentEnded = true
		return
	}

	if stopReason == "aborted" {
		task.ExecutionState = types.ExecStateIdle
		task.Status = types.StatusReview
		task.Output = "Agent was aborted"
		te.storeTask(task, projectID)
		*agentEnded = true
		return
	}

	task.ExecutionState = types.ExecStateIdle
	task.Status = types.StatusReview
	task.Output = fmt.Sprintf("Agent finished (%s)", stopReason)
	te.storeTask(task, projectID)
	// Don't set agentEnded = true — keep the event loop alive for follow-up messages.
	// The agent subprocess still has full conversation context.
	// The loop will wait for: userMsgChan (follow-up), ctx.Done (stop), or process exit.
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
}

// persistAgentTurnEntries writes pre-parsed agent turn entries to the DB. It is generic over
// adapters: it only iterates PersistEntry values into TaskMessage rows. The sandbox flag goes
// into tool_call ToolCallsJSON so the frontend still shows the sandbox icon after a refresh —
// the adapter knows nothing about sandbox mode, and this mirrors the non-agent loop.
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
