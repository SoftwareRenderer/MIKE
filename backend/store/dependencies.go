package store

import (
	"time"

	"kanban/backend/types"
)

// CreateDependency inserts a new task dependency.
func (s *Store) CreateDependency(d *types.TaskDependency) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO task_dependencies (id, parent_task_id, child_task_id, auto_start, created_at) VALUES (?, ?, ?, ?, ?)`,
		d.ID, d.ParentTaskID, d.ChildTaskID, B2I(d.AutoStart), d.CreatedAt.Format(time.RFC3339),
	)
	return err
}

// GetDependencies returns the parent dependencies of a task (tasks this task depends on).
func (s *Store) GetDependencies(taskID string) ([]types.TaskDependency, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(
		`SELECT id, parent_task_id, child_task_id, auto_start, created_at FROM task_dependencies WHERE child_task_id = ?`,
		taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deps := []types.TaskDependency{}
	for rows.Next() {
		var d types.TaskDependency
		if err := scanTaskDependency(rows, &d); err != nil {
			return nil, err
		}
		deps = append(deps, d)
	}
	return deps, rows.Err()
}

// GetDependents returns the child dependents of a task (tasks that depend on this task).
func (s *Store) GetDependents(taskID string) ([]types.TaskDependency, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(
		`SELECT id, parent_task_id, child_task_id, auto_start, created_at FROM task_dependencies WHERE parent_task_id = ?`,
		taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deps := []types.TaskDependency{}
	for rows.Next() {
		var d types.TaskDependency
		if err := scanTaskDependency(rows, &d); err != nil {
			return nil, err
		}
		deps = append(deps, d)
	}
	return deps, rows.Err()
}

// DeleteDependency removes a dependency link.
func (s *Store) DeleteDependency(parentTaskID, childTaskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM task_dependencies WHERE parent_task_id = ? AND child_task_id = ?`, parentTaskID, childTaskID)
	return err
}
