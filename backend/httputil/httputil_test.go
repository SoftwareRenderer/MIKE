package httputil

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"context"
	"mike/backend/errs"
	"mike/pkg/logging"
)

// decodeError reads the {"error": "..."} envelope JSONError/Fail write.
func decodeError(t *testing.T, body []byte) string {
	t.Helper()
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("response body is not the JSON error envelope: %q (%v)", body, err)
	}
	return got["error"]
}

func TestFailAnswersNotFoundWithTheCallersMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "bare taxonomy sentinel", err: errs.ErrNotFound},
		// The shape the store actually returns: an entity sentinel wrapping it.
		{name: "entity sentinel", err: fmt.Errorf("task %w", errs.ErrNotFound)},
		{name: "wrapped by the caller", err: fmt.Errorf("resolve worktree: %w", fmt.Errorf("task %w", errs.ErrNotFound))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Fail(rec, tc.err, "get task", "task not found")

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}
			if msg := decodeError(t, rec.Body.Bytes()); msg != "task not found" {
				t.Errorf("error message = %q, want %q", msg, "task not found")
			}
		})
	}
}

// A database fault is not a missing row, and its text must not reach the browser.
func TestFailTurnsAnyOtherErrorIntoServerFaultWithoutLeakingDetail(t *testing.T) {
	const internal = "no such table: tasks"

	logged, restore := logging.TestSink()
	defer restore()

	rec := httptest.NewRecorder()
	Fail(rec, errors.New(internal), "get project", "project not found")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	body := rec.Body.String()
	if msg := decodeError(t, rec.Body.Bytes()); msg != "internal error" {
		t.Errorf("error message = %q, want %q", msg, "internal error")
	}
	if strings.Contains(body, internal) {
		t.Errorf("response leaked the internal error: %s", body)
	}
	// The detail is not lost, only relocated to where an operator reads it.
	if !strings.Contains(logged.String(), internal) || !strings.Contains(logged.String(), "get project") {
		t.Errorf("log = %q, want the operation and the real error", logged.String())
	}
}

// A nil error is a handler bug; answering 500 surfaces it instead of a phantom 404.
func TestFailTreatsNilErrorAsServerFault(t *testing.T) {
	// Captured rather than discarded: silence is indistinguishable from "the fault was never
	// logged", which is the thing the sibling test asserts happens.
	_, restore := logging.TestSink()
	defer restore()

	rec := httptest.NewRecorder()
	Fail(rec, nil, "get task", "task not found")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// A conflict is the caller's own doing. Answering 500 for it — which is what `err != nil` does — reports a
// double-click as a server fault, so the classification has to reach the status.
func TestFailAnswersConflictWithTheProducersSentence(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{name: "sentinel", err: errs.Conflict("task already running"), want: "task already running"},
		{name: "wrapped by the caller", err: fmt.Errorf("start task: %w", errs.Conflict("task already running")), want: "task already running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Fail(rec, tc.err, "start task", "task not found")

			if rec.Code != http.StatusConflict {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusConflict)
			}
			if msg := decodeError(t, rec.Body.Bytes()); msg != tc.want {
				t.Errorf("error message = %q, want the producer's sentence %q", msg, tc.want)
			}
		})
	}
}

// Cancellation is neither a fault nor a mystery: it must not be logged at error (a closed tab would
// produce an error line for every navigation abandoned) and must not answer 5xx.
func TestFailTreatsCancellationAsNotAFault(t *testing.T) {
	logged, restore := logging.TestSink()
	defer restore()
	logging.SetLevel("debug")
	defer logging.SetLevel("info")

	rec := httptest.NewRecorder()
	Fail(rec, fmt.Errorf("clone: %w", context.Canceled), "clone repository", "project not found")

	if rec.Code >= 500 {
		t.Errorf("status = %d, want a non-5xx answer for a cancellation", rec.Code)
	}
	out := logged.String()
	if strings.Contains(out, "level=ERROR") {
		t.Errorf("a cancellation was logged as a fault:\n%s", out)
	}
	if !strings.Contains(out, "level=DEBUG") {
		t.Errorf("a cancellation was not logged at debug at all:\n%s", out)
	}
}

// A git subprocess failure carries git's stderr: absolute paths, the remote's URL, driver text. An
// operation-classified fault answers 500 with MIKE's own sentence and puts the cause in the log, which is
// the reverse of the hand-written 500 it replaces — that one showed the panel the path and left the server
// with no trace of the failure at all.
func TestFailAnswersAServerFaultWithMIKEsOwnSentenceAndLogsTheCause(t *testing.T) {
	const cause = `exit status 128
fatal: not a git repository: /home/operator/.worktrees/frost-amber-quartz/.git`

	logged, restore := logging.TestSink()
	defer restore()

	rec := httptest.NewRecorder()
	Fail(rec, errs.Operation("git log", errors.New(cause)), "git log", "project not found")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	msg := decodeError(t, rec.Body.Bytes())
	if msg != "git log failed" {
		t.Errorf("error message = %q, want the operation's own sentence", msg)
	}
	if strings.Contains(msg, "/home/operator") {
		t.Errorf("a host path reached the browser: %q", msg)
	}
	// Fragments, not the whole string: slog quotes the message, so a cause carrying a newline is
	// escaped on the way to the buffer and never appears as one substring.
	log := logged.String()
	if !strings.Contains(log, "git log") || !strings.Contains(log, "/home/operator/.worktrees") {
		t.Errorf("the cause must be logged, not answered; log was:\n%s", log)
	}
}

// The sentence survives the caller's own wrapping, because a producer returns the operation from inside a
// helper and the handler wraps it on the way out.
func TestFailAnswersAWrappedServerFaultWithTheSameSentence(t *testing.T) {
	rec := httptest.NewRecorder()
	Fail(rec, fmt.Errorf("load branch: %w", errs.Operation("git branches", errors.New("boom"))),
		"git branches", "project not found")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if msg := decodeError(t, rec.Body.Bytes()); msg != "git branches failed" {
		t.Errorf("error message = %q, want %q", msg, "git branches failed")
	}
}

// Fault's contract, stated once: 500, the operation's sentence in the body, the cause in the log. A
// handler that reaches for a status code re-opens the question of how much of the cause to show.
func TestFaultAnswersWithTheOperationAndLogsTheCause(t *testing.T) {
	logged, restore := logging.TestSink()
	defer restore()

	rec := httptest.NewRecorder()
	Fault(rec, "git status", errors.New("exit status 128: repo .worktrees/frost is corrupt"))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if msg := decodeError(t, rec.Body.Bytes()); msg != "git status failed" {
		t.Errorf("error message = %q, want %q", msg, "git status failed")
	}
	if strings.Contains(rec.Body.String(), "frost") {
		t.Errorf("the cause leaked into the body: %s", rec.Body.String())
	}
	if !strings.Contains(logged.String(), "frost is corrupt") {
		t.Errorf("Fault must log the cause it withheld; log was:\n%s", logged.String())
	}
}
