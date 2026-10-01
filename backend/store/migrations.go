package store

import (
	"database/sql"
	"fmt"
	"time"

	"mike/pkg/logging"
)

// SQLite has no "ALTER TABLE … ADD COLUMN IF NOT EXISTS", so startup used to fire ~50
// unconditional ALTERs and discard their errors - 34 of which can do nothing except fail
// with "duplicate column name". Asking first makes a logged failure a real failure.
//
// The cost is a probe per column, 58 per start, each one a no-op on a current database.
// schemaVersion is what lets a database that has been through the chain skip it.

// schemaVersion is the value PRAGMA user_version carries once initSchema has brought a database up
// to date, and the number a new migration bumps.
//
// To add a migration: extend initSchema's probe chain and raise this constant, leaving the earlier
// steps in place. A database stamped N has been through 1..N; an older one reads 0 and runs the
// whole chain. So the steps must stay idempotent, and one that must run exactly once needs its own
// marker (see the config-row guard on the backfill) rather than trusting this number.
const schemaVersion = doneMCPServersStamp

// doneDiffParsedStamp is the stamp from which a stored done-task diff body carries parsed_files.
// Older bodies are served byte for byte and cannot grow the key, so initSchema drops those rows.
const doneDiffParsedStamp = 3

// doneMemSupervisorStamp is the stamp from which tasks carry memory_supervisor_paused.
const doneMemSupervisorStamp = 4

// doneOOMKillStamp is the stamp from which tasks carry the OOM-kill count of their last sandbox run.
const doneOOMKillStamp = doneMemSupervisorStamp + 1

// doneMemoryAlertAckStamp is the stamp from which a task carries its dismissed memory alert.
const doneMemoryAlertAckStamp = doneOOMKillStamp + 1

// doneLegacyPeakClearedStamp is the schema version from which tasks.sandbox_memory_peak_mb stores
// held memory, as sandboxruntime.CgroupMemory.HeldBytes defines it.
const doneLegacyPeakClearedStamp = doneMemoryAlertAckStamp + 1

// doneMessageTimeKeysStamp is the version from which a stored instant that SQL compares as text is
// fixed-width UTC (see messageTimeLayout and timeTextColumns). Rows written before it are rewritten once
// by normalizeTimeText: a table holding both shapes cannot be ordered by its own text.
const doneMessageTimeKeysStamp = doneLegacyPeakClearedStamp + 1

// doneMessageOrderIndexesStamp is the version from which a thread's or a task's messages come out of a
// composite (scope, created_at) index rather than a temp B-tree over the whole thread.
const doneMessageOrderIndexesStamp = doneMessageTimeKeysStamp + 1

// doneUserTurnIndexesStamp is the version from which a conversation's user rows carry an index of their
// own, so naming the prompt above a position costs the prompts rather than every row between them.
const doneUserTurnIndexesStamp = doneMessageOrderIndexesStamp + 1

// doneProjectCommitTrailerStamp is the version from which a project carries its answer to the
// "Assisted-by" commit trailer.
const doneProjectCommitTrailerStamp = doneUserTurnIndexesStamp + 1

// doneMCPServersStamp is the version from which a project carries its MCP opt-in list.
const doneMCPServersStamp = doneProjectCommitTrailerStamp + 1

// userVersion reads PRAGMA user_version. A read failure answers 0, which reruns the probe chain:
// slow, but it cannot leave a column missing. The caller holds s.mu.
func (s *Store) userVersion() int {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		logging.For(logging.Store).Warnf("reading user_version failed, re-running schema probes: %v", err)
		return 0
	}
	return v
}

// setUserVersion stamps the schema version. A failed stamp only costs a rerun of the chain on the
// next start, so it is logged rather than fatal; refusing to open the database would be worse.
func (s *Store) setUserVersion(v int) {
	if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v)); err != nil {
		logging.For(logging.Store).Warnf("stamping schema version %d failed: %v", v, err)
	}
}

// hasColumn reports whether an existing table carries the named column. A missing table
// reads as "no such column", which is honest: the ALTER would have failed on it anyway.
func (s *Store) hasColumn(table, column string) bool {
	var n int
	// pragma_table_info is a table-valued function, so the table name binds as a
	// parameter instead of being formatted into SQL.
	if err := s.db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n); err != nil {
		logging.For(logging.Store).Warnf("schema probe %s.%s failed: %v", table, column, err)
		return false
	}
	return n > 0
}

// addColumn adds column with the given definition (everything after
// "ADD COLUMN <name>") unless the table already has it.
func (s *Store) addColumn(table, column, def string) {
	if s.hasColumn(table, column) {
		return
	}
	if _, err := s.db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, def)); err != nil {
		logging.For(logging.Store).Warnf("schema migration %s.%s failed: %v", table, column, err)
	}
}

// renameColumn renames from to to, and only while the database still uses the old name.
// If both names exist the data is ambiguous and a rename would lose a column, so it
// refuses and leaves it for a human.
func (s *Store) renameColumn(table, from, to string) {
	if !s.hasColumn(table, from) || s.hasColumn(table, to) {
		return
	}
	if _, err := s.db.Exec(fmt.Sprintf(`ALTER TABLE %s RENAME COLUMN %s TO %s`, table, from, to)); err != nil {
		logging.For(logging.Store).Warnf("schema rename %s.%s -> %s failed: %v", table, from, to, err)
	}
}

// The schema arrives in three layers, each repeatable for its own reason: createBaseTables uses
// CREATE TABLE IF NOT EXISTS, addMissingColumns probes each column before its ALTER, and a backfill
// checks whether it has already run. Every layer must stay safe on a database that is already current.

// createBaseTables writes the shape every fresh database starts from. It runs on every start, which is safe
// only because every statement is IF NOT EXISTS: an existing database keeps what it has, and what it lacks
// is added by addMissingColumns.
// The caller holds s.mu; initSchema takes it once for the whole chain.
func (s *Store) createBaseTables() error {
	_, err := s.db.Exec(baseSchemaSQL)
	return err
}

// baseSchemaSQL is the DDL, unchanged from the statement this function used to hold inline.
const baseSchemaSQL = `
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
    commit_assisted_by       INTEGER NOT NULL DEFAULT 1,
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
CREATE INDEX IF NOT EXISTS idx_task_messages_thread_time ON task_messages(thread_id, created_at);
CREATE INDEX IF NOT EXISTS idx_task_messages_task_time ON task_messages(task_id, created_at);
-- The prompts of a conversation, indexed apart from the answers under them: rowid is still the last key
-- column of a partial index, so the order a window read uses survives the role filter.
CREATE INDEX IF NOT EXISTS idx_task_messages_user_thread ON task_messages(thread_id, created_at) WHERE role = 'user';
CREATE INDEX IF NOT EXISTS idx_task_messages_user_task ON task_messages(task_id, created_at) WHERE role = 'user';
CREATE INDEX IF NOT EXISTS idx_task_threads_task ON task_threads(task_id);
`

// addMissingColumns is the probe chain: one column per statement, each probed by hasColumn first. A column
// that arrived under an earlier name is added under that name and then renamed, because a database older
// than both names needs both halves.
//
// plan_mode is absent on purpose: plan mode is task.OperatingMode == "plan", and the column was neither
// written nor read. Databases that have it keep it.
func (s *Store) addMissingColumns() {
	if !s.hasColumn("tasks", "auto_merge") && !s.hasColumn("tasks", "merge_on_commit") {
		s.addColumn("tasks", "merge_on_commit", "INTEGER NOT NULL DEFAULT 1")
	}
	s.renameColumn("tasks", "merge_on_commit", "auto_merge")
	if !s.hasColumn("tasks", "subtasks_auto_merge") && !s.hasColumn("tasks", "merge_children") {
		s.addColumn("tasks", "merge_children", "INTEGER NOT NULL DEFAULT 1")
	}
	s.renameColumn("tasks", "merge_children", "subtasks_auto_merge")
	s.addColumn("task_messages", "model_name", "TEXT DEFAULT NULL")
	s.addColumn("task_messages", "prompt_tokens", "INTEGER DEFAULT 0")
	s.addColumn("task_messages", "predicted_tokens", "INTEGER DEFAULT 0")
	s.addColumn("task_messages", "prompt_ms", "INTEGER DEFAULT 0")
	s.addColumn("task_messages", "predicted_ms", "INTEGER DEFAULT 0")
	s.addColumn("task_messages", "duration", "INTEGER DEFAULT 0")
	s.addColumn("task_messages", "tool_calls", "TEXT DEFAULT NULL")
	s.addColumn("task_messages", "tool_result", "TEXT DEFAULT NULL")
	s.addColumn("task_messages", "reasoning_content", "TEXT DEFAULT NULL")
	s.addColumn("tasks", "parent_task_id", "TEXT NULL")
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_parent ON tasks(parent_task_id)`)
	s.addColumn("tasks", "title_generating", "INTEGER NOT NULL DEFAULT 0")
	s.addColumn("task_messages", "cache_tokens", "INTEGER DEFAULT 0")
	s.addColumn("task_messages", "is_loop_hint", "INTEGER NOT NULL DEFAULT 0")
	s.addColumn("task_messages", "provider_name", "TEXT DEFAULT NULL")
	// Thread support
	s.addColumn("tasks", "current_thread_id", "TEXT NULL")
	s.addColumn("task_messages", "thread_id", "TEXT NULL")
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_task_threads_task ON task_threads(task_id)`)
	s.addColumn("task_threads", "parent_thread_id", "TEXT NULL")
	s.addColumn("task_threads", "checkpoint_message_id", "TEXT NULL")
	s.addColumn("task_threads", "checkpoint_message_time", "TEXT NULL")
	s.addColumn("task_threads", "checkpoint_commit_hash", "TEXT NULL")
	s.addColumn("task_threads", "worktree_stash_hash", "TEXT NULL")
	s.addColumn("tasks", "reasoning_effort", "TEXT DEFAULT NULL")
	s.addColumn("projects", "origin_branch", "TEXT DEFAULT NULL")
	s.addColumn("projects", "sandbox_enabled", "INTEGER NOT NULL DEFAULT 0")
	s.addColumn("projects", "sandbox_network_isolation", "INTEGER NOT NULL DEFAULT 0")
	s.addColumn("projects", "sandbox_image", "TEXT DEFAULT NULL")
	s.addColumn("projects", "sandbox_dockerfile_path", "TEXT DEFAULT NULL")
	s.addColumn("projects", "sandbox_memory_limit", "INTEGER NOT NULL DEFAULT 1024")
	s.addColumn("projects", "sandbox_forward_ports", "TEXT DEFAULT NULL")
	s.addColumn("projects", "sandbox_mounts", "TEXT DEFAULT NULL")
	s.addColumn("projects", "sandbox_envs", "TEXT DEFAULT NULL")
	s.addColumn("projects", "sandbox_capabilities", "TEXT DEFAULT NULL")
	s.addColumn("projects", "commit_assisted_by", "INTEGER NOT NULL DEFAULT 1")
	s.addColumn("tasks", "base_ref", "TEXT DEFAULT NULL")
	s.addColumn("tasks", "base_commit", "TEXT DEFAULT NULL")
	s.addColumn("tasks", "branch_tip", "TEXT DEFAULT NULL")
	s.renameColumn("tasks", "status_state", "execution_state")
	// Runtime port mappings (ephemeral, cleared when task ends)
	s.addColumn("tasks", "assigned_ports", "TEXT DEFAULT NULL")
	// File checkpoint tracking
	s.addColumn("task_messages", "git_commit_hash", "TEXT DEFAULT NULL")
	s.addColumn("task_messages", "attachments", "TEXT DEFAULT NULL")
	s.addColumn("task_messages", "severity", "TEXT DEFAULT NULL")
	// The assistant message a tool_call row belongs to. Rebuilding the conversation used
	// to rely on tool_call rows being contiguous with their assistant row, which any
	// message written during tool execution (LSP diagnostics, merge-conflict notes)
	// silently broke — dropping the rest of the turn's tool calls and results.
	s.addColumn("task_messages", "assistant_msg_id", "TEXT DEFAULT NULL")
	// Sub-classification of a row within its role — advisory rows carry "diagnostics" or
	// "merge_conflict" so the transcript can title the note after a reload, not just live.
	s.addColumn("task_messages", "kind", "TEXT DEFAULT NULL")
	// Separate merge conflict flag so execution_state can stay "running"
	s.addColumn("tasks", "has_merge_conflict", "INTEGER NOT NULL DEFAULT 0")
	// Operating mode: act (default), plan, or explore
	s.addColumn("tasks", "operating_mode", "TEXT NOT NULL DEFAULT 'act'")
	s.addColumn("tasks", "summarizing", "INTEGER NOT NULL DEFAULT 0")
	// Accumulated API cost (USD) for the last task run
	s.addColumn("tasks", "session_cost", "REAL NOT NULL DEFAULT 0")
	// Peak container memory (MB) of the last sandbox run; owned by RecordTaskSandboxMemory.
	s.addColumn("tasks", "sandbox_memory_peak_mb", "INTEGER NOT NULL DEFAULT 0")
	// Recorded with the peak: both die with the container.
	s.addColumn("tasks", "sandbox_oom_kills", "INTEGER NOT NULL DEFAULT 0")
	// The flag carries "nobody dismissed anything": {peak 0, 2 kills, no limit} is a real dismissal.
	s.addColumn("tasks", "memory_alert_acked", "INTEGER NOT NULL DEFAULT 0")
	s.addColumn("tasks", "memory_alert_ack_peak_mb", "INTEGER NOT NULL DEFAULT 0")
	s.addColumn("tasks", "memory_alert_ack_oom_kills", "INTEGER NOT NULL DEFAULT 0")
	s.addColumn("tasks", "memory_alert_ack_limit_mb", "INTEGER NOT NULL DEFAULT 0")
	// Records whether the task execution was auto-started from a parent (sibling continuation)
	s.addColumn("tasks", "autostarted", "INTEGER NOT NULL DEFAULT 0")
	// Set by the memory supervisor when it pauses a task; cleared by any user action.
	s.addColumn("tasks", "memory_supervisor_paused", "INTEGER NOT NULL DEFAULT 0")
	// These carry the order the conversation reads use, which a single-column index left to a temp
	// B-tree over every row. Each replaces the single-column index on its own leading column.
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_task_messages_thread_time ON task_messages(thread_id, created_at)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_task_messages_task_time ON task_messages(task_id, created_at)`)
	_, _ = s.db.Exec(`DROP INDEX IF EXISTS idx_task_messages_thread`)
	_, _ = s.db.Exec(`DROP INDEX IF EXISTS idx_task_messages_task`)
	// One index per conversation scope, partial on user rows: the read that names the prompt above a
	// position walks prompts only, and its cost stops depending on how long the turn below it is.
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_task_messages_user_thread ON task_messages(thread_id, created_at) WHERE role = 'user'`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_task_messages_user_task ON task_messages(task_id, created_at) WHERE role = 'user'`)
	// moved_at orders the board: it changes only when a task moves column (see UpdateTask).
	// The backfill belongs to the moment the column first appears, so this one probes.
	if !s.hasColumn("tasks", "moved_at") {
		s.addColumn("tasks", "moved_at", "TEXT NOT NULL DEFAULT ''")
		_, _ = s.db.Exec(`UPDATE tasks SET moved_at = created_at WHERE moved_at = ''`)
	}
	// A project's MCP opt-in list; NULL is the list of a row predating the column.
	s.addColumn("projects", "mcp_server_ids", "TEXT DEFAULT NULL")
}

// migrateTokenStats builds the cumulative token tables and backfills them once, guarded by its own config
// row rather than by schemaVersion: a re-run would double every sum.
func (s *Store) migrateTokenStats() {
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
	// A project_token_stats table predating cost has none; CREATE TABLE cannot alter it.
	s.addColumn("project_token_stats", "cost", "REAL NOT NULL DEFAULT 0")
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
}

// zeroLegacySandboxPeaks sets to zero the peaks stored under the old definition, which counted the
// host's page cache. Such a number can sit above the project's limit and no later run can bring it
// down, so it would alert forever. The dismissal the user acknowledged against it is cleared too,
// because it would otherwise cover every smaller alert. The step is guarded by its own config row
// rather than by schemaVersion, because running it a second time would erase honest numbers.
func (s *Store) zeroLegacySandboxPeaks() {
	var done int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM config WHERE key = 'migration_sandbox_peak_definition'`).Scan(&done)
	if done > 0 {
		return
	}
	if _, err := s.db.Exec(`UPDATE tasks SET sandbox_memory_peak_mb = 0,
		memory_alert_acked = 0, memory_alert_ack_peak_mb = 0, memory_alert_ack_oom_kills = 0,
		memory_alert_ack_limit_mb = 0`); err != nil {
		// No marker is written, so the next start tries again.
		logging.For(logging.Store).Warnf("zeroing pre-held-memory sandbox peaks failed: %v", err)
		return
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO config (key, value) VALUES ('migration_sandbox_peak_definition', '1')`); err != nil {
		logging.For(logging.Store).Warnf("stamping the sandbox peak migration failed: %v", err)
	}
}

// clearDoneDiffSnapshotsIfOlderThan drops the done-task diff snapshots a build cannot trust: a
// snapshot is a stored parse, so changing the parse grammar invalidates every one of them. They
// rebuild on demand.
func (s *Store) clearDoneDiffSnapshotsIfOlderThan(stored int) {
	if stored < doneDiffParsedStamp {
		if _, err := s.db.Exec(`DELETE FROM task_diff_snapshots`); err != nil {
			logging.For(logging.Store).Warnf("clearing done-task diff snapshots failed: %v", err)
		}
	}
}

// nonCanonicalMessageTimeText selects the task_messages rows whose created_at is not in
// messageTimeLayout. That layout is one fixed width, so any other shape is caught here; the Go pass
// behind this filter makes the decision row by row, and a current database reads nothing.
const nonCanonicalMessageTimeText = `
	  substr(created_at, 20, 1) <> '.'
	  OR length(created_at) <> 30
	  OR substr(created_at, 30, 1) <> 'Z'`

// nonCanonicalCheckpointTime selects the task_threads rows whose checkpoint_message_time was not written
// in UTC. The handler matches that column against a message's created_at rendered as text, so both sides
// have to be the same shape (see GetThreadsByCheckpointMessageTime).
const nonCanonicalCheckpointTime = `
	  checkpoint_message_time IS NOT NULL
	  AND (length(checkpoint_message_time) <> 20 OR substr(checkpoint_message_time, 20, 1) <> 'Z')`

// timeTextColumn is one stored instant whose text is compared as text: the rows this build did not
// write, and the shape to rewrite them into. Both are fixed width and UTC. The text before them was
// time.RFC3339Nano, which trims the zeros off the end of the fraction — 'Z' (0x5A) sorts above every
// digit, so an earlier instant rendered shorter sorted *after* a later one rendered longer — and it kept
// the writer's zone, where a fall-back repeats an hour of wall-clock time.
type timeTextColumn struct {
	name        string
	selectStale string
	update      string
	layout      string
}

var timeTextColumns = []timeTextColumn{
	{
		name:        "task_messages.created_at",
		selectStale: `SELECT id, created_at FROM task_messages WHERE ` + nonCanonicalMessageTimeText,
		update:      `UPDATE task_messages SET created_at = ? WHERE id = ?`,
		layout:      messageTimeLayout,
	},
	{
		name:        "task_threads.checkpoint_message_time",
		selectStale: `SELECT id, checkpoint_message_time FROM task_threads WHERE ` + nonCanonicalCheckpointTime,
		update:      `UPDATE task_threads SET checkpoint_message_time = ? WHERE id = ?`,
		layout:      time.RFC3339,
	},
}

// normalizeTimeText rewrites every stored instant that SQL compares as text into the one shape this
// build writes, so no table holds two orders for the same pair of instants.
//
// A row whose text this build cannot read is named and left as stored: a wrong order is survivable, a
// discarded timestamp is not. The caller holds s.mu, so this opens its own transaction rather than
// calling Store.tx.
func (s *Store) normalizeTimeText() {
	for _, col := range timeTextColumns {
		s.rewriteTimeText(col)
	}
}

func (s *Store) rewriteTimeText(col timeTextColumn) {
	rows, err := s.db.Query(col.selectStale)
	if err != nil {
		logging.For(logging.Store).Warnf("reading %s to normalize it failed: %v", col.name, err)
		return
	}
	stale, err := scanRows(rows, func(r *sql.Rows) (staleTimeText, error) {
		var row staleTimeText
		err := r.Scan(&row.id, &row.text)
		return row, err
	})
	if err != nil {
		logging.For(logging.Store).Warnf("reading %s to normalize it failed: %v", col.name, err)
		return
	}
	if len(stale) == 0 {
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		logging.For(logging.Store).Warnf("beginning the rewrite of %s failed: %v", col.name, err)
		return
	}
	rewritten := 0
	for _, row := range stale {
		at := scanTime(row.text)
		if at.IsZero() {
			logging.For(logging.Store).Warnf("%s of %s is text this build cannot read (%q); leaving it as stored",
				col.name, row.id, row.text)
			continue
		}
		if canonical := at.UTC().Format(col.layout); canonical != row.text {
			if _, err := tx.Exec(col.update, canonical, row.id); err != nil {
				logging.For(logging.Store).Warnf("rewriting the %s of %s failed: %v", col.name, row.id, err)
				_ = tx.Rollback()
				return
			}
			rewritten++
		}
	}
	if err := tx.Commit(); err != nil {
		logging.For(logging.Store).Warnf("committing the rewrite of %s failed: %v", col.name, err)
		return
	}
	if rewritten > 0 {
		logging.For(logging.Store).Infof("rewrote %d %s values into fixed-width UTC", rewritten, col.name)
	}
}

// staleTimeText is one row's id and the stored text it holds.
type staleTimeText struct {
	id, text string
}
