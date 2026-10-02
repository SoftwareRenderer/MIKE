package store

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A task message's created_at is compared as TEXT by the queries that list a chat and cut it at a
// message, so the stored form has to sort the way the instants sort. These tests hold that rule for
// the text this build writes.

// nearbyTimestamps are pairs one second apart in instants whose RFC3339Nano text is the wrong way
// round: the earlier one loses its trailing zeros and ends in 'Z' (0x5A), which sorts above every
// digit, so it came back after the later one whose fraction is longer.
var nearbyTimestamps = [][2]int{
	{389212100, 389212160},
	{500000000, 500000001},
	{100000000, 100000001},
}

func TestGetTaskMessagesOrdersNearbyTimestamps(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-t", "proj-t", "/tmp/proj-t")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-t", "proj-t")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	var want []string
	second := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	for i, pair := range nearbyTimestamps {
		for _, ns := range pair {
			id := fmt.Sprintf("msg-%d-%09d", i, ns)
			m := newMessage(id, "task-t", "user", id)
			m.CreatedAt = second.Add(time.Duration(ns) * time.Nanosecond)
			if err := s.CreateTaskMessage(m); err != nil {
				t.Fatalf("create message: %v", err)
			}
			want = append(want, id)
		}
		second = second.Add(time.Second)
	}

	got, err := s.GetTaskMessages("task-t", "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	var order []string
	for _, m := range got {
		order = append(order, m.ID)
	}
	if strings.Join(order, " ") != strings.Join(want, " ") {
		t.Errorf("messages read back as %v, want them in the order they were written (%v)", order, want)
	}
}

// The stored text and the instant have to agree for every row this build writes, because that text is
// what the ordering, the cutoff in DeleteTaskMessagesAfter and the walk back to a checkpoint compare.
func TestCreateTaskMessageStoresSortableTimestamps(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-k", "proj-k", "/tmp/proj-k")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-k", "proj-k")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	at := time.Date(2026, 3, 4, 5, 6, 7, 389212100, time.UTC)
	m := newMessage("msg-k", "task-k", "user", "hello")
	m.CreatedAt = at
	if err := s.CreateTaskMessage(m); err != nil {
		t.Fatalf("create message: %v", err)
	}

	var stored string
	if err := s.db.QueryRow(`SELECT created_at FROM task_messages WHERE id = 'msg-k'`).Scan(&stored); err != nil {
		t.Fatalf("read stored text: %v", err)
	}
	if want := "2026-03-04T05:06:07.389212100Z"; stored != want {
		t.Errorf("stored created_at = %q, want %q — a trimmed fraction sorts against a padded one", stored, want)
	}
	parsed, err := s.GetMessageCreatedAt("msg-k")
	if err != nil {
		t.Fatalf("read back the timestamp: %v", err)
	}
	if !parsed.Equal(at) {
		t.Errorf("created_at read back as %v, want the instant written (%v)", parsed, at)
	}
}
