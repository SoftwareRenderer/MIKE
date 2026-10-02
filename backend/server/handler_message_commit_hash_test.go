package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mike/backend/types"
)

// TestMessageCommitHashRequiresObjectID holds the shape rule at the entry point that is
// actually live. A checkpoint's hash is written through this PATCH and read back later by
// the message-deletion path, which hands the stored value to
//
//	git rev-parse --verify <hash>^{tree}
//	git read-tree --reset -u <hash>
//
// so an option-shaped value stored here is a flag on a command run later, not a strange
// commit. Every hash the product writes is `git stash create` output, so requiring an
// object id costs no legitimate request.
//
// The shape itself is unit-tested in backend/types (TestValidateCommitHash); what this adds is that
// the handler refuses before the store sees it — a forged hash that reached the database would be
// re-read by a path that does not re-check it.
func TestMessageCommitHashRequiresObjectID(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")
	msgID := createCheckpointMessage(t, s, projID, taskID)

	patch := func(hash string) *httptest.ResponseRecorder {
		t.Helper()
		req := testRequest(http.MethodPatch,
			"/api/projects/"+projID+"/tasks/"+taskID+"/messages/"+msgID,
			map[string]any{"git_commit_hash": hash})
		return testResponseAuth(t, s, req)
	}

	for _, forged := range []string{
		"--output=/tmp/mike_commit_hash_canary",  // git's own file-write option
		"-b",                                     // an option, whatever the subcommand
		"HEAD",                                   // a rev, not an object id
		"main~1",                                 // revision syntax
		"000000000000000000000000000000000000 0", // a space splits it into two arguments
		"000000000000000000000000000000000000000x",
	} {
		if rr := patch(forged); rr.Code != http.StatusBadRequest {
			t.Errorf("git_commit_hash %q: got %d, want 400: %s", forged, rr.Code, rr.Body.String())
		}
		if got := storedCommitHash(t, s, msgID); got != "" {
			t.Errorf("git_commit_hash %q was accepted and stored as %q", forged, got)
		}
	}

	// A well-formed id that is simply absent from the repository is stored: the rule is a
	// shape rule, not a blocklist, and a restore of a hash the repository has already
	// dropped must fail as a git answer rather than as a validation error.
	valid := "0000000000000000000000000000000000000000"
	if rr := patch(valid); rr.Code != http.StatusOK {
		t.Fatalf("a valid object id was refused as malformed: %d %s", rr.Code, rr.Body.String())
	}
	if got := storedCommitHash(t, s, msgID); got != valid {
		t.Errorf("stored hash = %q, want %q", got, valid)
	}
}

// createCheckpointMessage stores one assistant message and returns its id, so a PATCH has
// something real to address.
func createCheckpointMessage(t *testing.T, s *Server, projectID, taskID string) string {
	t.Helper()
	msg := &types.TaskMessage{
		ID:        "msg-checkpoint-probe",
		TaskID:    taskID,
		Role:      "assistant",
		Content:   "checkpoint",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}
	return msg.ID
}

// storedCommitHash reads what the database holds, because a 400 that wrote anyway would
// otherwise pass unnoticed.
func storedCommitHash(t *testing.T, s *Server, messageID string) string {
	t.Helper()
	msg, err := s.store.GetTaskMessageByID(messageID)
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	return msg.GitCommitHash
}
