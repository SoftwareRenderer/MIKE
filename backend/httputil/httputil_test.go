package httputil

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kanban/backend/errs"
	"kanban/pkg/logging"
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
