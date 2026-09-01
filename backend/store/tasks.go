package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"kanban/backend/types"
)

// marshalAssignedPorts encodes the assigned_ports column. An empty slice
// stores "" (not "null"), which is what every reader expects.
func marshalAssignedPorts(ports []types.AssignedPort) string {
	if len(ports) == 0 {
		return ""
	}
	if b, err := json.Marshal(ports); err == nil {
		return string(b)
	}
	return ""
}

// CreateTask inserts a new task.
// taskColumns lists the tasks table columns in the exact order scanTask reads
// them, and is the single source of truth for every query that returns a task
// (and for CreateTask's INSERT). The mapping is positional, so a column added to
// the schema but not to this list — or listed in a different order than scanTask
// expects — breaks at scan time rather than at compile time.
const taskColumns = `id, project_id, title, prompt, status, session_id, branch, base_ref, base_commit, branch_tip, output, execution_state, has_merge_conflict, llama_server_url, model_name, reasoning_effort, max_consecutive_failed_turns, auto_commit, auto_merge, subtasks_auto_merge, operating_mode, parent_task_id, autostarted, title_generating, summarizing, current_thread_id, created_at, updated_at, moved_at, assigned_ports, session_cost`

func (s *Store) CreateTask(t *types.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	assignedPortsJSON := marshalAssignedPorts(t.AssignedPorts)
	// A new task has never been moved, so its position is its creation time.
	movedAt := t.MovedAt
	if movedAt.IsZero() {
		movedAt = t.CreatedAt
	}
	_, err := s.db.Exec(
		`INSERT INTO tasks (`+taskColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ProjectID, t.Title, t.Prompt, t.Status, t.SessionID, t.Branch, ptrStr(t.BaseRef), ptrStr(t.BaseCommit), ptrStr(t.BranchTip), t.Output, t.ExecutionState, B2I(t.HasMergeConflict),
		t.LlamaServerURL, t.ModelName, ptrStr(t.ReasoningEffort), t.MaxConsecutiveFailedTurns, B2I(t.AutoCommit), B2I(t.AutoMerge), B2I(t.SubtasksAutoMerge), t.OperatingMode,
		ptrStr(t.ParentTaskID), B2I(t.Autostarted), B2I(t.TitleGenerating), B2I(t.Summarizing), ptrStr(t.CurrentThreadID), t.CreatedAt.Format(time.RFC3339), t.UpdatedAt.Format(time.RFC3339), movedAt.UTC().Format(time.RFC3339), assignedPortsJSON, t.SessionCost,
	)
	return err
}

// GetTask retrieves a task by ID within a project.
func (s *Store) GetTask(projectID, taskID string) (*types.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryTask(`id = ? AND project_id = ?`, taskID, projectID)
}

// GetTaskByID retrieves a task by its ID alone (without project ID).
func (s *Store) GetTaskByID(taskID string) (*types.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryTask(`id = ?`, taskID)
}

// queryTask reads a single task matched by `WHERE <where>`. Callers must hold
// s.mu. sql.ErrNoRows is mapped to ErrTaskNotFound: every caller treats
// a missing row the same way, and doing it here keeps the two readers identical.
func (s *Store) queryTask(where string, args ...any) (*types.Task, error) {
	var t types.Task
	err := scanTask(s.db.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE `+where, args...), &t)
	if err == sql.ErrNoRows {
		return nil, ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetTasksByProject returns all tasks for a project, newest created first. The board
// re-orders by moved_at and uses this order to break ties.
func (s *Store) GetTasksByProject(projectID string) ([]types.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryTasks(`project_id = ? ORDER BY created_at DESC, rowid DESC`, projectID)
}

// queryTasks reads every task matched by `WHERE <where>` (which may carry its own
// ORDER BY). Callers must hold s.mu.
func (s *Store) queryTasks(where string, args ...any) ([]types.Task, error) {
	rows, err := s.db.Query(`SELECT `+taskColumns+` FROM tasks WHERE `+where, args...)
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

// GetInFlightTasks returns all tasks whose execution state indicates an
// active run (running/pulling_image). Executors live in memory and die with
// the process, so at startup every such row is stale and needs recovery.
func (s *Store) GetInFlightTasks() ([]types.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryTasks(`execution_state IN ('running', 'pulling_image')`)
}

// GetPendingIdleTasks returns the tasks shown as "In Progress + Pending". Nothing may rest
// in that pair, so at startup — with no live executors — every row here needs a look; see
// TaskExecutor.normalizePendingTasks.
func (s *Store) GetPendingIdleTasks() ([]types.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryTasks(`status = 'in_progress' AND execution_state = 'idle' ORDER BY updated_at ASC`)
}

// UpdateTaskSessionCost updates only the session_cost of a task. Targeted
// (single-column) write so executor-exit bookkeeping can never clobber the
// task's current status/execution_state with a stale in-memory copy.
func (s *Store) UpdateTaskSessionCost(taskID string, cost float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE tasks SET session_cost = ?, updated_at = ? WHERE id = ?`, cost, now.Format(time.RFC3339), taskID)
	return err
}

// inFlightStates are the execution_state values that mean a run is live.
const inFlightStates = "('running','pulling_image')"

// UpdateTask updates all fields of the task.
//
// Ownership guard: it must not move a row into an in-flight execution_state.
// Callers hand in a long-lived copy of the task, so a stale copy (a shutting
// down executor, say) would restart a run that StopTask had just ended.
// UpdateTaskClaimingExecution is the version that may start a run.
//
// moved_at (the board's sort key) advances only when the column changes: SQLite evaluates
// SET expressions against the pre-update row, so "status <> ?" compares old with new status.
// RETURNING puts the stored value back into t, so a broadcast right after persisting is exact.
func (s *Store) UpdateTask(t *types.Task) error {
	return s.updateTask(t, false)
}

// UpdateTaskClaimingExecution is UpdateTask for the caller that owns the run.
func (s *Store) UpdateTaskClaimingExecution(t *types.Task) error {
	return s.updateTask(t, true)
}

func (s *Store) updateTask(t *types.Task, claim bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	assignedPortsJSON := marshalAssignedPorts(t.AssignedPorts)

	// A bare column name in SET reads the old row, so the CASE keeps the stored
	// state whenever the write would start a run.
	execSet := `execution_state = CASE WHEN ? IN ` + inFlightStates + ` THEN execution_state ELSE ? END`
	execArgs := []any{t.ExecutionState, t.ExecutionState}
	if claim {
		execSet = `execution_state = ?`
		execArgs = []any{t.ExecutionState}
	}

	args := []any{t.Status, now.Format(time.RFC3339), t.Title, t.Prompt, t.Status, t.SessionID, t.Branch, ptrStr(t.BaseRef), ptrStr(t.BaseCommit), ptrStr(t.BranchTip), t.Output}
	args = append(args, execArgs...)
	args = append(args, B2I(t.HasMergeConflict),
		t.LlamaServerURL, t.ModelName, ptrStr(t.ReasoningEffort), t.MaxConsecutiveFailedTurns, B2I(t.AutoCommit), B2I(t.AutoMerge), B2I(t.SubtasksAutoMerge), t.OperatingMode, ptrStr(t.ParentTaskID), B2I(t.Autostarted), B2I(t.TitleGenerating), ptrStr(t.CurrentThreadID), assignedPortsJSON, t.SessionCost, now.Format(time.RFC3339), t.ID)

	var movedAt string
	// The row answers with the state it actually holds; typing the scan target keeps
	// the assignment below from accepting anything the database happened to contain.
	var storedExec types.ExecutionState
	err := s.db.QueryRow(
		`UPDATE tasks SET moved_at = CASE WHEN status <> ? THEN ? ELSE moved_at END, title = ?, prompt = ?, status = ?, session_id = ?, branch = ?, base_ref = ?, base_commit = ?, branch_tip = ?, output = ?, `+execSet+`, has_merge_conflict = ?, llama_server_url = ?, model_name = ?, reasoning_effort = ?, max_consecutive_failed_turns = ?, auto_commit = ?, auto_merge = ?, subtasks_auto_merge = ?, operating_mode = ?, parent_task_id = ?, autostarted = ?, title_generating = ?, current_thread_id = ?, assigned_ports = ?, session_cost = ?, updated_at = ? WHERE id = ? RETURNING moved_at, execution_state`,
		args...,
	).Scan(&movedAt, &storedExec)
	if err != nil {
		// No row matched (task deleted concurrently): same outcome as the 0-rows-affected
		// case the previous Exec-based update returned without error for.
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	t.MovedAt = scanTime(movedAt)
	t.ExecutionState = storedExec
	return nil
}

// SetTaskAssignedPorts writes only the assigned_ports column. Ports are set
// while a run is live and cleared when it ends, so this must not touch the
// status or execution_state of the row.
func (s *Store) SetTaskAssignedPorts(taskID, projectID string, ports []types.AssignedPort) error {
	return s.updateTaskColumn(taskID, projectID, "assigned_ports", marshalAssignedPorts(ports))
}

// updateTaskColumn updates a single tasks column, scoped to the task's project.
// The column name comes from internal call sites only (never user input).
func (s *Store) updateTaskColumn(taskID, projectID, column string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec("UPDATE tasks SET "+column+" = ?, updated_at = ? WHERE id = ? AND project_id = ?",
		value, now.Format(time.RFC3339), taskID, projectID)
	return err
}

// UpdateTaskExecutionState updates only the execution_state of a task.
func (s *Store) UpdateTaskExecutionState(taskID, projectID string, executionState types.ExecutionState) error {
	return s.updateTaskColumn(taskID, projectID, "execution_state", string(executionState))
}

// TransitionInFlightTask atomically moves an in-flight task (running /
// pulling_image) to the given execution state
func (s *Store) TransitionInFlightTask(taskID, projectID string, executionState types.ExecutionState, output *string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	var res sql.Result
	var err error
	if output != nil {
		res, err = s.db.Exec(`UPDATE tasks SET execution_state = ?, output = ?, updated_at = ? WHERE id = ? AND project_id = ? AND execution_state IN `+inFlightStates,
			executionState, *output, now.Format(time.RFC3339), taskID, projectID)
	} else {
		res, err = s.db.Exec(`UPDATE tasks SET execution_state = ?, updated_at = ? WHERE id = ? AND project_id = ? AND execution_state IN `+inFlightStates,
			executionState, now.Format(time.RFC3339), taskID, projectID)
	}
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetTaskTitle updates the task title and clears the title_generating flag
// in a single targeted statement — the async title-generation goroutine must
// not write the full row (it read the row at task creation, so a full-row
// update would clobber concurrent changes such as status on task start).
func (s *Store) SetTaskTitle(projectID, taskID, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE tasks SET title = ?, title_generating = ?, updated_at = ? WHERE id = ? AND project_id = ?`,
		title, 0, now.Format(time.RFC3339), taskID, projectID)
	return err
}

// ClearTitleGenerating marks title generation as finished without touching
// any other field of the task row.
func (s *Store) ClearTitleGenerating(projectID, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE tasks SET title_generating = ?, updated_at = ? WHERE id = ? AND project_id = ?`,
		0, now.Format(time.RFC3339), taskID, projectID)
	return err
}

// SetTaskBranchIfEmpty sets branch only if empty. Returns true on success.
func (s *Store) SetTaskBranchIfEmpty(taskID, projectID, branch string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	res, err := s.db.Exec(`UPDATE tasks SET branch = ?, updated_at = ? WHERE id = ? AND project_id = ? AND branch = ''`,
		branch, now.Format(time.RFC3339), taskID, projectID)
	if err != nil {
		return false, err
	}
	rows, _ := res.RowsAffected()
	return rows > 0, nil
}

// ClearTaskBranch clears the branch field of a task. Used to roll back an
// orphaned branch name when worktree creation fails after SetTaskBranchIfEmpty.
func (s *Store) ClearTaskBranch(taskID, projectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE tasks SET branch = '', updated_at = ? WHERE id = ? AND project_id = ?`,
		now.Format(time.RFC3339), taskID, projectID)
	return err
}

// SetTaskBranch unconditionally sets the branch of a task. Used by
// recoverMissingBranch to update a stale branch reference.
func (s *Store) SetTaskBranch(taskID, projectID, branch string) error {
	return s.updateTaskColumn(taskID, projectID, "branch", branch)
}

// SetTaskMergeConflict sets or clears the merge conflict flag on a task.
func (s *Store) SetTaskMergeConflict(taskID, projectID string, hasConflict bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE tasks SET has_merge_conflict = ?, updated_at = ? WHERE id = ? AND project_id = ?`,
		B2I(hasConflict), now.Format(time.RFC3339), taskID, projectID)
	return err
}

// DeleteTask removes a task.
func (s *Store) DeleteTask(projectID, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM tasks WHERE id = ? AND project_id = ?`, taskID, projectID)
	return err
}

// queryTaskStrings runs a single-column string query against tasks and
// collects all rows.
func (s *Store) queryTaskStrings(query string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

// GetAllTaskIDs returns all task IDs across all projects.
func (s *Store) GetAllTaskIDs() ([]string, error) {
	return s.queryTaskStrings(`SELECT id FROM tasks`)
}

// GetAllTaskBranches returns all non-empty branch names referenced by tasks.
func (s *Store) GetAllTaskBranches() ([]string, error) {
	return s.queryTaskStrings(`SELECT DISTINCT branch FROM tasks WHERE branch != ''`)
}

// GetChildren returns all tasks whose parent_task_id matches the given taskID.
func (s *Store) GetChildren(taskID string) ([]types.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryTasks(`parent_task_id = ? ORDER BY created_at DESC, rowid DESC`, taskID)
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

// clearStaleFlag sets a 0/1 tasks flag column to 0 for all tasks where it is 1.
// Called on server startup to clear flags left by crashed/restarted goroutines.
func (s *Store) clearStaleFlag(column string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("UPDATE tasks SET "+column+" = 0, updated_at = ? WHERE "+column+" = 1",
		time.Now().UTC().Format(time.RFC3339))
	return err
}

// ClearStaleTitleGenerating sets title_generating to false for all tasks.
// Called on server startup to clear flags left by crashed/restarted title generation goroutines.
func (s *Store) ClearStaleTitleGenerating() error {
	return s.clearStaleFlag("title_generating")
}

// ClearStaleSummarizing sets summarizing to false for all tasks.
// Called on server startup to clear flags left by crashed/restarted summarization.
func (s *Store) ClearStaleSummarizing() error {
	return s.clearStaleFlag("summarizing")
}

// SetTaskSummarizing updates ONLY the summarizing flag (and updated_at) in a
// single targeted statement. The flag is transition-owned — never written by
// the full-row UpdateTask — so concurrent full-row writes from stale task
// copies cannot resurrect a flag that was already cleared (which would leave
// the UI summarization spinner spinning forever).
func (s *Store) SetTaskSummarizing(taskID string, summarizing bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE tasks SET summarizing = ?, updated_at = ? WHERE id = ?`,
		B2I(summarizing), time.Now().UTC().Format(time.RFC3339), taskID)
	return err
}
