package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"mike/backend/agent"
	"mike/backend/extensions"
	"mike/backend/git"
	"mike/backend/prompts"
	"mike/backend/types"
	pkgagent "mike/pkg/agent"

	"mike/pkg/logging"

	"github.com/google/uuid"
)

// The adapter owns its LLM calls, tools and compaction; this manages the subprocess and streams its events.
func agentInitialPrompt(task *types.Task, taskMsgs []types.TaskMessage, initialContent string, useSessionResumption bool) (initialPrompt string) {
	if initialContent != "" {
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
		if useSessionResumption {
			// Agent has the session, send the task prompt to continue.
			initialPrompt = task.Prompt
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("restart via session resume: sending task prompt")
		} else {
			initialPrompt = buildContextReplay(taskMsgs)
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("restart: replaying %d messages", len(taskMsgs))
		}
	} else {
		initialPrompt = task.Prompt
		logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("fresh-start: sending task prompt")
	}
	return initialPrompt
}

func (te *TaskExecutor) executeTaskWithAgent(setup *taskExecSetup) {
	// task is rebound from the store later in the run, so it is a local, not an alias of setup.task.
	ctx, task, projectID, worktreePath := setup.ctx, setup.task, setup.projectID, setup.worktreePath
	serverURL, requestedModel := setup.serverURL, setup.modelName
	userMsgChan, sandboxEnabled, initialContent := setup.userMsgChan, setup.sandboxEnabled, setup.initialContent

	defer func() {
		if r := recover(); r != nil {
			logging.For(logging.Recovery).With(logging.TaskID, task.ID).Errorf("recovered from panic: %v", r)
			te.sendAndPersistError(task.ID, fmt.Sprintf("Internal error: %v", r), "panic")
		}
	}()

	sessionName := fmt.Sprintf("kanban-%s", task.ID)

	agentName, ok := types.ParseAgentURL(serverURL)
	if !ok {
		te.failTask(task, projectID, "Invalid agent URL: "+serverURL, "invalid_agent_url")
		return
	}

	ext := te.agentRegistry.LookupByName(agentName)
	if ext == nil {
		te.failTask(task, projectID, fmt.Sprintf("Agent %q not found in extensions", agentName), "agent_not_found")
		return
	}

	// The adapter names the binary, so it is loaded before anything asks the container about it.
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

	// Kept on the state after the event loop exits: compaction can be asked for while idle.
	te.taskMu.Lock()
	if state, exists := te.tasks[task.ID]; exists && state.ctx == ctx {
		state.agentRunner = runner
	}
	te.taskMu.Unlock()

	logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("runner started (pid=%d)", runner.PID())

	if requestedModel != "" {
		logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("setting model=%s", requestedModel)
		setModelCtx, setModelCancel := context.WithTimeout(ctx, 5*time.Second)
		runner.SetModel(setModelCtx, requestedModel)
		setModelCancel()
	}

	modelCtx, modelCancel := context.WithTimeout(ctx, 5*time.Second)
	activeModel := runner.QueryModel(modelCtx)
	modelCancel()
	logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("model=%s", activeModel)

	// Needed to resume the agent's own session after a restart.
	sessionFile := runner.SessionFile()
	if sessionFile != "" {
		logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("sessionFile=%s", sessionFile)
	}

	taskMsgs, _ := te.store.GetTaskMessages(task.ID, task.CurrentThreadID)

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

	// The subprocess starts fresh every run: resume a saved session if there is one, otherwise
	// replay the conversation as a transcript.
	useSessionResumption := false
	if len(taskMsgs) > 1 && task.SessionID != "" {
		switchCtx, switchCancel := context.WithTimeout(ctx, 5*time.Second)
		if runner.SwitchSession(switchCtx, task.SessionID) {
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("switching to session %s", task.SessionID)
			time.Sleep(500 * time.Millisecond)
			useSessionResumption = true
		} else {
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Warnf("switch_session failed, falling back to context replay")
		}
		switchCancel()
	}
	if sessionFile != "" && task.SessionID == "" {
		task.SessionID = sessionFile
		te.storeTask(task, projectID)
		logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("saved sessionFile=%s", sessionFile)
	}

	initialPrompt := agentInitialPrompt(task, taskMsgs, initialContent, useSessionResumption)

	err = runner.Prompt(ctx, initialPrompt)
	if err != nil {
		te.failTask(task, projectID, fmt.Sprintf("Failed to send prompt to agent: %v", err), "agent_prompt_failed")
		return
	}

	// An agent that has not reported a status within a bounded window is a failed start; the wait owns
	// that window and has already reported why if it says stop.
	if !te.validateAgentStart(ctx, runner, task, projectID, sandboxEnabled) {
		return
	}

	startTime := time.Now()
	turnStartTime := startTime
	var firstTokenTime time.Time // tracks when first token arrives (prompt processing ends)
	agentEnded := false
	var stopReason string
	var totalPromptTokens, totalPredictedTokens int

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

	statsCtx, statsCancel := context.WithCancel(ctx)
	defer statsCancel()
	statsCtxInner, cancel := context.WithTimeout(statsCtx, 3*time.Second)
	sendAgentStats(runner.QueryStats(statsCtxInner))
	cancel()

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

			if streamEvent := toStreamEvent(event, sandboxEnabled); streamEvent.Type != "" {
				te.sendAgentStreamEvent(projectID, task.ID, streamEvent)
			}

			switch event.Type {
			case pkgagent.EventTypePersist:
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
				pt, ptok := te.persistAgentTurnEntries(projectID, task.ID, task.CurrentThreadID, event.PersistEntries, turnStartTime, sandboxEnabled)
				totalPromptTokens += pt
				totalPredictedTokens += ptok
				firstTokenTime = time.Time{} // reset for next turn

				checkpointMsgID := uuid.New().String()
				te.createCheckpoint(ctx, projectID, task.ID, checkpointMsgID)

				if event.Status.State == "idle" {
					stopReason = "stop"
				} else if event.Status.State == "running" {
					stopReason = "toolUse"
				}

				logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("persist complete, stopReason=%s", stopReason)

				select {
				case um := <-userMsgChan:
					if um.ContextChanged {
						logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("context changed notification, skipping")
					} else {
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
					te.handleAgentTurnComplete(task, projectID, worktreePath, runner, stopReason, &agentEnded)
					// idle keeps the loop alive for follow-ups; a terminal state exits and
					// sends its own complete
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
		}
	}

	duration := time.Since(startTime)
	logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("completed stopReason=%s duration=%s", stopReason, duration)

	task, _ = te.store.GetTaskByID(task.ID)
	if task.ExecutionState == types.ExecStateRunning {
		task.ExecutionState = types.ExecStateFailed
		task.Status = types.StatusReview
		task.Output = fmt.Sprintf("Agent process exited unexpectedly (stopReason=%s)", stopReason)
		te.storeTask(task, projectID)
	}

	// SessionCost() is the accumulator; get_session_stats reports cost as NaN.
	te.accumulateAgentSessionCost(projectID, task, runner.SessionCost(), runner.ModelName(), runner.ProviderName())

	sendAgentComplete(task.ExecutionState, totalPromptTokens, totalPredictedTokens, duration)

	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)

	if task.ParentTaskID != "" {
		_ = te.AutoStartNextSibling(projectID, task.ID, task.ParentTaskID)
	}
}

// agentBinaries is the container-side question the agent pre-flight asks. A test answers it to
// record which name was asked about, which is the whole behaviour under test.
type agentBinaries interface {
	BinaryAvailable(taskID, binary string) bool
}

// errAgentBinaryNotAvailable is the refusal that a task's sandbox container does not hold the
// binary its adapter names. It keeps its own code to the browser: a missing binary reads
// differently from an agent that failed to start.
var errAgentBinaryNotAvailable = errors.New("agent binary not available in the sandbox container")

// agentStartErrorCode picks the code a start that never reached the event loop reports.
func agentStartErrorCode(err error) string {
	if errors.Is(err, errAgentBinaryNotAvailable) {
		return "agent_not_available"
	}
	return "agent_start_failed"
}

// binaryProber answers the container question: whatever was injected, else the task sandbox
// manager, else nothing — and "nothing" is not "available".
func (te *TaskExecutor) binaryProber() agentBinaries {
	if te.agentBinaries != nil {
		return te.agentBinaries
	}
	if te.sandboxMgr != nil {
		return te.sandboxMgr
	}
	return nil
}

// prepareAgent loads the adapter and, for a sandboxed task, checks the container holds the binary
// that adapter named. The order is the rule: the adapter is the only place the name exists.
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

// accumulateAgentSessionCost adds a run's spend to the project's token stats and to the task.
//
// The task total is task.SessionCost + finalCost, never finalCost alone: the task arrives with
// whatever earlier runs left there, and overwriting it resets the session badge to one run's spend.
func (te *TaskExecutor) accumulateAgentSessionCost(projectID string, task *types.Task, finalCost float64, modelName, providerName string) {
	if finalCost > 0 {
		if providerName == "" {
			providerName = "Pi Coding Agent"
		}
		te.store.AccumulateProjectStats(projectID, modelName, providerName, finalCost)
		logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("accumulated session cost=%f for model=%s provider=%s", finalCost, modelName, providerName)
	}
	task.SessionCost += finalCost
	te.storeTask(task, projectID)
}

func (te *TaskExecutor) handleAgentTurnComplete(task *types.Task, projectID, worktreePath string, runner *agent.AgentRunner, stopReason string, agentEnded *bool) {
	if task.AutoCommit {
		// A commit-and-done click merges whatever auto_merge says: read it here, consume it after.
		commitDone := task.HasMergeConflict && te.merge.CommitAndDonePending(task.ID)
		if task.HasMergeConflict {
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("agent finished resolving conflicts, committing resolution")
			// Same stage + commit steps as the LLM loop (commitConflictResolution). The stash
			// deliberately differs: this flow pops it because the agent keeps working and
			// CommitAndMerge runs next, where the LLM loop drops it.
			commitOK := te.merge.stageResolvedConflicts(task.ID, worktreePath)
			if commitOK {
				commitOK = te.merge.commitMergeConflictResolution(task.ID, worktreePath)
			}
			if commitOK {
				logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("conflict resolution committed")
				// Found by label: this runs long after the sync-merge that pushed — see
				// NewestStashHashForMessage. Released explicitly, not deferred: a lock held past
				// here would span the merges below, and merge must come before the stash lock.
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
		commitMsg := te.merge.GenerateCommitMessage(context.Background(), task)
		mergeErr := te.merge.CommitAndMerge(projectID, task.ID, worktreePath, commitMsg, task.AutoMerge || commitDone)
		task, _ = te.store.GetTaskByID(task.ID)
		if task.HasMergeConflict {
			conflictedFiles := GetConflictedFiles(worktreePath)
			prompt, _ := prompts.RenderMergeConflictPrompt(prompts.MergeConflictData{
				TaskBranch:      task.Branch,
				TargetBranch:    te.merge.ResolveMergeTarget(projectID, task),
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
		commitDone = te.merge.takeCommitAndDone(task.ID, mergeErr == nil)
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
		res := te.merge.commitConflictResolution(projectID, task.ID, worktreePath, task)
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
	// agentEnded stays false: the subprocess keeps its context, so the loop waits for a
	// follow-up, a stop, or the process exiting.
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
			// The adapter knows nothing about sandbox mode, so the flag is added here — see the
			// function comment.
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

			if toolCallData != nil {
				te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeToolCall, Data: toolCallData})
			}
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

// buildContextReplay renders the conversation as text. It is the fallback when session resumption
// is unavailable: the subprocess starts with no memory of its own.
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
func toStreamEvent(event pkgagent.Event, sandbox bool) types.StreamEvent {
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

// validateAgentStart waits for the agent to show it is alive: a status of "running", or persist entries
// from an agent that answered before saying anything. It returns false when the run must not continue, and
// in every such case it has already reported the cause (a failed start, or a task the user stopped) — the
// caller's only job is to return. The window exists because a subprocess that never answers is not
// "slow", it is absent: nothing else in the run would notice, and the task would sit "running" forever.
func (te *TaskExecutor) validateAgentStart(ctx context.Context, runner *agent.AgentRunner, task *types.Task, projectID string, sandboxEnabled bool) bool {
	// An agent that has not reported a status within this timeout is a failed start.
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
				return false
			}
			logging.For(logging.Agent).With(logging.TaskID, task.ID).Infof("startup event: %s", event.Type)
			if streamEvent := toStreamEvent(event, sandboxEnabled); streamEvent.Type != "" {
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
				return false
			}
		case <-startupCtx.Done():
			if startupCtx.Err() == context.DeadlineExceeded {
				logging.For(logging.Agent).With(logging.TaskID, task.ID).Errorf("startup timeout (no status within 30s)")
				runner.Close()
				te.failTask(task, projectID, "Agent failed to start (timeout)", "agent_startup_timeout")
			} else {
				// Stopped by user during agent startup
				te.handleCancelledTask(task.ID, projectID)
				return false
			}
		}
	}
	return true
}
