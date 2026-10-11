package server

// The three writes that cannot be replayed safely — a message, a create, a fork — answer a later arrival of one
// identity with what the first arrival produced. docs/reconnection.md.

import (
	"net/http"
	"testing"
	"time"

	"mike/backend/types"
)

func TestReplayedSendWritesNoSecondRow(t *testing.T) {
	s := testServer(t)
	projectID := createTestProject(t, s)
	taskID := createTestTask(t, s, projectID, "one identity, two arrivals")
	threadID := seedThread(t, s, projectID, taskID)
	t.Cleanup(func() { _ = s.executor.StopTask(taskID) })

	body := sendBody("start the work", map[string]any{"client_token": "token-1"})
	path := "/api/projects/" + projectID + "/tasks/" + taskID + "/send"

	first := testResponseAuth(t, s, testRequest(http.MethodPost, path, body))
	if first.Code != http.StatusCreated {
		t.Fatalf("first send = %d: %s", first.Code, first.Body.String())
	}
	firstID, _ := mustJSON[map[string]any](t, first.Body)["id"].(string)

	// Something answered, so the identity is the only thing left saying "this one already happened".
	if err := s.store.CreateTaskMessage(&types.TaskMessage{
		ID: "a-answered", TaskID: taskID, ThreadID: threadID, Role: "assistant",
		Content: "here is what I found", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed the answer: %v", err)
	}

	second := testResponseAuth(t, s, testRequest(http.MethodPost, path, body))
	if second.Code != http.StatusCreated {
		t.Fatalf("replayed send = %d: %s", second.Code, second.Body.String())
	}
	if secondID, _ := mustJSON[map[string]any](t, second.Body)["id"].(string); secondID != firstID {
		t.Errorf("the replay answered a new row %q for the identity that wrote %q", secondID, firstID)
	}
	if got := len(threadRows(t, s, taskID, threadID, "user")); got != 1 {
		t.Errorf("user rows = %d, want 1 — one identity is one message", got)
	}
}

// An edit-and-send names the row it takes the thread back to, and the first arrival made that row not-last. The
// claim is the only thing that keeps the truncate from refusing a send that already happened.
func TestReplayedEditAndSendIsNotRefusedByTheTruncateItCaused(t *testing.T) {
	s := testServer(t)
	projectID := createTestProject(t, s)
	taskID := createTestTask(t, s, projectID, "an edit replayed late")
	threadID := seedThread(t, s, projectID, taskID,
		types.TaskMessage{ID: "u-old", Role: "user", Content: "the original question"},
		types.TaskMessage{ID: "a-old", Role: "assistant", Content: "an answer"},
	)
	t.Cleanup(func() { _ = s.executor.StopTask(taskID) })

	body := sendBody("the question, reworded", map[string]any{
		"replace_message_id": "u-old",
		"client_token":       "token-edit",
	})
	path := "/api/projects/" + projectID + "/tasks/" + taskID + "/send"

	if first := testResponseAuth(t, s, testRequest(http.MethodPost, path, body)); first.Code != http.StatusCreated {
		t.Fatalf("first send = %d: %s", first.Code, first.Body.String())
	}
	replay := testResponseAuth(t, s, testRequest(http.MethodPost, path, body))
	if replay.Code != http.StatusCreated {
		t.Fatalf("a replay of an edit-and-send was refused with %d: %s", replay.Code, replay.Body.String())
	}
	users := threadRows(t, s, taskID, threadID, "user")
	if len(users) != 1 || users[0].Content != "the question, reworded" {
		t.Errorf("the thread holds %+v, want the one edited row", users)
	}
}

// The identity belongs to one write, not to its words.
func TestAnotherIdentityForTheSameWordsIsStillItsOwnWrite(t *testing.T) {
	s := testServer(t)
	projectID := createTestProject(t, s)
	taskID := createTestTask(t, s, projectID, "the same words twice")
	seedThread(t, s, projectID, taskID,
		types.TaskMessage{ID: "u-asked", Role: "user", Content: "run the tests"},
		types.TaskMessage{ID: "a-answered", Role: "assistant", Content: "they pass"},
	)
	t.Cleanup(func() { _ = s.executor.StopTask(taskID) })

	path := "/api/projects/" + projectID + "/tasks/" + taskID + "/send"
	body := sendBody("run the tests", map[string]any{"client_token": "token-asked-again"})
	rr := testResponseAuth(t, s, testRequest(http.MethodPost, path, body))
	if rr.Code != http.StatusCreated {
		t.Fatalf("send = %d: %s", rr.Code, rr.Body.String())
	}
	if id, _ := mustJSON[map[string]any](t, rr.Body)["id"].(string); id == "u-asked" {
		t.Errorf("a fresh identity was answered with the older row %q", id)
	}
}

func TestReplayedCreateMakesNoSecondTask(t *testing.T) {
	s := testServer(t)
	projectID := createTestProject(t, s)
	path := "/api/projects/" + projectID + "/tasks"
	body := map[string]any{"prompt": "fix the flaky test", "client_token": "create-1"}

	first := testResponseAuth(t, s, testRequest(http.MethodPost, path, body))
	if first.Code != http.StatusCreated {
		t.Fatalf("first create = %d: %s", first.Code, first.Body.String())
	}
	firstID, _ := mustJSON[map[string]any](t, first.Body)["id"].(string)

	second := testResponseAuth(t, s, testRequest(http.MethodPost, path, body))
	if second.Code != http.StatusCreated {
		t.Fatalf("replayed create = %d: %s", second.Code, second.Body.String())
	}
	if secondID, _ := mustJSON[map[string]any](t, second.Body)["id"].(string); secondID != firstID {
		t.Errorf("the replay made a second task %q for the identity that made %q", secondID, firstID)
	}
	if tasks := mustJSON[[]map[string]any](t, testResponseAuth(t, s, testRequest(http.MethodGet, path, nil)).Body); len(tasks) != 1 {
		t.Errorf("the project holds %d tasks, want 1", len(tasks))
	}

	other := map[string]any{"prompt": "fix the flaky test", "client_token": "create-2"}
	third := testResponseAuth(t, s, testRequest(http.MethodPost, path, other))
	if third.Code != http.StatusCreated {
		t.Fatalf("create under another identity = %d: %s", third.Code, third.Body.String())
	}
	if thirdID, _ := mustJSON[map[string]any](t, third.Body)["id"].(string); thirdID == firstID {
		t.Errorf("a second identity was answered with the first task %q", thirdID)
	}
}

func TestReplayedForkMakesNoSecondBranch(t *testing.T) {
	s := testServer(t)
	projectID := createTestProject(t, s)
	taskID := createTestTask(t, s, projectID, "a fork arriving twice")
	mainThreadID := mustJSON[[]map[string]any](t,
		testResponseAuth(t, s, testRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID+"/threads", nil)).Body,
	)[0]["id"].(string)
	messageID := mustJSON[map[string]any](t, testResponseAuth(t, s, testRequest(http.MethodPost,
		"/api/projects/"+projectID+"/tasks/"+taskID+"/messages",
		map[string]any{"role": "user", "content": "the row a fork is asked from"})).Body)["id"].(string)

	body := map[string]any{
		"from_thread_id":  mainThreadID,
		"from_message_id": messageID,
		"client_token":    "fork-1",
	}
	path := "/api/projects/" + projectID + "/tasks/" + taskID + "/threads/fork"

	first := testResponseAuth(t, s, testRequest(http.MethodPost, path, body))
	if first.Code != http.StatusCreated {
		t.Fatalf("first fork = %d: %s", first.Code, first.Body.String())
	}
	firstID, _ := mustJSON[map[string]any](t, first.Body)["id"].(string)

	second := testResponseAuth(t, s, testRequest(http.MethodPost, path, body))
	if second.Code != http.StatusCreated {
		t.Fatalf("replayed fork = %d: %s", second.Code, second.Body.String())
	}
	if secondID, _ := mustJSON[map[string]any](t, second.Body)["id"].(string); secondID != firstID {
		t.Errorf("the replay made a second thread %q for the identity that made %q", secondID, firstID)
	}
	if threads := mustJSON[[]map[string]any](t,
		testResponseAuth(t, s, testRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID+"/threads", nil)).Body); len(threads) != 2 {
		t.Errorf("the task holds %d threads, want Main and the one fork", len(threads))
	}
}

// A token is answered only inside the task that wrote it.
func TestOneTasksIdentityIsNotAnothersAnswer(t *testing.T) {
	s := testServer(t)
	projectID := createTestProject(t, s)
	first := createTestTask(t, s, projectID, "one identity, two tasks")
	second := createTestTask(t, s, projectID, "the same identity again")
	t.Cleanup(func() {
		_ = s.executor.StopTask(first)
		_ = s.executor.StopTask(second)
	})

	body := sendBody("start the work", map[string]any{"client_token": "token-two-tasks"})
	send := func(taskID string) string {
		rr := testResponseAuth(t, s, testRequest(http.MethodPost,
			"/api/projects/"+projectID+"/tasks/"+taskID+"/send", body))
		if rr.Code != http.StatusCreated {
			t.Fatalf("send to %s = %d: %s", taskID, rr.Code, rr.Body.String())
		}
		id, _ := mustJSON[map[string]any](t, rr.Body)["id"].(string)
		return id
	}

	if firstID := send(first); send(second) == firstID {
		t.Errorf("the second task was answered with the first task's row %q", firstID)
	}
}
