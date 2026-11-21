package exec

import (
	"context"
	"time"

	"mike/backend/stream"
	"mike/backend/types"

	"mike/pkg/logging"
)

// streamOutcome is how streamTurn tells executeTask what the LLM call left behind. A stop is already
// persisted and broadcast by the branch that decided it, so executeTask only returns; a continue is
// a turn a user message replaced, whose conversation the fold has already rebuilt.
type streamOutcome int

const (
	// streamSettled: the call is over and its answer is in lc; finalizeTurn owns the rest of the turn.
	streamSettled streamOutcome = iota
	// streamContinue: a user message or a context change replaced the turn, so the loop makes the
	// next LLM call rather than settling this one.
	streamContinue
	// streamStopped: the run is over — cancelled, or failed with the cause on the task row.
	streamStopped
)

// streamTurn makes one LLM call: it holds a concurrency slot for the provider the run is on for the
// length of the call, streams the answer into lc while announcing it, and retries a transient
// failure. The wait ends when the call finishes, when the run is stopped, or when a user message
// replaces the turn; each of those returns the gate slot before it reports.
func (te *TaskExecutor) streamTurn(setup *taskExecSetup, lc *loopContext, gateURL, activeModel string) streamOutcome {
	concurrency := te.configStore.GetProviderConcurrency(gateURL)
	maxRPS := te.configStore.GetProviderMaxRequestsPerSecond(gateURL)

	// Queue status polling — runs while waiting for a gate slot, reporting
	// the call's own position (its queue entry is handed to the poller via
	// the onEnqueued callback as soon as it is enqueued).
	onEnqueued, queueDone := te.startGateQueuePoller(te.gate, gateURL, setup.projectID, setup.task.ID, setup.ctx)
	if _, err := te.gate.Acquire(setup.ctx, gateURL, concurrency, maxRPS, onEnqueued); err != nil {
		close(queueDone) // cleanup polling goroutine
		te.handleCancelledTask(setup.task.ID, setup.projectID)
		return streamStopped
	}
	close(queueDone)

	// Per-call context — can be cancelled independently to abort the LLM stream.
	callCtx, callCancel := context.WithCancel(setup.ctx)

	streamDone := make(chan struct{})
	// releaseAfterStreamEnd waits for the streaming goroutine to stop writing, then returns the
	// provider slot. The two seconds bounds this wait, not the goroutine: a slow abort is logged
	// and the turn moves on.
	releaseAfterStreamEnd := func() {
		select {
		case <-streamDone:
		case <-time.After(2 * time.Second):
			logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Warnf("streamDone timeout, forcing continue")
		}
		te.gate.Release(gateURL)
	}
	var streamErr error
	var lastProgressSend time.Time // throttle progress updates to ~1/2s
	var lastPredictedN int         // track predicted tokens for real-time stats events

	// Assemble LLM context, made to fit the window this model accepts.
	llmMessages := te.fitRequestToWindow(setup, lc, activeModel)

	// Set status before the LLM call so the frontend picks up streaming tokens.
	setup.task.Status = types.StatusInProgress
	setup.task.ExecutionState = types.ExecStateRunning
	te.persistTaskClaiming(setup)
	te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)

	// streamFlusher batches streaming events to reduce WebSocket write overhead.
	flusher := newStreamFlusher(setup.projectID, setup.task.ID,
		te.appStreamMgr.SendTaskEvent, lc.detector, lc.collapsedReasoning)
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
			lc.silence.clear()
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
			lc.foldToolCalls(choice.Delta.ToolCalls)
			for i := range len(lc.pendingToolCalls) {
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
				path := lc.toolPath(tc.ID, tc.Function.Arguments)
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
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeToolProgress, Data: map[string]any{"tool_id": tc.ID, "bytes_written": len(tc.Function.Arguments), "path": lc.toolPath(tc.ID, tc.Function.Arguments)}})
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
		defer lc.silence.clear() // every exit ends the notice, including a cancellation

		lc.usage, streamErr = lc.client.ChatStream(callCtx, llmMessages, lc.tools, tokenCb, progressCb, usageCb)

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
				case <-time.After(te.llmBackoff(i, streamErr)):
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
				lc.clearToolCalls()
				lc.finishReason = ""
				flusher.reset()
				lc.callStartTime = time.Now()
				lc.usage, streamErr = lc.client.ChatStream(callCtx, llmMessages, lc.tools, tokenCb, progressCb, usageCb)
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
		// An errored call's rows are never written, so its held reasoning is owed to no one.
		if streamErr == nil {
			flusher.sendHeldReasoning(lc.reasoningContent.String(), lc.assistantContent.String(), len(lc.pendingToolCalls) > 0)
		}
		close(streamDone)
	}()

	// Wait for: stream complete, new user message, or setup.task stop.
	select {
	case <-setup.ctx.Done():
		callCancel()
		te.gate.Release(gateURL) // Release gate immediately so other tasks can proceed
		<-streamDone             // Then wait for goroutine to finish
		te.handleCancelledTask(setup.task.ID, setup.projectID)
		return streamStopped
	case <-streamDone:
		// Stream completed normally (or all retries exhausted)
	case um := <-setup.userMsgChan:
		if um.ContextChanged {
			logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("conversation context changed during streaming, cancelling")
			lc.detector.reset()
			lc.detector.clearHints()
			callCancel()
			releaseAfterStreamEnd()
			lc.assistantContent.Reset()
			lc.reasoningContent.Reset()
			lc.clearToolCalls()
			lc.finishReason = ""
			lc.messages, setup.task, _ = te.conversationFromDB(setup.task, setup.worktreePath, setup.promptScope())
			return streamContinue
		}
		// New user message — cancel current LLM call.
		logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Infof("user message received during streaming, cancelling (setup.regenerate=%v)", um.Regenerate)
		lc.detector.reset() // user input breaks any loop pattern
		lc.detector.clearHints()
		callCancel()
		releaseAfterStreamEnd()

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
		lc.clearToolCalls()
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
			// and any deleted messages. Do NOT append — conversationFromDB already includes them.
			lc.messages, setup.task, _ = te.conversationFromDB(setup.task, setup.worktreePath, setup.promptScope())
		}

		// Apply model overrides from queued messages.
		for _, qm := range queuedMsgs {
			if qm.Regenerate {
				lc.messages = append(lc.messages, userMessageToChatMessage(qm))
			}
			if qm.ModelName != "" {
				switched := te.applyModelSwitch(lc.mc, setup.task, setup.projectID, qm.ServerURL, qm.ModelName, true, false, lc.silence)
				lc.client = switched.client
				lc.providerName = switched.providerName
			}
		}
		// If the queued messages moved the task to another provider, move
		// the running-task slot with it (no-op when the URL is unchanged).
		if curURL, _ := lc.mc.get(); curURL != "" && !te.syncTaskSlot(setup, curURL) {
			te.handleCancelledTask(setup.task.ID, setup.projectID)
			return streamStopped
		}

		// Notify frontend that response was cancelled.
		te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.TurnStatusEvent("cancelled", "Response cancelled, processing new message"))

		return streamContinue
	}
	callCancel()

	if streamErr != nil {
		// Release gate slot (was acquired before the LLM call above).
		te.gate.Release(gateURL)
		failure := classifyLLMFailure(streamErr)
		logging.For(logging.LLM).With(logging.TaskID, setup.task.ID).Errorf("LLM call failed (code=%s): %v", failure.code, streamErr)
		te.failTask(setup.task, setup.projectID, failure.sentence, failure.code)
		return streamStopped
	}

	// Release gate slot after LLM call (before tool execution).
	te.gate.Release(gateURL)
	return streamSettled
}
