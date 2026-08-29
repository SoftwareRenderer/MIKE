package exec

import (
	"context"
	"fmt"
	"log"
	"time"

	"kanban/backend/llama"
	"kanban/backend/prompts"
	kanban_sandbox "kanban/backend/sandbox"
	"kanban/backend/stream"
	"kanban/backend/types"

	sandbox "kanban/pkg/bridge"

	"github.com/google/uuid"
)

// storeTask writes task back to the DB, preserving the fields the executor
// does not own: Title and TitleGenerating belong to the task handlers (user
// edits, async title generation), so a full-row write from the executor's
// start-time copy would clobber a title that changed while the task was
// queued or running. Refresh them from the DB first.
//
// It cannot start a run; use storeTaskClaiming for that (see Store.UpdateTask).
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

// startGateQueuePoller runs the 1-second queue-status poller used while a
// caller waits for a gate slot (per-LLM-request gate in the loop, task gate
// on start/resume/switch). It sends the caller's own queue position and
// estimated wait to the task stream every second.
//
// The returned onEnqueued callback must be passed to gate.Acquire: it hands
// the caller's queue entry to the poller (via a channel — race-free) as soon
// as the caller is enqueued, so the reported position is the caller's own
// rather than the total waiter count. Close the returned done channel to
// stop the poller.
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
	if task.ExecutionState != "running" && task.ExecutionState != "pulling_image" {
		return false
	}
	switch task.Status {
	case "in_progress":
		task.ExecutionState = "paused"
		task.Output = "Paused by user"
	case "done":
		task.ExecutionState = "completed"
	default:
		task.ExecutionState = "idle"
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
		log.Printf("[exec] task=%s executor still writing after %s; applying the transition anyway", taskID, timeout)
	}
}

// sendStopStatusEvent sends the WS status event that tells task subscribers the executor exited
func (te *TaskExecutor) sendStopStatusEvent(projectID, taskID, state string) {
	activity := "Task stopped"
	if state == "paused" {
		activity = "Task paused by user"
	}
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": state, "activity": activity}})
}

// persistStopTransition applies the stop transition
func (te *TaskExecutor) persistStopTransition(task *types.Task) bool {
	// Only the paused transition carries user-visible output text; idle and
	// completed keep the row's previous output.
	var output *string
	if task.ExecutionState == "paused" {
		o := task.Output
		output = &o
	}
	changed, uerr := te.store.TransitionInFlightTask(task.ID, task.ProjectID, task.ExecutionState, output)
	if uerr != nil {
		log.Printf("[stop] task=%s in-flight transition failed: %v", task.ID, uerr)
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
				log.Printf("[context] discovered context window for %s: %d", modelName, *discovered)
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
	if task.Status == "done" {
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

// extractPrevSummary returns the content of the most recent summarization message.
func extractPrevSummary(messages []types.ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "summarization" {
			return messages[i].Content
		}
	}
	return ""
}

// maybeAutoSummarize runs summarization if the current context usage is approaching
// the window limit. It is invoked immediately after an assistant message is appended,
// so it triggers right when context crosses the threshold (no waiting for the task to
// transition to idle). applySummarization sets/clears the Summarizing flag, which shows
// the spinner in the UI, and installs the new summary into the system prompt so the
// next LLM call carries it.
//
// When injectProceed is true and summarization succeeds, a persisted "Proceed" user
// message is appended to the context to the next LLM call.
func (te *TaskExecutor) maybeAutoSummarize(setup *taskExecSetup, lc *loopContext, activeModel string, injectProceed bool) {
	if lc.contextWindow == nil {
		return
	}
	threshold := te.configStore.AutoSummarizeThreshold()
	if !shouldSummarize(contextTokensUsed(lc), *lc.contextWindow, threshold) {
		return
	}
	// Fallback (non-utility) summarization requests go to the task's CURRENT
	// provider — lc.mc follows mid-run switches, setup.serverURL does not.
	curURL, _ := lc.mc.get()
	var summary string
	lc.messages, summary = te.applySummarization(setup, setup.ctx, lc.messages, curURL, activeModel, nil)
	if summary != "" && injectProceed {
		te.injectProceedMessage(setup, lc)
	}
	lc.detector.reset()
	lc.detector.clearHints()
}

// injectProceedMessage persists and broadcasts a "Proceed" user message
func (te *TaskExecutor) injectProceedMessage(setup *taskExecSetup, lc *loopContext) {
	msgID := uuid.New().String()
	_ = te.store.CreateTaskMessage(&types.TaskMessage{
		ID: msgID, TaskID: setup.task.ID, ThreadID: setup.task.CurrentThreadID,
		Role: "user", Content: "Proceed", CreatedAt: time.Now().UTC(),
	})
	lc.messages = append(lc.messages, types.ChatMessage{Role: "user", Content: "Proceed"})
	te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{
		Type: types.WSTypeUserMessage,
		Data: map[string]any{"id": msgID, "role": "user", "content": "Proceed"},
	})
}

// setSystemPrompt installs prompt as the conversation's system message and returns
// the conversation. Index 0 is the system message in every path that builds one
// (buildMessagesFromDB), so the normal case is an in-place replace that shifts no
// message indices. Prepending is the defensive fallback: keeping the summary is worth
// more than the off-by-one it introduces, and it only ever grows the slice, so no
// index-based reader (lc.checkpointIdx) can run past the end.
func setSystemPrompt(messages []types.ChatMessage, prompt string) []types.ChatMessage {
	if len(messages) > 0 && messages[0].Role == "system" {
		messages[0].Content = prompt
		return messages
	}
	log.Printf("[summarize] warning: conversation has no system message at index 0; prepending one to carry the summary")
	return append([]types.ChatMessage{{Role: "system", Content: prompt}}, messages...)
}

// applySummarization runs summarization, persists the summary, installs it into the
// system prompt, and broadcasts the event. It returns the conversation for the next
// LLM call and the installed summary; the summary is "" when summarization failed and
// the caller's messages came back untouched.
//
// The summary row is persisted BEFORE anything is published, so a server restart at
// any point resumes with it: a fresh process rebuilds the conversation with
// buildMessagesFromDB, which embeds the newest summarization row into its system
// prompt. The live run needs the same treatment, because buildLLMContext sends
// messages[0] verbatim and drops the summarization marker together with every message
// the summary replaced — without the refresh below the new summary never reaches the
// model until something happens to re-sync from the DB.
//
// The prompt is rewritten in place rather than rebuilt from the DB: the in-memory
// conversation is a superset of the persisted one (loop hints, the merge-conflict
// instruction prompt), and buildMessagesFromDB deliberately skips rows the LLM still
// needs mid-turn.
func (te *TaskExecutor) applySummarization(setup *taskExecSetup, ctx context.Context, messages []types.ChatMessage, serverURL, modelName string, resultCh chan error) ([]types.ChatMessage, string) {
	task := setup.task
	taskID := setup.task.ID
	projectID := setup.projectID

	// Signal the UI that summarization is in progress
	origTask := task
	task.Summarizing = true
	_ = te.store.SetTaskSummarizing(taskID, true)
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)

	clearFlag := func() {
		// Clear on the caller's reference too (task may have been reloaded below).
		origTask.Summarizing = false
		task.Summarizing = false
		_ = te.store.SetTaskSummarizing(taskID, false)
		te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	}

	prevSummary := extractPrevSummary(messages)
	// Route summarization through the configured Utility provider/model
	utilURL, utilModel, ok := te.utilityTarget(task)
	if ok {
		serverURL = utilURL
		modelName = utilModel
	}
	sumResult, err := te.summarizeContext(ctx, messages, serverURL, modelName, prevSummary)
	if err != nil {
		log.Printf("[summarize] task=%s summarization failed: %v", taskID, err)
		clearFlag()
		if resultCh != nil {
			resultCh <- err
		}
		return messages, ""
	}

	// Persist to DB.
	summaryMsgID := uuid.New().String()
	summaryCreatedAt := time.Now().UTC()
	// Anchor: reload task to get UpdatedAt (set when Summarizing flag was set).
	if updated, err := te.store.GetTaskByID(taskID); err == nil && updated != nil {
		task = updated
	}
	summaryAnchor := task.UpdatedAt
	_ = te.store.CreateTaskMessage(&types.TaskMessage{
		ID:              summaryMsgID,
		TaskID:          taskID,
		ThreadID:        task.CurrentThreadID,
		Role:            "summarization",
		Content:         sumResult.summary,
		ModelName:       modelName,
		PromptTokens:    sumResult.promptTokens,
		PredictedTokens: sumResult.predictedTokens,
		PromptMs:        int(sumResult.promptMs),
		PredictedMs:     int(sumResult.predictedMs),
		CreatedAt:       summaryCreatedAt,
	})

	// Fix timestamps of messages sent during the summarization window.
	_ = te.store.FixPostSummaryTimestamps(taskID, task.CurrentThreadID, summaryAnchor, summaryCreatedAt)

	// Append to in-memory messages.
	messages = append(messages, types.ChatMessage{
		Role:    "summarization",
		Content: sumResult.summary,
	})

	// Install the summary into the live system prompt (see the doc comment): the
	// summary reaches the LLM through messages[0] only, never through the marker.
	// `task` is the row reloaded above, so the mode is current.
	messages = setSystemPrompt(messages, composeSystemPrompt(task, setup.worktreePath, setup.projectName(), setup.sandboxEnabled, sumResult.summary))

	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: "summarization", Data: map[string]any{
		"id":               summaryMsgID,
		"summary":          sumResult.summary,
		"created_at":       summaryCreatedAt.Format(time.RFC3339),
		"prompt_tokens":    sumResult.promptTokens,
		"predicted_tokens": sumResult.predictedTokens,
		"prompt_ms":        sumResult.promptMs,
		"predicted_ms":     sumResult.predictedMs,
		"model_name":       modelName,
	}})
	log.Printf("[summarize] task=%s summarized context (messages preserved, LLM context compressed)", taskID)

	clearFlag()

	if resultCh != nil {
		resultCh <- nil
	}
	return messages, sumResult.summary
}

// userMessageToChatMessage converts a UserMessage (with optional image attachments)
// into a ChatMessage. If attachments are present, builds multi-modal ContentParts.
func userMessageToChatMessage(um stream.UserMessage) types.ChatMessage {
	if parts, ok := types.ContentParts(um.Content, um.Attachments); ok {
		return types.ChatMessage{Role: "user", ContentParts: parts}
	}
	return types.ChatMessage{Role: "user", Content: um.Content}
}

// handleLoopDetected processes a detected loop: increments the consecutive loop counter,
// checks if max consecutive loops are reached (returns true if task should terminate),
// injects a hint message, and notifies the frontend.
// If target is non-empty, it is included in the hint (used for tool loops).
// If exploreMode is true, loop hints are suppressed (no hint injection, no streak tracking).
// Plan mode also suppresses hints — reading files and making statements is normal planning behavior.
// On valid turn (via recordTurn), loopStreak is reset to 0.
func handleLoopDetected(te *TaskExecutor, task *types.Task, projectID, taskID string, detector *loopDetector, loopType LoopType, target string, messages *[]types.ChatMessage, exploreMode bool) bool {
	// In exploration/plan mode, suppress all loop hints — let the LLM freely explore / plan
	if exploreMode || task.IsPlanMode() {
		detector.reset()
		return false // continue without intervention
	}

	detector.loopStreak++
	if detector.loopStreak >= maxLoopHints {
		task.ExecutionState = "failed"
		task.Status = "review"
		task.Output = "Model stuck in loop (max hints exhausted)"
		te.storeTask(task, projectID)
		te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		te.sendAndPersistError(taskID, "Model stuck in loop (max hints exhausted)", "loop_detected")
		return true // terminate
	}

	hint := buildHint(loopType, target)
	log.Printf("[loop] task=%s detected loop type=%d (streak %d/%d), target=%s", taskID, loopType, detector.loopStreak, maxLoopHints, target)
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

// setupSandbox creates or reuses a sandbox container and connects to the agent.
// Returns the image used, the agent client, and any assigned ports.
// The caller is responsible for creating callbacks from the agent client.
func (te *TaskExecutor) setupSandbox(setup *taskExecSetup) (string, *sandbox.Client, []types.AssignedPort, error) {
	task := setup.task
	projectID := setup.projectID
	projectName := setup.project.Name
	worktreePath := setup.worktreePath
	sandboxOpts := setup.sandboxOpts
	sandboxDockerfilePath := setup.sandboxDockerfilePath

	if te.sandboxMgr == nil || !te.sandboxMgr.IsAvailable() {
		return "", nil, nil, fmt.Errorf("gVisor not available")
	}

	imageToUse := ""
	// Determine image to use: Dockerfile build takes precedence over pre-built image
	if sandboxDockerfilePath != "" {
		imageToUse = kanban_sandbox.ProjectImageName(projectName)
		if !te.sandboxMgr.ImageReadyForProject(projectName) {
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "building_image", "activity": "Building sandbox image from Dockerfile"}})
			if err := te.sandboxMgr.BuildImageWithProgress(projectName, sandboxDockerfilePath, func(line string) {
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeBuildProgress, Data: map[string]string{"line": line}})
			}); err != nil {
				return "", nil, nil, fmt.Errorf("failed to build sandbox image: %w", err)
			}
		}
	} else {
		imageToUse = sandboxOpts.BaseImage
		if imageToUse == "" {
			imageToUse = kanban_sandbox.BaseImage
		}
		if !te.sandboxMgr.ImageReady(imageToUse) {
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "pulling_image", "activity": "Pulling " + imageToUse}})
			if err := te.sandboxMgr.PullImageWithProgress(imageToUse, func(line string) {
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeBuildProgress, Data: map[string]string{"line": line}})
			}); err != nil {
				return "", nil, nil, fmt.Errorf("failed to pull sandbox image: %w", err)
			}
		}
	}

	// Reuse existing container (lazy-start). Create only if none exists or if stopped/crashed.
	var assignedPorts []types.AssignedPort
	if !te.sandboxMgr.ContainerIsRunning(task.ID) {
		agentSandbox := te.agentRegistry.GetAnyAgentSandbox()
		var createErr error
		assignedPorts, createErr = te.sandboxMgr.Create(task.ID, worktreePath, setup.project, kanban_sandbox.SandboxOpts{
			BaseImage:        imageToUse,
			NetworkIsolation: sandboxOpts.NetworkIsolation,
			MemoryLimit:      sandboxOpts.MemoryLimit,
			ForwardPorts:     sandboxOpts.ForwardPorts,
			AgentSandbox:     agentSandbox,
			SkillsDir:        prompts.SkillsDir(),
			SandboxMounts:    sandboxOpts.SandboxMounts,
			SandboxEnvs:      sandboxOpts.SandboxEnvs,
			Capabilities:     sandboxOpts.Capabilities,
			PlanMode:         task.IsPlanMode(),
		})
		if createErr != nil {
			return "", nil, nil, fmt.Errorf("failed to create sandbox: %w", createErr)
		}
		log.Printf("[sandbox] task=%s sandbox created with image %s", task.ID, imageToUse)
		// Store and broadcast assigned ports (carried on the task object)
		if len(assignedPorts) > 0 {
			task.AssignedPorts = assignedPorts
			te.storeTask(task, setup.projectID)
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		}
		te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Sandbox container started"}})
	} else {
		log.Printf("[sandbox] task=%s reusing existing sandbox container", task.ID)
	}

	// Get agent client for tool calls (required — sandbox creation ensures agent is running)
	agentClient, err := te.sandboxMgr.GetClient(task.ID)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to connect to sandbox bridge: %w", err)
	}

	return imageToUse, agentClient, assignedPorts, nil
}
