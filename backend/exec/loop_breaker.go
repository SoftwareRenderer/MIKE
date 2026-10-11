package exec

// When loop_breaker is on, a run that reaches its consecutive-failed-turn limit is not handed back: the
// failed call and every turn after it are cut out of the conversation and "Proceed" is sent, so the model
// is asked again. The truncation belongs to the server's delete path — see Deps.RewindConversation.

import (
	"encoding/json"
	"slices"

	"mike/backend/types"

	"mike/pkg/logging"
)

// en.ts `chat.system.loopBreaker` must spell this sentence identically. docs/api-contract.md.
const loopBreakerNoteText = "Loop breaker removed the failed tool call and the turns after it, and told the agent to proceed"

// breakFailureLoop recovers from a run that reached its consecutive-failed-turn limit. It returns false
// when nothing was cut — preference off, no rewind hook, or the failing call no longer in the conversation
// — and the caller then reports the failure.
func (te *TaskExecutor) breakFailureLoop(setup *taskExecSetup, lc *loopContext) bool {
	if !te.configStore.GetLoopBreaker() || te.rewindConversation == nil {
		return false
	}
	name, rawArgs := lc.tracker.failingCall()
	if name == "" {
		return false
	}
	taskID := setup.task.ID
	msgs, err := te.store.GetTaskMessages(taskID, setup.task.CurrentThreadID)
	if err != nil {
		logging.For(logging.Exec).With(logging.TaskID, taskID).Warnf("loop breaker: conversation unreadable: %v", err)
		return false
	}
	cut, found := rewindPoint(msgs, name, rawArgs)
	if !found {
		return false
	}
	if err := te.rewindConversation(setup.projectID, taskID, cut); err != nil {
		logging.For(logging.Exec).With(logging.TaskID, taskID).Warnf("loop breaker: conversation not rewound (msg=%s): %v", cut, err)
		return false
	}

	// A cut can switch the run to another thread, so the thread is read after it.
	lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.projectName(), setup.sandboxEnabled)
	threadID := setup.task.CurrentThreadID

	note := systemNote{kind: loopBreakerNoteKind, severity: "info", content: loopBreakerNoteText}
	noteID := note.recordOnThread(te.store, taskID, threadID)
	note.show(te.appStreamMgr, setup.projectID, taskID, noteID, nil)

	if err := te.sendProceed(setup.projectID, taskID, threadID); err != nil {
		// The conversation is already cut, so continue without the instruction.
		logging.For(logging.Exec).With(logging.TaskID, taskID).Warnf("loop breaker: %q not recorded: %v", proceedInstruction, err)
	} else {
		lc.messages = append(lc.messages, types.ChatMessage{Role: "user", Content: proceedInstruction})
	}

	// The streak and the repetition window were counting turns that no longer exist. The delete path also
	// queues a context-changed notice that rebuilds the same way on the next iteration, but that notice is
	// dropped when the channel is full, so rebuild here too.
	lc.tracker.reset()
	lc.detector.reset()
	lc.detector.clearHints()
	logging.For(logging.Exec).With(logging.TaskID, taskID).Infof("loop breaker: cut the conversation at %s after %s failed", cut, name)
	te.appStreamMgr.SendTaskEvent(setup.projectID, taskID, types.TurnStatusEvent("running", "Failure loop broken, continuing"))
	return true
}

// rewindPoint returns the row to cut at: the earliest failed call matching the one that tripped the limit,
// or the assistant row that issued it when that row said nothing and made no other call. The cut deletes
// the named row and every row after it, so the earlier of the two ids is the one to name.
func rewindPoint(msgs []types.TaskMessage, name, rawArgs string) (string, bool) {
	toolRows, _ := toolCallRowsByOwner(msgs)
	for i, m := range msgs {
		if m.Role != "tool_call" {
			continue
		}
		_, toolName, toolArgs := parseToolCallRow(m)
		if toolName != name || toolArgs != rawArgs || !toolRowFailed(m) {
			continue
		}
		if owner, ok := silentOwnerOf(msgs, toolRows, i); ok {
			return msgs[owner].ID, true
		}
		return m.ID, true
	}
	return "", false
}

// toolRowFailed reports whether the row's stored result records a failure. An absent result is not one.
func toolRowFailed(m types.TaskMessage) bool {
	if m.ToolResult == "" {
		return false
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(m.ToolResult), &result); err != nil {
		return false
	}
	success, _ := result["success"].(bool)
	return !success
}

// silentOwnerOf returns the index of the assistant row that issued the tool_call at index i, when that row
// said nothing and made no other call. Ownership comes from toolCallRowsByOwner, so a row whose owner is
// not in the thread has no owner here either.
func silentOwnerOf(msgs []types.TaskMessage, toolRows map[int][]types.TaskMessage, i int) (int, bool) {
	row := msgs[i]
	owner := -1
	for idx, owned := range toolRows {
		if slices.ContainsFunc(owned, func(tc types.TaskMessage) bool { return tc.ID == row.ID }) {
			owner = idx
			break
		}
	}
	if owner < 0 {
		return -1, false
	}
	turn := msgs[owner]
	if len(toolRows[owner]) != 1 || turn.Content != "" || turn.ReasoningContent != "" {
		return -1, false
	}
	return owner, true
}
