package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"mike/backend/types"
)

// Pages must cover the thread exactly once, in the order it was written, and the (scope, created_at)
// indexes must answer the read rather than a temp B-tree.

// seedThread writes `count` rows in turns of three: a user row, an assistant row, and a tool_call sharing
// the assistant's created_at — the tie rowid breaks.
func seedThread(t *testing.T, s *Store, taskID, threadID string, count int) []string {
	t.Helper()
	if err := s.CreateProject(newProject("proj-1", "test", "/tmp/test")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask(taskID, "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		role := []string{"user", "assistant", "tool_call"}[i%3]
		m := newMessage(fmt.Sprintf("msg-%03d", i), taskID, role, strings.Repeat("x", 40))
		m.ThreadID = threadID
		m.CreatedAt = base.Add(time.Duration(i/3) * time.Second)
		if role == "tool_call" {
			m.AssistantMsgID = fmt.Sprintf("msg-%03d", i-1)
		}
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
		ids = append(ids, m.ID)
	}
	return ids
}

func TestGetTaskMessagePageWalksTheThreadOnce(t *testing.T) {
	s := testStore(t)
	ids := seedThread(t, s, "task-1", "thread-1", 20)

	var pages [][]string
	cursor := ""
	for page := 0; ; page++ {
		if page > len(ids) {
			t.Fatalf("pages did not terminate after %d reads", page)
		}
		got, err := s.GetTaskMessagePage("task-1", "thread-1", 6, cursor)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		var walked []string
		for _, m := range got.Messages {
			walked = append(walked, m.ID)
		}
		pages = append(pages, walked)
		if !got.HasMore {
			break
		}
		if got.Before == "" {
			t.Fatalf("page %d says more rows exist but names no cursor", page)
		}
		cursor = got.Before
	}

	// Pages arrive newest-first and are chronological within themselves, so back-to-front is write order.
	var walked []string
	for p := len(pages) - 1; p >= 0; p-- {
		walked = append(walked, pages[p]...)
	}
	if len(walked) != len(ids) {
		t.Fatalf("walked %d rows, thread holds %d", len(walked), len(ids))
	}
	for i, id := range walked {
		if want := ids[i]; id != want {
			t.Fatalf("row %d of the walk = %s, want %s", i, id, want)
		}
	}
}

// Every window opens on a turn's head, never on its work.
func TestGetTaskMessagePageNeverOpensInTheMiddleOfATurn(t *testing.T) {
	s := testStore(t)
	seedThread(t, s, "task-1", "thread-1", 21)

	for limit := 1; limit <= 12; limit++ {
		got, err := s.GetTaskMessagePage("task-1", "thread-1", limit, "")
		if err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if len(got.Messages) == 0 {
			t.Fatalf("limit %d returned no rows", limit)
		}
		if oldest := got.Messages[0]; continuesTurn(oldest.Role) {
			t.Fatalf("limit %d opened on a %s row (%s)", limit, oldest.Role, oldest.ID)
		}
	}

	// Paging to the end keeps the rule.
	cursor := ""
	for page := 0; page < 12; page++ {
		got, err := s.GetTaskMessagePage("task-1", "thread-1", 4, cursor)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(got.Messages) > 0 && continuesTurn(got.Messages[0].Role) {
			t.Fatalf("page %d opened on a %s row", page, got.Messages[0].Role)
		}
		if !got.HasMore {
			break
		}
		cursor = got.Before
	}
}

func TestGetTaskMessagePageRefusesACursorItDidNotIssue(t *testing.T) {
	s := testStore(t)
	seedThread(t, s, "task-1", "thread-1", 3)

	for _, bad := range []string{"not-a-cursor", "AAAA", "e30", "aaaaaaaaAAAAAAAAAAAAAAAAAAAAAAAAAAA-"} {
		if _, err := s.GetTaskMessagePage("task-1", "thread-1", 2, bad); !errors.Is(err, ErrMessageCursor) {
			t.Errorf("cursor %q: got %v, want ErrMessageCursor", bad, err)
		}
	}
}

// Rows sharing one instant still hand over exactly once, and a window of two over a four-row turn reaches
// back to the turn's head.
func TestGetTaskMessagePageCutsATurnSharedAcrossOneInstant(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-1", "test", "/tmp/test")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	stamp := time.Now().UTC()
	for i, role := range []string{"user", "assistant", "tool_call", "tool_call"} {
		m := newMessage(fmt.Sprintf("msg-%d", i), "task-1", role, "same instant")
		m.ThreadID = "thread-1"
		m.CreatedAt = stamp
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	seen := map[string]bool{}
	cursor := ""
	var first []types.TaskMessage
	for page := 0; page < 8; page++ {
		got, err := s.GetTaskMessagePage("task-1", "thread-1", 2, cursor)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if page == 0 {
			first = got.Messages
		}
		for _, m := range got.Messages {
			if seen[m.ID] {
				t.Fatalf("row %s arrived on two pages", m.ID)
			}
			seen[m.ID] = true
		}
		if !got.HasMore {
			break
		}
		cursor = got.Before
	}
	if len(seen) != 4 {
		t.Fatalf("the pages cover %d of the thread's 4 rows", len(seen))
	}
	if len(first) == 0 || first[0].Role != "user" {
		t.Fatalf("window = %+v, want it to start at the turn's user row", first)
	}
}

// The plan is the claim: an index carries the order, so SQLite never sorts the thread to answer these.
func TestConversationReadsDoNotSortInATempBTree(t *testing.T) {
	s := testStore(t)
	seedThread(t, s, "task-1", "thread-1", 30)

	threadWhere, threadArgs := conversationWhere("task-1", "thread-1", nil)
	taskWhere, taskArgs := conversationWhere("task-1", "", nil)
	window := func(where string) string {
		return messageKeyCols + where + ` ` + newestFirstOrder + ` LIMIT 10`
	}
	queries := []struct {
		name string
		sql  string
		args []any
	}{
		{"window of a thread", window(threadWhere), threadArgs},
		{"window of a task", window(taskWhere), taskArgs},
		{"full thread read", threadMessagesSQL, []any{"thread-1"}},
		{"thread of one task", taskThreadMessagesSQL, []any{"task-1", "thread-1"}},
		{"whole task read", taskMessagesAllSQL, []any{"task-1"}},
		{"newest row of a thread", lastMessageInThreadSQL, []any{"thread-1"}},
		{"newest summary of a thread", lastSummarizationSQL, []any{"thread-1"}},
	}
	for _, q := range queries {
		plan := explainPlan(t, s, q.sql, q.args...)
		if strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
			t.Errorf("%s sorts in a temp B-tree:\n%s", q.name, plan)
		}
		if !strings.Contains(plan, "idx_task_messages_thread_time") && !strings.Contains(plan, "idx_task_messages_task_time") {
			t.Errorf("%s does not read a (scope, created_at) index:\n%s", q.name, plan)
		}
	}
}

func explainPlan(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatalf("explain %q: %v", query, err)
	}
	defer rows.Close()
	var sb strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		sb.WriteString(detail + "\n")
	}
	return sb.String()
}

// The composite indexes replace the single-column ones they cover.
func TestConversationIndexesReplaceTheSingleColumnOnes(t *testing.T) {
	s := testStore(t)
	for _, name := range []string{"idx_task_messages_thread_time", "idx_task_messages_task_time"} {
		if !hasIndex(t, s, name) {
			t.Errorf("index %s is missing", name)
		}
	}
	for _, name := range []string{"idx_task_messages_thread", "idx_task_messages_task"} {
		if hasIndex(t, s, name) {
			t.Errorf("index %s is still there; the composite index covers its queries", name)
		}
	}
}

func hasIndex(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&count); err != nil {
		t.Fatalf("look up index %s: %v", name, err)
	}
	return count > 0
}
