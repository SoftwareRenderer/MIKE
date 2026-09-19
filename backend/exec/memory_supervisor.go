package exec

import (
	"sync"
	"sync/atomic"
	"time"

	"mike/backend/store"
	"mike/backend/types"

	"mike/pkg/logging"
)

// DefaultMemSupervisorInterval is how often MemorySupervisor.TickOnce runs in production.
const DefaultMemSupervisorInterval = 5 * time.Second

// "Paused by user" is what the shared stop transition writes for every pause, including this one.
const memoryPauseOutput = "Paused for memory pressure — resumes automatically when memory frees"

// memorySupervisorActions is the surface MemorySupervisor uses to pause, resume and annotate tasks.
// TaskExecutor implements it; tests substitute a recording mock.
type memorySupervisorActions interface {
	stopTask(taskID string) error
	startTask(projectID, taskID string) error
	startSubtask(projectID, taskID string) error
	noteMemoryPause(taskID string)
	noteMemoryResume(taskID string)
}

// MemorySupervisor keeps sandbox memory inside the budget. With a non-empty gate queue it pauses the
// running task whose pause frees the least, one per tick, and resumes a paused task once the budget
// admits it. Only supervisor-paused tasks are auto-resumed: any user action clears the flag.
//
// The smallest pause is deliberate: stopping a task discards the turn it is working on and its
// container's files, and the largest task is the one most likely to finish soon and free its own
// memory.
type MemorySupervisor struct {
	store    *store.Store
	gate     *MemoryGate
	charges  liveCharges // the cost of each live container; nil when no container manager is wired
	actions  memorySupervisorActions
	interval time.Duration

	stopOnce sync.Once
	stopCh   chan struct{}
	done     chan struct{}
	started  atomic.Bool
}

// liveCharges reports what each live container costs the memory budget. It is implemented by
// *sandbox.SandboxManager.
type liveCharges interface {
	Charges() map[string]int64
}

// NewMemorySupervisor builds a supervisor. A nil charges means there are no container readings, so
// every task is costed at the reserve the budget holds for it.
func NewMemorySupervisor(store *store.Store, gate *MemoryGate, charges liveCharges, actions memorySupervisorActions, interval time.Duration) *MemorySupervisor {
	return &MemorySupervisor{
		store:    store,
		gate:     gate,
		charges:  charges,
		actions:  actions,
		interval: interval,
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start launches the periodic supervisor goroutine.
func (s *MemorySupervisor) Start() {
	s.started.Store(true)
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				s.TickOnce()
			}
		}
	}()
}

// Stop terminates the supervisor goroutine and waits for it to exit.
// Safe to call when Start was never invoked.
func (s *MemorySupervisor) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	if s.started.Load() {
		<-s.done
	}
}

// TickOnce performs one supervisor scan. Exported for deterministic tests.
func (s *MemorySupervisor) TickOnce() {
	capacity := s.gate.Capacity()
	if capacity == 0 {
		return
	}

	running, err := s.store.GetRunningSandboxedTasks()
	if err != nil {
		logging.For(logging.Exec).Warnf("memory supervisor: reading running tasks: %v", err)
		return
	}
	budget := s.gate.Budget()
	// Held sits beside the charge on purpose: a container capped at 2 GB holds 2 GB of the ceiling while
	// holding 25 MB of RAM, and the two figures side by side is what makes that readable from the log.
	logging.For(logging.Exec).Debugf(
		"memory budget: %d/%d MB (MIKE %d, held %d, reservations %d), host free %d, measured=%t, running %d, queued %d",
		budget.InUseMB, budget.CapacityMB, budget.MikeMB, budget.HeldMB, budget.ReservationsMB,
		budget.AvailableMB, budget.Measured, len(running), s.gate.QueueLen())

	// A queue usually means tasks waiting for one to finish, and a task finishing frees memory, so
	// waiting is the right response. A host below its free-RAM floor is different: nothing finishing
	// soon is why it is tight, so the supervisor has to free memory now.
	queueBlocked := s.gate.QueueLen() > 0
	emergency := s.gate.HostMemoryTight()
	if (queueBlocked || emergency) && len(running) > 0 {
		victim, cost := s.smallestReserve(running)
		if err := s.actions.stopTask(victim.ID); err != nil {
			logging.For(logging.Exec).With(logging.TaskID, victim.ID).Warnf("memory supervisor: pause failed: %v", err)
			return
		}
		// Set after stopTask returns: StopTask waits for the executor to exit, so a
		// concurrent user start during the stop window won't be overwritten.
		if err := s.store.SetMemorySupervisorPaused(victim.ID, true); err != nil {
			logging.For(logging.Exec).With(logging.TaskID, victim.ID).Warnf("memory supervisor: setting paused flag: %v", err)
		}
		// After the flag: the note promises the auto-resume that flag arms.
		s.actions.noteMemoryPause(victim.ID)
		if emergency {
			logging.For(logging.Exec).With(logging.TaskID, victim.ID).Warnf(
				"memory supervisor: host has %d MB free, under %d%% of its RAM — paused (frees %d MB), waiting on the host itself, queued %d",
				budget.AvailableMB, memoryAvailableFloorPct, cost, s.gate.QueueLen())
		} else {
			logging.For(logging.Exec).With(logging.TaskID, victim.ID).Infof(
				"memory supervisor: paused (frees %d MB) to free memory for queued task(s)", cost)
		}
		return
	}

	// Resume path: no queue and a paused task fits.
	paused, err := s.store.GetMemorySupervisorPausedTasks()
	if err != nil {
		logging.For(logging.Exec).Warnf("memory supervisor: reading paused tasks: %v", err)
		return
	}
	if len(paused) == 0 {
		return
	}
	used := s.gate.InUse()
	candidate := paused[0]
	candEst := reserveForTask(s.store, candidate) // the same reserve that acquireMemSlot would make
	if used+candEst > capacity {
		return // would immediately re-queue
	}
	if err := s.store.SetMemorySupervisorPaused(candidate.ID, false); err != nil {
		logging.For(logging.Exec).With(logging.TaskID, candidate.ID).Warnf("memory supervisor: clearing paused flag: %v", err)
		return
	}
	var startErr error
	if candidate.ParentTaskID != "" {
		startErr = s.actions.startSubtask(candidate.ProjectID, candidate.ID)
	} else {
		startErr = s.actions.startTask(candidate.ProjectID, candidate.ID)
	}
	if startErr != nil {
		logging.For(logging.Exec).With(logging.TaskID, candidate.ID).Warnf("memory supervisor: resume failed: %v", startErr)
		// Restore the flag so a start failure doesn't orphan the task as user-paused.
		_ = s.store.SetMemorySupervisorPaused(candidate.ID, true)
		return
	}
	s.actions.noteMemoryResume(candidate.ID)
	logging.For(logging.Exec).With(logging.TaskID, candidate.ID).Infof(
		"memory supervisor: resumed (reserve %d MB, used %d/%d MB)", candEst, used, capacity)
}

func (te *TaskExecutor) noteMemoryPause(taskID string) {
	// The guarded write doubles as the ownership check: a row someone else started or stopped since the
	// flag went on gets no note.
	set, err := te.store.SetMemoryPressurePausedOutput(taskID, memoryPauseOutput)
	if err != nil {
		logging.For(logging.Exec).With(logging.TaskID, taskID).Warnf("memory supervisor note: output text: %v", err)
		return
	}
	if !set {
		logging.For(logging.Exec).With(logging.TaskID, taskID).Debugf("memory supervisor note: pause no longer the supervisor's, note not recorded")
		return
	}
	te.noteMemoryTransition(taskID, memoryPauseNoteKind, "warning",
		"Task has been temporarily paused due to memory pressure, and will automatically resume")
}

func (te *TaskExecutor) noteMemoryResume(taskID string) {
	te.noteMemoryTransition(taskID, memoryResumeNoteKind, "info",
		"Memory freed up, so the task resumed automatically")
}

// noteMemoryTransition reads the row afresh because the stop this follows rewrote it.
func (te *TaskExecutor) noteMemoryTransition(taskID, kind, severity, content string) {
	task, err := te.store.GetTaskByID(taskID)
	if err != nil {
		logging.For(logging.Exec).With(logging.TaskID, taskID).Warnf("memory supervisor note: task unreadable: %v", err)
		return
	}
	note := systemNote{kind: kind, severity: severity, content: content}
	id := note.recordOnThread(te.store, taskID, task.CurrentThreadID)
	if id == "" {
		return
	}
	note.show(te.appStreamMgr, task.ProjectID, taskID, id, nil)
}

// smallestReserve picks the running task whose pause costs the memory budget least. A task with no
// live container is costed at the reserve the budget holds for it rather than at zero, so it cannot
// become the permanent first choice. Tasks of equal cost keep the order the store returns.
func (s *MemorySupervisor) smallestReserve(running []types.Task) (types.Task, int64) {
	var charges map[string]int64
	if s.charges != nil {
		charges = s.charges.Charges()
	}

	var best types.Task
	var bestCost int64
	for i, task := range running {
		cost, live := charges[task.ID]
		if !live {
			cost = reserveForTask(s.store, task)
		}
		if i == 0 || cost < bestCost {
			best, bestCost = task, cost
		}
	}
	return best, bestCost
}
