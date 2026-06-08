package backend

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Store provides thread-safe access to the SQLite database.
type Store struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewStore creates a new Store instance, initializing the schema if needed.
func NewStore(dbPath string) (*Store, error) {
	// Ensure the parent directory exists
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// Enable foreign key enforcement (disabled by default in SQLite)
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	if err != nil {
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if err := s.initConfigDefaults(); err != nil {
		return nil, fmt.Errorf("init config defaults: %w", err)
	}
	// One-time migration: move global default_model to provider-level
	_ = s.migrateDefaultModelToProviders() // best-effort, ignore errors
	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) initSchema() error {
	schema := `
CREATE TABLE IF NOT EXISTS projects (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    path        TEXT NOT NULL UNIQUE,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
    id                           TEXT PRIMARY KEY,
    project_id                   TEXT NOT NULL,
    title                        TEXT NOT NULL,
    prompt                       TEXT NOT NULL,
    status                       TEXT NOT NULL DEFAULT 'backlog',
    column_order                 INTEGER NOT NULL DEFAULT 0,
    session_id                   TEXT,
    branch                       TEXT,
    output                       TEXT,
    status_state                 TEXT NOT NULL DEFAULT 'idle',
    llama_server_url             TEXT NOT NULL DEFAULT '',
    model_name                   TEXT NOT NULL DEFAULT '',
    max_consecutive_failed_turns INTEGER NOT NULL DEFAULT 3,
    auto_commit                  INTEGER NOT NULL DEFAULT 0,
    auto_pr                      INTEGER NOT NULL DEFAULT 0,
    plan_mode                    INTEGER NOT NULL DEFAULT 0,
    created_at                   TEXT NOT NULL,
    updated_at                   TEXT NOT NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS task_dependencies (
    id             TEXT PRIMARY KEY,
    parent_task_id TEXT NOT NULL,
    child_task_id  TEXT NOT NULL,
    auto_start     INTEGER NOT NULL DEFAULT 1,
    created_at     TEXT NOT NULL,
    FOREIGN KEY (parent_task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    FOREIGN KEY (child_task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    UNIQUE(parent_task_id, child_task_id)
);

CREATE TABLE IF NOT EXISTS review_comments (
    id         TEXT PRIMARY KEY,
    task_id    TEXT NOT NULL,
    file_path  TEXT NOT NULL,
    line_num   INTEGER NOT NULL,
    comment    TEXT NOT NULL,
    resolved   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS home_agent_messages (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    role       TEXT NOT NULL,
    content    TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS task_messages (
    id                 TEXT PRIMARY KEY,
    task_id            TEXT NOT NULL,
    role               TEXT NOT NULL,
    content            TEXT NOT NULL,
    reasoning_content  TEXT DEFAULT NULL,
    model_name         TEXT DEFAULT NULL,
    prompt_tokens      INTEGER DEFAULT 0,
    predicted_tokens   INTEGER DEFAULT 0,
    prompt_ms          INTEGER DEFAULT 0,
    predicted_ms       INTEGER DEFAULT 0,
    duration           INTEGER DEFAULT 0,
    created_at         TEXT NOT NULL,
    FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_tasks_project ON tasks(project_id);
CREATE INDEX IF NOT EXISTS idx_tasks_project_status ON tasks(project_id, status);
CREATE INDEX IF NOT EXISTS idx_task_dependencies_parent ON task_dependencies(parent_task_id);
CREATE INDEX IF NOT EXISTS idx_task_dependencies_child ON task_dependencies(child_task_id);
CREATE INDEX IF NOT EXISTS idx_review_comments_task ON review_comments(task_id);
CREATE INDEX IF NOT EXISTS idx_home_agent_messages_project ON home_agent_messages(project_id);
CREATE INDEX IF NOT EXISTS idx_task_messages_task ON task_messages(task_id);
`
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(schema)
	if err != nil {
		return err
	}
	// Migrations for existing databases
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN plan_mode INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN model_name TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN prompt_tokens INTEGER DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN predicted_tokens INTEGER DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN prompt_ms INTEGER DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN predicted_ms INTEGER DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN duration INTEGER DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN tool_calls TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN tool_result TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN reasoning_content TEXT DEFAULT NULL`)
	return nil
}

func (s *Store) initConfigDefaults() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defaults := map[string]string{
		"llama_server_url": "http://localhost:8081/v1",
		"model_name":       "",
		"workspace_dir":    "./.worktrees",
	}

	// Migrate old default worktrees path to hidden .worktrees
	var wsDir string
	if err := s.db.QueryRow(`SELECT value FROM config WHERE key = 'workspace_dir'`).Scan(&wsDir); err == nil {
		if wsDir == "./worktrees" || wsDir == "worktrees" {
			oldAbs := wsDir
			if !filepath.IsAbs(oldAbs) {
				if abs, err := filepath.Abs(oldAbs); err == nil {
					oldAbs = abs
				}
			}
			newAbs, _ := filepath.Abs("./.worktrees")
			if _, err := os.Stat(oldAbs); err == nil {
				if _, err := os.Stat(newAbs); os.IsNotExist(err) {
					if err := os.Rename(oldAbs, newAbs); err == nil {
						_, _ = s.db.Exec(`UPDATE config SET value = ? WHERE key = 'workspace_dir'`, "./.worktrees")
					}
				}
			}
		}
	}
	for k, v := range defaults {
		_, err := s.db.Exec(`INSERT OR IGNORE INTO config (key, value) VALUES (?, ?)`, k, v)
		if err != nil {
			return fmt.Errorf("init config %s: %w", k, err)
		}
	}
	return nil
}

// migrateDefaultModelToProviders migrates the old global default_model to the
// first provider that contains that model, then clears the global value.
// This is a one-time migration for the per-provider default model feature.
func (s *Store) migrateDefaultModelToProviders() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Read global default_model
	var globalDefaultModel string
	err := s.db.QueryRow(`SELECT value FROM config WHERE key = 'default_model'`).Scan(&globalDefaultModel)
	if err == sql.ErrNoRows || globalDefaultModel == "" {
		return nil // nothing to migrate
	}
	if err != nil {
		return fmt.Errorf("read default_model: %w", err)
	}

	// Read providers
	var providersRaw string
	err = s.db.QueryRow(`SELECT value FROM config WHERE key = 'providers'`).Scan(&providersRaw)
	if err == sql.ErrNoRows || providersRaw == "" {
		return nil // no providers to migrate to
	}
	if err != nil {
		return fmt.Errorf("read providers: %w", err)
	}

	var providers []Provider
	if err := json.Unmarshal([]byte(providersRaw), &providers); err != nil {
		return fmt.Errorf("unmarshal providers: %w", err)
	}

	// Find first provider that contains the global default model and set it
	migrated := false
	for i := range providers {
		if providers[i].DefaultModel != "" {
			continue // already has a default
		}
		for _, m := range providers[i].Models {
			if m.Name == globalDefaultModel {
				providers[i].DefaultModel = globalDefaultModel
				migrated = true
				break
			}
		}
		if migrated {
			break
		}
	}

	if !migrated {
		// Model not found in any provider, just clear the global value
		_, err = s.db.Exec(`DELETE FROM config WHERE key = 'default_model'`)
		return err
	}

	// Save updated providers
	providersJSON, err := json.Marshal(providers)
	if err != nil {
		return fmt.Errorf("marshal updated providers: %w", err)
	}
	_, err = s.db.Exec(`INSERT OR REPLACE INTO config (key, value) VALUES ('providers', ?)`, string(providersJSON))
	if err != nil {
		return fmt.Errorf("save migrated providers: %w", err)
	}

	// Clear global default_model
	_, err = s.db.Exec(`DELETE FROM config WHERE key = 'default_model'`)
	return err
}

// -- Config operations --

func (s *Store) GetConfigValue(key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var value string
	err := s.db.QueryRow(`SELECT value FROM config WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (s *Store) SetConfigValue(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)`, key, value)
	return err
}

// -- Project operations --

func (s *Store) CreateProject(p *Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO projects (id, name, path, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Path, p.CreatedAt.Format(time.RFC3339), p.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *Store) GetProject(id string) (*Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryProject(id)
}

func (s *Store) queryProject(id string) (*Project, error) {
	var p Project
	var createdAt, updatedAt string
	err := s.db.QueryRow(`SELECT id, name, path, created_at, updated_at FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Path, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("project not found")
	}
	p.CreatedAt = scanTime(createdAt)
	p.UpdatedAt = scanTime(updatedAt)
	return &p, err
}

func (s *Store) ListProjects() ([]Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT id, name, path, created_at, updated_at FROM projects ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := []Project{}
	for rows.Next() {
		var p Project
		var createdAt, updatedAt string
		if err := rows.Scan(&p.ID, &p.Name, &p.Path, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		p.CreatedAt = scanTime(createdAt)
		p.UpdatedAt = scanTime(updatedAt)
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (s *Store) UpdateProject(id string, name string) (*Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE projects SET name = ?, updated_at = ? WHERE id = ?`, name, now.Format(time.RFC3339), id)
	if err != nil {
		return nil, err
	}
	p, err := s.queryProject(id)
	return p, err
}

func (s *Store) DeleteProject(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	return err
}

// -- Task operations --

func (s *Store) CreateTask(t *Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO tasks (id, project_id, title, prompt, status, column_order, session_id, branch, output, status_state, llama_server_url, model_name, max_consecutive_failed_turns, auto_commit, auto_pr, plan_mode, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ProjectID, t.Title, t.Prompt, t.Status, t.ColumnOrder, t.SessionID, t.Branch, t.Output, t.StatusState,
		t.LlamaServerURL, t.ModelName, t.MaxConsecutiveFailedTurns, b2i(t.AutoCommit), b2i(t.AutoPR), b2i(t.PlanMode),
		t.CreatedAt.Format(time.RFC3339), t.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *Store) GetTask(projectID, taskID string) (*Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryTask(projectID, taskID)
}

// GetTaskByTaskID retrieves a task by its ID alone (without project ID).
func (s *Store) GetTaskByTaskID(taskID string) (*Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row := s.db.QueryRow(
		`SELECT id, project_id, title, prompt, status, column_order, session_id, branch, output, status_state, llama_server_url, model_name, max_consecutive_failed_turns, auto_commit, auto_pr, plan_mode, created_at, updated_at FROM tasks WHERE id = ?`,
		taskID,
	)
	var t Task
	err := scanTask(row, &t)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("task not found")
	}
	return &t, err
}

func (s *Store) queryTask(projectID, taskID string) (*Task, error) {
	row := s.db.QueryRow(
		`SELECT id, project_id, title, prompt, status, column_order, session_id, branch, output, status_state, llama_server_url, model_name, max_consecutive_failed_turns, auto_commit, auto_pr, plan_mode, created_at, updated_at FROM tasks WHERE id = ? AND project_id = ?`,
		taskID, projectID,
	)
	var t Task
	err := scanTask(row, &t)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("task not found")
	}
	return &t, err
}

func (s *Store) GetTasksByProject(projectID string) ([]Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryTasksByProject(projectID)
}

func (s *Store) queryTasksByProject(projectID string) ([]Task, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, title, prompt, status, column_order, session_id, branch, output, status_state, llama_server_url, model_name, max_consecutive_failed_turns, auto_commit, auto_pr, plan_mode, created_at, updated_at FROM tasks WHERE project_id = ? ORDER BY column_order ASC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []Task{}
	for rows.Next() {
		var t Task
		if err := scanTask(rows, &t); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *Store) UpdateTask(t *Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`UPDATE tasks SET title = ?, prompt = ?, status = ?, column_order = ?, session_id = ?, branch = ?, output = ?, status_state = ?, llama_server_url = ?, model_name = ?, max_consecutive_failed_turns = ?, auto_commit = ?, auto_pr = ?, plan_mode = ?, updated_at = ? WHERE id = ?`,
		t.Title, t.Prompt, t.Status, t.ColumnOrder, t.SessionID, t.Branch, t.Output, t.StatusState,
		t.LlamaServerURL, t.ModelName, t.MaxConsecutiveFailedTurns, b2i(t.AutoCommit), b2i(t.AutoPR), b2i(t.PlanMode), now.Format(time.RFC3339), t.ID,
	)
	return err
}

func (s *Store) UpdateTaskStatusState(taskID, projectID, statusState string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE tasks SET status_state = ?, updated_at = ? WHERE id = ? AND project_id = ?`,
		statusState, now.Format(time.RFC3339), taskID, projectID)
	return err
}

func (s *Store) UpdateTaskStatusAndState(taskID, projectID, status, statusState string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE tasks SET status = ?, status_state = ?, updated_at = ? WHERE id = ? AND project_id = ?`,
		status, statusState, now.Format(time.RFC3339), taskID, projectID)
	return err
}

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

func (s *Store) ReorderTasks(projectID string, reorders []ReorderRequest) error {
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

// GetAllProjectCounts returns task counts grouped by project and status.
// Only counts backlog, in_progress, and review (not done).
func (s *Store) GetAllProjectCounts() map[string]ProjectCounts {
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := make(map[string]ProjectCounts)
	rows, err := s.db.Query(
		`SELECT project_id, status, COUNT(*) FROM tasks WHERE status IN ('backlog', 'in_progress', 'review') GROUP BY project_id, status`,
	)
	if err != nil {
		return counts
	}
	defer rows.Close()
	for rows.Next() {
		var projectID, status string
		var count int
		if err := rows.Scan(&projectID, &status, &count); err != nil {
			continue
		}
		c := counts[projectID]
		switch status {
		case "backlog":
			c.Backlog = count
		case "in_progress":
			c.InProgress = count
		case "review":
			c.Review = count
		}
		counts[projectID] = c
	}
	return counts
}

// -- Task dependency operations --

func (s *Store) CreateDependency(d *TaskDependency) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO task_dependencies (id, parent_task_id, child_task_id, auto_start, created_at) VALUES (?, ?, ?, ?, ?)`,
		d.ID, d.ParentTaskID, d.ChildTaskID, b2i(d.AutoStart), d.CreatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *Store) GetDependencies(taskID string) ([]TaskDependency, error) {
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
	deps := []TaskDependency{}
	for rows.Next() {
		var d TaskDependency
		if err := scanTaskDependency(rows, &d); err != nil {
			return nil, err
		}
		deps = append(deps, d)
	}
	return deps, rows.Err()
}

func (s *Store) GetDependents(taskID string) ([]TaskDependency, error) {
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
	deps := []TaskDependency{}
	for rows.Next() {
		var d TaskDependency
		if err := scanTaskDependency(rows, &d); err != nil {
			return nil, err
		}
		deps = append(deps, d)
	}
	return deps, rows.Err()
}

func (s *Store) DeleteDependency(parentTaskID, childTaskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM task_dependencies WHERE parent_task_id = ? AND child_task_id = ?`, parentTaskID, childTaskID)
	return err
}

func (s *Store) DependencyExists(parentTaskID, childTaskID string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM task_dependencies WHERE parent_task_id = ? AND child_task_id = ?`,
		parentTaskID, childTaskID,
	).Scan(&count)
	return count > 0, err
}

// -- Review comment operations --

func (s *Store) CreateReviewComment(c *ReviewComment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO review_comments (id, task_id, file_path, line_num, comment, resolved, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.TaskID, c.FilePath, c.LineNum, c.Comment, b2i(c.Resolved), c.CreatedAt.Format(time.RFC3339), c.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *Store) GetReviewComments(taskID string) ([]ReviewComment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(
		`SELECT id, task_id, file_path, line_num, comment, resolved, created_at, updated_at FROM review_comments WHERE task_id = ? ORDER BY file_path, line_num`,
		taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	comments := []ReviewComment{}
	for rows.Next() {
		var c ReviewComment
		if err := scanReviewComment(rows, &c); err != nil {
			return nil, err
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

func (s *Store) GetUnresolvedReviewComments(taskID string) ([]ReviewComment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(
		`SELECT id, task_id, file_path, line_num, comment, resolved, created_at, updated_at FROM review_comments WHERE task_id = ? AND resolved = 0 ORDER BY file_path, line_num`,
		taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	comments := []ReviewComment{}
	for rows.Next() {
		var c ReviewComment
		if err := scanReviewComment(rows, &c); err != nil {
			return nil, err
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

func (s *Store) UpdateReviewComment(id string, comment *string, resolved *bool) (*ReviewComment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	query := `UPDATE review_comments SET updated_at = ?`
	args := []interface{}{now.Format(time.RFC3339)}
	if comment != nil {
		query += `, comment = ?`
		args = append(args, *comment)
	}
	if resolved != nil {
		query += `, resolved = ?`
		args = append(args, b2i(*resolved))
	}
	query += ` WHERE id = ?`
	args = append(args, id)
	_, err := s.db.Exec(query, args...)
	if err != nil {
		return nil, err
	}
	// Return updated comment
	rows, err := s.db.Query(
		`SELECT id, task_id, file_path, line_num, comment, resolved, created_at, updated_at FROM review_comments WHERE id = ?`, id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.Next() {
		var c ReviewComment
		if err := scanReviewComment(rows, &c); err != nil {
			return nil, err
		}
		return &c, nil
	}
	return nil, fmt.Errorf("comment not found")
}

func (s *Store) DeleteReviewComment(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM review_comments WHERE id = ?`, id)
	return err
}

// -- Home agent message operations --

func (s *Store) CreateHomeAgentMessage(m *HomeAgentMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO home_agent_messages (id, project_id, role, content, created_at) VALUES (?, ?, ?, ?, ?)`,
		m.ID, m.ProjectID, m.Role, m.Content, m.CreatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *Store) GetHomeAgentMessages(projectID string) ([]HomeAgentMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(
		`SELECT id, project_id, role, content, created_at FROM home_agent_messages WHERE project_id = ? ORDER BY created_at ASC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []HomeAgentMessage{}
	for rows.Next() {
		var m HomeAgentMessage
		var createdAt string
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.Role, &m.Content, &createdAt); err != nil {
			return nil, err
		}
		m.CreatedAt = scanTime(createdAt)
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// CreateTaskMessage inserts a task chat message.
func (s *Store) CreateTaskMessage(m *TaskMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO task_messages (id, task_id, role, content, reasoning_content, tool_calls, tool_result, model_name, prompt_tokens, predicted_tokens, prompt_ms, predicted_ms, duration, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.TaskID, m.Role, m.Content, m.ReasoningContent, m.ToolCalls, m.ToolResult, m.ModelName, m.PromptTokens, m.PredictedTokens, m.PromptMs, m.PredictedMs, m.Duration, m.CreatedAt.Format(time.RFC3339),
	)
	return err
}

// GetTaskMessages retrieves all messages for a task.
func (s *Store) GetTaskMessages(taskID string) ([]TaskMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(
		`SELECT id, task_id, role, content, reasoning_content, tool_calls, tool_result, model_name, prompt_tokens, predicted_tokens, prompt_ms, predicted_ms, duration, created_at FROM task_messages WHERE task_id = ? ORDER BY created_at ASC`,
		taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []TaskMessage{}
	for rows.Next() {
		var m TaskMessage
		var createdAt string
		var modelName, toolCalls, toolResult, reasoningContent sql.NullString
		if err := rows.Scan(&m.ID, &m.TaskID, &m.Role, &m.Content, &reasoningContent, &toolCalls, &toolResult, &modelName, &m.PromptTokens, &m.PredictedTokens, &m.PromptMs, &m.PredictedMs, &m.Duration, &createdAt); err != nil {
			return nil, err
		}
		if reasoningContent.Valid {
			m.ReasoningContent = reasoningContent.String
		}
		if modelName.Valid {
			m.ModelName = modelName.String
		}
		if toolCalls.Valid {
			m.ToolCalls = toolCalls.String
		}
		if toolResult.Valid {
			m.ToolResult = toolResult.String
		}
		m.CreatedAt = scanTime(createdAt)
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// DeleteTaskMessagesAfter deletes the message with the given ID and all messages after it.
func (s *Store) DeleteTaskMessagesAfter(taskID, messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Get the created_at of the message to delete
	var createdAt string
	err := s.db.QueryRow(`SELECT created_at FROM task_messages WHERE id = ?`, messageID).Scan(&createdAt)
	if err != nil {
		return err
	}
	// Delete this message and all messages created after it
	_, err = s.db.Exec(`DELETE FROM task_messages WHERE task_id = ? AND created_at >= ?`, taskID, createdAt)
	return err
}

// UpdateTaskMessage updates the content of a message.
func (s *Store) UpdateTaskMessage(messageID, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE task_messages SET content = ? WHERE id = ?`, content, messageID)
	return err
}

// ResetData clears all test data except config and projects.
// Projects are preserved so tests can find them. Used by e2e tests for a clean slate.
func (s *Store) ResetData() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Clear in reverse dependency order to respect foreign keys
	tables := []string{
		"task_messages",
		"home_agent_messages",
		"review_comments",
		"task_dependencies",
		"tasks",
	}
	for _, table := range tables {
		if _, err := s.db.Exec(fmt.Sprintf("DELETE FROM %s", table)); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	return nil
}

// scanner is the common interface for sql.Row and sql.Rows.
type scanner interface {
	Scan(dest ...interface{}) error
}

// scanTaskDependency scans a row into a TaskDependency, handling bool/time conversions.
func scanTaskDependency(s scanner, d *TaskDependency) error {
	var autoStart int
	var createdAt string
	err := s.Scan(&d.ID, &d.ParentTaskID, &d.ChildTaskID, &autoStart, &createdAt)
	if err != nil {
		return err
	}
	d.AutoStart = i2b(autoStart)
	d.CreatedAt = scanTime(createdAt)
	return nil
}

// scanReviewComment scans a row into a ReviewComment, handling bool/time conversions.
func scanReviewComment(s scanner, c *ReviewComment) error {
	var resolved int
	var createdAt, updatedAt string
	err := s.Scan(&c.ID, &c.TaskID, &c.FilePath, &c.LineNum, &c.Comment, &resolved, &createdAt, &updatedAt)
	if err != nil {
		return err
	}
	c.Resolved = i2b(resolved)
	c.CreatedAt = scanTime(createdAt)
	c.UpdatedAt = scanTime(updatedAt)
	return nil
}

// scanTask scans a row into a Task, handling bool/time conversions.
func scanTask(s scanner, t *Task) error {
	var autoCommit, autoPR, planMode int
	var createdAt, updatedAt string
	err := s.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Prompt, &t.Status, &t.ColumnOrder,
		&t.SessionID, &t.Branch, &t.Output, &t.StatusState,
		&t.LlamaServerURL, &t.ModelName, &t.MaxConsecutiveFailedTurns,
		&autoCommit, &autoPR, &planMode, &createdAt, &updatedAt)
	if err != nil {
		return err
	}
	t.AutoCommit = i2b(autoCommit)
	t.AutoPR = i2b(autoPR)
	t.PlanMode = i2b(planMode)
	t.CreatedAt = scanTime(createdAt)
	t.UpdatedAt = scanTime(updatedAt)
	return nil
}

// -- Helper functions --

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func i2b(i int) bool {
	return i != 0
}

// scanTime scans a TEXT column (RFC3339) into a *time.Time.
func scanTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
