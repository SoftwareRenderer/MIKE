package exec

import (
	"context"
	"fmt"
	"strings"
	"time"

	"kanban/backend/llama"
	"kanban/backend/prompts"
	"kanban/backend/sandbox"
	"kanban/backend/stream"
	"kanban/backend/types"

	"kanban/pkg/bridge"

	"github.com/google/uuid"
	"kanban/pkg/logging"
)

// sandboxRecreate describes a mid-turn rebuild of a running task's container: the task
// changed in a way the container was created around, so it has to be built again.
type sandboxRecreate struct {
	reason string // the change in user-facing text, e.g. "mode change"
	// connErrCode is stored when the new container is up but its bridge will not answer.
	// It differs per caller because the two causes read differently in a message.
	connErrCode string
	opts        sandbox.SandboxOpts
	// underLock applies the caller's own sandbox state inside the same write as the new callbacks,
	// so the two are never visible apart. It takes the sandboxState being written, not the
	// executionState: sandboxMu is already held here and is not reentrant.
	underLock func(*sandboxState)
}

// recreateTaskSandbox stops a running task container, creates it again with the given
// options, and points the sandbox tool callbacks at the new bridge connection. The
// operating-mode and agent-sandbox-config checks both need it and differ only in what they
// pass. A failure is reported before being returned — the task moves to failed/review with
// the reason in its Output and the browser gets a persisted error — and callers end the turn
// when an error comes back. Changed assigned ports are persisted.
func (te *TaskExecutor) recreateTaskSandbox(setup *taskExecSetup, state *executionState, r sandboxRecreate) error {
	// The old code built "[%s] setup.task=%s " as TEXT here and concatenated it onto every
	// message; subsystem and task id are attributes now, so the message is just the message.
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
		sb.exec, sb.execStream, sb.read, sb.write, sb.edit = makeSandboxCallbacks(newAgentClient)
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

// failSandboxRecreate lands a task in review after its container could not be rebuilt.
// The work is not lost — the worktree is intact — so the task pauses where someone can fix
// the sandbox configuration and resume it.
func (te *TaskExecutor) failSandboxRecreate(setup *taskExecSetup, output, userMessage, code string) {
	setup.task.ExecutionState = types.ExecStateFailed
	setup.task.Status = types.StatusReview
	setup.task.Output = output
	te.persistTask(setup)
	te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
	te.sendAndPersistError(setup.task.ID, userMessage, code)
}

// storeTask writes task back, preserving fields the executor does not own: Title and
// TitleGenerating belong to the task handlers, so a full-row write from the executor's
// start-time copy would clobber a title changed while the task was queued or running.
// It cannot start a run; use storeTaskClaiming (see Store.UpdateTask).
func (te *TaskExecutor) storeTask(task *types.Task, projectID string) error {
	return te.storeTaskWith(task, projectID, false)
}

// storeTaskClaiming is storeTask for the caller that owns the run: it may put
// the row into an in-flight execution_state.
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

// startGateQueuePoller reports queue position and estimated wait once a second while a
// caller waits for a gate slot (the per-request gate in the loop, the task gate on start).
//
// The returned onEnqueued callback must be passed to gate.Acquire: it hands the caller's
// queue entry to the poller over a channel as soon as it is enqueued, so the position
// reported is the caller's own and not the total waiter count. Close done to stop it.
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

// applyStopTransition transitions an in-flight task row out of its running state
func applyStopTransition(task *types.Task) bool {
	if task.ExecutionState != types.ExecStateRunning && task.ExecutionState != types.ExecStatePullingImage {
		return false
	}
	switch task.Status {
	case types.StatusInProgress:
		task.ExecutionState = types.ExecStatePaused
		task.Output = "Paused by user"
	case types.StatusDone:
		task.ExecutionState = types.ExecStateCompleted
	default:
		task.ExecutionState = types.ExecStateIdle
	}
	task.UpdatedAt = time.Now().UTC()
	return true
}

// executorExitGrace bounds how long a caller waits for an executor to finish
// writing before it applies a state transition anyway. Stop must stay
// responsive and authoritative: the transition happens on timeout too, and the
// guarded UpdateTask keeps a late writer from undoing it.
const executorExitGrace = 3 * time.Second

// cancelExistingExecutor cancels the executor registered for taskID, if any,
// and returns its state so the caller can wait for it. It leaves the map entry
// for the caller to replace or delete. Agent executors keep their event loop
// alive after completion, so they need cancelling too.
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

// waitForExecutorExit blocks until the executor has closed its finished channel
// (every exit-path write has landed) or the timeout expires. The transition is
// applied either way: a wedged executor must not keep a task stuck, and losing
// the ordering is the lesser evil.
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

// sendStopStatusEvent sends the WS status event that tells task subscribers the executor exited
func (te *TaskExecutor) sendStopStatusEvent(projectID, taskID string, state types.ExecutionState) {
	activity := "Task stopped"
	if state == types.ExecStatePaused {
		activity = "Task paused by user"
	}
	// The WebSocket frame carries a plain string; the conversion is written out so the
	// boundary between the typed state and the wire form stays visible.
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": string(state), "activity": activity}})
}

// persistStopTransition applies the stop transition
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

// handleCancelledTask applies the stop transition
func (te *TaskExecutor) handleCancelledTask(taskID, projectID string) {
	fresh, err := te.store.GetTaskByID(taskID)
	if err != nil {
		return
	}
	if applyStopTransition(fresh) {
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

// applyModelSwitch creates a new LlamaClient for the given serverURL/modelName,
// updates the modelConfig, and optionally persists the override to the task DB row.
// If discoverContext is true, it attempts to discover and save the context window.
func (te *TaskExecutor) applyModelSwitch(mc *modelConfig, task *types.Task, projectID, serverURL, modelName string, persistToDB, discoverContext bool) modelSwitchResult {
	reasoningEffort := task.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = te.configStore.GetModelReasoningEffort(modelName)
	}
	apiKey := te.configStore.GetProviderAPIKey(serverURL)
	client := te.newLlamaClient(serverURL, modelName, reasoningEffort, apiKey)
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
	// Use BaseCommit (fixed hash) when available, matches API handler
	project, pErr := te.store.GetProject(projectID)
	projectPath, originBranch := "", ""
	if pErr == nil && project != nil {
		projectPath, originBranch = project.Path, project.OriginBranch
	}
	baseRef, _ := te.worktreeMgr.ResolveDiffBaseline(task.ID, te.worktreeMgr.ResolveTaskBaseline(projectPath, originBranch, task), task.Branch, forkBranch)
	diffResult, err := te.worktreeMgr.GetTaskDiff(taskID, baseRef, "")
	if err != nil || (diffResult.Diff == "" && len(diffResult.Untracked) == 0) {
		return
	}
	hash := stream.ComputeDiffHash(diffResult.Diff, diffResult.CommittedDiff, diffResult.Untracked, diffResult.StagedHash)
	// Skip if hash unchanged — fsnotify already broadcast this state
	if hash == state.lastDiffHash {
		return
	}
	state.lastDiffHash = hash
	fileHashes := diffResult.GetPerFileHashes()
	stats := stream.ComputeDiffStats(diffResult, *task)
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
		HasCommittedChanges: diffResult.CommittedDiff != "",
		AlreadyMerged:       stats.AlreadyMerged,
	})
}

// computeTaskDiffStats computes the current diff-stats broadcast payload for a task,
// including committed-but-unmerged changes (unlike checkAndBroadcastDiff, which only
// fires on working-tree changes). Returns nil when the task has no visible changes
// or its worktree/branch cannot be resolved.
func (te *TaskExecutor) computeTaskDiffStats(projectID, taskID string) *stream.DiffStatsEvent {
	task, err := te.store.GetTaskByID(taskID)
	if err != nil {
		return nil
	}
	project, err := te.store.GetProject(projectID)
	if err != nil || project == nil || project.Path == "" {
		return nil
	}
	thread, _ := te.store.GetThread(task.CurrentThreadID)
	forkBranch := ""
	if thread != nil {
		forkBranch = thread.GitBranch
	}
	baseRef, _ := te.worktreeMgr.ResolveDiffBaseline(taskID, te.worktreeMgr.ResolveTaskBaseline(project.Path, project.OriginBranch, task), task.Branch, forkBranch)
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
	if diffResult.Diff == "" && len(diffResult.Untracked) == 0 && diffResult.CommittedDiff == "" {
		return nil
	}
	stats := stream.ComputeDiffStats(diffResult, *task)
	// Check if task branch commits are already merged into the target branch.
	if targetBranch, ok := stream.CheckAlreadyMerged(*task); ok {
		stats.AlreadyMerged = te.worktreeMgr.IsMergedInto(project.Path, task.Branch, targetBranch)
	}
	return &stream.DiffStatsEvent{
		ProjectID:           projectID,
		TaskID:              taskID,
		DiffHash:            stream.ComputeDiffHash(diffResult.Diff, diffResult.CommittedDiff, diffResult.Untracked, diffResult.StagedHash),
		ShortStat:           diffResult.ShortStat,
		FilesN:              stats.VisibleTracked + stats.VisibleUntracked,
		UntrackedN:          stats.VisibleUntracked,
		UntrackedLines:      stats.UntrackedLines,
		TrackedAdd:          stats.TrackedAdd,
		TrackedDel:          stats.TrackedDel,
		ChangedHashes:       diffResult.GetPerFileHashes(),
		StagedPaths:         diffResult.StagedPaths,
		HasCommittedChanges: diffResult.CommittedDiff != "",
		AlreadyMerged:       stats.AlreadyMerged,
	}
}

// broadcastParentDiffStats recomputes and broadcasts the parent task's diff stats
// after a subtask merge moved the parent's branch. A parent that was never started
// has no diff watcher of its own, so without this its card would not surface the
// merged changes (button stuck on "Move to done" instead of "Commit and move to done").
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
)

// advisory is a note about the workspace that the model must see but the user never
// said: LSP diagnostics after an edit, the instructions for resolving a merge conflict.
//
// Never store one as a user message: it fakes a user bubble, the summarizer believes the
// user said it, and a row written among the turn's tool_call rows costs that turn its
// remaining calls on every rebuild. They are buffered during tool execution and delivered
// by flushAdvisories once the turn's own messages are in the conversation.
type advisory struct {
	kind     string // advisoryDiagnostics | advisoryMergeConflict — how the UI titles the note
	severity string // mirrors TaskMessage.Severity: "" (default), "info", "error" — drives emphasis
	text     string // what the model reads and the UI shows
}

// queueAdvisory buffers a note for delivery at the turn boundary. Safe to call from
// inside tool execution, where writing a message would land it between tool_call rows.
func queueAdvisory(lc *loopContext, a advisory) {
	if strings.TrimSpace(a.text) == "" {
		return
	}
	lc.pendingAdvisories = append(lc.pendingAdvisories, a)
}

// flushAdvisories delivers every buffered note in queue order: persisted as an advisory
// row, appended to the live conversation, and broadcast to the UI. Call it after the
// turn's assistant and tool messages have been appended, so the note reads as commentary
// on work that already happened, and before summarization, so the note is in the
// summarizer's input.
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

// prepareMergeConflictTurn hands the model the instructions for resolving a conflict and
// resets the turn state around it: the loop detector forgets the history (conflict work
// repeating is expected, not a loop) and the half-built assistant/tool buffers are dropped.
//
// The note is flushed, not queued: every caller goes straight into the next LLM call, so a
// queued note would arrive one call late and the model would resolve a conflict before being
// told which files conflict. The full text is persisted, so a run restarted mid-conflict
// rebuilds the same instructions rather than a marker row.
func (te *TaskExecutor) prepareMergeConflictTurn(setup *taskExecSetup, lc *loopContext, conflictedFiles []string) {
	prompt, err := prompts.RenderMergeConflictPrompt(prompts.MergeConflictData{
		TaskBranch:      setup.task.Branch,
		TargetBranch:    te.merge.ResolveMergeTarget(setup.projectID, setup.task),
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
	te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Resolving merge conflicts"}})
	lc.pendingToolCalls = nil
	lc.assistantContent.Reset()
	lc.reasoningContent.Reset()
	lc.finishReason = ""
}

// userMessageToChatMessage converts a UserMessage (with optional image attachments)
// into a ChatMessage. If attachments are present, builds multi-modal ContentParts.
func userMessageToChatMessage(um stream.UserMessage) types.ChatMessage {
	if parts, ok := types.ContentParts(um.Content, um.Attachments); ok {
		return types.ChatMessage{Role: "user", ContentParts: parts}
	}
	return types.ChatMessage{Role: "user", Content: um.Content}
}

// handleLoopDetected counts a detected loop, returns true when the consecutive maximum is
// reached and the task should terminate, otherwise injects a hint and notifies the frontend.
// A non-empty target is named in the hint (tool loops).
// Explore mode suppresses hints entirely; plan mode does too - reading files and making
// statements is normal planning behaviour. A valid turn resets the streak (recordTurn).
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
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Loop detected, steering agent"}})
	return false // continue
}

// sandboxCreateOpts is the sandbox configuration for creating (or recreating) setup's
// container on the given image, in the given operating mode. Four paths create this
// container (start, plan/build-mode switch, agent config change, bridge recovery) and share
// it, so a new field cannot be set in three of them. Only the image comes from the caller -
// only EnsureImageForProject knows what it settled on.
func (te *TaskExecutor) sandboxCreateOpts(setup *taskExecSetup, image string, planMode bool) sandbox.SandboxOpts {
	// Everything the container may see on the host comes from the project's own configuration, which is
	// why all four create paths assemble their options here: a field set in one of them and not the
	// others is a container that changes shape depending on which way it was started.
	return sandbox.SandboxOpts{
		BaseImage:        image,
		NetworkIsolation: setup.sandboxOpts.NetworkIsolation,
		MemoryLimit:      setup.sandboxOpts.MemoryLimit,
		ForwardPorts:     setup.sandboxOpts.ForwardPorts,
		AgentSandbox:     te.agentRegistry.GetAnyAgentSandbox(),
		SkillsDir:        prompts.SkillsDir(),
		SandboxMounts:    setup.sandboxOpts.SandboxMounts,
		SandboxEnvs:      setup.sandboxOpts.SandboxEnvs,
		Capabilities:     setup.sandboxOpts.Capabilities,
		PlanMode:         planMode,
	}
}

// setupSandbox creates or reuses a sandbox container and connects to the agent.
// Returns the image used, the agent client, and any assigned ports.
// The caller is responsible for creating callbacks from the agent client.
func (te *TaskExecutor) setupSandbox(setup *taskExecSetup) (string, *bridge.Client, []types.AssignedPort, error) {
	task := setup.task
	projectID := setup.projectID
	worktreePath := setup.worktreePath

	if te.sandboxMgr == nil || !te.sandboxMgr.IsAvailable() {
		return "", nil, nil, fmt.Errorf("gVisor not available")
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
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{
				Type: types.WSTypeStatus, Data: map[string]string{"state": status.Phase, "activity": activity}})
		}
	})
	if imgErr != nil {
		return "", nil, nil, fmt.Errorf("failed to prepare sandbox image: %w", imgErr)
	}

	// Reuse existing container (lazy-start). Create only if none exists or if stopped/crashed.
	var assignedPorts []types.AssignedPort
	if !te.sandboxMgr.ContainerIsRunning(task.ID) {
		var createErr error
		assignedPorts, createErr = te.sandboxMgr.Create(task.ID, worktreePath, setup.project, te.sandboxCreateOpts(setup, imageToUse, task.IsPlanMode()))
		if createErr != nil {
			return "", nil, nil, fmt.Errorf("failed to create sandbox: %w", createErr)
		}
		logging.For(logging.Sandbox).With(logging.TaskID, task.ID).Infof("sandbox created with image %s", imageToUse)
		// Store and broadcast assigned ports (carried on the task object)
		if len(assignedPorts) > 0 {
			task.AssignedPorts = assignedPorts
			te.storeTask(task, setup.projectID)
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		}
		te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Sandbox container started"}})
	} else {
		logging.For(logging.Sandbox).With(logging.TaskID, task.ID).Infof("reusing existing sandbox container")
	}

	// Get agent client for tool calls (required — sandbox creation ensures agent is running)
	agentClient, err := te.sandboxMgr.GetClient(task.ID)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to connect to sandbox bridge: %w", err)
	}

	return imageToUse, agentClient, assignedPorts, nil
}
