package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mike/pkg/logging"
)

// A task message's created_at is compared as TEXT by the queries that list a chat and cut it at a
// message, so the stored form has to sort the way the instants sort. These tests hold that rule for
// what this build writes and for the rows a database already had.

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

// A database written before the fixed-width form holds both orders at once, so upgrading rewrites the
// rows it has. The instants survive; a row whose text cannot be read is left as stored and named.
func TestMessageTimeMigrationRewritesLegacyTimestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kanban.db")
	legacy := []struct {
		id   string
		text string
	}{
		{"trimmed", "2026-03-04T05:06:07.3892121Z"},
		{"longer", "2026-03-04T05:06:07.38921216Z"},
		{"offset", "2026-03-04T07:06:07.5+02:00"},
		{"whole-second", "2026-03-04T05:06:08Z"},
		{"unreadable", "yesterday"},
	}

	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := s.CreateProject(newProject("proj-m", "proj-m", "/tmp/proj-m")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-m", "proj-m")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	for _, row := range legacy {
		m := newMessage(row.id, "task-m", "user", row.id)
		m.CreatedAt = time.Unix(0, 0).UTC()
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message %s: %v", row.id, err)
		}
		if _, err := s.Exec(`UPDATE task_messages SET created_at = ? WHERE id = ?`, row.text, row.id); err != nil {
			t.Fatalf("store legacy text: %v", err)
		}
	}
	if v := s.userVersion(); v != schemaVersion {
		t.Fatalf("fixture store should be at the current stamp, found %d", v)
	}
	// An upgrade, not a fresh database: the stamp this rewrite lands under is one version older.
	if _, err := s.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, doneMessageTimeKeysStamp-1)); err != nil {
		t.Fatalf("stamp older version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	logs, restore := logging.TestSink()
	defer restore()
	upgraded, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen after the upgrade: %v", err)
	}
	defer func() { _ = upgraded.Close() }()

	stored := map[string]string{}
	rows, err := upgraded.Query(`SELECT id, created_at FROM task_messages`)
	if err != nil {
		t.Fatalf("read stored text: %v", err)
	}
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			t.Fatalf("scan stored text: %v", err)
		}
		stored[id] = text
	}
	rows.Close()

	for _, row := range legacy {
		got, ok := stored[row.id]
		if !ok {
			t.Fatalf("message %s disappeared", row.id)
		}
		if row.id == "unreadable" {
			if got != row.text {
				t.Errorf("unreadable row rewritten to %q; a timestamp this build cannot read is left as stored", got)
			}
			continue
		}
		want, err := time.Parse(time.RFC3339Nano, row.text)
		if err != nil {
			t.Fatalf("the fixture's own legacy text %q does not parse: %v", row.text, err)
		}
		back, err := time.Parse(messageTimeLayout, got)
		if err != nil {
			t.Errorf("%s: stored text %q is not in the fixed-width layout: %v", row.id, got, err)
			continue
		}
		if !back.Equal(want) {
			t.Errorf("%s: %q is the instant %v, want %v", row.id, got, back, want)
		}
	}
	if !strings.Contains(logs.String(), "unreadable") {
		t.Error("the row left unread by the migration was not reported")
	}

	// Both orders lived in this table before the rewrite; after it, the text order is the time order.
	msgs, err := upgraded.GetTaskMessages("task-m", "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	var order []string
	for _, m := range msgs {
		if m.ID != "unreadable" {
			order = append(order, m.ID)
		}
	}
	if want := "trimmed longer offset whole-second"; strings.Join(order, " ") != want {
		t.Errorf("messages read back as %q, want %q", strings.Join(order, " "), want)
	}
}

// A fork's checkpoint is found by matching a thread's stored checkpoint_message_time against a message's
// created_at rendered as text, so rewriting one side without the other stops an old fork from matching.
func TestMessageTimeMigrationKeepsCheckpointLookupMatching(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kanban.db")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := s.CreateProject(newProject("proj-c", "proj-c", "/tmp/proj-c")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-c", "proj-c")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	at := time.Date(2026, 3, 4, 5, 6, 7, 389212100, time.UTC)
	m := newMessage("msg-c", "task-c", "user", "fork here")
	m.CreatedAt = at
	if err := s.CreateTaskMessage(m); err != nil {
		t.Fatalf("create message: %v", err)
	}
	checkpoint := at.Truncate(time.Second)
	thread := newThread("thread-c", "task-c", "Fork #1")
	thread.CheckpointMessageID = m.ID
	thread.CheckpointMessageTime = &checkpoint
	if err := s.CreateThread(thread); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	// The same instant as an older build stored it: the machine's own zone.
	legacy := checkpoint.In(time.FixedZone("CEST", 2*60*60)).Format(time.RFC3339)
	if _, err := s.Exec(`UPDATE task_threads SET checkpoint_message_time = ? WHERE id = 'thread-c'`, legacy); err != nil {
		t.Fatalf("store legacy checkpoint time: %v", err)
	}
	if _, err := s.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, doneMessageTimeKeysStamp-1)); err != nil {
		t.Fatalf("stamp older version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	upgraded, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen after the upgrade: %v", err)
	}
	defer func() { _ = upgraded.Close() }()

	// What handler_message.go asks with: the message's time, rendered as text by its caller.
	msgTime, err := upgraded.GetMessageCreatedAt("msg-c")
	if err != nil {
		t.Fatalf("read the message time: %v", err)
	}
	threads, err := upgraded.GetThreadsByCheckpointMessageTime(msgTime.Format(time.RFC3339))
	if err != nil {
		t.Fatalf("look up the fork: %v", err)
	}
	if len(threads) != 1 || threads[0].ID != "thread-c" {
		t.Errorf("checkpoint lookup found %v, want thread-c — the two sides are text of different shapes", threads)
	}
}

// A database at this stamp is not rewritten again on the next start: the pass belongs to the upgrade.
func TestMessageTimeMigrationLeavesCurrentRowsAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kanban.db")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := s.CreateProject(newProject("proj-s", "proj-s", "/tmp/proj-s")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-s", "proj-s")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	m := newMessage("msg-s", "task-s", "user", "hello")
	m.CreatedAt = time.Date(2026, 3, 4, 5, 6, 7, 500000000, time.UTC)
	if err := s.CreateTaskMessage(m); err != nil {
		t.Fatalf("create message: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`UPDATE task_messages SET created_at = 'rewritten by the next start' WHERE id = 'msg-s'`); err != nil {
		t.Fatalf("mark the row: %v", err)
	}
	reopened, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	var got string
	if err := reopened.QueryRow(`SELECT created_at FROM task_messages WHERE id = 'msg-s'`).Scan(&got); err != nil {
		t.Fatalf("read stored text: %v", err)
	}
	if got != "rewritten by the next start" {
		t.Errorf("a database already at this stamp was rewritten (%q); the pass belongs to the upgrade", got)
	}
}
