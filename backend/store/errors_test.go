package store

import (
	"errors"
	"testing"

	"mike/backend/errs"
)

// The single-row readers carry two things in one error: the 404 classification and the
// message the API has always returned. Both are pinned; each breaks easily from the other.
func TestSingleRowReadersClassifyMissingRows(t *testing.T) {
	t.Parallel()
	s := testStore(t)

	for _, tc := range []struct {
		name     string
		lookup   func() error
		sentinel error
		message  string
	}{
		{
			name:     "task by project",
			lookup:   func() error { _, err := s.GetTask("proj-1", "missing-task"); return err },
			sentinel: ErrTaskNotFound,
			message:  "task not found",
		},
		{
			name:     "task by id alone",
			lookup:   func() error { _, err := s.GetTaskByID("missing-task"); return err },
			sentinel: ErrTaskNotFound,
			message:  "task not found",
		},
		{
			name:     "project",
			lookup:   func() error { _, err := s.GetProject("missing-project"); return err },
			sentinel: ErrProjectNotFound,
			message:  "project not found",
		},
		{
			name:     "thread",
			lookup:   func() error { _, err := s.GetThread("missing-thread"); return err },
			sentinel: ErrThreadNotFound,
			message:  "thread not found",
		},
		{
			name:     "message",
			lookup:   func() error { _, err := s.GetTaskMessageByID("missing-message"); return err },
			sentinel: ErrMessageNotFound,
			message:  "task message not found",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.lookup()
			if err == nil {
				t.Fatal("expected an error for a missing row")
			}
			// The entity's own identity, for callers that care which row was absent.
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("errors.Is(err, %v) = false, err = %v", tc.sentinel, err)
			}
			// The taxonomy every handler answers with, at any wrap depth.
			if !errs.IsNotFound(err) {
				t.Errorf("errs.IsNotFound(%v) = false, want true", err)
			}
			// And the wire-visible text, unchanged from before the taxonomy existed.
			if err.Error() != tc.message {
				t.Errorf("message = %q, want %q", err.Error(), tc.message)
			}
		})
	}
}

// A database fault must never look like a missing row - that is the whole distinction.
func TestDatabaseFaultIsNotReportedAsNotFound(t *testing.T) {
	t.Parallel()
	s := testStore(t)

	// Make every query fail: drop the table.
	if _, err := s.Exec(`DROP TABLE tasks`); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	_, err := s.GetTask("proj-1", "any-task")
	if err == nil {
		t.Fatal("expected a database error after dropping the table")
	}
	if errs.IsNotFound(err) {
		t.Errorf("a database fault was classified as not-found: %v", err)
	}
}
