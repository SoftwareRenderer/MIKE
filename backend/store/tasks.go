package store

import (
	"database/sql"
	"fmt"
	"time"

	"kanban/backend/types"
)

// CreateTask inserts a new task.
func (s *Store) CreateTask(t *types.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO tasks (id, project_id, title, prompt, status, column_order, session_id, branch, output, status_state, llama_server_url, model_name, max_consecutive_failed_turns, auto_commit, auto_pr, plan_mode, parent_task_id, title_generating, current_thread_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ProjectID, t.Title, t.Prompt, t.Status, t.ColumnOrder, t.SessionID, t.Branch, t.Output, t.StatusState,
		t.LlamaServerURL, t.ModelName, t.MaxConsecutiveFailedTurns, b2i(t.AutoCommit), b2i(t.AutoPR), b2i(t.PlanMode),
		ptrStr(t.ParentTaskID), b2i(t.TitleGenerating), ptrStr(t.CurrentThreadID), t.CreatedAt.Format(time.RFC3339), t.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

// GetTask retrieves a task by project and task ID.
func (s *Store) GetTask(projectID, taskID string) (*types.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryTask(projectID, taskID)
}

// GetTaskByTaskID retrieves a task by its ID alone (without project ID).
func (s *Store) GetTaskByTaskID(taskID string) (*types.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row := s.db.QueryRow(
		`SELECT id, project_id, title, prompt, status, column_order, session_id, branch, output, status_state, llama_server_url, model_name, max_consecutive_failed_turns, auto_commit, auto_pr, plan_mode, parent_task_id, title_generating, current_thread_id, created_at, updated_at FROM tasks WHERE id = ?`,
		taskID,
	)
	var t types.Task
	err := scanTask(row, &t)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("task not found")
	}
	return &t, err
}

func (s *Store) queryTask(projectID, taskID string) (*types.Task, error) {
	row := s.db.QueryRow(
		`SELECT id, project_id, title, prompt, status, column_order, session_id, branch, output, status_state, llama_server_url, model_name, max_consecutive_failed_turns, auto_commit, auto_pr, plan_mode, parent_task_id, title_generating, current_thread_id, created_at, updated_at FROM tasks WHERE id = ? AND project_id = ?`,
		taskID, projectID,
	)
	var t types.Task
	err := scanTask(row, &t)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("task not found")
	}
	return &t, err
}

// GetTasksByProject returns all tasks for a project ordered by column_order.
func (s *Store) GetTasksByProject(projectID string) ([]types.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryTasksByProject(projectID)
}

func (s *Store) queryTasksByProject(projectID string) ([]types.Task, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, title, prompt, status, column_order, session_id, branch, output, status_state, llama_server_url, model_name, max_consecutive_failed_turns, auto_commit, auto_pr, plan_mode, parent_task_id, title_generating, current_thread_id, created_at, updated_at FROM tasks WHERE project_id = ? ORDER BY column_order ASC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []types.Task{}
	for rows.Next() {
		var t types.Task
		if err := scanTask(rows, &t); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// UpdateTask updates all fields of a task.
func (s *Store) UpdateTask(t *types.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`UPDATE tasks SET title = ?, prompt = ?, status = ?, column_order = ?, session_id = ?, branch = ?, output = ?, status_state = ?, llama_server_url = ?, model_name = ?, max_consecutive_failed_turns = ?, auto_commit = ?, auto_pr = ?, plan_mode = ?, parent_task_id = ?, title_generating = ?, current_thread_id = ?, updated_at = ? WHERE id = ?`,
		t.Title, t.Prompt, t.Status, t.ColumnOrder, t.SessionID, t.Branch, t.Output, t.StatusState,
		t.LlamaServerURL, t.ModelName, t.MaxConsecutiveFailedTurns, b2i(t.AutoCommit), b2i(t.AutoPR), b2i(t.PlanMode), ptrStr(t.ParentTaskID), b2i(t.TitleGenerating), ptrStr(t.CurrentThreadID), now.Format(time.RFC3339), t.ID,
	)
	return err
}

// UpdateTaskStatusState updates only the status_state of a task.
func (s *Store) UpdateTaskStatusState(taskID, projectID, statusState string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE tasks SET status_state = ?, updated_at = ? WHERE id = ? AND project_id = ?`,
		statusState, now.Format(time.RFC3339), taskID, projectID)
	return err
}

// UpdateTaskStatusAndState updates both status and status_state of a task.
func (s *Store) UpdateTaskStatusAndState(taskID, projectID, status, statusState string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE tasks SET status = ?, status_state = ?, updated_at = ? WHERE id = ? AND project_id = ?`,
		status, statusState, now.Format(time.RFC3339), taskID, projectID)
	return err
}

// DeleteTask removes a task.
func (s *Store) DeleteTask(projectID, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM tasks WHERE id = ? AND project_id = ?`, taskID, projectID)
	return err
}

// GetAllTaskIDs returns all task IDs across all projects.
func (s *Store) GetAllTaskIDs() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT id FROM tasks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ReorderTasks bulk-updates column_order for tasks in a project.
func (s *Store) ReorderTasks(projectID string, reorders []types.ReorderRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`UPDATE tasks SET column_order = ?, updated_at = ? WHERE id = ? AND project_id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, r := range reorders {
		if _, err := stmt.Exec(r.ColumnOrder, now, r.ID, projectID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
