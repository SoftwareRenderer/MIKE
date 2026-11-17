package api

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mike/backend/store"
	"mike/backend/types"
)

// The digest a returning window asks for: every field a panel of the task view already holds a copy of.

// seedSyncProject wires one project and task, and returns their ids.
func seedSyncProject(t *testing.T, h *TaskHandler, s *store.Store) (string, string) {
	t.Helper()
	const projID, taskID = "sync-proj", "sync-task"
	if err := s.CreateProject(&types.Project{ID: projID, Name: projID, Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(&types.Task{
		ID: taskID, ProjectID: projID, Title: "sync", Status: types.StatusInProgress,
		Branch: "mike/sync", ExecutionState: types.ExecStateRunning, CurrentThreadID: "thread-1",
	}); err != nil {
		t.Fatal(err)
	}
	return projID, taskID
}

func seedSyncRows(t *testing.T, s *store.Store, taskID string, count int) {
	t.Helper()
	base := time.Now().UTC()
	for i := 0; i < count; i++ {
		if err := s.CreateTaskMessage(&types.TaskMessage{
			ID: taskID + "-row-" + string(rune('a'+i)), TaskID: taskID, ThreadID: "thread-1",
			Role: "user", Content: "row", CreatedAt: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func readSyncDigest(t *testing.T, h *TaskHandler, path string) taskSyncResponse {
	t.Helper()
	rr := getSync(t, syncMux(h), path)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rr.Code, rr.Body.String())
	}
	var got taskSyncResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode digest: %v", err)
	}
	return got
}

func getSync(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func syncMux(h *TaskHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/sync", h.HandleSync)
	return mux
}

func TestSyncDigestTracksTheRowAndTheRowsUnderIt(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	projID, taskID := seedSyncProject(t, h, s)
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/sync"

	before := readSyncDigest(t, h, path)
	if before.Status != types.StatusInProgress || before.ExecutionState != types.ExecStateRunning {
		t.Errorf("digest carries %s/%s, want the task row's in_progress/running", before.Status, before.ExecutionState)
	}
	if before.Thread != "thread-1" {
		t.Errorf("thread = %q, want the conversation the task is on", before.Thread)
	}
	if before.Conversation.Messages != 0 {
		t.Errorf("conversation counts %d rows, want 0", before.Conversation.Messages)
	}
	if before.Conversation.Revision != 0 {
		t.Errorf("a task no executor has written a row for is at revision %d, want 0", before.Conversation.Revision)
	}
	if before.Conversation.Epoch == "" {
		t.Error("no epoch: a client cannot tell a fresh server from an idle one")
	}
	// Nothing has been computed for this task's worktree, which is an unknown and never a "clean".
	if before.Diff.Known {
		t.Error("diff claims to be known with no watcher having computed one")
	}

	seedSyncRows(t, s, taskID, 3)
	after := readSyncDigest(t, h, path)
	if after.Conversation.Messages != before.Conversation.Messages+3 {
		t.Errorf("rows went from %d to %d across three writes", before.Conversation.Messages, after.Conversation.Messages)
	}
	if after.Conversation.Epoch != before.Conversation.Epoch {
		t.Errorf("epoch moved within one process (%q then %q): a client would re-read on every answer", before.Conversation.Epoch, after.Conversation.Epoch)
	}
	// The rows were written behind the stream's back, so only the count moved — a revision that
	// drifted here would make every returning window re-read a conversation it already holds.
	if after.Conversation.Revision != 0 {
		t.Errorf("revision moved to %d for rows no durable write produced", after.Conversation.Revision)
	}
}

// TestSyncDigestRefusesARequestThatNamesNoTaskOfItsOwn covers the two ways the path can fail to name
// this project's task: an id git could read as an option, and a task that belongs to somebody else.
func TestSyncDigestRefusesARequestThatNamesNoTaskOfItsOwn(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	projID, taskID := seedSyncProject(t, h, s)
	mux := syncMux(h)

	for _, tt := range []struct {
		name string
		path string
		want int
	}{
		{
			name: "a task id git could read as an option",
			path: "/api/projects/" + projID + "/tasks/--help/sync",
			want: http.StatusBadRequest,
		},
		{
			name: "a task of another project",
			path: "/api/projects/other-proj/tasks/" + taskID + "/sync",
			want: http.StatusNotFound,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rr := getSync(t, mux, tt.path)
			if rr.Code != tt.want {
				t.Fatalf("GET %s answered %d, want %d: %s", tt.path, rr.Code, tt.want, rr.Body.String())
			}
		})
	}
}
