package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"kanban/backend/types"
)

// CreateTask inserts a new task.
func (s *Store) CreateTask(t *types.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	assignedPortsJSON := ""
	if len(t.AssignedPorts) > 0 {
		if b, err := json.Marshal(t.AssignedPorts); err == nil {
			assignedPortsJSON = string(b)
		}
	}
	_, err := s.db.Exec(
		`INSERT INTO tasks (id, project_id, title, prompt, status, column_order, session_id, branch, base_ref, branch_tip, output, execution_state, has_merge_conflict, llama_server_url, model_name, reasoning_effort, max_consecutive_failed_turns, auto_commit, auto_merge, subtasks_auto_merge, operating_mode, parent_task_id, title_generating, current_thread_id, created_at, updated_at, assigned_ports)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ProjectID, t.Title, t.Prompt, t.Status, t.ColumnOrder, t.SessionID, t.Branch, ptrStr(t.BaseRef), ptrStr(t.BranchTip), t.Output, t.ExecutionState, b2i(t.HasMergeConflict),
		t.LlamaServerURL, t.ModelName, ptrStr(t.ReasoningEffort), t.MaxConsecutiveFailedTurns, b2i(t.AutoCommit), b2i(t.AutoMerge), b2i(t.SubtasksAutoMerge), t.OperatingMode,
		ptrStr(t.ParentTaskID), b2i(t.TitleGenerating), ptrStr(t.CurrentThreadID), t.CreatedAt.Format(time.RFC3339), t.UpdatedAt.Format(time.RFC3339), assignedPortsJSON,
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
		`SELECT id, project_id, title, prompt, status, column_order, session_id, branch, base_ref, branch_tip, output, execution_state, has_merge_conflict, llama_server_url, model_name, reasoning_effort, max_consecutive_failed_turns, auto_commit, auto_merge, subtasks_auto_merge, operating_mode, parent_task_id, title_generating, current_thread_id, created_at, updated_at, assigned_ports FROM tasks WHERE id = ?`,
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
		`SELECT id, project_id, title, prompt, status, column_order, session_id, branch, base_ref, branch_tip, output, execution_state, has_merge_conflict, llama_server_url, model_name, reasoning_effort, max_consecutive_failed_turns, auto_commit, auto_merge, subtasks_auto_merge, operating_mode, parent_task_id, title_generating, current_thread_id, created_at, updated_at, assigned_ports FROM tasks WHERE id = ? AND project_id = ?`,
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
		`SELECT id, project_id, title, prompt, status, column_order, session_id, branch, base_ref, branch_tip, output, execution_state, has_merge_conflict, llama_server_url, model_name, reasoning_effort, max_consecutive_failed_turns, auto_commit, auto_merge, subtasks_auto_merge, operating_mode, parent_task_id, title_generating, current_thread_id, created_at, updated_at, assigned_ports FROM tasks WHERE project_id = ? ORDER BY column_order ASC`,
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
	assignedPortsJSON := ""
	if len(t.AssignedPorts) > 0 {
		if b, err := json.Marshal(t.AssignedPorts); err == nil {
			assignedPortsJSON = string(b)
		}
	}
	_, err := s.db.Exec(
		`UPDATE tasks SET title = ?, prompt = ?, status = ?, column_order = ?, session_id = ?, branch = ?, base_ref = ?, branch_tip = ?, output = ?, execution_state = ?, has_merge_conflict = ?, llama_server_url = ?, model_name = ?, reasoning_effort = ?, max_consecutive_failed_turns = ?, auto_commit = ?, auto_merge = ?, subtasks_auto_merge = ?, operating_mode = ?, parent_task_id = ?, title_generating = ?, current_thread_id = ?, assigned_ports = ?, updated_at = ? WHERE id = ?`,
		t.Title, t.Prompt, t.Status, t.ColumnOrder, t.SessionID, t.Branch, ptrStr(t.BaseRef), ptrStr(t.BranchTip), t.Output, t.ExecutionState, b2i(t.HasMergeConflict),
		t.LlamaServerURL, t.ModelName, ptrStr(t.ReasoningEffort), t.MaxConsecutiveFailedTurns, b2i(t.AutoCommit), b2i(t.AutoMerge), b2i(t.SubtasksAutoMerge), t.OperatingMode, ptrStr(t.ParentTaskID), b2i(t.TitleGenerating), ptrStr(t.CurrentThreadID), assignedPortsJSON, now.Format(time.RFC3339), t.ID,
	)
	return err
}

// UpdateTaskExecutionState updates only the execution_state of a task.
func (s *Store) UpdateTaskExecutionState(taskID, projectID, executionState string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE tasks SET execution_state = ?, updated_at = ? WHERE id = ? AND project_id = ?`,
		executionState, now.Format(time.RFC3339), taskID, projectID)
	return err
}

// SetTaskMergeConflict sets or clears the merge conflict flag on a task.
func (s *Store) SetTaskMergeConflict(taskID, projectID string, hasConflict bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE tasks SET has_merge_conflict = ?, updated_at = ? WHERE id = ? AND project_id = ?`,
		b2i(hasConflict), now.Format(time.RFC3339), taskID, projectID)
	return err
}

// UpdateTaskStatusAndState updates both status and execution_state of a task.
func (s *Store) UpdateTaskStatusAndState(taskID, projectID, status, executionState string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE tasks SET status = ?, execution_state = ?, updated_at = ? WHERE id = ? AND project_id = ?`,
		status, executionState, now.Format(time.RFC3339), taskID, projectID)
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

// GetChildren returns all tasks whose parent_task_id matches the given taskID.
func (s *Store) GetChildren(taskID string) ([]types.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(
		`SELECT id, project_id, title, prompt, status, column_order, session_id, branch, base_ref, branch_tip, output, execution_state, has_merge_conflict, llama_server_url, model_name, reasoning_effort, max_consecutive_failed_turns, auto_commit, auto_merge, subtasks_auto_merge, operating_mode, parent_task_id, title_generating, current_thread_id, created_at, updated_at, assigned_ports FROM tasks WHERE parent_task_id = ? ORDER BY column_order ASC`,
		taskID,
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

// GetAllDescendants recursively returns all descendant tasks (children, grandchildren, etc.).
func (s *Store) GetAllDescendants(taskID string) ([]types.Task, error) {
	children, err := s.GetChildren(taskID)
	if err != nil {
		return nil, err
	}
	var descendants []types.Task
	for _, child := range children {
		descendants = append(descendants, child)
		grandchildren, err := s.GetAllDescendants(child.ID)
		if err != nil {
			return nil, err
		}
		descendants = append(descendants, grandchildren...)
	}
	return descendants, nil
}

// GetAncestorChain walks up the parent_task_id chain and returns all ancestors (immediate parent first).
func (s *Store) GetAncestorChain(taskID string) ([]types.Task, error) {
	var chain []types.Task
	currentID := taskID
	for {
		task, err := s.GetTaskByTaskID(currentID)
		if err != nil {
			return chain, nil // stop on missing ancestor
		}
		if task.ParentTaskID == "" {
			break
		}
		// Get the parent
		parent, err := s.GetTaskByTaskID(task.ParentTaskID)
		if err != nil {
			break
		}
		chain = append(chain, *parent)
		currentID = parent.ID
	}
	return chain, nil
}

// ClearStaleTitleGenerating sets title_generating to false for all tasks.
// Called on server startup to clear flags left by crashed/restarted title generation goroutines.
func (s *Store) ClearStaleTitleGenerating() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE tasks SET title_generating = 0, updated_at = ? WHERE title_generating = 1`,
		time.Now().UTC().Format(time.RFC3339))
	return err
}
