package exec

import "testing"

// TestCommitAndDoneIntent_ArmPeekConsume covers the intent state machine: arming is idempotent,
// peeking does not consume, and consuming happens once and reports the merge outcome it was given.
func TestCommitAndDoneIntent_ArmPeekConsume(t *testing.T) {
	t.Parallel()
	m := newBareTestExecutor(t).merge
	const taskID = "arm-peek-task"

	if m.CommitAndDonePending(taskID) {
		t.Fatal("an untouched task reports a click")
	}
	if m.takeCommitAndDone(taskID, true) {
		t.Fatal("taking from an un-armed task reported a click")
	}

	m.ArmCommitAndDone(taskID)
	if !m.CommitAndDonePending(taskID) {
		t.Fatal("armed task does not report a click")
	}
	m.ArmCommitAndDone(taskID) // a second click states the same intent
	if !m.CommitAndDonePending(taskID) {
		t.Fatal("re-arming lost the click")
	}
	if m.CommitAndDonePending("other-task") {
		t.Error("arming one task armed another")
	}

	// Two separate statements on purpose: `||` short-circuits, so the same call
	// written as `!f(x) || !f(x)` runs once and proves nothing about a second peek.
	if !m.CommitAndDonePending(taskID) {
		t.Fatal("peeking consumed the click")
	}
	if !m.CommitAndDonePending(taskID) {
		t.Fatal("a second peek consumed the click")
	}

	if !m.takeCommitAndDone(taskID, true) {
		t.Fatal("takeCommitAndDone did not report the click it consumed")
	}
	if m.CommitAndDonePending(taskID) {
		t.Fatal("the click survived being taken")
	}
	if m.takeCommitAndDone(taskID, true) {
		t.Fatal("the click was taken twice")
	}

	// A failed merge spends the click and answers false.
	m.ArmCommitAndDone(taskID)
	if m.takeCommitAndDone(taskID, false) {
		t.Error("a failed merge reported the task as done by the click")
	}
	if m.CommitAndDonePending(taskID) {
		t.Error("a failed merge left the click armed")
	}

	m.ArmCommitAndDone(taskID)
	m.clearCommitAndDone(taskID)
	if m.CommitAndDonePending(taskID) {
		t.Error("clear did not drop the click")
	}
	m.clearCommitAndDone(taskID)
}
