package exec

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mike/backend/llama"
	"mike/backend/prompts"
	"mike/backend/sandbox"
	"mike/backend/stream"
	"mike/backend/types"

	"mike/pkg/bridge"

	"github.com/google/uuid"
	"mike/pkg/logging"
)

// sandboxRecreate describes a mid-turn rebuild of a running task's container.
type sandboxRecreate struct {
	reason string // reason for rebuild (e.g. "mode change")
	// connErrCode is the error code reported if the new bridge doesn't answer.
	connErrCode string
	opts        sandbox.SandboxOpts
	// underLock applies state changes in the same write as the new callbacks.
	underLock func(*sandboxState)
}

// recreateTaskSandbox rebuilds the task container and updates the sandbox tool callbacks.
// Failures are reported to the user and end the turn. Changed assigned ports are persisted.
func (te *TaskExecutor) recreateTaskSandbox(setup *taskExecSetup, state *executionState, r sandboxRecreate) error {
	lg := logging.For(logging.Sandbox).With(logging.TaskID, setup.task.ID)

	if err := te.sandboxMgr.Stop(setup.task.ID); err != nil {
		lg.Infof("%s: failed to stop old sandbox: %v", r.reason, err)
	}

	assignedPorts, createErr := te.sandboxMgr.Create(setup.task.ID, setup.worktreePath, setup.project, r.opts)
	if createErr != nil {
		lg.Infof("%s: failed to recreate sandbox: %v", r.reason, createErr)
		te.failSandboxRecreate(setup,
			fmt.Sprintf("Failed to recreate sandbox for %s: %v", r.reason, createErr),
			fmt.Sprintf("Sandbox recreation failed: %v", createErr),
			"sandbox_recreate_failed")
		return createErr
	}

	newAgentClient, clientErr := te.sandboxMgr.GetClient(setup.task.ID)
	if clientErr != nil {
		lg.Infof("%s: failed to connect to new sandbox bridge: %v", r.reason, clientErr)
		te.failSandboxRecreate(setup,
			fmt.Sprintf("Failed to connect to sandbox bridge after %s: %v", r.reason, clientErr),
			fmt.Sprintf("Agent connection failed after %s: %v", r.reason, clientErr),
			r.connErrCode)
		return clientErr
	}

	// Callbacks and the caller's change go in one write: new callbacks beside a stale operating mode
	// would mean writing through a container mounted for the old mode.
	state.setSandbox(func(sb *sandboxState) {
		sb.exec, sb.execStream, sb.read, sb.write, sb.edit = makeSandboxCallbacks(setup.task.ID, newAgentClient)
		if r.underLock != nil {
			r.underLock(sb)
		}
	})

	// Persist and broadcast assigned ports if the recreation changed any.
	if len(assignedPorts) > 0 {
		setup.task.AssignedPorts = assignedPorts
		te.persistTask(setup)
		te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
	}
	return nil
}

// failSandboxRecreate puts a task in review after a failed container rebuild.
// The worktree is preserved so the configuration can be fixed and the task resumed.
func (te *TaskExecutor) failSandboxRecreate(setup *taskExecSetup, output, userMessage, code string) {
	setup.task.ExecutionState = types.ExecStateFailed
	setup.task.Status = types.StatusReview
	setup.task.Output = output
	te.persistTask(setup)
	te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
	te.sendAndPersistError(setup.task.ID, userMessage, code)
}

// storeTask writes a task back to the store, preserving Title and TitleGenerating.
// It cannot be used to start a run.
func (te *TaskExecutor) storeTask(task *types.Task, projectID string) error {
	return te.storeTaskWith(task, projectID, false)
}

// storeTaskClaiming is storeTask for the caller that owns the run; may set the execution state.
func (te *TaskExecutor) storeTaskClaiming(task *types.Task, projectID string) error {
	return te.storeTaskWith(task, projectID, true)
}

func (te *TaskExecutor) storeTaskWith(task *types.Task, projectID string, claim bool) error {
	if cur, err := te.store.GetTask(projectID, task.ID); err == nil {
		task.Title = cur.Title
		task.TitleGenerating = cur.TitleGenerating
	}
	if claim {
		return te.store.UpdateTaskClaimingExecution(task)
	}
	return te.store.UpdateTask(task)
}

// persistTask is storeTask for the executor's task copy.
func (te *TaskExecutor) persistTask(setup *taskExecSetup) {
	te.storeTask(setup.task, setup.projectID)
}

// persistTaskClaiming is persistTask for when the loop starts or resumes a run.
func (te *TaskExecutor) persistTaskClaiming(setup *taskExecSetup) {
	te.storeTaskClaiming(setup.task, setup.projectID)
}

// startGateQueuePoller reports queue position and wait estimate once a second.
// The returned onEnqueued callback must be passed to gate.Acquire.
func (te *TaskExecutor) startGateQueuePoller(gate *ConcurrencyGate, providerURL, projectID, taskID string, ctx context.Context) (onEnqueued func(*queueEntry), done chan struct{}) {
	entries := make(chan *queueEntry, 1)
	done = make(chan struct{})
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		var entry *queueEntry
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case e := <-entries:
				entry = e
				continue
			case <-tick.C:
			}
			pos := gate.Position(providerURL, entry)
			est := gate.EstimateWait(providerURL, entry)
			te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeQueue, Data: map[string]any{
				"position":               pos,
				"estimated_wait_seconds": est,
			}})
		}
	}()
	return func(e *queueEntry) {
		select {
		case entries <- e:
		default:
		}
	}, done
}

// The output an in-flight row is stopped with. A person's stop and a server fault are different facts about
// the same transition, and the row is where that distinction survives: the task is the same paused,
// resumable row either way, and `output` is what says who stopped it.
const (
	pausedByUserOutput = "Paused by user"
	crashedOutput      = "Stopped by an internal error"
)

// applyStopTransition transitions an in-flight task row out of its running state.
func applyStopTransition(task *types.Task, output string) bool {
	if !task.ExecutionState.InFlight() {
		return false
	}
	switch task.Status {
	case types.StatusInProgress:
		task.ExecutionState = types.ExecStatePaused
		task.Output = output
	case types.StatusDone:
		task.ExecutionState = types.ExecStateCompleted
	default:
		task.ExecutionState = types.ExecStateIdle
	}
	task.UpdatedAt = time.Now().UTC()
	return true
}

// executorExitGrace is the timeout for waiting for an executor to finish writing.
const executorExitGrace = 3 * time.Second

// cancelExistingExecutor cancels the registered executor for taskID.
// Agent executors are closed to stop their event loops.
func (te *TaskExecutor) cancelExistingExecutor(taskID string) *executionState {
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	state, exists := te.tasks[taskID]
	if !exists {
		return nil
	}
	if state.agentRunner != nil && !state.runnerClosed {
		state.agentRunner.Close()
		state.runnerClosed = true
	}
	state.cancel()
	return state
}

// waitForExecutorExit blocks until the executor finishes writing or the timeout expires.
func waitForExecutorExit(taskID string, state *executionState, timeout time.Duration) {
	if state == nil || state.finished == nil {
		return
	}
	select {
	case <-state.finished:
	case <-time.After(timeout):
		logging.For(logging.Exec).With(logging.TaskID, taskID).Infof("executor still writing after %s; applying the transition anyway", timeout)
	}
}

// sendStopStatusEvent broadcasts a WS status event when the executor exits.
func (te *TaskExecutor) sendStopStatusEvent(projectID, taskID string, state types.ExecutionState) {
	activity := "Task stopped"
	if state == types.ExecStatePaused {
		activity = "Task paused by user"
	}
	// The WebSocket frame carries a plain string; the conversion is written out so the
	// boundary between the typed state and the wire form stays visible.
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.TurnStatusEvent(string(state), activity))
}

// persistStopTransition applies the stop transition to the store.
func (te *TaskExecutor) persistStopTransition(task *types.Task) bool {
	// Only the paused transition carries user-visible output text; idle and
	// completed keep the row's previous output.
	var output *string
	if task.ExecutionState == types.ExecStatePaused {
		o := task.Output
		output = &o
	}
	changed, uerr := te.store.TransitionInFlightTask(task.ID, task.ProjectID, task.ExecutionState, output)
	if uerr != nil {
		logging.For(logging.Exec).With(logging.TaskID, task.ID).Warnf("in-flight transition failed: %v", uerr)
		return false
	}
	if changed {
		te.appStreamMgr.BroadcastTaskUpdated(task.ProjectID, task)
	}
	return changed
}

// handleCancelledTask applies the stop transition for a user-cancelled run.
func (te *TaskExecutor) handleCancelledTask(taskID, projectID string) {
	te.finishStoppedTask(taskID, projectID, pausedByUserOutput)
}

// handleCrashedTask applies the stop transition for a server fault.
// The row remains paused and resumable.
func (te *TaskExecutor) handleCrashedTask(taskID, projectID string) {
	te.finishStoppedTask(taskID, projectID, crashedOutput)
}

func (te *TaskExecutor) finishStoppedTask(taskID, projectID, output string) {
	fresh, err := te.store.GetTaskByID(taskID)
	if err != nil {
		return
	}
	if applyStopTransition(fresh, output) {
		te.persistStopTransition(fresh)
	}
	te.sendStopStatusEvent(projectID, taskID, fresh.ExecutionState)
}

// modelSwitchResult holds the outputs of applyModelSwitch.
type modelSwitchResult struct {
	client        *llama.LlamaClient
	providerName  string
	contextWindow *int
}

// applyModelSwitch creates a new LlamaClient and updates the model configuration.
func (te *TaskExecutor) applyModelSwitch(mc *modelConfig, task *types.Task, projectID, serverURL, modelName string, persistToDB, discoverContext bool, silence *streamSilenceWatcher) modelSwitchResult {
	reasoningEffort := task.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = te.configStore.GetModelReasoningEffort(modelName)
	}
	apiKey := te.configStore.GetProviderAPIKey(serverURL)
	client := te.newLlamaClient(serverURL, modelName, reasoningEffort, apiKey, silence)
	providerName := te.configStore.GetProviderIDByURL(serverURL)

	var contextWindow *int
	if discoverContext {
		contextWindow = te.configStore.GetModelContextWindow(modelName, serverURL)
		if contextWindow == nil {
			if discovered := te.configStore.DiscoverContextWindow(modelName, serverURL); discovered != nil {
				contextWindow = discovered
				te.configStore.SaveModelContextWindow(modelName, serverURL, *discovered)
				logging.For(logging.Exec).Infof("discovered context window for %s: %d", modelName, *discovered)
			}
		}
	}

	mc.set(serverURL, modelName)
	if persistToDB {
		task.LlamaServerURL = serverURL
		task.ModelName = modelName
		task.ReasoningEffort = reasoningEffort
		te.storeTask(task, projectID)
	}

	return modelSwitchResult{
		client:        client,
		providerName:  providerName,
		contextWindow: contextWindow,
	}
}

// broadcastDiffIfGitCmd broadcasts a diff if a git command changed the working tree.
func (te *TaskExecutor) broadcastDiffIfGitCmd(projectID, taskID string, task *types.Task) {
	te.taskMu.Lock()
	state := te.tasks[taskID]
	te.taskMu.Unlock()
	if state != nil {
		te.checkAndBroadcastDiff(projectID, taskID, task, state)
	}
}

// checkAndBroadcastDiff computes and broadcasts the task diff if the hash changed.
// This avoids redundant broadcasts from fsnotify.
func (te *TaskExecutor) checkAndBroadcastDiff(projectID, taskID string, task *types.Task, state *executionState) {
	diffResult, err := te.worktreeMgr.GetTaskDiff(taskID, te.worktreeMgr.TaskDiffBaseline(task), "")
	if err != nil || (diffResult.Diff == "" && len(diffResult.Untracked) == 0) {
		return
	}
	hash := stream.DiffHash(diffResult)
	// Skip if hash unchanged — fsnotify already broadcast this state
	if hash == state.lastDiffHash {
		return
	}
	state.lastDiffHash = hash
	fileHashes := diffResult.GetPerFileHashes()
	stats := stream.ComputeDiffStats(diffResult)
	filesN := stats.VisibleTracked + stats.VisibleUntracked
	// Check if task branch commits are already merged into the target branch.
	if targetBranch, ok := stream.CheckAlreadyMerged(*task); ok {
		if project, err := te.store.GetProject(projectID); err == nil && project.Path != "" {
			stats.AlreadyMerged = te.worktreeMgr.IsMergedInto(project.Path, task.Branch, targetBranch)
		}
	}
	te.appStreamMgr.BroadcastDiffStats(stream.DiffStatsEvent{
		ProjectID:           projectID,
		TaskID:              taskID,
		DiffHash:            hash,
		ShortStat:           diffResult.ShortStat,
		FilesN:              filesN,
		UntrackedN:          stats.VisibleUntracked,
		UntrackedLines:      stats.UntrackedLines,
		TrackedAdd:          stats.TrackedAdd,
		TrackedDel:          stats.TrackedDel,
		ChangedHashes:       fileHashes,
		StagedPaths:         diffResult.StagedPaths,
		HasCommittedChanges: diffResult.HasCommittedWork(),
		AlreadyMerged:       stats.AlreadyMerged,
	})
}

// computeTaskDiffStats computes the current diff stats broadcast payload.
func (te *TaskExecutor) computeTaskDiffStats(projectID, taskID string) *stream.DiffStatsEvent {
	task, err := te.store.GetTaskByID(taskID)
	if err != nil {
		return nil
	}
	project, err := te.store.GetProject(projectID)
	if err != nil || project == nil || project.Path == "" {
		return nil
	}
	baseRef := te.worktreeMgr.TaskDiffBaseline(task)
	headRef := ""
	if task.Status == types.StatusDone {
		headRef = task.BranchTip
		if headRef == "" {
			headRef = task.Branch
		}
	}
	diffResult, err := te.worktreeMgr.GetTaskDiff(taskID, baseRef, headRef)
	if err != nil {
		return nil
	}
	if !diffResult.HasChanges() {
		return nil
	}
	stats := stream.ComputeDiffStats(diffResult)
	// Check if task branch commits are already merged into the target branch.
	if targetBranch, ok := stream.CheckAlreadyMerged(*task); ok {
		stats.AlreadyMerged = te.worktreeMgr.IsMergedInto(project.Path, task.Branch, targetBranch)
	}
	return &stream.DiffStatsEvent{
		ProjectID:           projectID,
		TaskID:              taskID,
		DiffHash:            stream.DiffHash(diffResult),
		ShortStat:           diffResult.ShortStat,
		FilesN:              stats.VisibleTracked + stats.VisibleUntracked,
		UntrackedN:          stats.VisibleUntracked,
		UntrackedLines:      stats.UntrackedLines,
		TrackedAdd:          stats.TrackedAdd,
		TrackedDel:          stats.TrackedDel,
		ChangedHashes:       diffResult.GetPerFileHashes(),
		StagedPaths:         diffResult.StagedPaths,
		HasCommittedChanges: diffResult.HasCommittedWork(),
		AlreadyMerged:       stats.AlreadyMerged,
	}
}

// broadcastParentDiffStats recomputes and broadcasts the parent task's diff stats
// after a subtask merge.
func (te *TaskExecutor) broadcastParentDiffStats(projectID, parentID string) {
	if te.appStreamMgr == nil || parentID == "" {
		return
	}
	if event := te.computeTaskDiffStats(projectID, parentID); event != nil {
		te.appStreamMgr.BroadcastDiffStats(*event)
	}
}

// Advisory kinds. The kind is what the UI titles the note with; it never changes what
// the model receives.
const (
	advisoryDiagnostics   = "diagnostics"
	advisoryMergeConflict = "merge_conflict"
	advisoryAutoAnswer    = "auto_answer"
)

// advisory is a note about the workspace the model must see.
// Buffered during tool execution and delivered at the turn boundary.
type advisory struct {
	kind     string // advisoryDiagnostics | advisoryMergeConflict | advisoryAutoAnswer — how the UI titles the note
	severity string // mirrors TaskMessage.Severity: "" (default), "info", "error" — drives emphasis
	text     string // what the model reads and the UI shows
}

// queueAdvisory buffers a note for delivery at the turn boundary.
func queueAdvisory(lc *loopContext, a advisory) {
	if strings.TrimSpace(a.text) == "" {
		return
	}
	lc.pendingAdvisories = append(lc.pendingAdvisories, a)
}

// flushAdvisories delivers buffered notes, persisting them as advisory rows.
// Called after assistant/tool messages but before summarization.
func (te *TaskExecutor) flushAdvisories(setup *taskExecSetup, lc *loopContext) {
	if len(lc.pendingAdvisories) == 0 {
		return
	}
	for _, a := range lc.pendingAdvisories {
		msgID := uuid.New().String()
		now := time.Now().UTC()
		_ = te.store.CreateTaskMessage(&types.TaskMessage{
			ID: msgID, TaskID: setup.task.ID, ThreadID: setup.task.CurrentThreadID,
			Role: "advisory", Severity: a.severity, Content: a.text, Kind: a.kind, CreatedAt: now,
		})
		lc.messages = append(lc.messages, types.ChatMessage{Role: "advisory", Content: a.text})
		te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{
			Type: types.WSTypeAdvisory,
			Data: map[string]any{
				"id":         msgID,
				"kind":       a.kind,
				"severity":   a.severity,
				"text":       a.text,
				"created_at": now.Format(time.RFC3339Nano),
			},
		})
	}
	lc.pendingAdvisories = nil
}

// autoAnswerTextStop answers a text-only turn from the parent's context.
// Returns turnNextLLMCall if the answer landed, turnStop if cancelled, and turnWaitIdle otherwise.
func (te *TaskExecutor) autoAnswerTextStop(setup *taskExecSetup, lc *loopContext, assistantText string) turnOutcome {
	if !setup.task.Autostarted || setup.task.ParentTaskID == "" || setup.task.IsPlanMode() || setup.task.IsExploreMode() {
		return turnWaitIdle
	}
	if strings.TrimSpace(assistantText) == "" || setup.ctx.Err() != nil {
		return turnWaitIdle
	}

	answers, err := te.automatedAnswers(setup.ctx, setup.task, []string{assistantText})
	if err == nil && len(answers) != 1 {
		err = fmt.Errorf("got %d answers for 1 question", len(answers))
	}
	if err != nil {
		// A run nobody is watching still ends where a person can see it.
		logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Warnf("automated answer to a text-only stop failed: %v, leaving the task for the user", err)
		return turnWaitIdle
	}

	// The answer is what the user would have typed, and the user never typed it: an
	// advisory row is the one representation that says so in both directions.
	queueAdvisory(lc, advisory{kind: advisoryAutoAnswer, text: answers[0]})
	te.flushAdvisories(setup, lc)

	// The turn released the running-task slot on its way to idle; reclaim it before
	// claiming the run again.
	if curURL, _ := lc.mc.get(); curURL != "" && !te.syncTaskSlot(setup, curURL) {
		te.handleCancelledTask(setup.task.ID, setup.projectID)
		return turnStop
	}
	setup.task.Status = types.StatusInProgress
	setup.task.ExecutionState = types.ExecStateRunning
	te.persistTaskClaiming(setup)
	te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.TurnStatusEvent("running", "Automated answer from parent context"))
	logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("automated answer to a text-only stop from parent context, continuing")
	return turnNextLLMCall
}

// ReviewContextFor reads the task's thread for the merge-conflict prompt.
func (te *TaskExecutor) ReviewContextFor(task *types.Task) string {
	rows, err := te.store.GetTaskMessages(task.ID, task.CurrentThreadID)
	if err != nil {
		logging.For(logging.Merge).With(logging.TaskID, task.ID).Warnf("could not read the thread for the conflict prompt: %v", err)
		return ""
	}
	return reviewSummaries(rows)
}

// prepareMergeConflictTurn prepares the model to resolve a conflict and resets turn state.
// The note is flushed immediately so the model sees it before the next call.
func (te *TaskExecutor) prepareMergeConflictTurn(setup *taskExecSetup, lc *loopContext, conflictedFiles []string) {
	prompt, err := prompts.RenderMergeConflictPrompt(prompts.MergeConflictData{
		TaskBranch:      setup.task.Branch,
		TargetBranch:    te.merge.ResolveMergeTarget(setup.projectID, setup.task),
		ReviewContext:   te.ReviewContextFor(setup.task),
		TaskTitle:       setup.task.Title,
		TaskDescription: setup.task.Prompt,
		ConflictedFiles: conflictedFiles,
	})
	if err != nil {
		// No instructions, but still continue: the conflict is in the worktree either way,
		// and the model can read `git status` for itself.
		logging.For(logging.Merge).With(logging.TaskID, setup.task.ID).Warnf("failed to render merge conflict prompt: %v", err)
	} else {
		queueAdvisory(lc, advisory{kind: advisoryMergeConflict, severity: "error", text: prompt})
	}
	lc.detector.reset()
	lc.detector.clearHints()
	te.flushAdvisories(setup, lc)
	te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.TurnStatusEvent("running", "Resolving merge conflicts"))
	lc.pendingToolCalls = nil
	lc.assistantContent.Reset()
	lc.reasoningContent.Reset()
	lc.finishReason = ""
}

// userMessageToChatMessage converts a UserMessage to a ChatMessage, handling attachments.
func userMessageToChatMessage(um stream.UserMessage) types.ChatMessage {
	if parts, ok := types.ContentParts(um.Content, um.Attachments); ok {
		return types.ChatMessage{Role: "user", ContentParts: parts}
	}
	return types.ChatMessage{Role: "user", Content: um.Content}
}

// handleLoopDetected manages detected loops by injecting hints or terminating the task.
// Returns true if the task should terminate.
func handleLoopDetected(te *TaskExecutor, task *types.Task, projectID, taskID string, detector *loopDetector, loopType LoopType, target string, messages *[]types.ChatMessage, exploreMode bool) bool {
	// In exploration/plan mode, suppress all loop hints — let the LLM freely explore / plan
	if exploreMode || task.IsPlanMode() {
		detector.reset()
		return false // continue without intervention
	}

	detector.loopStreak++
	if detector.loopStreak >= maxLoopHints {
		task.ExecutionState = types.ExecStateFailed
		task.Status = types.StatusReview
		task.Output = "Model stuck in loop (max hints exhausted)"
		te.storeTask(task, projectID)
		te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		te.sendAndPersistError(taskID, "Model stuck in loop (max hints exhausted)", "loop_detected")
		return true // terminate
	}

	hint := buildHint(loopType, target)
	logging.For(logging.Exec).With(logging.TaskID, taskID).Infof("detected loop type=%d (streak %d/%d), target=%s", loopType, detector.loopStreak, maxLoopHints, target)
	hintIdx := len(*messages)
	*messages = append(*messages, types.ChatMessage{Role: "user", Content: hint})
	detector.recordHintIndex(hintIdx)

	hintMsgID := uuid.New().String()
	te.store.CreateTaskMessage(&types.TaskMessage{
		ID: hintMsgID, TaskID: taskID, ThreadID: task.CurrentThreadID, Role: "user",
		Content: hint, IsLoopHint: true, CreatedAt: time.Now().UTC(),
	})
	// Preserve loopStreak across hint cycles so consecutive loops accumulate.
	// A valid turn (via recordTurn) resets it to 0.
	streak := detector.loopStreak
	detector.reset()
	detector.loopStreak = streak
	// Broadcast hint with its DB message ID so the client can dedupe on history
	// reload. Loop hints are user-role messages; send the role explicitly.
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeHint, Data: map[string]any{
		"id":         hintMsgID,
		"role":       "user",
		"content":    hint,
		"created_at": time.Now().UTC().Format(time.RFC3339),
	}})
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.TurnStatusEvent("running", "Loop detected, steering agent"))
	return false // continue
}

// sandboxCreateOpts returns the sandbox configuration for a task container.
func (te *TaskExecutor) sandboxCreateOpts(setup *taskExecSetup, image string, readOnly bool) sandbox.SandboxOpts {
	// The host folders this container may see are the ones the project configured, plus the ones the
	// user approved for the agent this task runs. No other installed extension contributes a folder.
	opts := setup.sandboxOpts
	opts.BaseImage = image
	opts.AgentSandbox = te.agentRegistry.AgentSandboxForURL(setup.providerURL())
	opts.WorktreeReadOnly = readOnly
	return opts
}

// setupSandbox ensures the sandbox image exists, starts the container, and connects the agent.
func (te *TaskExecutor) setupSandbox(setup *taskExecSetup) (string, *bridge.Client, error) {
	task := setup.task
	projectID := setup.projectID
	worktreePath := setup.worktreePath

	if te.sandboxMgr == nil || !te.sandboxMgr.IsAvailable() {
		return "", nil, fmt.Errorf("gVisor not available")
	}

	// Make the project's sandbox image exist, building it from its Dockerfile or pulling it. This
	// is the one place a task's image is decided; the terminal and the explorer share the same build.
	imageToUse, imgErr := te.sandboxMgr.EnsureImageForProject(setup.project, func(status types.SandboxImageStatus) {
		switch status.Phase {
		case types.SandboxImagePhaseBuilding, types.SandboxImagePhasePulling:
			if status.Line != "" {
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{
					Type: types.WSTypeBuildProgress, Data: map[string]string{"line": status.Line}})
				return
			}
			activity := "Pulling " + status.Image
			if status.Phase == types.SandboxImagePhaseBuilding {
				activity = "Building sandbox image from Dockerfile"
			}
			// The phase doubles as the task state, which is what the card badge shows.
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.TurnStatusEvent(status.Phase, activity))
		}
	})
	if imgErr != nil {
		return "", nil, fmt.Errorf("failed to prepare sandbox image: %w", imgErr)
	}

	// Reuse existing container (lazy-start). Create only if none exists or if stopped/crashed.
	var assignedPorts []types.AssignedPort
	if !te.sandboxMgr.ContainerIsRunning(task.ID) {
		var createErr error
		assignedPorts, createErr = te.sandboxMgr.Create(task.ID, worktreePath, setup.project, te.sandboxCreateOpts(setup, imageToUse, task.IsPlanMode()))
		if createErr != nil {
			return "", nil, fmt.Errorf("failed to create sandbox: %w", createErr)
		}
		logging.For(logging.Sandbox).With(logging.TaskID, task.ID).Infof("sandbox created with image %s", imageToUse)
		// Store and broadcast assigned ports (carried on the task object)
		if len(assignedPorts) > 0 {
			task.AssignedPorts = assignedPorts
			te.storeTask(task, setup.projectID)
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		}
		te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.TurnStatusEvent("running", "Sandbox container started"))
	} else {
		logging.For(logging.Sandbox).With(logging.TaskID, task.ID).Infof("reusing existing sandbox container")
	}

	// Get agent client for tool calls (required — sandbox creation ensures agent is running)
	agentClient, err := te.sandboxMgr.GetClient(task.ID)
	if err != nil {
		return "", nil, fmt.Errorf("failed to connect to sandbox bridge: %w", err)
	}

	return imageToUse, agentClient, nil
}
