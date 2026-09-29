package exec

import (
	"sync"

	"mike/backend/store"
	"strings"

	"mike/backend/types"
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
// really pending: queued for a slot, a user message just accepted (HandleSend), a parent waiting
// on unfinished subtasks (MoveTask), or a subtask under a started parent that the user has not
// started (types.SubtaskStatusFor). Nothing may rest in in_progress+idle — check those four cases
// before adding a writer.

// queuedWaiters records which provider task-slot queue entry each task is blocked behind, so a request
// can reach the waiter a task is sitting on. An entry is recorded when the gate enqueues it and dropped
// when that wait ends, so "queued" here and "queued" in the gate are the same fact, read a moment apart.
type queuedWaiters struct {
	mu    sync.Mutex
	entry map[string]*queueEntry
}

// set records e as the waiter taskID is blocked behind.
func (w *queuedWaiters) set(taskID string, e *queueEntry) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.entry == nil {
		w.entry = make(map[string]*queueEntry)
	}
	w.entry[taskID] = e
}

// clear drops taskID's registration, but only while e is what is recorded there. A superseded run whose
// wait ends late must not unregister the run that replaced it.
func (w *queuedWaiters) clear(taskID string, e *queueEntry) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.entry[taskID] == e {
		delete(w.entry, taskID)
	}
}

// get returns the waiter taskID is blocked behind, or nil when it is not waiting for a slot.
func (w *queuedWaiters) get(taskID string) *queueEntry {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.entry[taskID]
}

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

	// The same hand-off also records where a prioritize request can find this task's waiter.
	entry, err := te.taskGate.Acquire(setup.ctx, providerURL, concurrency, 0, func(e *queueEntry) {
		te.taskSlotWaits.set(setup.task.ID, e)
		onEnqueued(e)
	})
	defer te.taskSlotWaits.clear(setup.task.ID, entry)

	// A task start is not an LLM request start — RPS is enforced at request
	// time by the per-request gate, so maxRPS = 0 here.
	if err != nil {
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

// PrioritizeQueuedTask moves a task waiting for a provider running-task slot to the front of that queue,
// so the next slot the provider frees is its caller's. One shot: the reorder covers the wait it lands
// during, and a later wait by the same task queues normally. It returns false when the task is not
// waiting for a slot — running, waiting on the memory budget, or not started — which is an answer about
// the queue, not a fault.
func (te *TaskExecutor) PrioritizeQueuedTask(taskID string) bool {
	return te.taskGate.Promote(te.taskSlotWaits.get(taskID))
}

// memoryReserveMB is what the memory budget reserves for one sandboxed task: the limit its project
// configures, which is the cap its container is created with. Zero means the project is unlimited.
func memoryReserveMB(limitMB int) int64 {
	if limitMB <= 0 {
		return 0
	}
	return int64(limitMB)
}

// reserveForTask answers the same question from a task row, for callers holding no sandbox options.
func reserveForTask(st *store.Store, task types.Task) int64 {
	project, err := st.GetProject(task.ProjectID)
	if err != nil || project == nil {
		return 0
	}
	return memoryReserveMB(project.SandboxMemoryLimit)
}

// acquireMemSlot blocks until the memory budget has room for this task. While blocked the task shows
// as Pending (in_progress+idle). Returns false when the context was cancelled; a disabled gate is a
// pass-through.
//
// The reservation is the limit the sandbox is about to be created with. A non-sandboxed run books the
// same number and keeps it for the whole run: its agent CLI runs on the host with no cgroup to
// measure, and the memory it takes is memory MIKE started.
func (te *TaskExecutor) acquireMemSlot(setup *taskExecSetup) bool {
	// An executor with no gate wired decides nothing about memory, exactly as one whose capacity is 0.
	if te.memGate == nil || te.memGate.Capacity() == 0 {
		return true
	}

	estimate := memoryReserveMB(setup.sandboxOpts.MemoryLimit)

	setup.task.Status = types.StatusInProgress
	setup.task.ExecutionState = types.ExecStateIdle
	te.persistTask(setup)
	te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)

	if err := te.memGate.Acquire(setup.ctx, estimate, nil); err != nil {
		return false
	}
	setup.memAcquiredMB = estimate

	setup.task.ExecutionState = types.ExecStateRunning
	te.persistTaskClaiming(setup)
	te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
	return true
}

// confirmMemReservation ends the reservation once the sandbox container exists, because the budget
// counts that container directly from then on. A non-sandboxed run never reaches this: it keeps the
// reservation until the run ends, where the ordered cleanup returns it.
func (te *TaskExecutor) confirmMemReservation(setup *taskExecSetup) {
	if !setup.sandboxEnabled {
		return
	}
	te.releaseMemSlot(setup)
}

// releaseMemSlot returns this execution's memory budget (no-op when nothing was acquired).
func (te *TaskExecutor) releaseMemSlot(setup *taskExecSetup) {
	if setup.memAcquiredMB == 0 {
		return
	}
	te.memGate.Release(setup.memAcquiredMB)
	setup.memAcquiredMB = 0
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
