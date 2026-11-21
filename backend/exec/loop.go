package exec

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mike/backend/git"
	"mike/backend/llama"
	"mike/backend/stream"
	"mike/backend/toolresult"
	"mike/backend/types"

	"github.com/google/uuid"
	"mike/pkg/logging"
)

// imageExts is the set of file extensions considered image files (allowed in staging).
var imageExts = map[string]struct{}{
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".webp": {}, ".bmp": {},
	".svg": {}, ".ico": {}, ".tiff": {}, ".tif": {}, ".avif": {}, ".apng": {},
}

func isImageFile(path string) bool {
	lower := strings.ToLower(filepath.Ext(path))
	_, ok := imageExts[lower]
	return ok
}

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

// backoffFor returns the wait duration for the next LLM retry attempt. It serves as the default backoff strategy.
func backoffFor(attempt int, err error) time.Duration {
	var le *llama.LlamaError
	if errors.As(err, &le) && le.IsRateLimit() && le.RetryAfter > 0 {
		return le.RetryAfter + time.Second + jitter()
	}
	// Waiting less than the observed silence just re-issues the request into the same slowness.
	var silent *llama.ErrStreamSilent
	if errors.As(err, &silent) && silent.Silent > 0 {
		return silent.Silent + time.Second + jitter()
	}
	return time.Duration(1<<attempt) * 2 * time.Second // 2s, 4s, 8s, ...
}

// llmIsRequestRejected reports a provider refusal. Since retrying the same request will fail again, the retry loop is skipped.
func llmIsRequestRejected(err error) bool {
	var le *llama.LlamaError
	return errors.As(err, &le) && le.IsRequestRejected()
}

// llmFailure pairs a failure code with a user-facing sentence. Different codes distinguish between transient connection issues and deterministic request rejections.
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

// streamSilenceWatcher announces a quiet provider and withdraws the notice when the stream speaks or the call ends. Silence indicates a slow provider, not a broken link.
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

// notice sends a lock-wait event to the frontend, specifying the current silence phase.
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
	// collapsedReasoning is this run's decision, handed in — see loopContext.collapsedReasoning.
	collapsedReasoning bool
	reasoningChars     int
	reasoningAnnounced int
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

func newStreamFlusher(projectID, taskID string, send func(projectID, taskID string, event types.StreamEvent), detector *loopDetector, collapsedReasoning bool) *streamFlusher {
	return &streamFlusher{
		toolCallDeltas:     make(map[string]toolCallDelta),
		projectID:          projectID,
		taskID:             taskID,
		send:               send,
		loopDetector:       detector,
		collapsedReasoning: collapsedReasoning,
	}
}

func (f *streamFlusher) appendToken(text string) {
	f.loopDetector.checkStreamingText(text)
	f.mu.Lock()
	f.tokenBuf.WriteString(text)
	f.mu.Unlock()
}

func (f *streamFlusher) appendReasoning(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.collapsedReasoning {
		f.reasoningChars += len(text)
		return
	}
	f.reasoningBuf.WriteString(text)
}

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
	f.announceReasoningLocked()

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

// announceReasoningLocked reports how much the call has reasoned, and only when that number moved. The
// caller holds f.mu, which is what keeps a fast provider to one count frame per tick.
func (f *streamFlusher) announceReasoningLocked() {
	if !f.collapsedReasoning || f.reasoningChars == f.reasoningAnnounced {
		return
	}
	f.reasoningAnnounced = f.reasoningChars
	f.send(f.projectID, f.taskID, types.StreamEvent{Type: types.WSTypeReasoningProgress, Data: map[string]any{"chars": f.reasoningChars}})
}

// sendHeldReasoning sends the reasoning the collapse held back, once the call is over and only when it is
// that call's whole answer. Called after the final flush, so the cards it carried are already on screen.
func (f *streamFlusher) sendHeldReasoning(reasoning, content string, hasToolCalls bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.collapsedReasoning || types.ShouldHideReasoning(reasoning, content, hasToolCalls) {
		return
	}
	f.send(f.projectID, f.taskID, types.StreamEvent{Type: types.WSTypeReasoning, Data: map[string]string{"text": reasoning}})
}

// reset clears all buffered content. Called before retry.
func (f *streamFlusher) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenBuf.Reset()
	f.reasoningBuf.Reset()
	// A retry re-reasons from nothing, so the count the client follows restarts with it.
	f.reasoningChars = 0
	f.reasoningAnnounced = 0
	f.toolCallDeltas = make(map[string]toolCallDelta)
}

// loopContext holds mutable state for the AI conversation loop.
type loopContext struct {
	messages         []types.ChatMessage // full conversation (all messages preserved)
	assistantContent strings.Builder
	reasoningContent strings.Builder
	pendingToolCalls []types.ChatToolCall
	// toolCallStream holds the streamed tool calls of the current LLM call; the loop reads them
	// back through pendingToolCalls.
	toolCallStream llama.ToolCallAccumulator
	toolPaths      map[string]string // tool_id -> the path once it could be read from the args
	prevArgLens    map[string]int    // tool_id -> arg length, for detecting streaming updates
	finishReason   string            // LLM finish_reason from last stream ("stop", "tool_calls", etc.)
	detector       *loopDetector
	silence        *streamSilenceWatcher // announces a quiet provider for the whole run
	tracker        failureTracker
	mc             *modelConfig
	client         *llama.LlamaClient
	providerName   string
	contextWindow  *int
	sandboxEnabled bool
	// collapsedReasoning is this run's answer to the preference, read once so a run never changes its own
	// wire: the flusher, the held-text send and the row announcement all have to agree.
	collapsedReasoning bool
	// tools is the set this run offers: the operating mode picks the acting set, so a mode change
	// that recreates the container re-reads it.
	tools         []types.ChatTool
	callStartTime time.Time // start of current LLM call (for per-call Duration)
	usage         *types.ChatUsage
	// reportedPrompt is the token count the provider gave for the stored turn this conversation was rebuilt
	// from. A run starting from stored rows has made no call yet, and this is the only measurement of what
	// that conversation costs that was not estimated.
	reportedPrompt int
	// summarizedAtLen is the conversation length a summarization last produced.
	summarizedAtLen int
	// pendingAdvisories holds lsp notes collected during tool execution
	pendingAdvisories        []advisory
	touchedFiles             []string
	shellMayHaveChangedFiles bool
	// lastDiagnosticsText suppresses re-injecting findings the model has already been shown.
	lastDiagnosticsText string
	sessionCost         float64 // accumulated API cost (USD) of completed calls for this task run
}

// foldToolCalls merges one frame's tool-call deltas into the calls of the current LLM call.
func (lc *loopContext) foldToolCalls(deltas []types.ChatToolCall) {
	lc.toolCallStream.Add(deltas)
	lc.pendingToolCalls = lc.toolCallStream.Calls()
}

// clearToolCalls drops the calls of an LLM call that will not finish — a retry, a cancellation, a
// user message that replaces the turn. What was accumulated would otherwise open the next call
// beside a call the provider already stopped sending.
func (lc *loopContext) clearToolCalls() {
	lc.toolCallStream.Reset()
	lc.toolPaths = nil
	lc.pendingToolCalls = nil
}

// resetTurnOutput drops what the last LLM call streamed. The four hold nothing the next call reads
// back, so a turn that is over leaves no partial answer behind in the conversation the next call is
// built from.
func (lc *loopContext) resetTurnOutput() {
	lc.assistantContent.Reset()
	lc.reasoningContent.Reset()
	lc.clearToolCalls()
	lc.finishReason = ""
}

// toolPath names the file a streaming tool call is working on, for the browser's live label. The
// arguments grow every token, so a path is looked up once and remembered.
func (lc *loopContext) toolPath(toolID, args string) string {
	if p, ok := lc.toolPaths[toolID]; ok {
		return p
	}
	p := extractPathFromArgs(args)
	if p == "" {
		return ""
	}
	if lc.toolPaths == nil {
		lc.toolPaths = make(map[string]string)
	}
	lc.toolPaths[toolID] = p
	return p
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

	var lc *loopContext
	defer func() {
		close(setup.done) // signal executor exit for ForwardUserMessage type-switch
		// Signal exit last, even if the cleanup below panics: callers waiting on it
		// (StopTask) then know every write of this defer has landed.
		defer func() {
			// A prepared setup carries the closure that ends the wait; a setup built without one (a test's)
			// owns its channel outright.
			if setup.finish != nil {
				setup.finish()
			} else if setup.finished != nil {
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

	// Both admission gates are taken before the container exists; acquireRunSlots says why.
	if !te.acquireRunSlots(setup) {
		return
	}

	// Reads, writes and edits inside the container go through the task's execution state, which this run
	// created when it started. If the task has been stopped or started again since then, this run ends
	// here without creating a container.
	state := te.ownExecutionState(setup.task.ID, setup.ctx)
	if state == nil {
		// A run released before it started is cancelled and removed without the task being marked
		// stopped, so mark the task stopped here. When a new run took the task over, that run sets the
		// task's status itself.
		if setup.ctx.Err() != nil {
			te.handleCancelledTask(setup.task.ID, setup.projectID)
		}
		return
	}

	imageToUse, ok := te.openTaskSandbox(setup, state)
	if !ok {
		return
	}

	if te.configStore.IsAgentProvider(setup.serverURL) {
		logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("detected agent provider (url=%s), calling executeTaskWithAgent", setup.serverURL)
		te.executeTaskWithAgent(setup)
		logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("executeTaskWithAgent returned")
		return
	}

	lc = te.newLoopContext(setup)

	maxFailures := setup.task.MaxConsecutiveFailedTurns
	if maxFailures == 0 {
		maxFailures = 3
	}

	for {
		select {
		case <-setup.ctx.Done():
			te.handleCancelledTask(setup.task.ID, setup.projectID)
			return
		default:
		}

		// A model switch, an operating-mode change or a changed agent grant, each answered as it
		// arrives rather than at the next LLM call.
		if te.refreshRunConfig(setup, lc, state, imageToUse) {
			return
		}

		// Check for user messages that arrived during tool execution.
		if te.intakeUserMessage(setup, lc) {
			return
		}

		// Clear turn-specific state before each LLM call.
		lc.prevArgLens = make(map[string]int)
		lc.resetTurnOutput()

		// Keyed by the CURRENT provider — lc.mc follows mid-run model/provider switches, while
		// setup.serverURL is only the start-time URL.
		gateURL, activeModel := lc.mc.get()

		// One LLM call: the gate slot, the stream, and the wait for its end.
		switch te.streamTurn(setup, lc, gateURL, activeModel) {
		case streamStopped:
			return
		case streamContinue:
			continue
		}

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

// Which tools a task exposes. The operating mode chooses the acting set (plan and explore replace it),
// and contributed and MCP tools are appended to whatever the mode chose.
func (te *TaskExecutor) toolDefinitionsFor(ctx context.Context, task *types.Task) []types.ChatTool {
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
	tools = append(tools, llama.ContributedToolDefinitions(te.contributedSpecs())...)
	return append(tools, llama.ContributedToolDefinitions(te.mcpSpecs(ctx, task.ProjectID))...)
}

// mcpSpecs reads the offer per turn, so saving a server or enabling one for the project takes effect on
// the next turn. A project that enabled nothing costs no dial.
func (te *TaskExecutor) mcpSpecs(ctx context.Context, projectID string) []types.ToolSpec {
	if te.mcpReg == nil {
		return nil
	}
	return te.mcpReg.SpecsForProject(ctx, projectID)
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

// heldReasoningFrame is the announcement a collapsed call owes when its reasoning stays hidden: the total
// the reader never saw, and the row that stores it. `complete` names only the run's last row, so without
// this every earlier call of a multi-call turn would be unopenable until the thread was read again.
func heldReasoningFrame(msgID, reasoning, content string, hasToolCalls, collapsed bool) (types.StreamEvent, bool) {
	// Nothing reasoned is nothing withheld: a frame here would hand the reader a toggle over an empty row.
	if !collapsed || reasoning == "" || !types.ShouldHideReasoning(reasoning, content, hasToolCalls) {
		return types.StreamEvent{}, false
	}
	return types.StreamEvent{Type: types.WSTypeReasoningProgress, Data: map[string]any{
		"chars": len(reasoning), "message_id": msgID,
	}}, true
}

// announceHeldReasoning asks where the row is written. The frame is what a reader needs to open the
// reasoning through `?full=1`; the reader already holding the text is owed nothing.
func (te *TaskExecutor) announceHeldReasoning(projectID, taskID, msgID, reasoning, content string, hasToolCalls, collapsed bool) {
	frame, ok := heldReasoningFrame(msgID, reasoning, content, hasToolCalls, collapsed)
	if !ok {
		return
	}
	te.appStreamMgr.SendTaskEvent(projectID, taskID, frame)
}

// finalizeTurn handles what follows one LLM call: persist the assistant message and run its tool
// calls, settle on a text answer, or record a reasoning-only turn. Which shape it is, it decides
// here; each shape settles in its own stage.
func (te *TaskExecutor) finalizeTurn(setup *taskExecSetup, lc *loopContext, activeModel string, maxFailures int) turnOutcome {
	assistantText := lc.assistantContent.String()

	if len(lc.pendingToolCalls) > 0 {
		return te.finalizeToolTurn(setup, lc, assistantText, activeModel, maxFailures)
	}
	if assistantText != "" {
		return te.finalizeTextTurn(setup, lc, assistantText, activeModel)
	}
	if lc.reasoningContent.Len() > 0 {
		return te.finalizeReasoningTurn(setup, lc, activeModel)
	}

	// The provider finished the call and said nothing. An assistant row is what marks its prompt
	// as answered, so none is written, and the run cannot settle quietly here: the task would
	// stay running with no answer and no event to send.
	logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Warnf("LLM call produced no response (finish_reason=%q)", lc.finishReason)
	te.releaseTaskSlot(setup)
	te.failTask(setup.task, setup.projectID, "LLM provider returned no response", "llm_empty_response")
	return turnStop
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
	// and any deleted messages. Do NOT append — conversationFromDB already includes it.
	lc.messages, setup.task, _ = te.conversationFromDB(setup.task, setup.worktreePath, setup.promptScope())
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
			// Nothing else writes to the thread while the summary is being made, so its newest user
			// row is compared across the summarization: a different row is input that arrived
			// meanwhile. A length cannot answer this, because the summarization writes a row of its own
			// and re-dates anything that arrived while it ran.
			queuedThread := setup.task.CurrentThreadID
			queuedBefore, _ := te.store.GetLastUserMessageInThread(queuedThread)
			lc.messages, _, _ = te.summarize.applySummarization(setup, req.ctx, lc.messages, sumURL, activeModel, req.resultCh, false)
			lc.detector.reset() // context changed, clear stale text window
			lc.detector.clearHints()
			// Re-sync from DB to pick up any messages queued during summarization, and any deleted ones.
			lc.messages, setup.task, _ = te.conversationFromDB(setup.task, setup.worktreePath, setup.promptScope())
			// This conversation is what that summarization produced, so the auto triggers wait for it
			// to grow.
			lc.summarizedAtLen = len(lc.messages)
			queuedNow, _ := te.store.GetLastUserMessageInThread(queuedThread)
			hasQueuedInput := queuedNow != "" && queuedNow != queuedBefore
			// Clear summarizing flag
			setup.task.Summarizing = false
			_ = te.store.SetTaskSummarizing(setup.task.ID, false)
			te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
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

// conversationFromDB rebuilds the loop's conversation from the newest summarization row onward, which is
// all buildLLMContext keeps of a rebuilt conversation anyway. It reads the task as stored now — a fork
// cleanup can move the run to another thread mid-turn — and rebuilds rather than edits, because rows are
// added and deleted outside the run. The token count it returns is the provider's own last measurement of
// this conversation, which is the one number a resumed run cannot estimate for itself.
func (te *TaskExecutor) conversationFromDB(task *types.Task, worktreePath string, scope promptScope) ([]types.ChatMessage, *types.Task, int) {
	if updated, err := te.store.GetTaskByID(task.ID); err == nil && updated != nil {
		task = updated
	}
	rows, _ := te.store.GetTaskMessagesFromSummary(task.ID, task.CurrentThreadID)
	return buildMessagesFromDB(task, rows, worktreePath, scope), task, lastReportedPrompt(rows)
}

// lastReportedPrompt is the prompt count the provider answered the newest assistant row with.
func lastReportedPrompt(rows []types.TaskMessage) int {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Role == "assistant" && rows[i].PromptTokens > 0 {
			return rows[i].PromptTokens
		}
	}
	return 0
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
	te.fail(task, projectID, output, output, code)
}

// fail is a run ending in Review: the row keeps the output, the board sees the state, and the panel
// gets userMessage, which is the same sentence as the output except where an internal step is the cause
// and naming it would tell a person nothing. The row goes to Review, never Failed: the worktree stands
// so the cause can be fixed and the task resumed.
func (te *TaskExecutor) fail(task *types.Task, projectID, output, userMessage, code string) {
	task.ExecutionState = types.ExecStateFailed
	task.Status = types.StatusReview
	task.Output = output
	te.storeTask(task, projectID)
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	te.sendAndPersistError(task.ID, userMessage, code)
}

// turnStatsFrame builds the WSTypeComplete payload a turn ends with. The frame is a flat map of these
// keys (docs/api-contract.md); extra carries any sentence an outcome adds.
func turnStatsFrame(state any, msgID string, promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs int, model string, durationMs int64, extra map[string]any) map[string]any {
	frame := map[string]any{
		"state": state, "assistant_message_id": msgID,
		"prompt_tokens": promptTokens, "predicted_tokens": predictedTokens,
		"cache_tokens": cacheTokens, "prompt_ms": promptMs, "predicted_ms": predictedMs,
		"model_name": model, "duration": durationMs,
	}
	maps.Copy(frame, extra)
	return frame
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
