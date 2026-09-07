package exec

import (
	"strings"

	"kanban/backend/types"
)

// Task-level provider concurrency.
//
// A task holds a provider slot (te.taskGate, keyed by provider URL) from the start of
// executeTask until it goes idle, is stopped, or fails; resuming an idle task acquires one
// again. It is separate from the per-request gate (te.gate) and cannot cycle with it: a task
// slot is never acquired while holding a request slot. Agent providers are excluded — they
// manage their own concurrency.
//
// "In Progress + Pending" is in_progress with execution_state idle, allowed only while work is
// really pending: queued for a slot, a message just accepted (HandleSend), or a parent waiting
// on subtasks (MoveTask). Nothing may rest in in_progress+idle.

// acquireTaskSlot blocks until a running-task slot for providerURL is granted or the
// execution context is cancelled (which returns false). While waiting the task reads as
// started-but-pending (in_progress + idle → "Pending" badge) and queue position/wait events
// go to the task stream every second. On grant the task is marked running.
func (te *TaskExecutor) acquireTaskSlot(setup *taskExecSetup, providerURL string) bool {
	providerURL = strings.TrimRight(providerURL, "/")
	concurrency := te.configStore.GetProviderConcurrency(providerURL)

	// allowed Pending: queued for a slot is real pending work.
	// Show the task as started-but-pending while waiting for a slot:
	// In Progress column with the "Pending" badge.
	setup.task.Status = types.StatusInProgress
	setup.task.ExecutionState = types.ExecStateIdle
	te.persistTask(setup)
	te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)

	// Queue status polling — runs while waiting for a slot, reporting this
	// task's own position (its queue entry is handed to the poller via the
	// onEnqueued callback as soon as it is enqueued).
	onEnqueued, queueDone := te.startGateQueuePoller(te.taskGate, providerURL, setup.projectID, setup.task.ID, setup.ctx)
	defer close(queueDone)

	// A task start is not an LLM request start — RPS is enforced at request
	// time by the per-request gate, so maxRPS = 0 here.
	if _, err := te.taskGate.Acquire(setup.ctx, providerURL, concurrency, 0, onEnqueued); err != nil {
		return false
	}

	setup.taskSlotURL = providerURL

	// Slot granted — the task is actually running now.
	setup.task.ExecutionState = types.ExecStateRunning
	te.persistTaskClaiming(setup)
	te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
	return true
}

// releaseTaskSlot releases the running-task slot held by this execution
// (no-op when no slot is held).
func (te *TaskExecutor) releaseTaskSlot(setup *taskExecSetup) {
	if setup.taskSlotURL == "" {
		return
	}
	te.taskGate.Release(setup.taskSlotURL)
	setup.taskSlotURL = ""
}

// syncTaskSlot ensures the execution holds a slot for wantURL: a no-op when
// already holding it, otherwise releases any slot on another provider and
// acquires wantURL (blocking, with queue status — the task may show as
// Pending while the new provider is saturated). Returns false if the
// context was cancelled while waiting.
func (te *TaskExecutor) syncTaskSlot(setup *taskExecSetup, wantURL string) bool {
	wantURL = strings.TrimRight(wantURL, "/")
	if setup.taskSlotURL == wantURL {
		return true
	}
	if setup.taskSlotURL != "" {
		te.taskGate.Release(setup.taskSlotURL)
		setup.taskSlotURL = ""
	}
	return te.acquireTaskSlot(setup, wantURL)
}
