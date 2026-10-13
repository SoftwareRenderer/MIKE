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
		got, err := s.GetTaskMessagePage("task-1", "thread-1", 6, cursor, "")
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

// Every window opens on an answer, never on a tool_call row whose assistant row it does not hold.
func TestGetTaskMessagePageNeverOpensOnAToolCall(t *testing.T) {
	s := testStore(t)
	seedThread(t, s, "task-1", "thread-1", 21)

	for limit := 1; limit <= 12; limit++ {
		got, err := s.GetTaskMessagePage("task-1", "thread-1", limit, "", "")
		if err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if len(got.Messages) == 0 {
			t.Fatalf("limit %d returned no rows", limit)
		}
		if oldest := got.Messages[0]; !startsUnit(oldest.Role) {
			t.Fatalf("limit %d opened on a %s row (%s)", limit, oldest.Role, oldest.ID)
		}
	}

	// Paging to the end keeps the rule.
	cursor := ""
	for page := 0; page < 12; page++ {
		got, err := s.GetTaskMessagePage("task-1", "thread-1", 4, cursor, "")
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(got.Messages) > 0 && !startsUnit(got.Messages[0].Role) {
			t.Fatalf("page %d opened on a %s row", page, got.Messages[0].Role)
		}
		if !got.HasMore {
			break
		}
		cursor = got.Before
	}
}

// An answer holding more rows than the read's ceiling cannot be cut at an answer boundary. What this pins
// is that every row is still handed over exactly once, with the answer's opening row on the next page.
func TestGetTaskMessagePageCutsInsideAnAnswerLongerThanTheCeiling(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-1", "test", "/tmp/test")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	// Every row shares the assistant's instant, so rowid is what orders them.
	at := time.Now().UTC().Add(-time.Hour)
	head := newMessage("head-1", "task-1", "assistant", "one answer, too many calls")
	head.ThreadID, head.CreatedAt = "thread-1", at
	if err := s.CreateTaskMessage(head); err != nil {
		t.Fatalf("create head: %v", err)
	}
	total := 1
	for i := 0; i <= MessageRowCeiling; i++ {
		call := newMessage(fmt.Sprintf("call-%04d", i), "task-1", "tool_call", "x")
		call.ThreadID, call.CreatedAt, call.AssistantMsgID = "thread-1", at, head.ID
		if err := s.CreateTaskMessage(call); err != nil {
			t.Fatalf("create call: %v", err)
		}
		total++
	}

	first, err := s.GetTaskMessagePage("task-1", "thread-1", 1, "", "")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Messages) != MessageRowCeiling {
		t.Fatalf("first page holds %d rows, want the ceiling of %d", len(first.Messages), MessageRowCeiling)
	}
	if startsUnit(first.Messages[0].Role) {
		t.Fatalf("a cut inside one answer was expected to open on a tool_call row, got %s", first.Messages[0].Role)
	}
	if !first.HasMore || first.Before == "" {
		t.Fatalf("the rows left behind must be one cursor away: more=%v cursor=%q", first.HasMore, first.Before)
	}

	second, err := s.GetTaskMessagePage("task-1", "thread-1", 1, first.Before, "")
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	seen := map[string]bool{}
	for _, m := range append(append([]types.TaskMessage{}, first.Messages...), second.Messages...) {
		if seen[m.ID] {
			t.Fatalf("row %s arrived on two pages", m.ID)
		}
		seen[m.ID] = true
	}
	if !seen[head.ID] {
		t.Fatalf("the row that opened the answer never arrived: %d of %d rows seen", len(seen), total)
	}
	if len(seen) != total {
		t.Fatalf("the two pages cover %d of the thread's %d rows", len(seen), total)
	}
}

// A page is counted in answers: the newest five assistant rows and their tool calls, however many rows
// that is, and the answers older than the fifth are one cursor away.
func TestGetTaskMessagePageCountsAnswersNotRows(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-1", "test", "/tmp/test")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	// One turn: a user row, then 40 answers, each carrying two tool calls.
	turn := []*types.TaskMessage{newMessage("turn-head", "task-1", "user", "the turn's prompt")}
	for i := 0; i < 40; i++ {
		a := newMessage(fmt.Sprintf("a-%02d", i), "task-1", "assistant", fmt.Sprintf("answer %02d", i))
		turn = append(turn, a)
		for j := 0; j < 2; j++ {
			c := newMessage(fmt.Sprintf("c-%02d-%d", i, j), "task-1", "tool_call", "call")
			c.AssistantMsgID = a.ID
			turn = append(turn, c)
		}
	}
	for i, m := range turn {
		m.ThreadID = "thread-1"
		m.CreatedAt = base.Add(time.Duration(i) * time.Millisecond)
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	got, err := s.GetTaskMessagePage("task-1", "thread-1", 5, "", "")
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	var answers, calls int
	for _, m := range got.Messages {
		switch m.Role {
		case "assistant":
			answers++
		case "tool_call":
			calls++
		}
	}
	if answers != 5 || calls != 10 {
		t.Errorf("window holds %d answers and %d tool calls, want 5 and 10", answers, calls)
	}
	if !got.HasMore {
		t.Error("a 40-answer turn answered with 5 answers but says nothing older exists")
	}
	if got.Messages[0].ID != "a-35" {
		t.Errorf("window opens on %s, want the oldest answer the page holds", got.Messages[0].ID)
	}

	// Paging walks back to the turn's user row without repeating or skipping a row.
	seen := map[string]bool{}
	for m := range got.Messages {
		seen[got.Messages[m].ID] = true
	}
	cursor := got.Before
	for page := 0; cursor != ""; page++ {
		if page > 40 {
			t.Fatal("paging did not terminate")
		}
		older, err := s.GetTaskMessagePage("task-1", "thread-1", 5, cursor, "")
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		for _, m := range older.Messages {
			if seen[m.ID] {
				t.Fatalf("row %s arrived on two pages", m.ID)
			}
			seen[m.ID] = true
		}
		cursor = older.Before
		if !older.HasMore && cursor != "" {
			t.Fatal("a page named a cursor it did not need")
		}
	}
	if len(seen) != len(turn) {
		t.Fatalf("the pages cover %d of the turn's %d rows", len(seen), len(turn))
	}
	if !seen["turn-head"] {
		t.Error("paging never reached the turn's user row")
	}
}

func TestGetTaskMessagePageRefusesACursorItDidNotIssue(t *testing.T) {
	s := testStore(t)
	seedThread(t, s, "task-1", "thread-1", 3)

	for _, bad := range []string{"not-a-cursor", "AAAA", "e30", "aaaaaaaaAAAAAAAAAAAAAAAAAAAAAAAAAAA-"} {
		if _, err := s.GetTaskMessagePage("task-1", "thread-1", 2, bad, ""); !errors.Is(err, ErrMessageCursor) {
			t.Errorf("cursor %q: got %v, want ErrMessageCursor", bad, err)
		}
	}
}

// Rows sharing one instant still hand over exactly once, and a window of two over a four-row turn still
// opens on a row that starts an answer.
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
		got, err := s.GetTaskMessagePage("task-1", "thread-1", 2, cursor, "")
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

	threadWhere, threadArgs := conversationWhere("task-1", "thread-1", nil, nil)
	taskWhere, taskArgs := conversationWhere("task-1", "", nil, nil)
	floor := messageKey{at: "2024-06-15T12:00:00.000000000Z", rowid: 3}
	windowedWhere, windowedArgs := conversationWhere("task-1", "thread-1", nil, &floor)
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
		{"newest summary of a thread", newestSummarizationSQL, []any{"thread-1"}},
		{"read from a summary onward", conversationRowsSQL + windowedWhere + ` ` + messageOrder, windowedArgs},
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

// cursorOfRow encodes one row's position, the way the prompt read hands one out.
func cursorOfRow(t *testing.T, s *Store, taskID, threadID, id string) string {
	t.Helper()
	keys, err := s.messageKeys(taskID, threadID, nil, nil, 100000)
	if err != nil {
		t.Fatalf("read positions: %v", err)
	}
	for _, k := range keys {
		if k.id == id {
			return encodeMessageCursor(k)
		}
	}
	t.Fatalf("no position read named row %s", id)
	return ""
}

func TestGetTaskMessagePageStopsAtTheFloorItWasGiven(t *testing.T) {
	s := testStore(t)
	ids := seedThread(t, s, "task-1", "thread-1", 20)

	first, err := s.GetTaskMessagePage("task-1", "thread-1", 4, "", "")
	if err != nil {
		t.Fatalf("newest window: %v", err)
	}
	// A floor inside what this page answers.
	floor := cursorOfRow(t, s, "task-1", "thread-1", ids[len(ids)-8])
	page, err := s.GetTaskMessagePage("task-1", "thread-1", 25, first.Before, floor)
	if err != nil {
		t.Fatalf("floored page: %v", err)
	}
	if len(page.Messages) == 0 {
		t.Fatal("a floor inside the span answered with nothing")
	}
	oldest := page.Messages[0]
	if oldest.ID != ids[len(ids)-8] {
		t.Fatalf("a reachable floor was not answered: oldest row = %s, want %s", oldest.ID, ids[len(ids)-8])
	}
	// The floor bounds this page, not the conversation: rows below it stay one cursor away.
	if !page.HasMore || page.Before == "" {
		t.Errorf("rows below the floor must stay readable: more=%v cursor=%q", page.HasMore, page.Before)
	}
}

func TestGetTaskMessagePageIgnoresAFloorItNeverReaches(t *testing.T) {
	s := testStore(t)
	seedThread(t, s, "task-1", "thread-1", 20)

	unfloored, err := s.GetTaskMessagePage("task-1", "thread-1", 2, "", "")
	if err != nil {
		t.Fatalf("unfloored page: %v", err)
	}
	floor := cursorOfRow(t, s, "task-1", "thread-1", "msg-000")
	floored, err := s.GetTaskMessagePage("task-1", "thread-1", 2, "", floor)
	if err != nil {
		t.Fatalf("floored page: %v", err)
	}
	if len(floored.Messages) != len(unfloored.Messages) {
		t.Fatalf("a floor above the span changed the page: %d rows, want %d", len(floored.Messages), len(unfloored.Messages))
	}
	for i := range floored.Messages {
		if floored.Messages[i].ID != unfloored.Messages[i].ID {
			t.Fatalf("row %d differs: %s, want %s", i, floored.Messages[i].ID, unfloored.Messages[i].ID)
		}
	}
}

func TestGetTaskMessagePagesToAFloorHandOverEveryRowOnce(t *testing.T) {
	s := testStore(t)
	ids := seedThread(t, s, "task-1", "thread-1", 20)
	floor := cursorOfRow(t, s, "task-1", "thread-1", "msg-005")

	seen := map[string]bool{}
	cursor := ""
	for page := 0; ; page++ {
		if page > len(ids) {
			t.Fatalf("floored pages did not terminate after %d reads", page)
		}
		got, err := s.GetTaskMessagePage("task-1", "thread-1", 2, cursor, floor)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		for _, m := range got.Messages {
			if seen[m.ID] {
				t.Fatalf("row %s arrived on two pages", m.ID)
			}
			seen[m.ID] = true
		}
		if !got.HasMore || len(got.Messages) == 0 {
			break
		}
		if got.Messages[0].ID == "msg-005" {
			break
		}
		cursor = got.Before
	}
	if !seen["msg-005"] {
		t.Error("the walk stopped above the floor's row")
	}
	if seen["msg-004"] {
		t.Error("the walk answered below the floor")
	}
	for _, id := range ids[5:] {
		if !seen[id] {
			t.Fatalf("the walk skipped %s", id)
		}
	}
}

// A span past the row ceiling is cut, and the next page picks up with the floor still set.
func TestGetTaskMessagePagesToAFloorAcrossTheRowCeiling(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-1", "test", "/tmp/test")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	at := time.Now().UTC().Add(-time.Hour)
	head := newMessage("head-1", "task-1", "assistant", "one answer, too many calls")
	head.ThreadID, head.CreatedAt = "thread-1", at
	if err := s.CreateTaskMessage(head); err != nil {
		t.Fatalf("create head: %v", err)
	}
	for i := 0; i <= MessageRowCeiling; i++ {
		call := newMessage(fmt.Sprintf("call-%04d", i), "task-1", "tool_call", "x")
		call.ThreadID, call.CreatedAt, call.AssistantMsgID = "thread-1", at, head.ID
		if err := s.CreateTaskMessage(call); err != nil {
			t.Fatalf("create call: %v", err)
		}
	}
	floor := cursorOfRow(t, s, "task-1", "thread-1", head.ID)

	first, err := s.GetTaskMessagePage("task-1", "thread-1", 1, "", floor)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Messages) != MessageRowCeiling {
		t.Fatalf("first page holds %d rows, want the ceiling of %d", len(first.Messages), MessageRowCeiling)
	}
	second, err := s.GetTaskMessagePage("task-1", "thread-1", 1, first.Before, floor)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	seen := map[string]bool{}
	for _, m := range append(append([]types.TaskMessage{}, first.Messages...), second.Messages...) {
		if seen[m.ID] {
			t.Fatalf("row %s arrived on two pages", m.ID)
		}
		seen[m.ID] = true
	}
	if !seen[head.ID] {
		t.Error("the floor's row never arrived across the ceiling")
	}
}

func TestGetTaskMessagePageRefusesAFloorItDidNotIssue(t *testing.T) {
	s := testStore(t)
	seedThread(t, s, "task-1", "thread-1", 3)

	for _, bad := range []string{"not-a-cursor", "e30"} {
		if _, err := s.GetTaskMessagePage("task-1", "thread-1", 2, "", bad); !errors.Is(err, ErrMessageFloor) {
			t.Errorf("floor %q: got %v, want ErrMessageFloor", bad, err)
		}
	}
}
