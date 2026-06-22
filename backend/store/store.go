package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"kanban/backend/types"

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
	// Use a single connection so PRAGMA settings (foreign_keys, journal_mode) apply to all operations.
	// The store already serializes access via s.mu, so pooling provides no benefit.
	db.SetMaxOpenConns(1)
	// Enable WAL mode for better concurrent read/write performance
	_, err = db.Exec("PRAGMA journal_mode = WAL")
	if err != nil {
		return nil, fmt.Errorf("set journal mode: %w", err)
	}
	// Set busy timeout to handle concurrent access (e.g., during parallel test runs)
	_, err = db.Exec("PRAGMA busy_timeout = 10000")
	if err != nil {
		return nil, fmt.Errorf("set busy timeout: %w", err)
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

// Exec runs a raw SQL statement with arguments.
func (s *Store) Exec(query string, args ...interface{}) (sql.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Exec(query, args...)
}

func (s *Store) initSchema() error {
	schema := `
CREATE TABLE IF NOT EXISTS projects (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    path          TEXT NOT NULL UNIQUE,
    origin_branch TEXT DEFAULT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
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
    base_ref                     TEXT DEFAULT NULL,
    branch_tip                   TEXT DEFAULT NULL,
    output                       TEXT,
    status_state                 TEXT NOT NULL DEFAULT 'idle',
    llama_server_url             TEXT NOT NULL DEFAULT '',
    model_name                   TEXT NOT NULL DEFAULT '',
    max_consecutive_failed_turns INTEGER NOT NULL DEFAULT 3,
    auto_commit                  INTEGER NOT NULL DEFAULT 0,
    auto_pr                      INTEGER NOT NULL DEFAULT 0,
    plan_mode                    INTEGER NOT NULL DEFAULT 0,
    parent_task_id               TEXT NULL,
    change_summary               TEXT DEFAULT NULL,
    created_at                   TEXT NOT NULL,
    updated_at                   TEXT NOT NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_task_id) REFERENCES tasks(id) ON DELETE SET NULL
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

CREATE TABLE IF NOT EXISTS task_threads (
    id              TEXT PRIMARY KEY,
    task_id         TEXT NOT NULL,
    parent_thread_id TEXT NULL,
    name            TEXT NOT NULL DEFAULT 'Main',
    is_active       INTEGER NOT NULL DEFAULT 0,
    git_branch      TEXT DEFAULT NULL,
    created_at      TEXT NOT NULL,
    FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_thread_id) REFERENCES task_threads(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS task_messages (
    id                 TEXT PRIMARY KEY,
    task_id            TEXT NOT NULL,
    thread_id          TEXT NULL,
    role               TEXT NOT NULL,
    content            TEXT NOT NULL,
    reasoning_content  TEXT DEFAULT NULL,
    tool_calls         TEXT DEFAULT NULL,
    tool_result        TEXT DEFAULT NULL,
    attachments        TEXT DEFAULT NULL,
    model_name         TEXT DEFAULT NULL,
    provider_name      TEXT DEFAULT NULL,
    prompt_tokens      INTEGER DEFAULT 0,
    predicted_tokens   INTEGER DEFAULT 0,
    cache_tokens       INTEGER DEFAULT 0,
    prompt_ms          INTEGER DEFAULT 0,
    predicted_ms       INTEGER DEFAULT 0,
    duration           INTEGER DEFAULT 0,
    git_commit_hash    TEXT DEFAULT NULL,
    created_at         TEXT NOT NULL,
    FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS project_token_stats (
    project_id       TEXT NOT NULL,
    model_name       TEXT NOT NULL DEFAULT 'unknown',
    provider_name    TEXT NOT NULL DEFAULT '',
    prompt_tokens    INTEGER NOT NULL DEFAULT 0,
    cache_tokens     INTEGER NOT NULL DEFAULT 0,
    predicted_tokens INTEGER NOT NULL DEFAULT 0,
    prompt_ms        INTEGER NOT NULL DEFAULT 0,
    predicted_ms     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (project_id, model_name, provider_name),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS project_token_tasks (
    project_id    TEXT NOT NULL,
    model_name    TEXT NOT NULL DEFAULT 'unknown',
    provider_name TEXT NOT NULL DEFAULT '',
    task_id       TEXT NOT NULL,
    PRIMARY KEY (project_id, model_name, provider_name, task_id),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_tasks_project ON tasks(project_id);
CREATE INDEX IF NOT EXISTS idx_tasks_project_status ON tasks(project_id, status);
CREATE INDEX IF NOT EXISTS idx_task_dependencies_parent ON task_dependencies(parent_task_id);
CREATE INDEX IF NOT EXISTS idx_task_dependencies_child ON task_dependencies(child_task_id);
CREATE INDEX IF NOT EXISTS idx_review_comments_task ON review_comments(task_id);
CREATE INDEX IF NOT EXISTS idx_home_agent_messages_project ON home_agent_messages(project_id);
CREATE INDEX IF NOT EXISTS idx_task_messages_task ON task_messages(task_id);
CREATE INDEX IF NOT EXISTS idx_task_threads_task ON task_threads(task_id);
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
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN parent_task_id TEXT NULL`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_parent ON tasks(parent_task_id)`)
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN title_generating INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN cache_tokens INTEGER DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN provider_name TEXT DEFAULT NULL`)
	// Thread support migrations
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN current_thread_id TEXT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN thread_id TEXT NULL`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_task_threads_task ON task_threads(task_id)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_task_messages_thread ON task_messages(thread_id)`)
	_, _ = s.db.Exec(`ALTER TABLE task_threads ADD COLUMN parent_thread_id TEXT NULL`) // best-effort for existing tables
	_, _ = s.db.Exec(`ALTER TABLE task_threads ADD COLUMN checkpoint_message_id TEXT NULL`) // best-effort
	_, _ = s.db.Exec(`ALTER TABLE task_threads ADD COLUMN checkpoint_message_time TEXT NULL`) // best-effort
	_, _ = s.db.Exec(`ALTER TABLE task_threads ADD COLUMN checkpoint_commit_hash TEXT NULL`) // best-effort
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN reasoning_effort TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE projects ADD COLUMN origin_branch TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE projects ADD COLUMN sandbox_enabled INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN base_ref TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN branch_tip TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN change_summary TEXT DEFAULT NULL`)
	// File checkpoint tracking
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN git_commit_hash TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN attachments TEXT DEFAULT NULL`)
	// Cumulative token stats table (survives task deletion)
	_, _ = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS project_token_stats (
			project_id       TEXT NOT NULL,
			model_name       TEXT NOT NULL DEFAULT 'unknown',
			provider_name    TEXT NOT NULL DEFAULT '',
			prompt_tokens    INTEGER NOT NULL DEFAULT 0,
			cache_tokens     INTEGER NOT NULL DEFAULT 0,
			predicted_tokens INTEGER NOT NULL DEFAULT 0,
			prompt_ms        INTEGER NOT NULL DEFAULT 0,
			predicted_ms     INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (project_id, model_name, provider_name),
			FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
		)
	`)
	// Cumulative task tracking table (survives task deletion)
	_, _ = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS project_token_tasks (
			project_id    TEXT NOT NULL,
			model_name    TEXT NOT NULL DEFAULT 'unknown',
			provider_name TEXT NOT NULL DEFAULT '',
			task_id       TEXT NOT NULL,
			PRIMARY KEY (project_id, model_name, provider_name, task_id),
			FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
		)
	`)
	// Backfill existing data from task_messages (idempotent)
	var migrationDone int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM config WHERE key = 'migration_cumulative_stats'`).Scan(&migrationDone)
	if migrationDone == 0 {
		_, _ = s.db.Exec(`
			INSERT INTO project_token_stats (project_id, model_name, provider_name, prompt_tokens, cache_tokens, predicted_tokens, prompt_ms, predicted_ms)
			SELECT
				t.project_id,
				COALESCE(tm.model_name, 'unknown'),
				COALESCE(tm.provider_name, ''),
				SUM(tm.prompt_tokens),
				SUM(COALESCE(tm.cache_tokens, 0)),
				SUM(tm.predicted_tokens),
				SUM(tm.prompt_ms),
				SUM(tm.predicted_ms)
			FROM task_messages tm
			JOIN tasks t ON t.id = tm.task_id
			WHERE tm.role IN ('assistant', 'title')
			GROUP BY t.project_id, tm.model_name, tm.provider_name
		`)
		_, _ = s.db.Exec(`
			INSERT INTO project_token_tasks (project_id, model_name, provider_name, task_id)
			SELECT DISTINCT
				t.project_id,
				COALESCE(tm.model_name, 'unknown'),
				COALESCE(tm.provider_name, ''),
				tm.task_id
			FROM task_messages tm
			JOIN tasks t ON t.id = tm.task_id
			WHERE tm.role IN ('assistant', 'title')
		`)
		_, _ = s.db.Exec(`INSERT OR IGNORE INTO config (key, value) VALUES ('migration_cumulative_stats', '1')`)
	}
	return nil
}

func (s *Store) initConfigDefaults() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defaults := map[string]string{
		"workspace_dir":    "./.worktrees",
		"providers":        "",
		"default_provider": "",
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

	var providers []types.Provider
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

// scanner is the common interface for sql.Row and sql.Rows.
type scanner interface {
	Scan(dest ...interface{}) error
}

// b2i converts bool to int (0 or 1).
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// i2b converts int (0 or 1) to bool.
func i2b(i int) bool {
	return i != 0
}

// ptrStr returns a pointer to a string, or nil if empty.
func ptrStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ptrTime formats a time pointer as RFC3339 string, or nil if zero.
func ptrTime(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}

// scanTime scans a TEXT column (RFC3339) into a time.Time.
func scanTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// scanTask scans a row into a Task, handling bool/time conversions.
func scanTask(s scanner, t *types.Task) error {
	var autoCommit, autoPR, planMode, titleGenerating int
	var createdAt, updatedAt string
	var parentTaskID, currentThreadID, reasoningEffort, baseRef, branchTip, changeSummary sql.NullString
	err := s.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Prompt, &t.Status, &t.ColumnOrder,
		&t.SessionID, &t.Branch, &baseRef, &branchTip, &t.Output, &t.StatusState,
		&t.LlamaServerURL, &t.ModelName, &reasoningEffort, &t.MaxConsecutiveFailedTurns,
		&autoCommit, &autoPR, &planMode, &parentTaskID, &titleGenerating, &currentThreadID, &changeSummary, &createdAt, &updatedAt)
	if err != nil {
		return err
	}
	t.AutoCommit = i2b(autoCommit)
	t.AutoPR = i2b(autoPR)
	t.PlanMode = i2b(planMode)
	t.ParentTaskID = parentTaskID.String
	t.TitleGenerating = i2b(titleGenerating)
	t.CurrentThreadID = currentThreadID.String
	t.ReasoningEffort = reasoningEffort.String
	t.BaseRef = baseRef.String
	t.BranchTip = branchTip.String
	t.ChangeSummary = changeSummary.String
	t.CreatedAt = scanTime(createdAt)
	t.UpdatedAt = scanTime(updatedAt)
	return nil
}

// scanTaskThread scans a row into a TaskThread.
func scanTaskThread(s scanner, th *types.TaskThread) error {
	var parentThreadID, gitBranch, checkpointMsgID, checkpointMsgTime, checkpointCommitHash sql.NullString
	var createdAt string
	err := s.Scan(&th.ID, &th.TaskID, &parentThreadID, &th.Name, &th.IsActive, &gitBranch, &checkpointMsgID, &checkpointMsgTime, &checkpointCommitHash, &createdAt)
	if err != nil {
		return err
	}
	th.ParentThreadID = parentThreadID.String
	th.GitBranch = gitBranch.String
	th.CheckpointMessageID = checkpointMsgID.String
	if checkpointMsgTime.Valid {
		t := scanTime(checkpointMsgTime.String)
		th.CheckpointMessageTime = &t
	}
	th.CheckpointCommitHash = checkpointCommitHash.String
	th.CreatedAt = scanTime(createdAt)
	return nil
}

// scanTaskDependency scans a row into a TaskDependency, handling bool/time conversions.
func scanTaskDependency(s scanner, d *types.TaskDependency) error {
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
func scanReviewComment(s scanner, c *types.ReviewComment) error {
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
