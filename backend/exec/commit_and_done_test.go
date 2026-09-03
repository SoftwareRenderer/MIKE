package exec

import "testing"

// TestCommitAndDoneIntent_ArmPeekConsume covers the intent state machine: arming is idempotent,
// peeking does not consume, and consuming happens once and reports the merge outcome it was given.
func TestCommitAndDoneIntent_ArmPeekConsume(t *testing.T) {
	te := &TaskExecutor{}
	const taskID = "arm-peek-task"

	if te.CommitAndDonePending(taskID) {
		t.Fatal("an untouched task reports a click")
	}
	if te.takeCommitAndDone(taskID, true) {
		t.Fatal("taking from an un-armed task reported a click")
	}

	te.ArmCommitAndDone(taskID)
	if !te.CommitAndDonePending(taskID) {
		t.Fatal("armed task does not report a click")
	}
	te.ArmCommitAndDone(taskID) // a second click states the same intent
	if !te.CommitAndDonePending(taskID) {
		t.Fatal("re-arming lost the click")
	}
	if te.CommitAndDonePending("other-task") {
		t.Error("arming one task armed another")
	}

	if !te.CommitAndDonePending(taskID) || !te.CommitAndDonePending(taskID) {
		t.Fatal("peeking consumed the click")
	}

	if !te.takeCommitAndDone(taskID, true) {
		t.Fatal("takeCommitAndDone did not report the click it consumed")
	}
	if te.CommitAndDonePending(taskID) {
		t.Fatal("the click survived being taken")
	}
	if te.takeCommitAndDone(taskID, true) {
		t.Fatal("the click was taken twice")
	}

	// A failed merge spends the click and answers false.
	te.ArmCommitAndDone(taskID)
	if te.takeCommitAndDone(taskID, false) {
		t.Error("a failed merge reported the task as done by the click")
	}
	if te.CommitAndDonePending(taskID) {
		t.Error("a failed merge left the click armed")
	}

	te.ArmCommitAndDone(taskID)
	te.clearCommitAndDone(taskID)
	if te.CommitAndDonePending(taskID) {
		t.Error("clear did not drop the click")
	}
	te.clearCommitAndDone(taskID)
}
