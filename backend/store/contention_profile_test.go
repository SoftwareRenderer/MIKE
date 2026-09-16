// Measurement tool, not an assertion: does anyone ever wait for the store's single database
// connection, and for how long? That is the question that decides whether the store needs a
// context.Context, because queueing for the pool is the only wait a context can cut short.
//
// The pool is pinned to one connection on purpose (the PRAGMAs are per-connection, so
// widening it silently turns foreign-key enforcement off on new connections) and several
// methods hold s.mu across statements in place of a transaction, so queueing exists. It is
// cheap behind an indexed lookup and expensive behind a scan of a long task's history.
//
// Skipped in a normal run because it contends on purpose:
//
//	MIKE_STORE_PROFILE=1 go test ./backend/store -run ContentionProfile -v
//
// Profiles a seeded database by default. MIKE_STORE_DB=/path profiles a copy of a real one
// (copied to a temp dir, so the original is never opened and the copy absorbs the writes).

package store

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mike/backend/types"
)

const profileEnv = "MIKE_STORE_PROFILE"

func TestStoreContentionProfile(t *testing.T) {
	if os.Getenv(profileEnv) == "" {
		t.Skipf("profiling tool: set %s=1 to run (it contends on purpose)", profileEnv)
	}
	t.Setenv("MIKE_ENCRYPTION_KEY", "profile-only-key")

	s, cleanup := openProfileStore(t)
	defer cleanup()

	var projects, tasks, threads, msgs int
	if err := s.QueryRow(`SELECT (SELECT COUNT(*) FROM projects), (SELECT COUNT(*) FROM tasks),
		(SELECT COUNT(*) FROM task_threads), (SELECT COUNT(*) FROM task_messages)`).
		Scan(&projects, &tasks, &threads, &msgs); err != nil {
		t.Fatalf("shape: %v", err)
	}
	t.Logf("database shape: %d projects, %d tasks, %d threads, %d messages", projects, tasks, threads, msgs)

	var heavyThread string
	var heavyRows int
	if err := s.QueryRow(`SELECT thread_id, COUNT(*) n FROM task_messages
		WHERE thread_id <> '' GROUP BY thread_id ORDER BY n DESC LIMIT 1`).Scan(&heavyThread, &heavyRows); err != nil {
		t.Fatalf("heaviest thread: %v", err)
	}
	var scratchProject, scratchTask string
	if err := s.QueryRow(`SELECT project_id, id FROM tasks LIMIT 1`).Scan(&scratchProject, &scratchTask); err != nil {
		t.Fatalf("scratch rows: %v", err)
	}
	oneTask, err := s.GetTaskByID(scratchTask)
	if err != nil {
		t.Fatalf("scratch task: %v", err)
	}

	// The workloads the running system actually issues: the board's first read, a task's full
	// history (the HTTP history endpoint, and every turn's buildLLMContext), and the two writes
	// a turn performs.
	var msgSeq atomic.Int64
	newID := func() string { return fmt.Sprintf("prof-msg-%d", msgSeq.Add(1)) }
	workloads := []struct {
		name string
		op   func()
	}{
		{"read  GetTaskByID", func() { profileMust(s.GetTaskByID(scratchTask)) }},
		{"read  GetAllProjectCounts", func() { s.GetAllProjectCounts() }},
		{fmt.Sprintf("read  GetTaskMessages (%d rows)", heavyRows),
			func() { profileMustSlice(s.GetTaskMessages(scratchTask, heavyThread)) }},
		{"write UpdateTask (1 stmt)", func() { profileMustErr(s.UpdateTask(oneTask)) }},
		{"write CreateTaskMessage (3 stmts, 1 lock)", func() {
			profileMustErr(s.CreateTaskMessage(profileMessage(newID(), scratchTask, heavyThread)))
		}},
	}

	t.Logf("\n%-42s %4s %9s %9s %9s %9s %8s %8s %10s", "workload", "conc", "p50", "p95", "p99", "max", "ops/s", "waitN", "waitDur")
	for _, w := range workloads {
		for _, conc := range []int{1, 4, 8, 16} {
			r := runConcurrent(s, conc, 200, w.op)
			t.Logf("%-42s %4d %9s %9s %9s %9s %8.0f %8d %10s",
				w.name, conc, dur(r.pctile(0.50)), dur(r.pctile(0.95)), dur(r.pctile(0.99)), dur(r.max),
				r.opsPerSec(), r.waitCount, r.waitDur.Round(time.Millisecond))
		}
	}

	// Reader lockout: how long does one turn's write stall an unrelated read? That is what a
	// browser sees — an HTTP handler waiting behind the executor.
	idle := runConcurrent(s, 4, 200, func() { profileMust(s.GetTaskByID(scratchTask)) })
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				profileMustErr(s.CreateTaskMessage(profileMessage(newID(), scratchTask, heavyThread)))
			}
		}
	}()
	time.Sleep(20 * time.Millisecond) // let the writer reach steady state
	busy := runConcurrent(s, 4, 200, func() { profileMust(s.GetTaskByID(scratchTask)) })
	close(done)
	wg.Wait()
	t.Logf("\nGetTaskByID at conc=4 while a writer runs CreateTaskMessage back to back:\n  idle: p50=%s p99=%s max=%s\n  busy: p50=%s p99=%s max=%s  (waitN+%d, waitDur+%s)",
		dur(idle.pctile(0.50)), dur(idle.pctile(0.99)), dur(idle.max),
		dur(busy.pctile(0.50)), dur(busy.pctile(0.99)), dur(busy.max), busy.waitCount, busy.waitDur.Round(time.Millisecond))

	// The unbounded case. s.mu is released before the driver hands the connection back, so a
	// caller can also wait for the connection itself — and with no context on the API there is
	// no way to stop waiting. One long aggregate holds the only connection; every other caller
	// can only wait for it to finish.
	var scanDur, blockedDur time.Duration
	var scanWaits int64
	var inner sync.WaitGroup
	inner.Add(2)
	go func() {
		defer inner.Done()
		var n int64
		t0 := time.Now()
		// ~200M iterations of a recursive CTE: a stand-in for "a query took a long time", the
		// one case no timeout exists for.
		_ = s.QueryRow(`WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM c WHERE i<200000000) SELECT COUNT(*) FROM c`).Scan(&n)
		scanDur = time.Since(t0)
	}()
	time.Sleep(30 * time.Millisecond) // ensure the scan owns the connection first
	go func() {
		defer inner.Done()
		t0 := time.Now()
		profileMust(s.GetTaskByID(scratchTask))
		blockedDur = time.Since(t0)
		scanWaits = s.DBStats().WaitCount
	}()
	inner.Wait()
	t.Logf("\none slow query holding the single connection, with no context on the API to abandon it:\n  slow scan held the connection for %s; a routine GetTaskByID behind it waited %s (pool WaitCount since start: %d)",
		scanDur.Round(time.Millisecond), blockedDur.Round(time.Millisecond), scanWaits)
}

// ---------------------------------------------------------------------- harness

type profileRun struct {
	durs      []time.Duration
	waitCount int64
	waitDur   time.Duration
	max       time.Duration
}

func (r profileRun) pctile(p float64) time.Duration {
	if len(r.durs) == 0 {
		return 0
	}
	i := int(p * float64(len(r.durs)))
	if i >= len(r.durs) {
		i = len(r.durs) - 1
	}
	return r.durs[i]
}

func (r profileRun) opsPerSec() float64 {
	var total time.Duration
	for _, d := range r.durs {
		total += d
	}
	if total == 0 {
		return 0
	}
	return float64(len(r.durs)) / total.Seconds()
}

// runConcurrent issues total ops across conc goroutines and reports the latency distribution
// plus what the database handle itself recorded waiting for a connection.
func runConcurrent(s *Store, conc, total int, op func()) profileRun {
	per := max(total/conc, 1)
	before := s.DBStats()
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		durs []time.Duration
	)
	wg.Add(conc)
	for g := 0; g < conc; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				t0 := time.Now()
				op()
				d := time.Since(t0)
				mu.Lock()
				durs = append(durs, d)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	after := s.DBStats()
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	var worst time.Duration
	for _, d := range durs {
		worst = max(worst, d)
	}
	return profileRun{durs: durs, max: worst,
		waitCount: after.WaitCount - before.WaitCount, waitDur: after.WaitDuration - before.WaitDuration}
}

func dur(d time.Duration) string { return d.Round(time.Microsecond).String() }

func openProfileStore(t *testing.T) (*Store, func()) {
	t.Helper()
	if src := os.Getenv("MIKE_STORE_DB"); src != "" {
		raw, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		dst := t.TempDir() + "/copy.db"
		if err := os.WriteFile(dst, raw, 0o600); err != nil {
			t.Fatalf("copy: %v", err)
		}
		t.Logf("profiling a copy of %s (%.1f MB)", src, float64(len(raw))/1e6)
		s, err := NewStore(dst)
		if err != nil {
			t.Fatalf("open copy: %v", err)
		}
		return s, func() { s.Close() }
	}
	s, err := NewStore(t.TempDir() + "/profile.db")
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	seedProfile(t, s)
	return s, func() { s.Close() }
}

// seedProfile builds a database shaped like a real deployment: a handful of projects, tens of
// tasks each, and one task whose thread carries a full turn-by-turn history — the row set that
// makes a history read expensive rather than a lookup.
func seedProfile(t *testing.T, s *Store) {
	t.Helper()
	body := strings.Repeat("a fairly typical assistant turn of prose and a tool result. ", 24)
	now := time.Now().UTC()
	for p := 0; p < 9; p++ {
		pid := fmt.Sprintf("proj-%d", p)
		profileMustErr(s.CreateProject(&types.Project{ID: pid, Name: pid, Path: "/tmp/" + pid, CreatedAt: now, UpdatedAt: now}))
		for k := 0; k < 60; k++ {
			tid := fmt.Sprintf("%s-task-%d", pid, k)
			thread := tid + "-main"
			profileMustErr(s.CreateTask(&types.Task{
				ID: tid, ProjectID: pid, Title: fmt.Sprintf("task %d", k), Prompt: "seeded prompt",
				Status: types.StatusBacklog, ExecutionState: types.ExecStateIdle,
				CreatedAt: now, UpdatedAt: now, CurrentThreadID: thread,
			}))
			profileMustErr(s.CreateThread(&types.TaskThread{ID: thread, TaskID: tid, Name: "Main", IsActive: true, CreatedAt: now}))
			msgs := 30
			if p == 0 && k == 0 {
				msgs = 1200 // the long tail a real long-running task reaches
			}
			for m := 0; m < msgs; m++ {
				msg := profileMessage(fmt.Sprintf("seed-%s-%d", tid, m), tid, thread)
				msg.Content = body
				msg.ReasoningContent = body[:len(body)/2]
				msg.ToolResult = body[:len(body)/3]
				profileMustErr(s.CreateTaskMessage(msg))
			}
		}
	}
}

func profileMessage(id, taskID, threadID string) *types.TaskMessage {
	now := time.Now().UTC()
	return &types.TaskMessage{
		ID: id, TaskID: taskID, ThreadID: threadID, Role: "assistant", Content: "profile row",
		ModelName: "profile-model", ProviderName: "profile", PromptTokens: 120, PredictedTokens: 60,
		Kind: "chat", CreatedAt: now,
	}
}

func profileMust[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func profileMustSlice[T any](v []T, err error) []T {
	if err != nil {
		panic(err)
	}
	return v
}

func profileMustErr(err error) {
	if err != nil {
		panic(err)
	}
}
