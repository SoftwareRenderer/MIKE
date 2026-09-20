package exec

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"mike/backend/types"
)

// refusedByProvider answers the way a provider refuses a request it could not decode: a 4xx with its
// parse error in the body. The identical body sent again is refused identically.
func refusedByProvider(status int, message string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"error":{"message":%q,"type":"BadRequestError","param":null,"code":%d}}`, message, status)
	}
}

// TestRefusedRequestEndsTheRunInOneCall covers the request the provider refuses rather than loses:
// retrying it sends the same bytes to the same answer, and reporting it as a connection problem sends
// the user off to check their network.
func TestRefusedRequestEndsTheRunInOneCall(t *testing.T) {
	const reason = "Expecting value: line 1 column 111 (char 110)"
	srv, calls := crashLLM(t, refusedByProvider(http.StatusBadRequest, reason))
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	const taskID = "request-refused"
	worktreePath := crashTask(t, te, projectID, initialSHA, taskID, srv.URL)

	startExecRun(t, te, projectID, taskID, srv.URL, worktreePath)

	waitForFailureRow(t, te, taskID, 30*time.Second)

	row := mustGetTask(t, te, taskID)
	if row.ExecutionState != types.ExecStateFailed || row.Status != types.StatusReview {
		t.Fatalf("after a refused request: execution=%q status=%q, want failed/review so a person picks it up",
			row.ExecutionState, row.Status)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("streaming calls = %d, want 1: a refusal is deterministic, and retrying it only "+
			"delays the sentence by the backoff", n)
	}

	msgs := mustMessages(t, te, taskID, "thread-"+taskID)
	err := lastErrorRow(msgs)
	if !strings.Contains(err, "[llm_request_rejected]") {
		t.Errorf("error row = %q, want the llm_request_rejected one; roles = %v", err, rolesInOrder(t, te, taskID, "thread-"+taskID))
	}
	if strings.Contains(err, "[llm_connection_failed]") || strings.Contains(err, "connection failed") {
		t.Errorf("a refused request was reported as a connection failure: %q", err)
	}
	if !strings.Contains(err, reason) {
		t.Errorf("the error row dropped the provider's reason, leaving nothing to act on: %q", err)
	}
	if n := countErrors(msgs); n != 1 {
		t.Errorf("%d error rows, want 1", n)
	}
	for _, m := range msgs {
		if m.Role == "system" && strings.Contains(m.Content, "retrying") {
			t.Errorf("a retry hint was stored for a call that is not retried: %q", m.Content)
		}
	}
}

// TestRateLimitedRequestIsStillRetried holds the boundary the other side of the classification: a 429
// is a 4xx that a later call can get an answer from.
func TestRateLimitedRequestIsStillRetried(t *testing.T) {
	srv, calls := crashLLM(t, refusedByProvider(http.StatusTooManyRequests, "rate limit"))
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	const taskID = "rate-limited"
	worktreePath := crashTask(t, te, projectID, initialSHA, taskID, srv.URL)

	startExecRun(t, te, projectID, taskID, srv.URL, worktreePath)

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && calls.Load() < 2 {
		time.Sleep(50 * time.Millisecond)
	}
	if calls.Load() < 2 {
		t.Errorf("streaming calls = %d, want the 429 retried", calls.Load())
	}
}

// countErrors counts the rows a run told the user about.
func countErrors(msgs []types.TaskMessage) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "error" {
			n++
		}
	}
	return n
}
