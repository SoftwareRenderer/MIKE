package exec

import (
	"sync"
	"sync/atomic"
	"time"

	"kanban/backend/store"

	"kanban/pkg/logging"
)

// DefaultMemSupervisorInterval is how often MemorySupervisor.TickOnce runs in production.
const DefaultMemSupervisorInterval = 5 * time.Second

// memorySupervisorActions is the surface MemorySupervisor uses to pause and resume tasks.
// TaskExecutor implements it; tests substitute a recording mock.
type memorySupervisorActions interface {
	stopTask(taskID string) error
	startTask(projectID, taskID string) error
	startSubtask(projectID, taskID string) error
}

// MemorySupervisor keeps estimated sandbox memory within the gate capacity.
// When the gate queue is non-empty it pauses the lowest-estimate running task;
// when the queue drains and a paused task would fit it resumes that task.
// A user-initiated start or stop clears the supervisor flag, so only supervisor-paused
// tasks are ever auto-resumed.
type MemorySupervisor struct {
	store    *store.Store
	gate     *MemoryGate
	actions  memorySupervisorActions
	interval time.Duration

	stopOnce sync.Once
	stopCh   chan struct{}
	done     chan struct{}
	started  atomic.Bool
}

// NewMemorySupervisor builds a supervisor bound to the given gate and action set.
func NewMemorySupervisor(store *store.Store, gate *MemoryGate, actions memorySupervisorActions, interval time.Duration) *MemorySupervisor {
	return &MemorySupervisor{
		store:    store,
		gate:     gate,
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
	avgPeak := computeAvgPeakMB(running)

	// Pause path: queue non-empty means a task cannot start.
	if s.gate.QueueLen() > 0 && len(running) > 0 {
		lowest := running[0] // store returns ascending by peak
		estMB := estimateTaskMemory(lowest, avgPeak)
		if err := s.actions.stopTask(lowest.ID); err != nil {
			logging.For(logging.Exec).With(logging.TaskID, lowest.ID).Warnf("memory supervisor: pause failed: %v", err)
			return
		}
		// Set after stopTask returns: StopTask waits for the executor to exit, so a
		// concurrent user start during the stop window won't be overwritten.
		if err := s.store.SetMemorySupervisorPaused(lowest.ID, true); err != nil {
			logging.For(logging.Exec).With(logging.TaskID, lowest.ID).Warnf("memory supervisor: setting paused flag: %v", err)
		}
		logging.For(logging.Exec).With(logging.TaskID, lowest.ID).Infof(
			"memory supervisor: paused (est %d MB) to free memory for queued task(s)", estMB)
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
	used := s.gate.Used()
	candidate := paused[0]
	candEst := estimateTaskMemory(candidate, avgPeak) // same estimate acquireMemSlot would use
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
	logging.For(logging.Exec).With(logging.TaskID, candidate.ID).Infof(
		"memory supervisor: resumed (est %d MB, used %d/%d MB)", candEst, used, capacity)
}
