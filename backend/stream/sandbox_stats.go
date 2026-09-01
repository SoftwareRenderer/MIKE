package stream

import (
	"sync"
	"sync/atomic"
	"time"

	"kanban/backend/types"
)

// DefaultSandboxStatsInterval is the sampling period for sandbox memory usage.
const DefaultSandboxStatsInterval = 2 * time.Second

// SandboxStatsSource provides live sandbox memory stats.
// Satisfied by *sandbox.SandboxManager.
type SandboxStatsSource interface {
	ActiveTaskIDs() []string
	GetMemoryStats(taskID string) *types.SandboxMemoryStats
}

// TaskStatusLookup looks up a task by ID. Satisfied by *store.Store.
type TaskStatusLookup interface {
	GetTaskByID(taskID string) (*types.Task, error)
}

// SandboxStatsBroadcaster sends task-scoped events. Satisfied by *AppStreamManager.
type SandboxStatsBroadcaster interface {
	SendTaskEvent(projectID, taskID string, event types.StreamEvent)
}

// SandboxStatsSampler samples sandbox container memory usage and pushes
// sandbox_stats task events to subscribers when the sampled value changes.
//
// Only tasks with status "in_progress" are sampled — updates run while the
// task is actually running and stop when it goes idle. Idle tasks keep their
// most recent sample in the latest map, served to clients on task
// subscription via LatestStatsProvider (see
// AppStreamManager.SetSandboxStatsProvider).
type SandboxStatsSampler struct {
	source      SandboxStatsSource
	tasks       TaskStatusLookup
	broadcaster SandboxStatsBroadcaster
	interval    time.Duration

	mu       sync.Mutex
	lastSent map[string]types.SandboxMemoryStats // taskID -> last pushed value
	latest   map[string]types.SandboxMemoryStats // taskID -> most recent sample

	stopOnce sync.Once
	stopCh   chan struct{}
	done     chan struct{}
	started  atomic.Bool
}

// NewSandboxStatsSampler creates a sampler. Call Start to begin periodic
// sampling; TickOnce is exported for deterministic tests.
func NewSandboxStatsSampler(source SandboxStatsSource, tasks TaskStatusLookup, broadcaster SandboxStatsBroadcaster, interval time.Duration) *SandboxStatsSampler {
	return &SandboxStatsSampler{
		source:      source,
		tasks:       tasks,
		broadcaster: broadcaster,
		interval:    interval,
		lastSent:    make(map[string]types.SandboxMemoryStats),
		latest:      make(map[string]types.SandboxMemoryStats),
		stopCh:      make(chan struct{}),
		done:        make(chan struct{}),
	}
}

// Start launches the sampling ticker.
func (s *SandboxStatsSampler) Start() {
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

// Stop terminates the sampling ticker, waiting for the goroutine to exit.
// Safe to call when Start was never invoked.
func (s *SandboxStatsSampler) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	if s.started.Load() {
		<-s.done
	}
}

// TickOnce samples all running tasks once and pushes changed values.
func (s *SandboxStatsSampler) TickOnce() {
	for _, taskID := range s.source.ActiveTaskIDs() {
		task, err := s.tasks.GetTaskByID(taskID)
		if err != nil || task == nil {
			continue // project-level "proj:..." containers or unknown tasks
		}
		if task.Status != types.StatusInProgress {
			continue // only update while the task is actually running
		}
		stats := s.source.GetMemoryStats(taskID)
		if stats == nil {
			continue
		}

		s.mu.Lock()
		s.latest[taskID] = *stats
		prev, sent := s.lastSent[taskID]
		changed := !sent || prev != *stats
		if changed {
			s.lastSent[taskID] = *stats
		}
		s.mu.Unlock()

		if changed {
			s.broadcaster.SendTaskEvent(task.ProjectID, taskID, types.StreamEvent{
				Type: types.WSTypeSandboxStats,
				Data: stats,
			})
		}
	}
}

// Latest returns the most recent sample for a task, or nil when the task has
// never been sampled. The value persists while the task is idle.
func (s *SandboxStatsSampler) Latest(taskID string) *types.SandboxMemoryStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats, ok := s.latest[taskID]
	if !ok {
		return nil
	}
	return &stats
}

// LatestStatsProvider returns a function suitable for
// AppStreamManager.SetSandboxStatsProvider so clients subscribing to a task
// receive its most recent usage.
func (s *SandboxStatsSampler) LatestStatsProvider() func(projectID, taskID string) *types.SandboxMemoryStats {
	return func(projectID, taskID string) *types.SandboxMemoryStats {
		return s.Latest(taskID)
	}
}
