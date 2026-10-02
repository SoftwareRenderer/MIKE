package server

// "New Thread" is one click that forks and then sends. The fork holds the task's worktree lock, so a
// provider call inside its response stalls the click and takes the slot that turn needs next.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"mike/pkg/logging"
)

// providerThat answers a completion request with `answer`, holding every request open until the returned
// release runs. A fork that answers before the release runs spends no provider request.
func providerThat(t *testing.T, answer string) (string, func()) {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"`+answer+`","finish_reason":"stop"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
	}))
	t.Cleanup(srv.Close)
	// Teardown runs last-registered first: a waiting request has to be released before the listener closes.
	t.Cleanup(unblock)
	return srv.URL, unblock
}

func useProvider(t *testing.T, s *Server, projID, taskID, url string) {
	t.Helper()
	task, err := s.store.GetTask(projID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	task.LlamaServerURL = url
	task.ModelName = "a-model"
	if err := s.store.UpdateTask(task); err != nil {
		t.Fatalf("update task: %v", err)
	}
}

// forkAndWait posts the fork from its own goroutine, so a handler that never answers fails with this
// message instead of the package timeout.
func forkAndWait(t *testing.T, s *Server, projID, taskID, fromThread, fromMsg, newContent string, wait time.Duration) *httptest.ResponseRecorder {
	t.Helper()
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		answered <- testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork",
			map[string]any{"from_thread_id": fromThread, "from_message_id": fromMsg, "new_content": newContent}))
	}()
	select {
	case got := <-answered:
		return got
	case <-time.After(wait):
		t.Fatalf("the fork did not answer within %s", wait)
		return nil
	}
}

// TestForkNamesTheThreadWithoutTheProvider: the tab is named from the question itself, so a provider that
// has not answered cannot hold the click — or the worktree lock — open.
func TestForkNamesTheThreadWithoutTheProvider(t *testing.T) {
	labelProvider, releaseLabel := providerThat(t, "an LLM label")

	s := testServer(t)
	fx := setupForkWithCheckpoint(t, s, "fork-naming")
	useProvider(t, s, fx.projID, fx.taskID, labelProvider)

	msg := postMessageIn(t, s, fx, "count to a hundred")
	from := activeThreadID(t, s, fx.projID, fx.taskID)

	// 40 runes of three-byte text: a byte-sized cut would end the name inside a character.
	edited := strings.Repeat("質", 40)
	rr := forkAndWait(t, s, fx.projID, fx.taskID, from, msg, edited, 30*time.Second)
	releaseLabel()
	if rr.Code != http.StatusCreated {
		t.Fatalf("fork: %d %s", rr.Code, rr.Body.String())
	}
	if got, want := mustJSON[map[string]any](t, rr.Body)["name"].(string), strings.Repeat("質", 35); got != want {
		t.Errorf("thread name %q, want the question's first 35 runes %q", got, want)
	}
}

// TestForkLabelsItsCheckpointAfterAnswering: the checkpoint label is still generated — after the response,
// over the frame that carries every other checkpoint label.
func TestForkLabelsItsCheckpointAfterAnswering(t *testing.T) {
	labelProvider, releaseLabel := providerThat(t, "counting higher")

	sink, restore := logging.TestSink()
	defer restore()
	s := testServer(t)
	fx := setupForkWithCheckpoint(t, s, "fork-label")
	useProvider(t, s, fx.projID, fx.taskID, labelProvider)

	msg := postMessageIn(t, s, fx, "count higher still")
	from := activeThreadID(t, s, fx.projID, fx.taskID)
	rr := forkAndWait(t, s, fx.projID, fx.taskID, from, msg, "count higher again", 30*time.Second)
	releaseLabel()
	if rr.Code != http.StatusCreated {
		t.Fatalf("fork: %d %s", rr.Code, rr.Body.String())
	}

	deadline := time.Now().Add(30 * time.Second)
	for !loggedCheckpointLabel(sink.String(), "counting higher") {
		if time.Now().After(deadline) {
			t.Fatalf("no checkpoint label was generated after the fork answered; log:\n%s", sink.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// loggedCheckpointLabel looks for the generate component's own line. The log escapes the quotes around
// the label, so the parts are matched separately.
func loggedCheckpointLabel(log, label string) bool {
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "component=generate") && strings.Contains(line, "generated:") && strings.Contains(line, label) {
			return true
		}
	}
	return false
}
