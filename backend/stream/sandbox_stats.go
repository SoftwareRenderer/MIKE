package stream

import (
	"sync"
	"time"

	"kanban/backend/types"
)

const (
	// DefaultSandboxSampleInterval is how often every live container is read. It is also how often
	// the memory budget is measured, so a container can only grow unmeasured for one interval.
	DefaultSandboxSampleInterval = 500 * time.Millisecond
	// DefaultSandboxStatsInterval is how often changed values are pushed to clients.
	DefaultSandboxStatsInterval = 2 * time.Second
)

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

// BudgetMeasure reports, in megabytes, what MIKE costs the memory budget: mikeMB is every container it
// started plus its own process (each container at its limit when the limit is the larger figure),
// heldMB what those containers actually hold. ok is false when the host could not be read at all, so
// the budget keeps its own ledger instead of being told the host is empty. It is implemented by a
// wiring type in backend/server.
type BudgetMeasure interface {
	Measure() (mikeMB, heldMB, availableMB, totalMB int64, ok bool)
}

// SandboxStatsSampler samples every live sandbox container and pushes sandbox_stats task events
// to subscribers when a sampled value changes.
//
// Sampling follows the container, not the task's status: a task that is idle with its container
// alive still holds memory, and the budget must see it. Tasks with no row (the project's "proj:"
// terminal container) are not sampled.
type SandboxStatsSampler struct {
	source         SandboxStatsSource
	tasks          TaskStatusLookup
	broadcaster    SandboxStatsBroadcaster
	sampleInterval time.Duration
	sendInterval   time.Duration

	mu        sync.Mutex
	lastSent  map[string]types.SandboxMemoryStats // taskID -> last pushed value
	latest    map[string]types.SandboxMemoryStats // taskID -> most recent sample
	projectOf map[string]string                   // taskID -> project, from the first sample

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup

	// The budget is measured once per sample tick and passed to the sink unchanged. Both fields are
	// under mu because SetBudget can be called after the sampler has started.
	budget     BudgetMeasure
	budgetSink func(mikeMB, heldMB, availableMB, totalMB int64)
}

// SetBudget wires the memory budget's inputs. Either argument may be nil, which leaves the budget
// unmeasured.
func (s *SandboxStatsSampler) SetBudget(measure BudgetMeasure, sink func(mikeMB, heldMB, availableMB, totalMB int64)) {
	s.mu.Lock()
	s.budget, s.budgetSink = measure, sink
	s.mu.Unlock()
}

// NewSandboxStatsSampler creates a sampler. Call Start to begin periodic sampling; SampleOnce,
// publish and TickOnce are exported for deterministic tests and for the budget's own tick.
func NewSandboxStatsSampler(source SandboxStatsSource, tasks TaskStatusLookup, broadcaster SandboxStatsBroadcaster, sampleInterval, sendInterval time.Duration) *SandboxStatsSampler {
	return &SandboxStatsSampler{
		source:         source,
		tasks:          tasks,
		broadcaster:    broadcaster,
		sampleInterval: sampleInterval,
		sendInterval:   sendInterval,
		lastSent:       make(map[string]types.SandboxMemoryStats),
		latest:         make(map[string]types.SandboxMemoryStats),
		projectOf:      make(map[string]string),
		stopCh:         make(chan struct{}),
	}
}

// Start launches the sampling ticker and the slower broadcast ticker. A zero interval means that
// ticker never fires, which is how a test drives the sampler by hand.
func (s *SandboxStatsSampler) Start() {
	s.run(s.sampleInterval, s.SampleOnce)
	s.run(s.sendInterval, s.publish)
}

func (s *SandboxStatsSampler) run(every time.Duration, fn func()) {
	if every <= 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				fn()
			}
		}
	}()
}

// Stop terminates both tickers, waiting for their goroutines to exit.
// Safe to call when Start was never invoked.
func (s *SandboxStatsSampler) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.wg.Wait()
}

// SampleOnce reads every live container once and records what each holds. This is the number
// every client and the memory budget is built from.
func (s *SandboxStatsSampler) SampleOnce() {
	for _, taskID := range s.source.ActiveTaskIDs() {
		task, err := s.tasks.GetTaskByID(taskID)
		if err != nil || task == nil {
			continue // the project's terminal container, or a task deleted mid-sample
		}
		stats := s.source.GetMemoryStats(taskID)
		if stats == nil {
			continue
		}

		s.mu.Lock()
		s.latest[taskID] = *stats
		s.projectOf[taskID] = task.ProjectID
		s.mu.Unlock()
	}
	s.reportBudget()
}

// reportBudget passes this tick's numbers to the memory budget. A sampler with no budget wired does
// nothing.
func (s *SandboxStatsSampler) reportBudget() {
	s.mu.Lock()
	measure, sink := s.budget, s.budgetSink
	s.mu.Unlock()
	if measure == nil || sink == nil {
		return
	}
	if mikeMB, heldMB, availableMB, totalMB, ok := measure.Measure(); ok {
		sink(mikeMB, heldMB, availableMB, totalMB)
	}
}

// TickOnce samples and then pushes what changed — the whole job of one deterministic tick.
func (s *SandboxStatsSampler) TickOnce() {
	s.SampleOnce()
	s.publish()
}

// publish sends one sandbox_stats event per task whose sample differs from what was last sent.
func (s *SandboxStatsSampler) publish() {
	type pending struct {
		projectID, taskID string
		stats             *types.SandboxMemoryStats
	}
	var out []pending

	s.mu.Lock()
	for taskID, stats := range s.latest {
		if prev, sent := s.lastSent[taskID]; sent && prev == stats {
			continue
		}
		s.lastSent[taskID] = stats
		snapshot := stats
		out = append(out, pending{projectID: s.projectOf[taskID], taskID: taskID, stats: &snapshot})
	}
	s.mu.Unlock()

	for _, p := range out {
		s.broadcaster.SendTaskEvent(p.projectID, p.taskID, types.StreamEvent{
			Type: types.WSTypeSandboxStats,
			Data: p.stats,
		})
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
