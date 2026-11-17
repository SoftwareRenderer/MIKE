package store

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A run's conversation starts at its newest summarization row: everything older was folded into
// it, so reading it again every turn costs the whole history to send the model the same window.

// summarySeedBase is the instant these seeds count their rows from, so a summary's place in the
// conversation order is fixed rather than read off the clock.
var summarySeedBase = time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)

func seedMessages(t *testing.T, s *Store, taskID, threadID string, from, count int) []string {
	t.Helper()
	base := summarySeedBase
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		n := from + i
		role := []string{"user", "assistant", "tool_call"}[n%3]
		m := newMessage(fmt.Sprintf("msg-%03d", n), taskID, role, strings.Repeat("x", 32))
		m.ThreadID = threadID
		m.CreatedAt = base.Add(time.Duration(n) * time.Second)
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
		ids = append(ids, m.ID)
	}
	return ids
}

func seedSummary(t *testing.T, s *Store, taskID, threadID, id string, at time.Time) {
	t.Helper()
	m := newMessage(id, taskID, "summarization", "summary of the turns before "+id)
	m.ThreadID = threadID
	m.CreatedAt = at
	if err := s.CreateTaskMessage(m); err != nil {
		t.Fatalf("create summary: %v", err)
	}
}

func threadAndProject(t *testing.T, s *Store, taskID string) {
	t.Helper()
	if err := s.CreateProject(newProject("proj-1", "test", "/tmp/test")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask(taskID, "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}
}

func TestGetTaskMessagesFromSummaryStartsAtTheNewestSummary(t *testing.T) {
	s := testStore(t)
	threadAndProject(t, s, "task-1")
	seedMessages(t, s, "task-1", "thr-1", 0, 6) // 6 turns before the first summary
	seedSummary(t, s, "task-1", "thr-1", "sum-1", summarySeedBase.Add(6*time.Second))
	seedMessages(t, s, "task-1", "thr-1", 6, 3)
	seedSummary(t, s, "task-1", "thr-1", "sum-2", summarySeedBase.Add(9*time.Second))
	last := seedMessages(t, s, "task-1", "thr-1", 9, 2)

	got, err := s.GetTaskMessagesFromSummary("task-1", "thr-1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1+len(last) {
		t.Fatalf("%d rows, want the newest summary and the %d rows after it", len(got), len(last))
	}
	if got[0].ID != "sum-2" || got[0].Role != "summarization" {
		t.Errorf("window opens at %s (%s), want the newest summary sum-2", got[0].ID, got[0].Role)
	}
	for i, id := range last {
		if got[i+1].ID != id {
			t.Errorf("row %d is %s, want %s", i+1, got[i+1].ID, id)
		}
	}
	if whole, err := s.GetTaskMessages("task-1", "thr-1"); err != nil || len(whole) != 13 {
		t.Errorf("the whole-thread read changed: %d rows, err %v", len(whole), err)
	}
}

// The cut is scoped to the reader's own scope, and with nothing of its own to cut at the read is that
// whole scope in the order it was written: a fork inherits the summarization rows and switches to a
// thread of its own, which has said nothing yet, and a legacy conversation whose rows carry no thread is
// read by task.
func TestGetTaskMessagesFromSummaryCutsOnlyItsOwnScope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		thread string
		// seed writes the rows and names the ones this scope must answer with.
		seed func(t *testing.T, s *Store) []string
	}{
		{
			name:   "another thread's summary bounds nothing",
			thread: "thr-1",
			seed: func(t *testing.T, s *Store) []string {
				ids := seedMessages(t, s, "task-1", "thr-1", 0, 9)
				seedSummary(t, s, "task-1", "thr-2", "sum-other", summarySeedBase.Add(3*time.Second))
				return ids
			},
		},
		{
			name:   "rows with no thread are read by task",
			thread: "",
			seed: func(t *testing.T, s *Store) []string {
				return seedMessages(t, s, "task-1", "", 0, 4)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			threadAndProject(t, s, "task-1")
			want := tc.seed(t, s)

			got, err := s.GetTaskMessagesFromSummary("task-1", tc.thread)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if len(got) != len(want) {
				t.Fatalf("%d rows, want the scope's %d", len(got), len(want))
			}
			for i, id := range want {
				if got[i].ID != id {
					t.Fatalf("row %d is %s, want %s — the scope read back out of order", i, got[i].ID, id)
				}
			}
		})
	}
}
