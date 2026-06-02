package backend

import (
	"database/sql"
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
    created_at                   TEXT NOT NULL,
    updated_at                   TEXT NOT NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS conversation_history (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id    TEXT NOT NULL,
    turn_index INTEGER NOT NULL,
    role       TEXT NOT NULL,
    content    TEXT NOT NULL,
    tool_calls TEXT,
    tool_result TEXT,
    created_at TEXT NOT NULL,
    FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
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

CREATE INDEX IF NOT EXISTS idx_tasks_project ON tasks(project_id);
CREATE INDEX IF NOT EXISTS idx_tasks_project_status ON tasks(project_id, status);
CREATE INDEX IF NOT EXISTS idx_conversation_history_task ON conversation_history(task_id);
CREATE INDEX IF NOT EXISTS idx_task_dependencies_parent ON task_dependencies(parent_task_id);
CREATE INDEX IF NOT EXISTS idx_task_dependencies_child ON task_dependencies(child_task_id);
CREATE INDEX IF NOT EXISTS idx_review_comments_task ON review_comments(task_id);
CREATE INDEX IF NOT EXISTS idx_home_agent_messages_project ON home_agent_messages(project_id);
`
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(schema)
	return err
}

func (s *Store) initConfigDefaults() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defaults := map[string]string{
		"llama_server_url": "http://localhost:8081/v1",
		"model_name":       "",
		"workspace_dir":    "./worktrees",
		"endpoint_limits":  `{"http://localhost:8081/v1":2}`,
	}
	for k, v := range defaults {
		_, err := s.db.Exec(`INSERT OR IGNORE INTO config (key, value) VALUES (?, ?)`, k, v)
		if err != nil {
			return fmt.Errorf("init config %s: %w", k, err)
		}
	}
	return nil
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
	rows, err := s.db.Query(`SELECT id, name, path, created_at, updated_at FROM projects ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []Project
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
		`INSERT INTO tasks (id, project_id, title, prompt, status, column_order, session_id, branch, output, status_state, llama_server_url, model_name, max_consecutive_failed_turns, auto_commit, auto_pr, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ProjectID, t.Title, t.Prompt, t.Status, t.ColumnOrder, t.SessionID, t.Branch, t.Output, t.StatusState,
		t.LlamaServerURL, t.ModelName, t.MaxConsecutiveFailedTurns, b2i(t.AutoCommit), b2i(t.AutoPR),
		t.CreatedAt.Format(time.RFC3339), t.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *Store) GetTask(projectID, taskID string) (*Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryTask(projectID, taskID)
}

func (s *Store) queryTask(projectID, taskID string) (*Task, error) {
	var t Task
	var autoCommit, autoPR int
	var createdAt, updatedAt string
	err := s.db.QueryRow(
		`SELECT id, project_id, title, prompt, status, column_order, session_id, branch, output, status_state, llama_server_url, model_name, max_consecutive_failed_turns, auto_commit, auto_pr, created_at, updated_at FROM tasks WHERE id = ? AND project_id = ?`,
		taskID, projectID,
	).Scan(&t.ID, &t.ProjectID, &t.Title, &t.Prompt, &t.Status, &t.ColumnOrder, &t.SessionID, &t.Branch, &t.Output, &t.StatusState,
		&t.LlamaServerURL, &t.ModelName, &t.MaxConsecutiveFailedTurns, &autoCommit, &autoPR, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("task not found")
	}
	t.AutoCommit = i2b(autoCommit)
	t.AutoPR = i2b(autoPR)
	t.CreatedAt = scanTime(createdAt)
	t.UpdatedAt = scanTime(updatedAt)
	return &t, err
}

func (s *Store) GetTasksByProject(projectID string) ([]Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryTasksByProject(projectID)
}

func (s *Store) queryTasksByProject(projectID string) ([]Task, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, title, prompt, status, column_order, session_id, branch, output, status_state, llama_server_url, model_name, max_consecutive_failed_turns, auto_commit, auto_pr, created_at, updated_at FROM tasks WHERE project_id = ? ORDER BY column_order ASC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		var t Task
		var autoCommit, autoPR int
		var createdAt, updatedAt string
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Prompt, &t.Status, &t.ColumnOrder, &t.SessionID, &t.Branch, &t.Output, &t.StatusState,
			&t.LlamaServerURL, &t.ModelName, &t.MaxConsecutiveFailedTurns, &autoCommit, &autoPR, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		t.AutoCommit = i2b(autoCommit)
		t.AutoPR = i2b(autoPR)
		t.CreatedAt = scanTime(createdAt)
		t.UpdatedAt = scanTime(updatedAt)
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *Store) UpdateTask(t *Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`UPDATE tasks SET title = ?, prompt = ?, status = ?, column_order = ?, session_id = ?, branch = ?, output = ?, status_state = ?, llama_server_url = ?, model_name = ?, max_consecutive_failed_turns = ?, auto_commit = ?, auto_pr = ?, updated_at = ? WHERE id = ?`,
		t.Title, t.Prompt, t.Status, t.ColumnOrder, t.SessionID, t.Branch, t.Output, t.StatusState,
		t.LlamaServerURL, t.ModelName, t.MaxConsecutiveFailedTurns, b2i(t.AutoCommit), b2i(t.AutoPR), now.Format(time.RFC3339), t.ID,
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

// -- Conversation history operations --

func (s *Store) SaveConversationHistory(ch *ConversationHistory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO conversation_history (task_id, turn_index, role, content, tool_calls, tool_result, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ch.TaskID, ch.TurnIndex, ch.Role, ch.Content, ch.ToolCalls, ch.ToolResult, ch.CreatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *Store) GetConversationHistory(taskID string) ([]ConversationHistory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(
		`SELECT id, task_id, turn_index, role, content, tool_calls, tool_result, created_at FROM conversation_history WHERE task_id = ? ORDER BY turn_index ASC, id ASC`,
		taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var history []ConversationHistory
	for rows.Next() {
		var ch ConversationHistory
		var createdAt string
		if err := rows.Scan(&ch.ID, &ch.TaskID, &ch.TurnIndex, &ch.Role, &ch.Content, &ch.ToolCalls, &ch.ToolResult, &createdAt); err != nil {
			return nil, err
		}
		ch.CreatedAt = scanTime(createdAt)
		history = append(history, ch)
	}
	return history, rows.Err()
}

func (s *Store) ClearConversationHistory(taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM conversation_history WHERE task_id = ?`, taskID)
	return err
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
	var deps []TaskDependency
	for rows.Next() {
		var d TaskDependency
		var autoStart int
		var createdAt string
		if err := rows.Scan(&d.ID, &d.ParentTaskID, &d.ChildTaskID, &autoStart, &createdAt); err != nil {
			return nil, err
		}
		d.AutoStart = i2b(autoStart)
		d.CreatedAt = scanTime(createdAt)
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
	var deps []TaskDependency
	for rows.Next() {
		var d TaskDependency
		var autoStart int
		var createdAt string
		if err := rows.Scan(&d.ID, &d.ParentTaskID, &d.ChildTaskID, &autoStart, &createdAt); err != nil {
			return nil, err
		}
		d.AutoStart = i2b(autoStart)
		d.CreatedAt = scanTime(createdAt)
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
	var comments []ReviewComment
	for rows.Next() {
		var c ReviewComment
		var resolved int
		var createdAt, updatedAt string
		if err := rows.Scan(&c.ID, &c.TaskID, &c.FilePath, &c.LineNum, &c.Comment, &resolved, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		c.Resolved = i2b(resolved)
		c.CreatedAt = scanTime(createdAt)
		c.UpdatedAt = scanTime(updatedAt)
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
	var comments []ReviewComment
	for rows.Next() {
		var c ReviewComment
		var resolved int
		var createdAt, updatedAt string
		if err := rows.Scan(&c.ID, &c.TaskID, &c.FilePath, &c.LineNum, &c.Comment, &resolved, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		c.Resolved = i2b(resolved)
		c.CreatedAt = scanTime(createdAt)
		c.UpdatedAt = scanTime(updatedAt)
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
		var resolved int
		var createdAt, updatedAt string
		if err := rows.Scan(&c.ID, &c.TaskID, &c.FilePath, &c.LineNum, &c.Comment, &resolved, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		c.Resolved = i2b(resolved)
		c.CreatedAt = scanTime(createdAt)
		c.UpdatedAt = scanTime(updatedAt)
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
	var messages []HomeAgentMessage
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
