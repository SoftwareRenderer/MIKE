package api

import (
	"encoding/json"
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

func TestSyncDigestNamesEveryPartAReturningScreenCompares(t *testing.T) {
	t.Parallel()
	h, s, _ := newTestTaskHandler(t)
	projID, taskID := seedSyncProject(t, h, s)
	seedSyncRows(t, s, taskID, 3)

	got := readSyncDigest(t, h, "/api/projects/"+projID+"/tasks/"+taskID+"/sync")

	if got.Status != types.StatusInProgress || got.ExecutionState != types.ExecStateRunning {
		t.Errorf("digest carries %s/%s, want the task row's in_progress/running", got.Status, got.ExecutionState)
	}
	if got.Thread != "thread-1" {
		t.Errorf("thread = %q, want the conversation the task is on", got.Thread)
	}
	if got.Conversation.Messages != 3 {
		t.Errorf("conversation counts %d rows, want 3", got.Conversation.Messages)
	}
	if got.Conversation.Epoch == "" {
		t.Error("no epoch: a client cannot tell a fresh server from an idle one")
	}
	// Nothing has been computed for this task's worktree, which is an unknown and never a "clean".
	if got.Diff.Known {
		t.Error("diff claims to be known with no watcher having computed one")
	}
}

func TestSyncDigestConversationMovesWithWhatTheRowsHold(t *testing.T) {
	t.Parallel()
	h, s, _ := newTestTaskHandler(t)
	projID, taskID := seedSyncProject(t, h, s)
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/sync"

	before := readSyncDigest(t, h, path)
	if before.Conversation.Revision != 0 {
		t.Errorf("a task no executor has written a row for is at revision %d, want 0", before.Conversation.Revision)
	}
	seedSyncRows(t, s, taskID, 1)
	after := readSyncDigest(t, h, path)
	if after.Conversation.Messages != before.Conversation.Messages+1 {
		t.Errorf("rows went from %d to %d across one write", before.Conversation.Messages, after.Conversation.Messages)
	}
	if after.Conversation.Epoch != before.Conversation.Epoch {
		t.Errorf("epoch moved within one process (%q then %q): a client would re-read on every answer", before.Conversation.Epoch, after.Conversation.Epoch)
	}
}

func TestSyncDigestRefusesATaskIDGitCouldReadAsAnOption(t *testing.T) {
	t.Parallel()
	h, s, _ := newTestTaskHandler(t)
	projID, _ := seedSyncProject(t, h, s)

	rr := getSync(t, syncMux(h), "/api/projects/"+projID+"/tasks/--help/sync")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("a malformed task id answered %d, want 400: %s", rr.Code, rr.Body.String())
	}
}

func TestSyncDigestAnswersNotFoundForAnotherProjectsTask(t *testing.T) {
	t.Parallel()
	h, s, _ := newTestTaskHandler(t)
	_, taskID := seedSyncProject(t, h, s)

	rr := getSync(t, syncMux(h), "/api/projects/other-proj/tasks/"+taskID+"/sync")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("a task of another project answered %d, want 404: %s", rr.Code, rr.Body.String())
	}
}
