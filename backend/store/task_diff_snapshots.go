package store

import (
	"database/sql"
	"fmt"
	"time"
)

// TaskDiffSnapshot caches the immutable diff data of a done task.
//
// A done task's canonical diff is BaseCommit..BranchTip — two pinned commit
// SHAs — so the snapshot content can never change unless the task's refs
// change (i.e. the task is re-run and redone, which updates both columns).
// StatsJSON carries the task_diff_stats WebSocket frame fields (without
// type/task_id/already_merged); DiffJSON is the exact JSON body of the
// REST GET /tasks/{id}/diff response for the canonical done diff.
type TaskDiffSnapshot struct {
	TaskID     string
	ProjectID  string
	BaseCommit string
	BranchTip  string
	StatsJSON  string
	DiffJSON   string
	CreatedAt  time.Time
}

const taskDiffSnapshotColumns = "task_id, project_id, base_commit, branch_tip, stats_json, diff_json, created_at"

// GetTaskDiffSnapshot returns the cached snapshot for a task, or (nil, nil)
// when no snapshot exists. Not-found is the normal cache-miss case, not an
// error — callers branch on a nil snapshot.
func (s *Store) GetTaskDiffSnapshot(taskID string) (*TaskDiffSnapshot, error) {
	var snap TaskDiffSnapshot
	var createdAt string
	err := s.QueryRow(
		`SELECT `+taskDiffSnapshotColumns+` FROM task_diff_snapshots WHERE task_id = ?`,
		taskID,
	).Scan(&snap.TaskID, &snap.ProjectID, &snap.BaseCommit, &snap.BranchTip, &snap.StatsJSON, &snap.DiffJSON, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get task diff snapshot: %w", err)
	}
	snap.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return &snap, nil
}

// UpsertTaskDiffSnapshot inserts or replaces a task's snapshot.
func (s *Store) UpsertTaskDiffSnapshot(snap *TaskDiffSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	createdAt := snap.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := s.db.Exec(`
INSERT INTO task_diff_snapshots (task_id, project_id, base_commit, branch_tip, stats_json, diff_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(task_id) DO UPDATE SET
    project_id = excluded.project_id,
    base_commit = excluded.base_commit,
    branch_tip = excluded.branch_tip,
    stats_json = excluded.stats_json,
    diff_json = excluded.diff_json,
    created_at = excluded.created_at`,
		snap.TaskID, snap.ProjectID, snap.BaseCommit, snap.BranchTip, snap.StatsJSON, snap.DiffJSON,
		createdAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("upsert task diff snapshot: %w", err)
	}
	return nil
}

// DeleteTaskDiffSnapshot removes a task's snapshot (task deletion / Clear Done).
func (s *Store) DeleteTaskDiffSnapshot(taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM task_diff_snapshots WHERE task_id = ?`, taskID)
	if err != nil {
		return fmt.Errorf("delete task diff snapshot: %w", err)
	}
	return nil
}
