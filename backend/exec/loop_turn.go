package exec

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"time"

	"mike/backend/types"

	"github.com/google/uuid"
	"mike/pkg/logging"
)

// finalizeToolTurn settles a turn the model answered with tool calls. The assistant row is written
// before any call runs, so the DB order matches what the model emitted and every call has an owner
// to be rebuilt from; then the turn ends where its results put the task — back for another call,
// waiting on a question, or settled by the commit a review asked for.
func (te *TaskExecutor) finalizeToolTurn(setup *taskExecSetup, lc *loopContext, assistantText, activeModel string, maxFailures int) turnOutcome {
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
		te.announceClosingReasoning(setup.projectID, setup.task.ID, assistantMsgID, lc.reasoningContent.String(), lc.collapsedReasoning)
	}

	// What cancelling this kills, and what it must never reach, is in toolCallContext.
	toolCtx, cancelTools := toolCallContext(setup)
	defer cancelTools()
	// A task stop is a stop, and its own path reports it; only a message is an interrupt.
	interrupted := func() bool { return toolCtx.Err() != nil && setup.ctx.Err() == nil }

	// Every call gets an answer, including the ones this never runs.
	toolResults, turnFailures, hadGitCmd, hadRequestReview := te.runTurnToolCalls(setup, lc, toolCtx, interrupted, assistantMsgID)

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
	if te.settleAskQuestion(setup, lc, toolResults, hadRequestReview) {
		return turnStop
	}

	// request_review: handle task completion (commit/merge)
	// This runs AFTER auto-answer so that ask_question in the same turn
	// is answered before the task completes.
	if hadRequestReview {
		return te.settleReviewTurn(setup, lc, assistantMsgID, activeModel, hadGitCmd)
	}

	// Check consecutive identical tool call failures (turn-level).
	if count := lc.tracker.recordTurn(turnFailures); count >= maxFailures {
		// The breaker cuts the failed call and the turns after it out of the conversation and asks
		// the model to proceed; declining leaves the ordinary failure path.
		if te.breakFailureLoop(setup, lc) {
			return turnNextLLMCall
		}
		setup.task.ExecutionState = types.ExecStateFailed
		setup.task.Status = types.StatusReview
		setup.task.Output = fmt.Sprintf("Max consecutive failed turns (%d) reached", maxFailures)
		te.persistTask(setup)
		te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
		te.sendAndPersistError(setup.task.ID, fmt.Sprintf("Max consecutive failed turns (%d) reached", maxFailures), "max_failures")
		return turnStop
	}

	// The turn and its answers join the conversation the next call is built from.
	lc.appendToolTurn(assistantText, toolResults)

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

	lc.resetTurnOutput()

	// Auto-commit file changes as a checkpoint.
	te.createCheckpoint(setup.ctx, setup.projectID, setup.task.ID, assistantMsgID)

	// Auto-summarize if context is approaching the limit
	te.summarize.maybeAutoSummarize(setup, lc, activeModel, true)

	// Continue to call LLM again with tool results — don't fall through to blocking select
	return turnNextLLMCall
}

// runTurnToolCalls runs one turn's tool calls in the order the model asked for them and reports what
// they left behind: the results the provider is answered with, the per-call failures the turn is
// scored on, whether any bash call ran git, and whether the model asked for a review. The git flag
// matters because fsnotify can't detect diff state changes from a commit, checkout or merge — the
// branch pointer moves without any working tree file changing. An incomplete call is skipped rather
// than run, so the results are not index-aligned with lc.pendingToolCalls.
func (te *TaskExecutor) runTurnToolCalls(setup *taskExecSetup, lc *loopContext, toolCtx context.Context, interrupted func() bool, assistantMsgID string) (toolResults []toolResult, turnFailures []turnResult, hadGitCmd bool, hadRequestReview bool) {
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
		// A tool call takes its read, write and edit functions from the task's execution state, so look
		// that up again for each call. If the task was stopped or started again since this turn began,
		// the lookup finds nothing and the rest of this batch must not run.
		state := te.ownExecutionState(setup.task.ID, setup.ctx)
		switch {
		case interrupted():
			result, success, skipped, wasInterrupted = interruptedToolResult, false, true, true
		case refusal != "":
			result, success, skipped = refusal, false, true
			logging.For(logging.Tools).With(logging.TaskID, setup.task.ID).Warnf("refused %s: arguments that did not decode", tc.Function.Name)
		case state == nil:
			// The task was stopped or started again, so this call does not run. Storing Stopped as its
			// result gives the card and the conversation an answer for it.
			result, success, skipped = stoppedToolResult, false, true
			logging.For(logging.Tools).With(logging.TaskID, setup.task.ID).Infof("answered %s as stopped: the task was stopped or started again", tc.Function.Name)
		default:
			// tool_call event already sent during streaming, just execute and send result
			logging.For(logging.Tools).Infof("executing %s for setup.task=%s", tc.Function.Name, setup.task.ID)
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
		result = toolResultText(result)
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

		turnFailures = append(turnFailures, callResult(tc.Function.Name, tc.Function.Arguments, success))

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
	return toolResults, turnFailures, hadGitCmd, hadRequestReview
}

// settleAskQuestion answers the questions this turn asked, and reports whether the run must stop.
// An auto-started task is the only one whose questions get answered for it, from the parent's
// context; any other question hands the task back to the person, which also clears an armed
// commit-and-done click — the merge-conflict prompt invites exactly that question. A question beside
// a request_review is answered first and does not stop the run, because the review still has to run.
func (te *TaskExecutor) settleAskQuestion(setup *taskExecSetup, lc *loopContext, toolResults []toolResult, hadRequestReview bool) bool {
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
			return true
		}
	}
	return false
}

// settleReviewTurn does what a request_review asks — the commit, and the merge when the task asked
// for one — and puts the task where the result earned. Nothing reaches Done that did not reach the
// target branch: a merge that failed, and a task that was never going to merge, both land in Review.
// A conflict the resolution did not clear hands the model the instructions and runs the turn again.
//
// The turn's usage is what the stream reported, and nothing writes it once the stream has ended, so
// these are the numbers the assistant row was stored with.
func (te *TaskExecutor) settleReviewTurn(setup *taskExecSetup, lc *loopContext, assistantMsgID, activeModel string, hadGitCmd bool) turnOutcome {
	promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(lc.usage)
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
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: turnStatsFrame(
				setup.task.ExecutionState, assistantMsgID, promptTokens, predictedTokens, cacheTokens,
				promptMs, predictedMs, activeModel, time.Since(lc.callStartTime).Milliseconds(), nil)})
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
	te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: turnStatsFrame(
		setup.task.ExecutionState, assistantMsgID, promptTokens, predictedTokens, cacheTokens,
		promptMs, predictedMs, activeModel, time.Since(lc.callStartTime).Milliseconds(), nil)})
	te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
	if hadGitCmd {
		te.broadcastDiffIfGitCmd(setup.projectID, setup.task.ID, setup.task)
	}
	if setup.task.ParentTaskID != "" {
		_ = te.AutoStartNextSibling(setup.projectID, setup.task.ID, setup.task.ParentTaskID)
	}
	return turnStop
}

// appendToolTurn adds this turn's assistant message and one tool message per answered call to the
// conversation. Including tool_calls in the message history is critical for models like gemma to
// understand what tool was called and match it to the result. Reasoning stays in ReasoningContent so
// toAPIMessage sends it as reasoning_content.
func (lc *loopContext) appendToolTurn(assistantText string, toolResults []toolResult) {
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
}

// finalizeTextTurn settles the turn that answered in prose and called nothing. Waiting for the
// person is what it settles to — the phrase detector, a queued message or an auto-answer from the
// parent are the ways it keeps running instead.
func (te *TaskExecutor) finalizeTextTurn(setup *taskExecSetup, lc *loopContext, assistantText, activeModel string) turnOutcome {
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
	te.announceClosingReasoning(setup.projectID, setup.task.ID, assistantMsgID, lc.reasoningContent.String(), lc.collapsedReasoning)
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
	te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: turnStatsFrame(
		"idle", assistantMsgID, promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs,
		activeModel, duration, map[string]any{"summary": "Agent finished responding"})})

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
	return turnWaitIdle
}

// finalizeReasoningTurn settles the turn that thought and said nothing: the reasoning is stored and
// joins the history, so the model sees its own thinking, and the loop detector decides whether to
// ask for an action.
func (te *TaskExecutor) finalizeReasoningTurn(setup *taskExecSetup, lc *loopContext, activeModel string) turnOutcome {
	// Reasoning-only response: no visible content, no tool calls.
	// Persist the reasoning, inject a hint, and retry.
	reasoningText := lc.reasoningContent.String()

	// Persist the reasoning-only assistant message
	promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(lc.usage)
	logStatsSpeed(setup.task.ID, activeModel, promptTokens-cacheTokens, predictedTokens, promptMs, predictedMs)
	assistantMsgID := uuid.New().String()
	te.store.CreateTaskMessage(&types.TaskMessage{
		ID: assistantMsgID, TaskID: setup.task.ID, ThreadID: setup.task.CurrentThreadID, Role: "assistant",
		Content: "", ReasoningContent: reasoningText,
		ModelName: activeModel, ProviderName: lc.providerName,
		PromptTokens: promptTokens, PredictedTokens: predictedTokens, CacheTokens: cacheTokens,
		PromptMs: promptMs, PredictedMs: predictedMs,
		Duration: time.Since(lc.callStartTime).Milliseconds(), CreatedAt: time.Now().UTC(),
	})
	te.announceClosingReasoning(setup.projectID, setup.task.ID, assistantMsgID, reasoningText, lc.collapsedReasoning)

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
}
