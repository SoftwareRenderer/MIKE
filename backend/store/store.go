package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
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
	// The database holds session credentials and encrypted provider secrets, so
	// neither the directory nor the file may be world-readable. MkdirAll applies the
	// mode only to directories it creates and sql.Open would create the file under
	// the umask (0644), so both modes are set explicitly — also for pre-existing
	// 0755 ones.
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	if err := os.Chmod(dbDir, 0o700); err != nil {
		return nil, fmt.Errorf("chmod data dir: %w", err)
	}
	// Create the file before handing the path to the driver.
	f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create db file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close db file: %w", err)
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		return nil, fmt.Errorf("chmod db file: %w", err)
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
	// The WAL sidecars (-wal/-shm) are written by the driver under the umask and can
	// hold the same rows as the main file until checkpointed.
	restrictSidecarModes(dbPath)
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
	if err := s.initSessions(); err != nil {
		return nil, fmt.Errorf("init sessions: %w", err)
	}
	if err := s.initConfigDefaults(); err != nil {
		return nil, fmt.Errorf("init config defaults: %w", err)
	}
	// Schema creation above is the first write — the WAL sidecar exists by now.
	restrictSidecarModes(dbPath)
	return s, nil
}

// restrictSidecarModes tightens the WAL sidecar files (-wal/-shm) to 0600.
// Best-effort: they may not exist yet, and a file this process does not own is no
// reason to refuse to start.
func restrictSidecarModes(dbPath string) {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Chmod(dbPath+suffix, 0o600); err != nil && !os.IsNotExist(err) {
			log.Printf("[store] could not restrict permissions of %s: %v", dbPath+suffix, err)
		}
	}
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Exec runs a raw SQL statement with arguments.
func (s *Store) Exec(query string, args ...any) (sql.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Exec(query, args...)
}

// Query runs a raw SQL query that returns rows.
func (s *Store) Query(query string, args ...any) (*sql.Rows, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db.Query(query, args...)
}

// QueryRow runs a raw SQL query that returns a single row.
func (s *Store) QueryRow(query string, args ...any) *sql.Row {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db.QueryRow(query, args...)
}

func (s *Store) initSchema() error {
	schema := `
CREATE TABLE IF NOT EXISTS projects (
    id                       TEXT PRIMARY KEY,
    name                     TEXT NOT NULL,
    path                     TEXT NOT NULL UNIQUE,
    origin_branch            TEXT DEFAULT NULL,
    sandbox_enabled          INTEGER NOT NULL DEFAULT 0,
    sandbox_network_isolation INTEGER NOT NULL DEFAULT 0,
    sandbox_image            TEXT DEFAULT NULL,
    sandbox_dockerfile_path  TEXT DEFAULT NULL,
    sandbox_memory_limit     INTEGER NOT NULL DEFAULT 1024,
    created_at               TEXT NOT NULL,
    updated_at               TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
    id                           TEXT PRIMARY KEY,
    project_id                   TEXT NOT NULL,
    title                        TEXT NOT NULL,
    prompt                       TEXT NOT NULL,
    status                       TEXT NOT NULL DEFAULT 'backlog',
    session_id                   TEXT,
    branch                       TEXT,
    base_ref                     TEXT DEFAULT NULL,
    base_commit                  TEXT DEFAULT NULL,
    branch_tip                   TEXT DEFAULT NULL,
    output                       TEXT,
    execution_state              TEXT NOT NULL DEFAULT 'idle',
    has_merge_conflict           INTEGER NOT NULL DEFAULT 0,
    llama_server_url             TEXT NOT NULL DEFAULT '',
    model_name                   TEXT NOT NULL DEFAULT '',
    max_consecutive_failed_turns INTEGER NOT NULL DEFAULT 3,
    auto_commit                  INTEGER NOT NULL DEFAULT 0,
    auto_merge                   INTEGER NOT NULL DEFAULT 1,
    subtasks_auto_merge          INTEGER NOT NULL DEFAULT 1,
    operating_mode               TEXT NOT NULL DEFAULT 'act',
    parent_task_id               TEXT NULL,
    autostarted                  INTEGER NOT NULL DEFAULT 0,
    created_at                   TEXT NOT NULL,
    updated_at                   TEXT NOT NULL,
    moved_at                     TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_task_id) REFERENCES tasks(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
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
    severity           TEXT DEFAULT NULL,
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
    is_loop_hint       INTEGER NOT NULL DEFAULT 0,
    assistant_msg_id   TEXT DEFAULT NULL,
    kind               TEXT DEFAULT NULL,
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
    cost             REAL NOT NULL DEFAULT 0,
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

-- Immutable diff snapshot for done tasks (BaseCommit..BranchTip — pinned SHAs).
-- Lazily materialized on first read; valid until the task's base/tip refs change.
CREATE TABLE IF NOT EXISTS task_diff_snapshots (
    task_id     TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL,
    base_commit TEXT NOT NULL,
    branch_tip  TEXT NOT NULL,
    stats_json  TEXT NOT NULL,
    diff_json   TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_tasks_project ON tasks(project_id);
CREATE INDEX IF NOT EXISTS idx_tasks_project_status ON tasks(project_id, status);
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
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN merge_on_commit INTEGER NOT NULL DEFAULT 1`)
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN merge_children INTEGER NOT NULL DEFAULT 1`)
	_, _ = s.db.Exec(`ALTER TABLE tasks RENAME COLUMN merge_on_commit TO auto_merge`)
	_, _ = s.db.Exec(`ALTER TABLE tasks RENAME COLUMN merge_children TO subtasks_auto_merge`)
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
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN is_loop_hint INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN provider_name TEXT DEFAULT NULL`)
	// Thread support migrations
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN current_thread_id TEXT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN thread_id TEXT NULL`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_task_threads_task ON task_threads(task_id)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_task_messages_thread ON task_messages(thread_id)`)
	_, _ = s.db.Exec(`ALTER TABLE task_threads ADD COLUMN parent_thread_id TEXT NULL`)        // best-effort for existing tables
	_, _ = s.db.Exec(`ALTER TABLE task_threads ADD COLUMN checkpoint_message_id TEXT NULL`)   // best-effort
	_, _ = s.db.Exec(`ALTER TABLE task_threads ADD COLUMN checkpoint_message_time TEXT NULL`) // best-effort
	_, _ = s.db.Exec(`ALTER TABLE task_threads ADD COLUMN checkpoint_commit_hash TEXT NULL`)  // best-effort
	_, _ = s.db.Exec(`ALTER TABLE task_threads ADD COLUMN worktree_stash_hash TEXT NULL`)     // best-effort
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN reasoning_effort TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE projects ADD COLUMN origin_branch TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE projects ADD COLUMN sandbox_enabled INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE projects ADD COLUMN sandbox_network_isolation INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE projects ADD COLUMN sandbox_image TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE projects ADD COLUMN sandbox_dockerfile_path TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE projects ADD COLUMN sandbox_memory_limit INTEGER NOT NULL DEFAULT 1024`)
	_, _ = s.db.Exec(`ALTER TABLE projects ADD COLUMN sandbox_forward_ports TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE projects ADD COLUMN sandbox_mounts TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE projects ADD COLUMN sandbox_envs TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE projects ADD COLUMN sandbox_capabilities TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN base_ref TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN base_commit TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN branch_tip TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE tasks RENAME COLUMN status_state TO execution_state`)
	// Runtime port mappings (ephemeral, cleared when task ends)
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN assigned_ports TEXT DEFAULT NULL`)
	// File checkpoint tracking
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN git_commit_hash TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN attachments TEXT DEFAULT NULL`)
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN severity TEXT DEFAULT NULL`)
	// The assistant message a tool_call row belongs to. Rebuilding the conversation used
	// to rely on tool_call rows being contiguous with their assistant row, which any
	// message written during tool execution (LSP diagnostics, merge-conflict notes)
	// silently broke — dropping the rest of the turn's tool calls and results.
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN assistant_msg_id TEXT DEFAULT NULL`)
	// Sub-classification of a row within its role — advisory rows carry "diagnostics" or
	// "merge_conflict" so the transcript can title the note after a reload, not just live.
	_, _ = s.db.Exec(`ALTER TABLE task_messages ADD COLUMN kind TEXT DEFAULT NULL`)
	// Add cost column to cumulative token stats (idempotent — ALTER TABLE IF NOT EXISTS is not standard SQLite)
	_, _ = s.db.Exec(`ALTER TABLE project_token_stats ADD COLUMN cost REAL NOT NULL DEFAULT 0`)
	// Separate merge conflict flag so execution_state can stay "running"
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN has_merge_conflict INTEGER NOT NULL DEFAULT 0`)
	// Operating mode: act (default), plan, or explore
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN operating_mode TEXT NOT NULL DEFAULT 'act'`)
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN summarizing INTEGER NOT NULL DEFAULT 0`)
	// Accumulated API cost (USD) for the last task run
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN session_cost REAL NOT NULL DEFAULT 0`)
	// Records whether the task execution was auto-started from a parent (sibling continuation)
	_, _ = s.db.Exec(`ALTER TABLE tasks ADD COLUMN autostarted INTEGER NOT NULL DEFAULT 0`)
	// moved_at orders the board: it changes only when a task moves column (see UpdateTask).
	// Backfilled from created_at on the first startup after the upgrade.
	if _, err := s.db.Exec(`ALTER TABLE tasks ADD COLUMN moved_at TEXT NOT NULL DEFAULT ''`); err == nil {
		_, _ = s.db.Exec(`UPDATE tasks SET moved_at = created_at WHERE moved_at = ''`)
	}
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
			cost             REAL NOT NULL DEFAULT 0,
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
		"providers":        "",
		"worker_provider":  "",
		"worker_model":     "",
		"utility_provider": "",
		"utility_model":    "",
	}

	for k, v := range defaults {
		_, err := s.db.Exec(`INSERT OR IGNORE INTO config (key, value) VALUES (?, ?)`, k, v)
		if err != nil {
			return fmt.Errorf("init config %s: %w", k, err)
		}
	}
	return nil
}

// scanner is the common interface for sql.Row and sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// B2I converts a bool to an integer (0 or 1) for storage.
func B2I(b bool) int {
	if b {
		return 1
	}
	return 0
}

// I2B converts an integer (0 or 1) from storage to a bool.
func I2B(i int) bool {
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
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s) // fallback for legacy data
		if err != nil {
			return time.Time{}
		}
	}
	return t
}

// scanTask scans a row into a Task, handling bool/time conversions.
func scanTask(s scanner, t *types.Task) error {
	var autoCommit, autoMerge, subtasksAutoMerge, titleGenerating, summarizing, hasMergeConflict, autostarted int
	var createdAt, updatedAt, movedAt string
	var parentTaskID, currentThreadID, reasoningEffort, baseRef, baseCommit, branchTip, assignedPortsJSON sql.NullString
	err := s.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Prompt, &t.Status,
		&t.SessionID, &t.Branch, &baseRef, &baseCommit, &branchTip, &t.Output, &t.ExecutionState, &hasMergeConflict,
		&t.LlamaServerURL, &t.ModelName, &reasoningEffort, &t.MaxConsecutiveFailedTurns,
		&autoCommit, &autoMerge, &subtasksAutoMerge, &t.OperatingMode, &parentTaskID, &autostarted, &titleGenerating, &summarizing, &currentThreadID, &createdAt, &updatedAt, &movedAt, &assignedPortsJSON, &t.SessionCost)
	if err != nil {
		return err
	}
	t.AutoCommit = I2B(autoCommit)
	t.AutoMerge = I2B(autoMerge)
	t.SubtasksAutoMerge = I2B(subtasksAutoMerge)
	t.HasMergeConflict = I2B(hasMergeConflict)
	t.ParentTaskID = parentTaskID.String
	t.Autostarted = I2B(autostarted)
	t.TitleGenerating = I2B(titleGenerating)
	t.Summarizing = I2B(summarizing)
	t.CurrentThreadID = currentThreadID.String
	t.ReasoningEffort = reasoningEffort.String
	t.BaseRef = baseRef.String
	t.BaseCommit = baseCommit.String
	t.BranchTip = branchTip.String
	t.CreatedAt = scanTime(createdAt)
	t.UpdatedAt = scanTime(updatedAt)
	// Rows predating the migration have an empty moved_at; created_at keeps their order stable.
	if movedAt == "" {
		movedAt = createdAt
	}
	t.MovedAt = scanTime(movedAt)
	if assignedPortsJSON.Valid && assignedPortsJSON.String != "" {
		_ = json.Unmarshal([]byte(assignedPortsJSON.String), &t.AssignedPorts)
	}
	return nil
}

// scanTaskThread scans a row into a TaskThread.
func scanTaskThread(s scanner, th *types.TaskThread) error {
	var parentThreadID, gitBranch, checkpointMsgID, checkpointMsgTime, checkpointCommitHash, worktreeStashHash sql.NullString
	var createdAt string
	err := s.Scan(&th.ID, &th.TaskID, &parentThreadID, &th.Name, &th.IsActive, &gitBranch, &checkpointMsgID, &checkpointMsgTime, &checkpointCommitHash, &worktreeStashHash, &createdAt)
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
	th.WorktreeStashHash = worktreeStashHash.String
	th.CreatedAt = scanTime(createdAt)
	return nil
}
