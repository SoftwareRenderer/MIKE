package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"mike/backend/git"
	"mike/backend/llama"
	"mike/backend/stream"
	"mike/backend/toolresult"
	"mike/backend/types"

	"mike/pkg/bridge"

	"github.com/google/uuid"
	"mike/pkg/logging"
)

// imageExts is the set of file extensions considered image files (allowed in staging).
var imageExts = map[string]struct{}{
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".webp": {}, ".bmp": {},
	".svg": {}, ".ico": {}, ".tiff": {}, ".tif": {}, ".avif": {}, ".apng": {},
}

// isImageFile returns true if the file path has a known image extension.
func isImageFile(path string) bool {
	lower := strings.ToLower(filepath.Ext(path))
	_, ok := imageExts[lower]
	return ok
}

// unstageNewBinaryFiles unstages newly staged binary non-image files.
func unstageNewBinaryFiles(wm *git.WorktreeManager, taskID string) {
	out, err := wm.GitRunInWorktreeOutput(taskID, "diff", "--staged", "--numstat", "--diff-filter=A")
	if err != nil || len(strings.TrimSpace(out)) == 0 {
		return
	}
	paths := binaryPathsFromStaged(out)
	unstagePaths(wm, taskID, paths)
}

// Retry bounds for LLM streaming. llmRetryWindow caps how long a run keeps retrying at all: a
// provider that is ill does not get better because four identical requests arrived in order.
const (
	llmRetryAttempts = 3
	llmRetryWindow   = 3 * time.Minute
)

// backoffFor returns the duration to wait before the next LLM retry attempt
func backoffFor(attempt int, err error) time.Duration {
	var le *llama.LlamaError
	if errors.As(err, &le) && le.IsRateLimit() && le.RetryAfter > 0 {
		return le.RetryAfter + time.Second + jitter()
	}
	// A stream that went quiet is the provider being slow, not the link being broken. Waiting less
	// than the silence already observed just re-issues the request into the same slowness.
	var silent *llama.ErrStreamSilent
	if errors.As(err, &silent) && silent.Silent > 0 {
		return silent.Silent + time.Second + jitter()
	}
	return time.Duration(1<<attempt) * 2 * time.Second // 2s, 4s, 8s, ...
}

// llmIsRequestRejected reports a provider answer that says our request was unacceptable. Calling it
// again sends the same body to the same refusal, so the retry loop is skipped and the failure is
// reported as what it is: a request to fix, not a connection to wait out.
func llmIsRequestRejected(err error) bool {
	var le *llama.LlamaError
	return errors.As(err, &le) && le.IsRequestRejected()
}

// llmFailure is the sentence and the code a failed LLM call is reported under. They are decided
// together because the two faults ask different things of the reader: a lost connection may answer on
// the next try, a refused request will not, and only a refused request is a bug in MIKE.
type llmFailure struct {
	code     string
	sentence string
}

func classifyLLMFailure(err error) llmFailure {
	if llmIsRequestRejected(err) {
		return llmFailure{
			"llm_request_rejected",
			fmt.Sprintf("The model provider refused the request MIKE sent, so it was not retried: %v", err),
		}
	}
	var silent *llama.ErrStreamSilent
	if errors.As(err, &silent) {
		return llmFailure{
			"llm_stream_silent",
			fmt.Sprintf("%v, which reached the quiet-stream ceiling set for this provider. "+
				"Nothing was lost in transit: raise or clear that ceiling in Settings if the provider answers this slowly on purpose.", silent),
		}
	}
	return llmFailure{"llm_connection_failed", fmt.Sprintf("LLM connection failed: %v", err)}
}

// streamSilenceDomain is the wait domain a quiet provider announces under; the frontend owns the
// sentence shown for it.
const streamSilenceDomain = "model"

// streamSilenceWatcher tells the people watching a turn that its provider has sent nothing for a
// while, and takes the notice back as soon as the stream speaks or the call ends. It rides the
// slow-lock channel because a wait with a name already has a place in the chat.
//
// Nothing here ends a call: silence means the provider is holding its answer back, not that the
// link is gone - llama.ErrStreamSilent holds that measurement.
type streamSilenceWatcher struct {
	mu        sync.Mutex
	send      func(string, string, types.StreamEvent)
	projectID string
	taskID    string
	every     time.Duration
	last      time.Time
	phase     string
	reported  bool
}

func (te *TaskExecutor) newStreamSilenceWatcher(projectID, taskID string) *streamSilenceWatcher {
	return &streamSilenceWatcher{
		send:      te.appStreamMgr.SendTaskEvent,
		projectID: projectID,
		taskID:    taskID,
		every:     stream.LockWaitNoticeEvery,
	}
}

// notice is the wait frame every locked-out caller already uses, carrying one extra field: which
// kind of quiet this is. "Still waiting for the first token" and "stopped mid-answer" need
// different sentences, and a person deciding whether to wait needs the difference.
func (w *streamSilenceWatcher) notice(waited time.Duration, acquired bool, phase string) {
	w.send(w.projectID, w.taskID, types.StreamEvent{Type: types.WSTypeLockWait, Data: map[string]any{
		"domain":         streamSilenceDomain,
		"waited_seconds": int(waited.Seconds()),
		"acquired":       acquired,
		"phase":          phase,
	}})
}

// onSilence is the callback the client invokes once per second while a stream stays quiet.
func (w *streamSilenceWatcher) onSilence(silent time.Duration, phase string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.last.IsZero() && time.Since(w.last) < w.every {
		return
	}
	w.last = time.Now()
	w.phase = phase
	w.reported = true
	w.notice(silent, false, phase)
}

// clear withdraws the announcement. Calling it when nothing was announced costs nothing, so every
// exit from a call can call it.
func (w *streamSilenceWatcher) clear() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.reported {
		return
	}
	w.reported = false
	w.last = time.Time{}
	w.notice(0, true, w.phase)
}

// onWait is the callback shape the LLM client wants, or nil when nobody is watching this call.
func (w *streamSilenceWatcher) onWait() llama.SilenceCallback {
	if w == nil {
		return nil
	}
	return w.onSilence
}

// sendRetryHint records an interrupted LLM call and that the executor is calling it again. It is
// called once per LLM call, not once per attempt.
func (te *TaskExecutor) sendRetryHint(projectID, taskID string, task *types.Task, streamErr error) {
	retryMsg := "Connection interrupted, retrying the request."
	logging.For(logging.Exec).With(logging.TaskID, taskID).Warnf("LLM call interrupted: %v", streamErr)
	note := systemNote{kind: retryNoteKind, content: retryMsg}
	id := note.record(te.store, taskID, task.CurrentThreadID)
	note.show(te.appStreamMgr, projectID, taskID, id, map[string]any{"error": streamErr.Error()})
}

// jitter returns a random duration between 250ms and 500ms (inclusive)
func jitter() time.Duration {
	return 250*time.Millisecond + time.Duration(rand.Int64N(251))*time.Millisecond
}

// streamFlusher batches streaming events to reduce WebSocket write overhead.
type streamFlusher struct {
	mu             sync.Mutex
	tokenBuf       strings.Builder
	reasoningBuf   strings.Builder
	toolCallDeltas map[string]toolCallDelta // tool_id -> delta info
	projectID      string
	taskID         string
	send           func(projectID, taskID string, event types.StreamEvent)
	loopDetector   *loopDetector // for phrase repetition detection on tokens
}

// toolCallDelta holds buffered delta info for a single tool call.
type toolCallDelta struct {
	toolName      string
	displayName   string
	path          string
	sandbox       bool
	startArgLen   int    // arg length when first seen (0 = new, send full raw_args)
	currentArgLen int    // latest arg length
	args          string // full args string at currentArgLen
}

const streamThrottleInterval = 100 * time.Millisecond

func newStreamFlusher(projectID, taskID string, send func(projectID, taskID string, event types.StreamEvent), detector *loopDetector) *streamFlusher {
	return &streamFlusher{
		toolCallDeltas: make(map[string]toolCallDelta),
		projectID:      projectID,
		taskID:         taskID,
		send:           send,
		loopDetector:   detector,
	}
}

// appendToken buffers a token for batched sending.
func (f *streamFlusher) appendToken(text string) {
	f.loopDetector.checkStreamingText(text)
	f.mu.Lock()
	f.tokenBuf.WriteString(text)
	f.mu.Unlock()
}

// appendReasoning buffers reasoning text for batched sending.
func (f *streamFlusher) appendReasoning(text string) {
	f.mu.Lock()
	f.reasoningBuf.WriteString(text)
	f.mu.Unlock()
}

// appendToolCall buffers a tool call delta for batched sending.
func (f *streamFlusher) appendToolCall(toolID, toolName, displayName, path, args string, argLen, prevLen int, sandbox bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	existing, ok := f.toolCallDeltas[toolID]
	if !ok {
		// First time seeing this tool_id — record start position.
		f.toolCallDeltas[toolID] = toolCallDelta{
			toolName:      toolName,
			displayName:   displayName,
			path:          path,
			sandbox:       sandbox,
			startArgLen:   prevLen,
			currentArgLen: argLen,
			args:          args,
		}
	} else if argLen > existing.currentArgLen {
		// Args grew — update to latest state.
		existing.currentArgLen = argLen
		existing.args = args
		existing.path = path
		f.toolCallDeltas[toolID] = existing
	}
}

// flush sends all buffered events. Must be called with f.mu held by caller
// OR from the ticker goroutine (which locks internally).
func (f *streamFlusher) flush() {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Flush buffered tokens as a single batched event.
	if f.tokenBuf.Len() > 0 {
		text := f.tokenBuf.String()
		f.tokenBuf.Reset()
		f.send(f.projectID, f.taskID, types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": text}})
	}

	// Flush buffered reasoning as a single batched event.
	if f.reasoningBuf.Len() > 0 {
		text := f.reasoningBuf.String()
		f.reasoningBuf.Reset()
		f.send(f.projectID, f.taskID, types.StreamEvent{Type: types.WSTypeReasoning, Data: map[string]string{"text": text}})
	}

	// Flush buffered tool call deltas.
	if len(f.toolCallDeltas) > 0 {
		for toolID, d := range f.toolCallDeltas {
			data := map[string]any{
				"tool_id":           toolID,
				"tool_name":         d.toolName,
				"tool_display_name": d.displayName,
				"args":              nil,
				"path":              d.path,
				"sandbox":           d.sandbox,
			}
			if d.startArgLen == 0 {
				// New tool call — send full raw_args.
				data["raw_args"] = d.args
			} else if d.currentArgLen <= d.startArgLen || d.currentArgLen > len(d.args) {
				// Defensive: invalid/empty delta bounds — send full raw_args instead of
				// panicking on an out-of-range slice (e.g. slice bounds out of range [217:194]).
				data["raw_args"] = d.args
			} else {
				// Update — send delta from start position.
				data["raw_args_delta"] = d.args[d.startArgLen:d.currentArgLen]
			}
			// Include edit line counts in the tool_call event so they're available immediately.
			if d.toolName == types.ToolEditFile {
				added, removed := countEditLines(d.args)
				data["lines_added"] = added
				data["lines_removed"] = removed
			}
			f.send(f.projectID, f.taskID, types.StreamEvent{Type: types.WSTypeToolCall, Data: data})
		}
		f.toolCallDeltas = make(map[string]toolCallDelta)
	}
}

// reset clears all buffered content. Called before retry.
func (f *streamFlusher) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenBuf.Reset()
	f.reasoningBuf.Reset()
	f.toolCallDeltas = make(map[string]toolCallDelta)
}

// loopContext holds mutable state for the AI conversation loop.
type loopContext struct {
	messages         []types.ChatMessage // full conversation (all messages preserved)
	assistantContent strings.Builder
	reasoningContent strings.Builder
	pendingToolCalls []types.ChatToolCall
	prevArgLens      map[string]int // tool_id -> arg length, for detecting streaming updates
	finishReason     string         // LLM finish_reason from last stream ("stop", "tool_calls", etc.)
	detector         *loopDetector
	silence          *streamSilenceWatcher // announces a quiet provider for the whole run
	tracker          failureTracker
	mc               *modelConfig
	client           *llama.LlamaClient
	providerName     string
	contextWindow    *int
	sandboxEnabled   bool
	callStartTime    time.Time // start of current LLM call (for per-call Duration)
	usage            *types.ChatUsage
	// pendingAdvisories holds lsp notes collected during tool execution
	pendingAdvisories        []advisory
	touchedFiles             []string
	shellMayHaveChangedFiles bool
	// lastDiagnosticsText suppresses re-injecting findings the model has already been shown.
	lastDiagnosticsText string
	sessionCost         float64 // accumulated API cost (USD) of completed calls for this task run
}

// toolResult holds one executed tool call's outcome. Incomplete calls (empty
// ID/name) are skipped during execution, so toolResults is NOT index-aligned
// with lc.pendingToolCalls.
type toolResult struct {
	toolCallID string
	output     string
	messageID  string         // persisted tool_call message ID
	args       map[string]any // parsed tool args
	rawArgs    string         // raw tool args JSON
	success    bool
	duration   int64
}

// askQuestionResults maps the pendingToolCalls index of every ask_question call that
// returned a question to its index in results.
func askQuestionResults(pending []types.ChatToolCall, results []toolResult) map[int]int {
	m := make(map[int]int)
	ri := 0
	for i, tc := range pending {
		if tc.ID == "" || tc.Function.Name == "" {
			continue // incomplete call — skipped during execution, no result
		}
		// ri < len(results): a call can have no result row, and callers index
		// results with the mapped value.
		if tc.Function.Name == types.ToolAskQuestion && ri < len(results) && results[ri].success {
			m[i] = ri
		}
		ri++
	}
	return m
}

// executeTask runs one task's LLM loop. It copies the setup because
// prepareTaskExecution's caller keeps using the struct afterwards (handleLazyStart,
// recovery, tests) while this loop reassigns setup.task and setup.taskSlotURL. The copy is
// the ownership rule, not a convenience — the pointers inside stay shared.
func (te *TaskExecutor) executeTask(shared *taskExecSetup) {
	setupCopy := *shared
	setup := &setupCopy

	// Declare lc at function scope so the defer can reference it.
	var lc *loopContext
	defer func() {
		close(setup.done) // signal executor exit for ForwardUserMessage type-switch
		// Signal exit last, even if the cleanup below panics: callers waiting on it
		// (StopTask) then know every write of this defer has landed.
		defer func() {
			if setup.finished != nil {
				close(setup.finished)
			}
		}()
		// Steps 2-6 of the ordered cleanup in loop_cleanup.go, which states why this order and no other.
		te.persistSessionCost(setup, lc)
		// Release the running-task slot if still held (no-op when released at
		// an idle transition or never acquired — agent providers skip it).
		te.releaseTaskSlot(setup)
		// Return after releaseTaskSlot so the supervisor sees the memory freed on the next tick.
		te.releaseMemSlot(setup)
		if r := recover(); r != nil {
			te.recoverTaskFromPanic(setup, r)
		}
		te.forgetTaskSetup(setup)
		te.releaseAssignedPorts(setup)
	}()

	// Acquire the per-provider running-task slot BEFORE sandbox setup so a
	// queued task doesn't hold a sandbox container while waiting. Agent
	// providers manage their own concurrency and are excluded.
	if !te.configStore.IsAgentProvider(setup.serverURL) {
		if !te.acquireTaskSlot(setup, setup.serverURL) {
			te.handleCancelledTask(setup.task.ID, setup.projectID)
			return
		}
	}

	// Acquire after the task slot and before sandbox setup so a queued task holds no container.
	if !te.acquireMemSlot(setup) {
		te.handleCancelledTask(setup.task.ID, setup.projectID)
		return
	}

	// Create sandbox if enabled
	// Get state reference to store sandbox callbacks
	te.taskMu.Lock()
	state := te.tasks[setup.task.ID]
	te.taskMu.Unlock()

	// Resolve image and agent config dir early — needed for both initial creation and mid-execution recreation.
	imageToUse := ""
	if setup.sandboxEnabled {
		var agentClient *bridge.Client
		var err error
		imageToUse, agentClient, err = te.setupSandbox(setup)
		if err != nil {
			te.failTask(setup.task, setup.projectID, err.Error(), "sandbox_setup_failed")
			return
		}
		sb := sandboxState{operatingMode: setup.task.OperatingMode, agentConfigHash: te.agentGrantHash(setup.providerURL())}
		sb.exec, sb.execStream, sb.read, sb.write, sb.edit = makeSandboxCallbacks(agentClient)
		// Wire up bridge recovery — reconnects client or (re)creates sandbox.
		sb.recoverBridge = func() bool {
			return te.recoverBridgeForTask(te.sandboxMgr, setup, state, imageToUse, func() {
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID,
					types.TurnStatusEvent("running", "Sandbox bridge recovered"))
			})
		}
		state.setSandbox(func(dst *sandboxState) { *dst = sb })
		// The container exists now, so the budget counts it directly and the reservation made before
		// the create is no longer needed.
		te.confirmMemReservation(setup)
	}

	// Check if this setup.task uses an agent provider
	if te.configStore.IsAgentProvider(setup.serverURL) {
		logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("detected agent provider (url=%s), calling executeTaskWithAgent", setup.serverURL)
		te.executeTaskWithAgent(setup)
		logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("executeTaskWithAgent returned")
		return
	}

	mc := &modelConfig{serverURL: setup.serverURL, modelName: setup.modelName}
	// A provider switch during the run changes mc, and container creation reads mc.
	setup.provider = mc
	providerName := te.configStore.GetProviderIDByURL(setup.serverURL)
	reasoningEffort := setup.task.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = te.configStore.GetModelReasoningEffort(setup.modelName)
	}
	apiKey := te.configStore.GetProviderAPIKey(setup.serverURL)
	// One watcher for the whole run, so a provider switch mid-run keeps announcing instead of
	// leaving the last notice standing.
	silence := te.newStreamSilenceWatcher(setup.projectID, setup.task.ID)
	client := te.newLlamaClient(setup.serverURL, setup.modelName, reasoningEffort, apiKey, silence)
	tools := te.toolDefinitionsFor(setup.task)
	taskMsgs, _ := te.store.GetTaskMessages(setup.task.ID, setup.task.CurrentThreadID)
	messages := buildMessagesFromDB(setup.task, taskMsgs, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)

	// Remove duplicate user messages that may occur during regeneration due to
	// race conditions between DELETE and POST handlers.
	if setup.regenerate {
		messages = removeDuplicateUserMessage(messages)
	}

	// Context tracking: load context window from provider config, discover if unknown
	var contextWindow *int
	if cw := te.configStore.GetModelContextWindow(setup.modelName, setup.serverURL); cw != nil {
		contextWindow = cw
	} else if discovered := te.configStore.DiscoverContextWindow(setup.modelName, setup.serverURL); discovered != nil {
		contextWindow = discovered
		te.configStore.SaveModelContextWindow(setup.modelName, setup.serverURL, *discovered)
		logging.For(logging.Exec).Infof("discovered context window for %s: %d", setup.modelName, *discovered)
	}

	maxFailures := setup.task.MaxConsecutiveFailedTurns
	if maxFailures == 0 {
		maxFailures = 3
	}

	// Consolidate all mutable loop state into a single struct.
	lc = &loopContext{
		messages:         messages,
		pendingToolCalls: nil,
		prevArgLens:      make(map[string]int),
		detector:         newLoopDetector(),
		silence:          silence,
		mc:               mc,
		client:           client,
		providerName:     providerName,
		contextWindow:    contextWindow,
		sandboxEnabled:   setup.sandboxEnabled,
	}
	// Sync once more to ensure we have the latest DB state (e.g. if messages were added/deleted)
	lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)

	// Carry over the accumulated session cost from prior runs/executors so the badge
	// reflects the full session cost across provider/model switches and executor restarts.
	lc.sessionCost = setup.task.SessionCost

	for {
		select {
		case <-setup.ctx.Done():
			te.handleCancelledTask(setup.task.ID, setup.projectID)
			return
		default:
		}

		// Check for model/provider changes before each LLM call.
		if updatedTask, err := te.store.GetTaskByID(setup.task.ID); err == nil {
			newURL := updatedTask.LlamaServerURL
			newModel := updatedTask.ModelName
			curURL, curModel := lc.mc.get()
			if newURL != "" && newURL != curURL || newModel != "" && newModel != curModel {
				// Skip model switch if target is agent — requires different executor
				if te.configStore.IsAgentProvider(newURL) {
					logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("skipping switch to agent (requires executor restart)")
				} else {
					if newURL == "" {
						newURL = curURL
					}
					if newModel == "" {
						newModel = curModel
					}
					switched := te.applyModelSwitch(lc.mc, setup.task, setup.projectID, newURL, newModel, false, true, silence)
					lc.client = switched.client
					lc.providerName = switched.providerName
					if switched.contextWindow != nil {
						lc.contextWindow = switched.contextWindow
					}
					lc.detector.reset() // model changed, clear stale text window
					lc.detector.clearHints()
					logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("url=%s model=%s context_window=%v", newURL, newModel, lc.contextWindow)
					// Provider (not just model) changed - move the running-task
					// slot to the new provider (may queue while it's saturated).
					if strings.TrimRight(newURL, "/") != strings.TrimRight(curURL, "/") {
						if !te.syncTaskSlot(setup, newURL) {
							te.handleCancelledTask(setup.task.ID, setup.projectID)
							return
						}
					}
				}
			}

			// Check for operating mode change — recreate sandbox with correct mount mode
			if setup.sandboxEnabled && te.sandboxMgr != nil && te.sandboxMgr.ContainerIsRunning(setup.task.ID) {
				currentOperatingMode := state.sandbox().operatingMode
				newOperatingMode := updatedTask.OperatingMode
				if newOperatingMode != currentOperatingMode {
					logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("operating mode changed (%s → %s), recreating sandbox", currentOperatingMode, newOperatingMode)
					if err := te.recreateTaskSandbox(setup, state, sandboxRecreate{
						reason:      "mode change",
						connErrCode: "agent_connection_failed",
						opts:        te.sandboxCreateOpts(setup, imageToUse, updatedTask.IsPlanMode()),
						underLock:   func(sb *sandboxState) { sb.operatingMode = newOperatingMode },
					}); err != nil {
						return
					}
					// Update setup.task reference with latest mode
					setup.task.OperatingMode = updatedTask.OperatingMode
					tools = te.toolDefinitionsFor(updatedTask)
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.TurnStatusEvent("running", fmt.Sprintf("Sandbox recreated for mode change (%s)", newOperatingMode)))
				}
			}
		}

		// A folder the user just removed counts as a change.
		if setup.sandboxEnabled && te.sandboxMgr != nil && te.sandboxMgr.ContainerIsRunning(setup.task.ID) {
			currentHash := state.sandbox().agentConfigHash
			newHash := te.agentGrantHash(setup.providerURL())
			if newHash != currentHash {
				logging.For(logging.Sandbox).With(logging.TaskID, setup.task.ID).Infof("agent sandbox config changed, recreating sandbox")
				if err := te.recreateTaskSandbox(setup, state, sandboxRecreate{
					reason:      "config change",
					connErrCode: "agent_config_failed",
					opts:        te.sandboxCreateOpts(setup, imageToUse, setup.task.IsPlanMode()),
					underLock:   func(sb *sandboxState) { sb.agentConfigHash = newHash },
				}); err != nil {
					return
				}
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.TurnStatusEvent("running", "Sandbox recreated for extension config change"))
			}
		}

		// Check for user messages that arrived during tool execution.
		if te.intakeUserMessage(setup, lc) {
			return
		}

		// Clear turn-specific state before each LLM call.
		lc.prevArgLens = make(map[string]int)
		lc.finishReason = ""
		lc.pendingToolCalls = nil
		lc.assistantContent.Reset()
		lc.reasoningContent.Reset()

		// Acquire concurrency gate slot before LLM call. Keyed by the CURRENT
		// provider — lc.mc follows mid-run model/provider switches, while
		// setup.serverURL is only the start-time URL.
		gateURL, activeModel := lc.mc.get()
		concurrency := te.configStore.GetProviderConcurrency(gateURL)
		maxRPS := te.configStore.GetProviderMaxRequestsPerSecond(gateURL)

		// Queue status polling — runs while waiting for a gate slot, reporting
		// the call's own position (its queue entry is handed to the poller via
		// the onEnqueued callback as soon as it is enqueued).
		onEnqueued, queueDone := te.startGateQueuePoller(te.gate, gateURL, setup.projectID, setup.task.ID, setup.ctx)
		if _, err := te.gate.Acquire(setup.ctx, gateURL, concurrency, maxRPS, onEnqueued); err != nil {
			close(queueDone) // cleanup polling goroutine
			te.handleCancelledTask(setup.task.ID, setup.projectID)
			return
		}
		close(queueDone)

		// Per-call context — can be cancelled independently to abort the LLM stream.
		callCtx, callCancel := context.WithCancel(setup.ctx)

		streamDone := make(chan struct{})
		var streamErr error
		var lastProgressSend time.Time // throttle progress updates to ~1/2s
		var lastPredictedN int         // track predicted tokens for real-time stats events

		// Assemble LLM context (compresses history via summary if one exists).
		llmMessages := buildLLMContext(lc.messages)

		// Set status before the LLM call so the frontend picks up streaming tokens.
		setup.task.Status = types.StatusInProgress
		setup.task.ExecutionState = types.ExecStateRunning
		te.persistTaskClaiming(setup)
		te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)

		// streamFlusher batches streaming events to reduce WebSocket write overhead.
		flusher := newStreamFlusher(setup.projectID, setup.task.ID,
			te.appStreamMgr.SendTaskEvent, lc.detector)
		flushDone := make(chan struct{})
		go func() {
			tick := time.NewTicker(streamThrottleInterval)
			defer tick.Stop()
			defer close(flushDone)
			for {
				select {
				case <-callCtx.Done():
					return
				case <-tick.C:
					flusher.flush()
				}
			}
		}()

		// Shared callbacks for ChatStream — used by both the initial call and retries.
		tokenCb := func(choice types.ChatChoice) {
			// A token is the end of the wait, whatever the provider was holding back.
			if (choice.Delta.Content != nil && *choice.Delta.Content != "") ||
				(choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "") ||
				len(choice.Delta.ToolCalls) > 0 {
				silence.clear()
			}
			// Capture finish_reason for loop detection (last non-empty value wins).
			if choice.FinishReason != "" {
				lc.finishReason = choice.FinishReason
			}
			if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
				lc.reasoningContent.WriteString(*choice.Delta.ReasoningContent)
				flusher.appendReasoning(*choice.Delta.ReasoningContent)
			}
			if choice.Delta.Content != nil && *choice.Delta.Content != "" {
				lc.assistantContent.WriteString(*choice.Delta.Content)
				flusher.appendToken(*choice.Delta.Content)
				// Real-time phrase repetition detection (not throttled — detection runs per-token)
				if loopType := lc.detector.checkStreamingText(*choice.Delta.Content); loopType == LoopPhrase {
					logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("phrase repetition detected during streaming")
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.TurnStatusEvent("running", "Repetitive response detected"))
				}
			}
			if len(choice.Delta.ToolCalls) > 0 {
				priorLen := len(lc.pendingToolCalls)
				lc.pendingToolCalls = llama.MergeToolCalls(lc.pendingToolCalls, choice.Delta.ToolCalls)
				for i := 0; i < len(lc.pendingToolCalls); i++ {
					tc := lc.pendingToolCalls[i]
					if tc.ID == "" || tc.Function.Name == "" {
						continue
					}
					argLen := len(tc.Function.Arguments)
					prevLen := lc.prevArgLens[tc.ID]
					isNew := i >= priorLen
					isUpdated := !isNew && argLen != prevLen
					if !isNew && !isUpdated {
						continue
					}
					lc.prevArgLens[tc.ID] = argLen
					path := extractPathFromArgs(tc.Function.Arguments)
					actualPrevLen := prevLen
					if isNew {
						actualPrevLen = 0
					}
					flusher.appendToolCall(tc.ID, tc.Function.Name, te.toolDisplayName(tc.Function.Name), path, tc.Function.Arguments, argLen, actualPrevLen, (lc.sandboxEnabled && isSandboxableTool(tc.Function.Name)) || isLSPTool(tc.Function.Name))
				}
			}
			// Throttled progress updates for write_file (1 per 2 seconds)
			if time.Since(lastProgressSend) > 2*time.Second {
				for _, tc := range lc.pendingToolCalls {
					if tc.Function.Name == types.ToolWriteFile && tc.Function.Arguments != "" {
						te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeToolProgress, Data: map[string]any{"tool_id": tc.ID, "bytes_written": len(tc.Function.Arguments), "path": extractPathFromArgs(tc.Function.Arguments)}})
						lastProgressSend = time.Now()
					}
				}
			}
		}
		// usageCb publishes live token stats. It reads the snapshot, never
		// lc.usage: that field is only set once ChatStream returns, so mid-stream
		// it holds nil (first turn) or the previous turn's numbers (later turns).
		usageCb := func(u *types.ChatUsage) {
			if u == nil {
				return
			}
			if u.CompletionTokens != lastPredictedN {
				lastPredictedN = u.CompletionTokens
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStats, Data: map[string]any{
					"model_name":       activeModel,
					"prompt_tokens":    u.PromptTokens,
					"predicted_tokens": u.CompletionTokens,
					"predicted_ms":     u.PredictedMs,
				}})
			}
		}
		progressCb := func(progress types.PromptProgressData) {
			// Forward prompt processing progress to the frontend.
			// Throttle to ~2 updates/sec to avoid flooding the WebSocket.
			if time.Since(lastProgressSend) > 500*time.Millisecond {
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypePromptProgress, Data: map[string]any{
					"total":     progress.Total,
					"cache":     progress.Cache,
					"processed": progress.Processed,
					"time_ms":   progress.TimeMs,
				}})
				lastProgressSend = time.Now()
			}
		}

		lc.callStartTime = time.Now()
		go func() {
			defer silence.clear() // every exit ends the notice, including a cancellation

			lc.usage, streamErr = lc.client.ChatStream(callCtx, llmMessages, tools, tokenCb, progressCb, usageCb)

			// Retry on transient errors, unless the call was cancelled by a user message or refused by
			// the provider: a refusal is deterministic, so the loop would only repeat it.
			if streamErr != nil && callCtx.Err() == nil && !llmIsRequestRejected(streamErr) {
				retryStarted := time.Now()
				for i := range llmRetryAttempts {
					select {
					case <-callCtx.Done():
						flusher.flush()
						close(streamDone)
						return
					case <-time.After(backoffFor(i, streamErr)):
					}
					if time.Since(retryStarted) > llmRetryWindow {
						break // the provider is ill and four requests in a row will not cure it
					}
					if i == 0 {
						te.sendRetryHint(setup.projectID, setup.task.ID, setup.task, streamErr)
					}
					logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Warnf("LLM call retry %d/%d after: %v", i+1, llmRetryAttempts, streamErr)
					// Flush any remaining buffered events before retry.
					flusher.flush()
					// Reset builders so retry doesn't concatenate partial content.
					lc.assistantContent.Reset()
					lc.reasoningContent.Reset()
					lc.pendingToolCalls = nil
					lc.finishReason = ""
					flusher.reset()
					lc.callStartTime = time.Now()
					lc.usage, streamErr = lc.client.ChatStream(callCtx, llmMessages, tools, tokenCb, progressCb, usageCb)
					if streamErr == nil {
						break
					}
				}
			}

			// Emit accumulated session cost (LLM providers only; agents report their own).
			if streamErr == nil && lc.usage != nil {
				curURL, _ := lc.mc.get()
				if cost := te.configStore.EstimateCallCost(curURL, activeModel, lc.usage); cost > 0 {
					lc.sessionCost += cost
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStats, Data: map[string]any{
						"model_name":       activeModel,
						"prompt_tokens":    lc.usage.PromptTokens,
						"predicted_tokens": lc.usage.CompletionTokens,
						"predicted_ms":     lc.usage.PredictedMs,
						"cost":             lc.sessionCost,
					}})
				}
			}

			// Flush any remaining buffered events before signalling completion.
			flusher.flush()
			close(streamDone)
		}()

		// Wait for: stream complete, new user message, or setup.task stop.
		select {
		case <-setup.ctx.Done():
			callCancel()
			te.gate.Release(gateURL) // Release gate immediately so other tasks can proceed
			<-streamDone             // Then wait for goroutine to finish
			te.handleCancelledTask(setup.task.ID, setup.projectID)
			return
		case <-streamDone:
			// Stream completed normally (or all retries exhausted)
		case um := <-setup.userMsgChan:
			if um.ContextChanged {
				logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("conversation context changed during streaming, cancelling")
				lc.detector.reset()
				lc.detector.clearHints()
				callCancel()
				select {
				case <-streamDone:
				case <-time.After(2 * time.Second):
					logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Warnf("streamDone timeout, forcing continue")
				}
				te.gate.Release(gateURL)
				lc.assistantContent.Reset()
				lc.reasoningContent.Reset()
				lc.pendingToolCalls = nil
				lc.finishReason = ""
				lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)
				continue
			}
			// New user message — cancel current LLM call.
			logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("user message received during streaming, cancelling (setup.regenerate=%v)", um.Regenerate)
			lc.detector.reset() // user input breaks any loop pattern
			lc.detector.clearHints()
			callCancel()
			// Wait for goroutine to finish (with timeout in case HTTP stream is slow to abort).
			select {
			case <-streamDone:
			case <-time.After(2 * time.Second):
				logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Warnf("streamDone timeout, forcing continue")
			}
			te.gate.Release(gateURL)

			// Drain all queued messages from channel (non-blocking).
			var queuedMsgs []stream.UserMessage
			queuedMsgs = append(queuedMsgs, um)
			for range 16 {
				select {
				case q := <-setup.userMsgChan:
					queuedMsgs = append(queuedMsgs, q)
				default:
					goto drainDone
				}
			}
		drainDone:

			// Clear partial assistant state.
			lc.assistantContent.Reset()
			lc.reasoningContent.Reset()
			lc.pendingToolCalls = nil
			lc.finishReason = ""

			// If any queued message is a setup.regenerate, truncate stale assistant messages.
			// Otherwise, re-sync with DB to pick up any deleted messages.
			hadRegenerate := false
			for _, qm := range queuedMsgs {
				if qm.Regenerate {
					lc.messages = truncateForRegenerate(lc.messages)
					hadRegenerate = true
					break
				}
			}
			if !hadRegenerate {
				// Re-sync with DB to pick up user messages (already created by HandleSend)
				// and any deleted messages. Do NOT append — syncMessagesFromDB already includes them.
				lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)
			}

			// Apply model overrides from queued messages.
			for _, qm := range queuedMsgs {
				if qm.Regenerate {
					lc.messages = append(lc.messages, userMessageToChatMessage(qm))
				}
				if qm.ModelName != "" {
					switched := te.applyModelSwitch(lc.mc, setup.task, setup.projectID, qm.ServerURL, qm.ModelName, true, false, silence)
					lc.client = switched.client
					lc.providerName = switched.providerName
				}
			}
			// If the queued messages moved the task to another provider, move
			// the running-task slot with it (no-op when the URL is unchanged).
			if curURL, _ := lc.mc.get(); curURL != "" && !te.syncTaskSlot(setup, curURL) {
				te.handleCancelledTask(setup.task.ID, setup.projectID)
				return
			}

			// Notify frontend that response was cancelled.
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.TurnStatusEvent("cancelled", "Response cancelled, processing new message"))

			continue
		}
		callCancel()

		if streamErr != nil {
			// Release gate slot (was acquired before the LLM call above).
			te.gate.Release(gateURL)
			failure := classifyLLMFailure(streamErr)
			logging.For(logging.LLM).With(logging.TaskID, setup.task.ID).Errorf("LLM call failed (code=%s): %v", failure.code, streamErr)
			te.failTask(setup.task, setup.projectID, failure.sentence, failure.code)
			return
		}

		// Release gate slot after LLM call (before tool execution).
		te.gate.Release(gateURL)

		// Everything that follows an LLM call: persist the assistant turn and run its tool
		// calls, settle for the text answer, or record a reasoning-only turn.
		switch te.finalizeTurn(setup, lc, activeModel, maxFailures) {
		case turnStop:
			return
		case turnNextLLMCall:
			continue
		}

		// Block for user message before next LLM call
		if te.waitForIdleInput(setup, lc, activeModel) == idleStopped {
			return
		}
	}
}

// turnOutcome is how finalizeTurn tells executeTask what to do next: end the run, make the next LLM
// call now, or fall through to the idle wait for a user message. The last two must stay separate -
// collapse them into one "keep going" and the executor blocks waiting for the user after a turn that
// was supposed to run straight on to the next call.
type turnOutcome int

const (
	// turnWaitIdle: the turn is settled; block for the user before the next LLM call.
	turnWaitIdle turnOutcome = iota
	// turnNextLLMCall: the context is ready for another call - tool results or a summarization
	// landed, or a user message interrupted the turn's tool calls and the loop folds it in next.
	turnNextLLMCall
	// turnStop: the run is over - cancelled, failed, loop-detected or max-turns reached. The
	// reason is already persisted and broadcast by the branch that decided it.
	turnStop
)

// Which tools a task exposes. The operating mode chooses the acting set (plan and explore replace it), and
// contributed and LSP tools are appended to whatever the mode chose.
func (te *TaskExecutor) toolDefinitionsFor(task *types.Task) []types.ChatTool {
	var tools []types.ChatTool
	switch {
	case task.IsPlanMode():
		tools = llama.GetPlanModeToolDefinitions()
	case task.IsExploreMode():
		tools = llama.GetExploreModeToolDefinitions()
	default:
		tools = llama.GetToolDefinitions()
	}
	if te.lspMgr != nil && te.lspMgr.GetRegistry().HasAnyLSPToolCallsExtension() {
		tools = append(tools, llama.GetLSPToolDefinitions()...)
	}
	return append(tools, llama.ContributedToolDefinitions(te.contributedSpecs())...)
}

// Read per turn, so installing or configuring an extension takes effect on the next turn.
func (te *TaskExecutor) contributedSpecs() []types.ToolSpec {
	contributed := te.extTools.Contributed()
	if len(contributed) == 0 {
		return nil
	}
	specs := make([]types.ToolSpec, 0, len(contributed))
	for _, c := range contributed {
		specs = append(specs, c.Spec)
	}
	return specs
}

// rowsDurable tells the hub that the conversation up to here exists as rows, so its replay buffer keeps
// only what no row answers. Call it where a write succeeded: a row that failed to land leaves the frames
// as the only copy of what they say.
func (te *TaskExecutor) rowsDurable(projectID, taskID string) {
	if te.appStreamMgr == nil {
		return
	}
	te.appStreamMgr.DurableThrough(projectID, taskID)
}

// finalizeTurn handles what follows one LLM call: persist the assistant message and run its tool calls,
// settle on a text answer, or record a reasoning-only turn.
func (te *TaskExecutor) finalizeTurn(setup *taskExecSetup, lc *loopContext, activeModel string, maxFailures int) turnOutcome {
	assistantText := lc.assistantContent.String()

	var toolResults []toolResult
	var turnFailures []turnResult

	if len(lc.pendingToolCalls) > 0 {
		// Persist assistant message BEFORE tool calls so DB ordering matches LLM output order
		promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(lc.usage)
		logStatsSpeed(setup.task.ID, activeModel, promptTokens-cacheTokens, predictedTokens, promptMs, predictedMs)
		assistantCreatedAt := time.Now().UTC()
		assistantMsgID := uuid.New().String()
		if err := te.store.CreateTaskMessage(&types.TaskMessage{
			ID: assistantMsgID, TaskID: setup.task.ID, ThreadID: setup.task.CurrentThreadID, Role: "assistant",
			Content: assistantText, ReasoningContent: lc.reasoningContent.String(),
			ModelName: activeModel, ProviderName: lc.providerName,
			PromptTokens: promptTokens, PredictedTokens: predictedTokens, CacheTokens: cacheTokens,
			PromptMs: promptMs, PredictedMs: predictedMs,
			Duration: time.Since(lc.callStartTime).Milliseconds(), CreatedAt: assistantCreatedAt,
		}); err == nil {
			te.rowsDurable(setup.projectID, setup.task.ID)
		}

		// Track if any tool call was a git command — needed because fsnotify
		// can't detect diff state changes from git commit/checkout/merge
		// (branch pointer moves without working tree file changes).
		hadGitCmd := false
		hadRequestReview := false // request_review detected in this turn

		// What cancelling this kills, and what it must never reach, is in toolCallContext.
		toolCtx, cancelTools := toolCallContext(setup)
		defer cancelTools()
		// A task stop is a stop, and its own path reports it; only a message is an interrupt.
		interrupted := func() bool { return toolCtx.Err() != nil && setup.ctx.Err() == nil }

		for _, tc := range lc.pendingToolCalls {
			if tc.ID == "" || tc.Function.Name == "" {
				logging.For(logging.Tools).Infof("skipping incomplete tool call (id=%q, name=%q)", tc.ID, tc.Function.Name)
				continue
			}
			args := parseToolArgs(tc.Function.Arguments)
			// Send final path update before execution (full args now available)
			if tc.Function.Name == types.ToolWriteFile {
				path := extractPathFromArgs(tc.Function.Arguments)
				if path == "" {
					if p, ok := args["path"].(string); ok && p != "" {
						path = p
					}
				}
				if path != "" {
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeToolProgress, Data: map[string]any{"tool_id": tc.ID, "bytes_written": len(tc.Function.Arguments), "path": path}})
				}
			}
			// Persist tool_call message BEFORE execution so it survives page refresh.
			// ToolResult is empty until execution completes.
			tcJSON, _ := json.Marshal(map[string]any{
				"tool_id":           tc.ID,
				"tool_name":         tc.Function.Name,
				"tool_display_name": te.toolDisplayName(tc.Function.Name),
				"args":              args,
				"raw_args":          tc.Function.Arguments,
				"sandbox":           (lc.sandboxEnabled && isSandboxableTool(tc.Function.Name)) || isLSPTool(tc.Function.Name),
			})
			toolCallMsgID := uuid.New().String()
			if err := te.store.CreateTaskMessage(&types.TaskMessage{
				ID:        toolCallMsgID,
				TaskID:    setup.task.ID,
				ThreadID:  setup.task.CurrentThreadID,
				Role:      "tool_call",
				Content:   "",
				ToolCalls: string(tcJSON),
				// The assistant row is persisted before tools run, so its id is known
				// here: recording it is what lets the conversation be rebuilt without
				// relying on the tool_call rows being contiguous.
				AssistantMsgID: assistantMsgID,
				CreatedAt:      time.Now().UTC(),
			}); err != nil {
				logging.For(logging.Tools).Warnf("FAILED to persist tool_call message for setup.task=%s tool=%s: %v", setup.task.ID, tc.Function.Name, err)
			} else {
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeToolProgress, Data: map[string]any{"tool_id": tc.ID, "message_id": toolCallMsgID}})
			}

			// A call the model could not state its arguments for is refused rather than run with an
			// empty argument map: see unparseableToolArgs.
			refusal := unparseableToolArgs(tc.Function.Arguments, args)
			var result string
			var success bool
			toolStart := time.Now()
			streamed := false
			// skipped: nothing ran, so nothing was committed, touched or put up for review.
			skipped := false
			wasInterrupted := false
			switch {
			case interrupted():
				result, success, skipped, wasInterrupted = interruptedToolResult, false, true, true
			case refusal != "":
				result, success, skipped = refusal, false, true
				logging.For(logging.Tools).With(logging.TaskID, setup.task.ID).Warnf("refused %s: arguments that did not decode", tc.Function.Name)
			default:
				// tool_call event already sent during streaming, just execute and send result
				logging.For(logging.Tools).Infof("executing %s for setup.task=%s", tc.Function.Name, setup.task.ID)
				te.taskMu.Lock()
				state := te.tasks[setup.task.ID]
				te.taskMu.Unlock()
				// runToolProtected holds the worktree lock for this block and turns a panic
				// into a failed result, so the tool_result below is always sent and stored.
				result, success = te.runToolProtected(setup.task.ID, tc.Function.Name, func() (string, bool) {
					result, success := te.executeTool(toolCtx, tc.Function.Name, args, setup.worktreePath, setup.task, state, func(toolID string, text string) {
						streamed = true
						te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeToolOutput, Data: map[string]any{"tool_id": tc.ID, "text": text}})
					})

					// Remember what this tool touched. Diagnostics run at the turn boundary
					// (collectTurnDiagnostics), not here: this callback holds the worktree
					// mutex, where a language server round trip would serialise the rest of
					// the turn and report a state the turn's later edits may already fix.
					switch tc.Function.Name {
					case types.ToolWriteFile, types.ToolEditFile:
						if path, ok := args["path"].(string); ok && path != "" {
							lc.noteTouchedFile(path)
						}
					case types.ToolBash:
						lc.noteShellMayHaveChangedFiles()
					}
					return result, success
				})
				if interrupted() {
					// A killed call answers what a skipped one does, and its partial output goes with
					// the stop.
					result, success, wasInterrupted = interruptedToolResult, false, true
				}
			}
			toolDuration := time.Since(toolStart).Milliseconds()
			isSandboxed := (lc.sandboxEnabled && isSandboxableTool(tc.Function.Name)) || isLSPTool(tc.Function.Name)
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeToolResult, Data: buildToolResultEvent(tc.ID, tc.Function.Name, result, success, wasInterrupted, toolDuration, args, tc.Function.Arguments, extractPathFromArgs(tc.Function.Arguments), isSandboxed, streamed, toolCallMsgID)})
			toolResults = append(toolResults, toolResult{toolCallID: tc.ID, output: result, messageID: toolCallMsgID, args: args, rawArgs: tc.Function.Arguments, success: success, duration: toolDuration})

			// Update tool_call message with result after execution completes.
			resultJSON, _ := json.Marshal(map[string]any{
				"output":      result,
				"success":     success,
				"interrupted": wasInterrupted,
				"duration":    toolDuration,
			})
			if err := te.store.UpdateTaskMessageToolResult(toolCallMsgID, string(resultJSON)); err != nil {
				// The row keeps an empty result, which the chat renders as still running.
				logging.For(logging.Tools).Warnf("FAILED to persist tool_result for msg=%s tool=%s: %v", toolCallMsgID, tc.Function.Name, err)
			} else {
				te.rowsDurable(setup.projectID, setup.task.ID)
			}

			turnFailures = append(turnFailures, turnResult{
				signature: tc.Function.Name + ":" + tc.Function.Arguments,
				success:   success,
			})

			if skipped {
				continue
			}

			// Detect git commands in bash tool calls
			if tc.Function.Name == types.ToolBash {
				if cmd, ok := args["command"].(string); ok && strings.Contains(cmd, "git ") {
					hadGitCmd = true
				}
			}

			if tc.Function.Name == types.ToolRequestReview {
				hadRequestReview = true
			}
		}
		// Post-tool git check: broadcast diff if a git command changed the state
		if hadGitCmd {
			te.broadcastDiffIfGitCmd(setup.projectID, setup.task.ID, setup.task)
		}

		if interrupted() {
			// Nothing this turn started gets settled: no failure counted, no question awaited, no
			// commit, merge or Done, and the armed commit-and-done click stays armed. The message that
			// stopped it is still queued, so the next iteration folds it in before the provider runs.
			logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("user message interrupted the turn's tool calls")
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.TurnStatusEvent("cancelled", "Response cancelled, processing new message"))
			return turnNextLLMCall
		}

		// ask_question: auto-started tasks auto-answer from parent context and
		// continue; otherwise stop and wait for the user.
		askResults := askQuestionResults(lc.pendingToolCalls, toolResults)
		autoAnswered := false
		if len(askResults) > 0 {
			// Collect question indexes in call order (map iteration is unordered).
			questionIdxs := make([]int, 0, len(askResults))
			for i := range askResults {
				questionIdxs = append(questionIdxs, i)
			}
			slices.Sort(questionIdxs)
			if setup.task.Autostarted {
				questions := make([]string, 0, len(questionIdxs))
				for _, i := range questionIdxs {
					questions = append(questions, toolResults[askResults[i]].output)
				}
				answers, aerr := te.automatedAnswers(setup.ctx, setup.task, questions)
				if aerr != nil || len(answers) != len(questionIdxs) {
					if aerr != nil {
						logging.For(logging.Tools).With(logging.TaskID, setup.task.ID).Warnf("auto-answer failed: %v, falling back to user", aerr)
					} else {
						logging.For(logging.Tools).With(logging.TaskID, setup.task.ID).Infof("auto-answer count mismatch (%d answers, %d questions), falling back to user", len(answers), len(questionIdxs))
					}
				} else {
					for k, idx := range questionIdxs {
						ri := askResults[idx]
						tr := toolResults[ri]
						tr.output = answers[k]
						toolResults[ri] = tr
						// Persist the answer as this tool call's result.
						resultJSON, _ := json.Marshal(map[string]any{"output": tr.output, "success": tr.success, "duration": tr.duration})
						if err := te.store.UpdateTaskMessageToolResult(tr.messageID, string(resultJSON)); err == nil {
							te.rowsDurable(setup.projectID, setup.task.ID)
						}
						// Re-broadcast so the UI shows the answer in place.
						te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeToolResult, Data: buildToolResultEvent(tr.toolCallID, types.ToolAskQuestion, tr.output, tr.success, false, tr.duration, tr.args, tr.rawArgs, "", false, false, tr.messageID)})
					}
					autoAnswered = true
					logging.For(logging.Tools).With(logging.TaskID, setup.task.ID).Infof("auto-answered %d question(s) from parent context", len(answers))
				}
			}

			// Not auto-answered and no request_review: stop and wait for the user.
			if !hadRequestReview && !autoAnswered {
				// A question hands the task back to the user, so it replaces any pending
				// commit-and-done click - the merge-conflict prompt invites exactly that question.
				te.merge.clearCommitAndDone(setup.task.ID)
				tr := toolResults[askResults[questionIdxs[0]]]
				setup.task.ExecutionState = types.ExecStateIdle
				setup.task.Status = types.StatusReview
				te.persistTask(setup)
				te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
					"state":    "idle",
					"question": tr.output,
					"summary":  "Agent has a question",
				}})
				logging.For(logging.Tools).With(logging.TaskID, setup.task.ID).Infof("stopping executor, awaiting user answer")
				// Deliver anything the environment said this turn before idling: the answer
				// arrives on a later run, which rebuilds its context from the DB.
				te.flushAdvisories(setup, lc)
				// Remove from setup.task registry so next user message triggers handleLazyStart
				te.taskMu.Lock()
				delete(te.tasks, setup.task.ID)
				te.taskMu.Unlock()
				return turnStop
			}
		}

		// request_review: handle task completion (commit/merge)
		// This runs AFTER auto-answer so that ask_question in the same turn
		// is answered before the task completes.
		if hadRequestReview {
			if setup.task.AutoCommit {
				if setup.task.HasMergeConflict {
					res := te.merge.commitConflictResolution(setup.projectID, setup.task.ID, setup.worktreePath, setup.task)
					setup.task = res.task
					if setup.task.HasMergeConflict {
						// Still conflicted — hand the model the instructions and continue
						te.prepareMergeConflictTurn(setup, lc, GetConflictedFiles(setup.worktreePath))
						return turnNextLLMCall
					}
					if res.finishesTask() {
						setup.task.ExecutionState = types.ExecStateCompleted
						setup.task.Status = types.StatusDone
					} else {
						// No merge, or a failed one: the work is not in the target branch.
						if res.mergeErr != nil {
							setup.task.Output = "Merge failed: " + res.mergeErr.Error()
						}
						setup.task.ExecutionState = types.ExecStateIdle
						setup.task.Status = types.StatusReview
					}
					if te.sandboxMgr != nil {
						_ = te.sandboxMgr.Stop(setup.task.ID)
					}
					te.persistTask(setup)
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
						"state":                setup.task.ExecutionState,
						"assistant_message_id": assistantMsgID,
						"prompt_tokens":        promptTokens, "predicted_tokens": predictedTokens,
						"cache_tokens": cacheTokens,
						"prompt_ms":    promptMs, "predicted_ms": predictedMs,
						"model_name": activeModel, "duration": time.Since(lc.callStartTime).Milliseconds(),
					}})
					te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
					if setup.task.ParentTaskID != "" {
						_ = te.AutoStartNextSibling(setup.projectID, setup.task.ID, setup.task.ParentTaskID)
					}
					return turnStop
				}

				// No merge conflict — normal auto-commit path
				commitMsg := te.merge.GenerateCommitMessage(setup.ctx, setup.task)
				mergeErr := te.merge.CommitAndMerge(setup.projectID, setup.task.ID, setup.worktreePath, commitMsg, setup.task.AutoMerge)
				setup.task, _ = te.store.GetTaskByID(setup.task.ID)
				if setup.task.HasMergeConflict {
					te.prepareMergeConflictTurn(setup, lc, GetResolvedConflictedFiles(setup.worktreePath))
					return turnNextLLMCall
				}
				if !MergedDelivered(mergeErr) {
					logging.For(logging.Merge).With(logging.TaskID, setup.task.ID).Warnf("merge failed, moving to review: %v", mergeErr)
					setup.task.ExecutionState = types.ExecStateIdle
					setup.task.Status = types.StatusReview
					setup.task.Output = "Merge failed: " + mergeErr.Error()
					if te.sandboxMgr != nil {
						_ = te.sandboxMgr.Stop(setup.task.ID)
					}
				} else if !setup.task.AutoMerge {
					setup.task.ExecutionState = types.ExecStateIdle
					setup.task.Status = types.StatusReview
					if te.sandboxMgr != nil {
						_ = te.sandboxMgr.Stop(setup.task.ID)
					}
				} else {
					setup.task.ExecutionState = types.ExecStateCompleted
					setup.task.Status = types.StatusDone
					if te.sandboxMgr != nil {
						_ = te.sandboxMgr.Stop(setup.task.ID)
					}
				}
			} else if setup.task.HasMergeConflict {
				res := te.merge.commitConflictResolution(setup.projectID, setup.task.ID, setup.worktreePath, setup.task)
				setup.task = res.task
				if setup.task.HasMergeConflict {
					logging.For(logging.Merge).With(logging.TaskID, setup.task.ID).Infof("merge still conflicted after request_review")
					setup.task.ExecutionState = types.ExecStateFailed
					setup.task.Status = types.StatusReview
					if te.sandboxMgr != nil {
						_ = te.sandboxMgr.Stop(setup.task.ID)
					}
				} else if res.finishesTask() {
					setup.task.ExecutionState = types.ExecStateCompleted
					setup.task.Status = types.StatusDone
					if te.sandboxMgr != nil {
						_ = te.sandboxMgr.Stop(setup.task.ID)
					}
				} else {
					if res.mergeErr != nil {
						setup.task.Output = "Merge failed: " + res.mergeErr.Error()
					}
					setup.task.ExecutionState = types.ExecStateIdle
					setup.task.Status = types.StatusReview
					if te.sandboxMgr != nil {
						_ = te.sandboxMgr.Stop(setup.task.ID)
					}
				}
			} else {
				setup.task.ExecutionState = types.ExecStateIdle
				setup.task.Status = types.StatusReview
			}
			te.persistTask(setup)
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
				"state":                setup.task.ExecutionState,
				"assistant_message_id": assistantMsgID,
				"prompt_tokens":        promptTokens, "predicted_tokens": predictedTokens,
				"cache_tokens": cacheTokens,
				"prompt_ms":    promptMs, "predicted_ms": predictedMs,
				"model_name": activeModel, "duration": time.Since(lc.callStartTime).Milliseconds(),
			}})
			te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
			if hadGitCmd {
				te.broadcastDiffIfGitCmd(setup.projectID, setup.task.ID, setup.task)
			}
			if setup.task.ParentTaskID != "" {
				_ = te.AutoStartNextSibling(setup.projectID, setup.task.ID, setup.task.ParentTaskID)
			}
			return turnStop
		}

		// Check consecutive identical tool call failures (turn-level).
		if count := lc.tracker.recordTurn(turnFailures); count >= maxFailures {
			setup.task.ExecutionState = types.ExecStateFailed
			setup.task.Status = types.StatusReview
			setup.task.Output = fmt.Sprintf("Max consecutive failed turns (%d) reached", maxFailures)
			te.persistTask(setup)
			te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
			te.sendAndPersistError(setup.task.ID, fmt.Sprintf("Max consecutive failed turns (%d) reached", maxFailures), "max_failures")
			return turnStop
		}

		// Build assistant message with reasoning, content, and tool calls.
		// Including tool_calls in the message history is critical for models like gemma
		// to understand what tool was called and match it to the result.
		// Keep reasoning in ReasoningContent so toAPIMessage sends it as reasoning_content.
		var assistantToolCalls []types.ToolCallMsg
		for _, tc := range lc.pendingToolCalls {
			if tc.ID != "" && tc.Function.Name != "" {
				assistantToolCalls = append(assistantToolCalls, types.ToolCallMsg{
					ID:   tc.ID,
					Type: "function",
				})
				assistantToolCalls[len(assistantToolCalls)-1].Function.Name = tc.Function.Name
				assistantToolCalls[len(assistantToolCalls)-1].Function.Arguments = tc.Function.Arguments
			}
		}
		assistantMsg := types.ChatMessage{
			Role:      "assistant",
			Content:   assistantText,
			ToolCalls: assistantToolCalls,
		}
		if lc.reasoningContent.Len() > 0 {
			assistantMsg.ReasoningContent = lc.reasoningContent.String()
		}
		lc.messages = append(lc.messages, assistantMsg)
		for _, tr := range toolResults {
			lc.messages = append(lc.messages, types.ChatMessage{Role: "tool", Content: tr.output, ToolCallID: tr.toolCallID})
		}

		// One diagnostics pass for everything this turn changed, then deliver the
		// buffered environment notes. Both must happen after the tool messages above
		// (so the notes comment on work the model can already see) and before
		// summarization (so the notes survive compaction).
		te.collectTurnDiagnostics(setup, lc)
		te.flushAdvisories(setup, lc)

		// Loop detection: record this turn and check for repetition
		loopType, toolArgs := lc.detector.recordTurn(lc.pendingToolCalls, assistantText, lc.finishReason)
		if loopType != LoopNone {
			target := ""
			if loopType == LoopTool {
				target = extractPathFromArgs(toolArgs)
			}
			if handleLoopDetected(te, setup.task, setup.projectID, setup.task.ID, lc.detector, loopType, target, &lc.messages, setup.task.IsExploreMode()) {
				return turnStop
			}
		} else {
			lc.messages = lc.detector.removeHints(lc.messages)
		}

		lc.pendingToolCalls = nil
		lc.assistantContent.Reset()
		lc.reasoningContent.Reset()
		lc.finishReason = ""

		// Auto-commit file changes as a checkpoint.
		te.createCheckpoint(setup.ctx, setup.projectID, setup.task.ID, assistantMsgID)

		// Auto-summarize if context is approaching the limit
		te.summarize.maybeAutoSummarize(setup, lc, activeModel, true)

		// Continue to call LLM again with tool results — don't fall through to blocking select
		return turnNextLLMCall
	} else if assistantText != "" {
		// Text-only response resets the failure tracker
		lc.tracker.reset()

		// Keep reasoning in ReasoningContent so toAPIMessage sends it as reasoning_content.
		assistantMsg := types.ChatMessage{Role: "assistant", Content: assistantText}
		if lc.reasoningContent.Len() > 0 {
			assistantMsg.ReasoningContent = lc.reasoningContent.String()
		}
		lc.messages = append(lc.messages, assistantMsg)
		promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(lc.usage)
		logStatsSpeed(setup.task.ID, activeModel, promptTokens-cacheTokens, predictedTokens, promptMs, predictedMs)
		duration := time.Since(lc.callStartTime).Milliseconds()
		assistantMsgID := uuid.New().String()
		te.store.CreateTaskMessage(&types.TaskMessage{
			ID: assistantMsgID, TaskID: setup.task.ID, ThreadID: setup.task.CurrentThreadID, Role: "assistant",
			Content: assistantText, ReasoningContent: lc.reasoningContent.String(),
			ModelName: activeModel, ProviderName: lc.providerName,
			PromptTokens: promptTokens, PredictedTokens: predictedTokens, CacheTokens: cacheTokens,
			PromptMs: promptMs, PredictedMs: predictedMs,
			Duration: duration, CreatedAt: time.Now().UTC(),
		})
		lc.assistantContent.Reset()
		lc.reasoningContent.Reset()

		// Loop detection for text-only responses
		loopType, _ := lc.detector.recordTurn(nil, assistantText, lc.finishReason)
		lc.finishReason = ""
		// Also check for phrase repetition detected during streaming.
		// Skip if the response contains a question (natural question, not a loop).
		if loopType == LoopNone && lc.detector.phraseDetected() && !strings.Contains(assistantText, "?") {
			loopType = LoopPhrase
		}
		if loopType != LoopNone {
			if handleLoopDetected(te, setup.task, setup.projectID, setup.task.ID, lc.detector, loopType, "", &lc.messages, setup.task.IsExploreMode()) {
				return turnStop
			}
			if !setup.task.IsExploreMode() && !setup.task.IsPlanMode() {
				// In act mode: loop detected, inject hint, continue
				setup.task.ExecutionState = types.ExecStateRunning
				te.persistTaskClaiming(setup)
				return turnNextLLMCall
			}
			// In exploration/plan mode: text-only responses are valid (brainstorming / planning).
			// Fall through to end the turn.
		}
		// Valid text-only response: remove previously injected hints from context
		lc.messages = lc.detector.removeHints(lc.messages)

		// Mark the response complete and the task idle FIRST so the assistant
		// message renders as finished (streaming cursor stops) before any
		// auto-summarization runs. This prevents the message from looking like it
		// is still generating while the summary is produced. Release the
		// running-task slot too - an idle task no longer blocks other tasks.
		te.releaseTaskSlot(setup)
		setup.task.Status = types.StatusReview
		setup.task.ExecutionState = types.ExecStateIdle
		te.persistTask(setup)
		te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
		te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
			"state": "idle", "summary": "Agent finished responding",
			"assistant_message_id": assistantMsgID,
			"prompt_tokens":        promptTokens, "predicted_tokens": predictedTokens,
			"cache_tokens": cacheTokens,
			"prompt_ms":    promptMs, "predicted_ms": predictedMs,
			"model_name": activeModel, "duration": duration,
		}})

		// Auto-summarize if context is approaching the window limit. Runs right
		// after the message completes, without waiting for the task transition.
		// The task goes idle here (context is NOT fed back to the LLM), so no
		// "Proceed" is injected — a queued real user message supersedes it.
		te.summarize.maybeAutoSummarize(setup, lc, activeModel, false)

		// Check for queued user messages.
		select {
		case um := <-setup.userMsgChan:
			if um.ContextChanged {
				logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("conversation context changed after response, re-syncing")
				te.foldQueuedUserMessage(setup, lc, um)
				// A fresh LLM call follows - re-acquire the running-task slot.
				if te.claimProviderSlot(setup, lc) {
					return turnStop
				}
				return turnNextLLMCall
			}
			logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("received message after response, continuing (setup.regenerate=%v)", um.Regenerate)
			te.foldQueuedUserMessage(setup, lc, um)
			if um.ModelName != "" {
				te.adoptMessageModel(setup, lc, um)
			}
			// The task went idle after the response - re-acquire the
			// running-task slot (a provider switch rides along via lc.mc).
			if te.claimProviderSlot(setup, lc) {
				return turnStop
			}
			setup.task.ExecutionState = types.ExecStateRunning
			te.persistTaskClaiming(setup)
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.TurnStatusEvent("running", "Processing new message"))
			return turnNextLLMCall
		default:
		}

		// Answer from the parent's context
		if out := te.autoAnswerTextStop(setup, lc, assistantText); out != turnWaitIdle {
			return out
		}

		// If this is a subtask, auto-start the next sibling
		if setup.task.ParentTaskID != "" {
			_ = te.AutoStartNextSibling(setup.projectID, setup.task.ID, setup.task.ParentTaskID)
		}
	} else if lc.reasoningContent.Len() > 0 {
		// Reasoning-only response: no visible content, no tool calls.
		// Persist the reasoning, inject a hint, and retry.
		reasoningText := lc.reasoningContent.String()

		// Persist the reasoning-only assistant message
		promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(lc.usage)
		logStatsSpeed(setup.task.ID, activeModel, promptTokens-cacheTokens, predictedTokens, promptMs, predictedMs)
		te.store.CreateTaskMessage(&types.TaskMessage{
			ID: uuid.New().String(), TaskID: setup.task.ID, ThreadID: setup.task.CurrentThreadID, Role: "assistant",
			Content: "", ReasoningContent: reasoningText,
			ModelName: activeModel, ProviderName: lc.providerName,
			PromptTokens: promptTokens, PredictedTokens: predictedTokens, CacheTokens: cacheTokens,
			PromptMs: promptMs, PredictedMs: predictedMs,
			Duration: time.Since(lc.callStartTime).Milliseconds(), CreatedAt: time.Now().UTC(),
		})

		// Append reasoning to conversation history so the LLM sees its own thinking.
		// Put in ReasoningContent so toAPIMessage sends it as reasoning_content.
		lc.messages = append(lc.messages, types.ChatMessage{Role: "assistant", Content: "", ReasoningContent: reasoningText})

		// Advance loop detector and check max hints (injects hint into messages + DB + WebSocket)
		lc.detector.recordTurn(nil, "", lc.finishReason)
		if handleLoopDetected(te, setup.task, setup.projectID, setup.task.ID, lc.detector, LoopReasoningOnly, "", &lc.messages, setup.task.IsExploreMode()) {
			return turnStop
		}

		lc.assistantContent.Reset()
		lc.reasoningContent.Reset()
		lc.finishReason = ""
		te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.TurnStatusEvent("running", "Reasoning-only response, requesting action"))

		// Auto-summarize if context is approaching the window limit
		te.summarize.maybeAutoSummarize(setup, lc, activeModel, true)

		return turnNextLLMCall
	} else {
		// The provider finished the call and said nothing. An assistant row is what marks its prompt
		// as answered, so none is written, and the run cannot settle quietly here: the task would
		// stay running with no answer and no event to send.
		logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Warnf("LLM call produced no response (finish_reason=%q)", lc.finishReason)
		te.releaseTaskSlot(setup)
		te.failTask(setup.task, setup.projectID, "LLM provider returned no response", "llm_empty_response")
		return turnStop
	}

	// Unreachable in practice: every branch above either returns or continues the loop. Kept so
	// the outcome is total rather than relying on the compiler accepting a missing return.
	return turnWaitIdle
}

// foldQueuedUserMessage rebuilds the loop context around a message taken off setup.userMsgChan. The
// detector's state goes first: new input breaks any pattern it was counting, and a rebuilt
// conversation leaves a stale text window behind. A regenerate truncates the assistant turns it
// replaces and appends its own message; anything else, a bare context change included, is rebuilt
// from the DB.
func (te *TaskExecutor) foldQueuedUserMessage(setup *taskExecSetup, lc *loopContext, um stream.UserMessage) {
	lc.detector.reset()
	lc.detector.clearHints()
	if um.Regenerate {
		lc.messages = truncateForRegenerate(lc.messages)
		lc.messages = append(lc.messages, userMessageToChatMessage(um))
		return
	}
	// Re-sync with DB to pick up the user message (already created by HandleSend)
	// and any deleted messages. Do NOT append — syncMessagesFromDB already includes it.
	lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)
}

// adoptMessageModel points the run at the provider and model a queued message names.
func (te *TaskExecutor) adoptMessageModel(setup *taskExecSetup, lc *loopContext, um stream.UserMessage) {
	switched := te.applyModelSwitch(lc.mc, setup.task, setup.projectID, um.ServerURL, um.ModelName, true, false, lc.silence)
	lc.client = switched.client
	lc.providerName = switched.providerName
}

// claimProviderSlot makes the run hold the running-task slot for the provider lc names, and reports
// whether the caller must return: the wait was cancelled and handleCancelledTask has already reported
// the stop. A run already holding that provider's slot gets a no-op out of syncTaskSlot, so a caller
// claims the same way whether or not the message switched provider. An empty provider URL is a run
// that holds no slot and needs none.
func (te *TaskExecutor) claimProviderSlot(setup *taskExecSetup, lc *loopContext) bool {
	curURL, _ := lc.mc.get()
	if curURL == "" {
		return false
	}
	if te.syncTaskSlot(setup, curURL) {
		return false
	}
	te.handleCancelledTask(setup.task.ID, setup.projectID)
	return true
}

// intakeUserMessage folds in a user message that arrived while the previous turn's tools were running.
// The non-blocking select is what makes this cheap to call at the top of every iteration. It reports
// whether executeTask must stop: a provider switch can queue for a running-task slot and lose.
func (te *TaskExecutor) intakeUserMessage(setup *taskExecSetup, lc *loopContext) (stop bool) {
	// Check for user messages that arrived during tool execution.
	select {
	case um := <-setup.userMsgChan:
		if um.ContextChanged {
			logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("conversation context changed, resetting loop detector")
			te.foldQueuedUserMessage(setup, lc, um)
			// The run never released its slot, so there is nothing here to re-claim.
			return false
		}
		logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("received message before LLM call (setup.regenerate=%v)", um.Regenerate)
		te.foldQueuedUserMessage(setup, lc, um)
		// A message aimed at an agent provider belongs to the agent executor, which is where
		// ForwardUserMessage sends it rather than to this channel.
		if um.ModelName != "" && !te.configStore.IsAgentProvider(um.ServerURL) {
			te.adoptMessageModel(setup, lc, um)
		}
		return te.claimProviderSlot(setup, lc)
	default:
	}
	// Nothing was waiting, which is the common case: keep running.
	return false
}

// idleOutcome is what the post-turn idle wait decided.
type idleOutcome int

const (
	// idleResume: the user sent something (or a summarization found queued input), so the
	// outer loop should make the next LLM call.
	idleResume idleOutcome = iota
	// idleStopped means setup.ctx was cancelled and handleCancelledTask has already run, so executeTask must
	// return.
	idleStopped
)

// waitForIdleInput blocks after a turn ends until the user sends a message, asks for a summarization, or
// the run is cancelled. The label is kept so `continue` semantics read the same as before the block was
// extracted; the exits that were bare `return` report idleStopped so the caller can return.
func (te *TaskExecutor) waitForIdleInput(setup *taskExecSetup, lc *loopContext, activeModel string) idleOutcome {
	// Block for user message before next LLM call
idleWait:
	for {
		select {
		case <-setup.ctx.Done():
			te.handleCancelledTask(setup.task.ID, setup.projectID)
			return idleStopped
		case req := <-setup.summarizeCh:
			// Manual summarization request from HandleSummarize.
			// Check if it was cancelled (user clicked ✕ or new request arrived).
			if req.ctx.Err() != nil {
				if req.ctx.Err() == context.Canceled {
					logging.For(logging.Summarize).With(logging.TaskID, setup.task.ID).Infof("summarization cancelled")
				}
				req.resultCh <- req.ctx.Err()
				continue idleWait
			}
			logging.For(logging.Summarize).With(logging.TaskID, setup.task.ID).Infof("manual summarization requested")
			// Fallback (non-utility) summarization requests go to the task's
			// CURRENT provider - lc.mc follows mid-run switches.
			sumURL, _ := lc.mc.get()
			lc.messages, _ = te.summarize.applySummarization(setup, req.ctx, lc.messages, sumURL, activeModel, req.resultCh)
			lc.detector.reset() // context changed, clear stale text window
			lc.detector.clearHints()
			// Count messages before DB sync (includes in-memory summary).
			preSyncCount := len(lc.messages)
			// Re-sync from DB to pick up any messages queued during summarization.
			lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)
			// Clear summarizing flag
			setup.task.Summarizing = false
			_ = te.store.SetTaskSummarizing(setup.task.ID, false)
			te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
			// If DB has more messages than we had in-memory, new input arrived.
			hasQueuedInput := len(lc.messages) > preSyncCount
			if !hasQueuedInput {
				// No new user input — stay idle, don't call LLM with just the summary.
				// Defensive release: the task is expected not to hold a slot
				// while idle (it was released when it went idle).
				te.releaseTaskSlot(setup)
				setup.task.Status = types.StatusReview
				setup.task.ExecutionState = types.ExecStateIdle
				te.persistTask(setup)
				te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
				// Re-block for a user message instead of looping into a fresh LLM call.
				continue idleWait
			}
			// User message present — continue to LLM call. The task was idle,
			// so re-acquire the running-task slot first (may queue).
			if curURL, _ := lc.mc.get(); curURL != "" && !te.syncTaskSlot(setup, curURL) {
				te.handleCancelledTask(setup.task.ID, setup.projectID)
				return idleStopped
			}
			setup.task.Status = types.StatusInProgress
			setup.task.ExecutionState = types.ExecStateRunning
			te.persistTaskClaiming(setup)
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.TurnStatusEvent("running", "Processing queued message"))
			return idleResume
		case um := <-setup.userMsgChan:
			if um.ContextChanged {
				logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("conversation context changed while idle, re-syncing")
				te.foldQueuedUserMessage(setup, lc, um)
				// A context change claims nothing: the run is idle and stays idle.
				continue idleWait
			}
			logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("received message while idle, continuing (setup.regenerate=%v)", um.Regenerate)
			te.foldQueuedUserMessage(setup, lc, um)
			if um.ModelName != "" && !te.configStore.IsAgentProvider(um.ServerURL) {
				te.adoptMessageModel(setup, lc, um)
			}
			// The task was idle - re-acquire the running-task slot (a
			// provider switch rides along via lc.mc) before the LLM call.
			if te.claimProviderSlot(setup, lc) {
				return idleStopped
			}
			setup.task.ExecutionState = types.ExecStateRunning
			te.persistTaskClaiming(setup)
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.TurnStatusEvent("running", "Processing new message"))
			return idleResume
		}
	}
}

// sendAndPersistError reports an error that ends the current run.
func (te *TaskExecutor) sendAndPersistError(taskID, message, code string) {
	te.persistAndSendError(taskID, message, code, true)
}

// persistAndSendError stores the error and streams it with the stored row's id
func (te *TaskExecutor) persistAndSendError(taskID, message, code string, turnOver bool) {
	task, _ := te.store.GetTaskByID(taskID)
	errMsg := &types.TaskMessage{
		ID: uuid.New().String(), TaskID: taskID, Role: "error",
		Content: fmt.Sprintf("[%s] %s", code, message), CreatedAt: time.Now(),
	}
	if task != nil {
		errMsg.ThreadID = task.CurrentThreadID
	}
	if err := te.store.CreateTaskMessage(errMsg); err != nil {
		logging.For(logging.Exec).With(logging.TaskID, taskID).Warnf("failed to persist error: %v", err)
	}
	if task == nil {
		return
	}
	te.appStreamMgr.SendTaskEvent(task.ProjectID, taskID, types.StreamEvent{Type: types.WSTypeError, Data: map[string]string{
		"message": message, "code": code, "message_id": errMsg.ID,
	}})
	if turnOver {
		te.appStreamMgr.ClearTaskBuffer(task.ProjectID, taskID)
	}
}

// syncMessagesFromDB re-syncs the in-memory conversation with the database.
// When messages are deleted from the DB (e.g., user deletes chat messages),
// the in-memory lc.messages slice is stale. This function reloads messages
// from the DB and rebuilds the conversation, ensuring deleted messages are
// excluded from the next LLM call. It also refreshes the task to pick up
// any thread changes from fork cleanup.
// Returns the synced messages and the (possibly updated) task.
func (te *TaskExecutor) syncMessagesFromDB(task *types.Task, worktreePath, projectName string, sandboxEnabled bool) ([]types.ChatMessage, *types.Task) {
	// Reload task from DB to pick up thread changes (e.g., fork cleanup switched active thread).
	if updatedTask, err := te.store.GetTaskByID(task.ID); err == nil && updatedTask != nil {
		task = updatedTask
	}
	taskMsgs, _ := te.store.GetTaskMessages(task.ID, task.CurrentThreadID)
	return buildMessagesFromDB(task, taskMsgs, worktreePath, projectName, sandboxEnabled), task
}

// truncateForRegenerate drops the last user message and everything after it from the
// in-memory conversation. The DB was already trimmed by DeleteTaskMessagesAfter; this is the
// same cut applied to lc.messages before the re-sent user message is appended.
func truncateForRegenerate(messages []types.ChatMessage) []types.ChatMessage {
	for i := len(messages) - 1; i > 0; i-- {
		if messages[i].Role == "user" {
			return messages[:i]
		}
	}
	return messages
}

// removeDuplicateUserMessage removes the first of two consecutive user messages
// with identical content. During regeneration, a race between the DELETE and POST
// handlers can leave the old user message in the DB alongside the new one.
// Compares both Content and ContentParts (for messages with image attachments).
func removeDuplicateUserMessage(messages []types.ChatMessage) []types.ChatMessage {
	for i := 1; i < len(messages); i++ {
		if messages[i].Role == "user" && messages[i-1].Role == "user" && userMessagesEqual(messages[i-1], messages[i]) {
			// Keep the newer one (at index i), remove the older one (at index i-1)
			messages = append(messages[:i-1], messages[i:]...)
			break
		}
	}
	return messages
}

// userMessagesEqual compares two user messages for content identity.
// Checks both plain Content and ContentParts (multi-modal messages store text in ContentParts[0].Text).
func userMessagesEqual(a, b types.ChatMessage) bool {
	if len(a.ContentParts) > 0 && len(b.ContentParts) > 0 {
		// Both have multi-modal parts — compare text parts and attachment count
		if len(a.ContentParts) != len(b.ContentParts) {
			return false
		}
		for j := range a.ContentParts {
			if a.ContentParts[j].Text != b.ContentParts[j].Text {
				return false
			}
			// Compare image presence (not raw data — attachments may differ between stale/new)
			aHasImg := a.ContentParts[j].ImageSource != nil
			bHasImg := b.ContentParts[j].ImageSource != nil
			if aHasImg != bHasImg {
				return false
			}
		}
		return true
	}
	// Plain text comparison
	return a.Content == b.Content
}

// failTask sets the task to failed/review state, persists it, broadcasts the
// update, and sends a WebSocket error event.
func (te *TaskExecutor) failTask(task *types.Task, projectID, output, code string) {
	task.ExecutionState = types.ExecStateFailed
	task.Status = types.StatusReview
	task.Output = output
	te.storeTask(task, projectID)
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	te.sendAndPersistError(task.ID, output, code)
}

func computeStats(usage *types.ChatUsage) (promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs int) {
	if usage != nil {
		return usage.PromptTokens, usage.CompletionTokens, usage.PromptTokensDetails.CachedTokens, int(usage.PromptMs), int(usage.PredictedMs)
	}
	// usage is nil only when ChatStream returns no SSE data at all (empty/error stream).
	// Callers should treat zero stats as "unknown" — no estimation needed.
	return 0, 0, 0, 0, 0
}

// logStatsSpeed logs prompt and generation speeds for a turn.
func logStatsSpeed(taskID, model string, processedTokens, predictedTokens, promptMs, predictedMs int) {
	var promptTps, genTps float64
	if promptMs > 0 {
		promptTps = float64(processedTokens) * 1000.0 / float64(promptMs)
	}
	if predictedMs > 0 {
		genTps = float64(predictedTokens) * 1000.0 / float64(predictedMs)
	}
	logging.For(logging.Exec).With(logging.TaskID, taskID).Infof("model=%s prompt=%.0f t/s, gen=%.0f t/s (prompt_tokens=%d, gen_tokens=%d, prompt_ms=%d, gen_ms=%d)", model, promptTps, genTps, processedTokens, predictedTokens, promptMs, predictedMs)
}

// buildToolResultEvent constructs the data payload for a WSTypeToolResult event.
// For bash and read_file tools, the output is truncated to ~1KB/10 lines with
// metadata about the full output. For all other tools, the full output is sent.
func buildToolResultEvent(toolID, toolName, result string, success, interrupted bool, duration int64,
	args map[string]any, rawArgs, path string, sandbox, streamed bool, messageID string) map[string]any {
	data := map[string]any{
		"tool_id":     toolID,
		"success":     success,
		"interrupted": interrupted,
		"duration":    duration,
		"args":        args,
		"raw_args":    rawArgs,
		"path":        path,
		"sandbox":     sandbox,
	}

	if toolName == types.ToolBash || toolName == types.ToolReadFile {
		preview, originalLines, originalBytes, truncated := toolresult.Truncate(result)
		data["output"] = preview
		data["result_truncated"] = truncated
		data["original_lines"] = originalLines
		data["original_bytes"] = originalBytes
		data["streamed"] = streamed
		data["message_id"] = messageID
	} else {
		data["output"] = result
	}

	return data
}
