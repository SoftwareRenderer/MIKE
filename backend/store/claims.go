package store

import (
	"fmt"
	"time"

	"mike/pkg/logging"
)

// A claim records what the first arrival of a write produced, under an identity the client minted and every
// replay of that write repeats. It is recorded after the write lands, and it goes when the task is deleted or the
// row ages past claimRetention, so a replay is answered by the row the first arrival made or not at all.
// docs/reconnection.md → "One identity per write, so a replay cannot land twice".
//
// Each statement below is one statement, so nothing here needs Store.tx: the prune and the insert share no
// outcome, and a claim that goes unpruned costs space, never correctness.

// The writes a claim guards. `kind` says which one a row came from; nothing decides on it.
const (
	ClaimSend       = "send"
	ClaimCreateTask = "createTask"
	ClaimForkThread = "forkTaskThread"
)

// How long a claim outlives the write it guards. Only the browser's own queued action replays, and a claim pruned
// early costs a duplicate rather than a lost write, so the horizon trades guard coverage for table growth.
const claimRetention = 30 * 24 * time.Hour

// The horizon delete, shared with the schema test that proves it walks the created_at index.
const pruneExpiredClaimsSQL = `DELETE FROM client_claims WHERE created_at < ?`

// ClaimedWrite answers what this write identity already produced inside one task. A token is text from the
// request, so it is answered only where it was written: another task's row is a read nobody granted.
func (s *Store) ClaimedWrite(token, taskID string) (resultID string, claimed bool) {
	if token == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	// A read that fails is "nothing claimed": a claim table that cannot be read must not refuse a message.
	var id string
	if err := s.db.QueryRow(`SELECT result_id FROM client_claims WHERE token = ? AND task_id = ?`,
		token, taskID).Scan(&id); err != nil {
		return "", false
	}
	return id, true
}

// ClaimedCreate answers what a create carrying this identity already made, scoped to the project the request
// names — a create has no task to name yet.
func (s *Store) ClaimedCreate(token, projectID string) (taskID string, claimed bool) {
	if token == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var id string
	if err := s.db.QueryRow(`SELECT result_id FROM client_claims WHERE token = ? AND project_id = ?`,
		token, projectID).Scan(&id); err != nil {
		return "", false
	}
	return id, true
}

// ClaimWrite records what this write identity produced, and answers nothing: an identity already recorded keeps
// the result it was first given. Losing the record costs the guard, not the write.
func (s *Store) ClaimWrite(token, projectID, taskID, kind, resultID string) error {
	if token == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredClaims()
	if _, err := s.db.Exec(
		`INSERT INTO client_claims (token, project_id, task_id, kind, result_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(token) DO NOTHING`,
		token, projectID, taskID, kind, resultID, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("claim %s write for task %s: %w", kind, taskID, err)
	}
	return nil
}

// pruneExpiredClaims drops the claims older than claimRetention. It runs here because this is the only writer, so
// no sweep, goroutine or boot step is owed, and idx_client_claims_created_at is what keeps the delete a seek.
// The caller holds s.mu.
func (s *Store) pruneExpiredClaims() {
	if _, err := s.db.Exec(pruneExpiredClaimsSQL,
		time.Now().UTC().Add(-claimRetention).Format(time.RFC3339)); err != nil {
		logging.For(logging.Store).Warnf("pruning expired client claims failed: %v", err)
	}
}
