package store

// baseSchemaSQL is the schema of everything the task, project, message, thread and stats accessors
// read; the two credential tables are declared beside their own accessors under the same rule. Every
// column and index a scanner uses is declared here, and nothing repairs an older database: no release
// has shipped, so there is no older database to upgrade. A column a row struct carries is added here,
// and the schema tests fail if a scanner reads a column or an index this statement does not declare.
//
// Every statement is IF NOT EXISTS because this runs on every start. An existing database is left
// exactly as it is, so a statement that cannot be satisfied — a pre-existing table whose shape
// predates this DDL — is a startup failure naming itself rather than a silent repair.
//
// A table lists its columns in the order its scanner reads them (`taskColumns`, `projectColumns`,
// `taskMessageCols`, `threadColumns`, `taskDiffSnapshotColumns`): the scan is positional, so a
// reorder there is a reorder here.
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
    sandbox_forward_ports    TEXT DEFAULT NULL,
    sandbox_mounts           TEXT DEFAULT NULL,
    sandbox_envs             TEXT DEFAULT NULL,
    sandbox_capabilities     TEXT DEFAULT NULL,
    commit_assisted_by       INTEGER NOT NULL DEFAULT 1,
    mcp_server_ids           TEXT DEFAULT NULL,
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
    reasoning_effort             TEXT DEFAULT NULL,
    max_consecutive_failed_turns INTEGER NOT NULL DEFAULT 3,
    auto_commit                  INTEGER NOT NULL DEFAULT 0,
    auto_merge                   INTEGER NOT NULL DEFAULT 1,
    subtasks_auto_merge          INTEGER NOT NULL DEFAULT 1,
    operating_mode               TEXT NOT NULL DEFAULT 'act',
    parent_task_id               TEXT NULL,
    autostarted                  INTEGER NOT NULL DEFAULT 0,
    title_generating             INTEGER NOT NULL DEFAULT 0,
    summarizing                  INTEGER NOT NULL DEFAULT 0,
    -- The thread a turn runs on, owned by SetActiveThread; the thread table holds its checkpoints.
    current_thread_id            TEXT NULL,
    created_at                   TEXT NOT NULL,
    updated_at                   TEXT NOT NULL,
    -- moved_at orders the board: it changes only when a task moves column (see UpdateTask).
    moved_at                     TEXT NOT NULL DEFAULT '',
    -- Ephemeral runtime port mappings, cleared when the task ends.
    assigned_ports               TEXT DEFAULT NULL,
    session_cost                 REAL NOT NULL DEFAULT 0,
    -- The last sandbox run's high-water and its OOM kills; both die with the container and are
    -- written only by RecordTaskSandboxMemory.
    sandbox_memory_peak_mb       INTEGER NOT NULL DEFAULT 0,
    sandbox_oom_kills            INTEGER NOT NULL DEFAULT 0,
    -- Set by the memory supervisor when it pauses a task; cleared by any user action.
    memory_supervisor_paused     INTEGER NOT NULL DEFAULT 0,
    -- The flag carries "nobody dismissed anything": {peak 0, 2 kills, no limit} is a real dismissal.
    memory_alert_acked           INTEGER NOT NULL DEFAULT 0,
    memory_alert_ack_peak_mb     INTEGER NOT NULL DEFAULT 0,
    memory_alert_ack_oom_kills   INTEGER NOT NULL DEFAULT 0,
    memory_alert_ack_limit_mb    INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_task_id) REFERENCES tasks(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS task_threads (
    id                       TEXT PRIMARY KEY,
    task_id                  TEXT NOT NULL,
    parent_thread_id         TEXT NULL,
    name                     TEXT NOT NULL DEFAULT 'Main',
    is_active                INTEGER NOT NULL DEFAULT 0,
    git_branch               TEXT DEFAULT NULL,
    -- The fork's pin: the message, its instant, the commit its files were captured at, and the stash
    -- that capture needed (docs/threads-checkpoints.md).
    checkpoint_message_id    TEXT DEFAULT NULL,
    checkpoint_message_time  TEXT DEFAULT NULL,
    checkpoint_commit_hash   TEXT DEFAULT NULL,
    worktree_stash_hash      TEXT DEFAULT NULL,
    created_at               TEXT NOT NULL,
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
CREATE INDEX IF NOT EXISTS idx_tasks_parent ON tasks(parent_task_id);
CREATE INDEX IF NOT EXISTS idx_task_messages_thread_time ON task_messages(thread_id, created_at);
CREATE INDEX IF NOT EXISTS idx_task_messages_task_time ON task_messages(task_id, created_at);
-- The prompts of a conversation, indexed apart from the answers under them: rowid is still the last key
-- column of a partial index, so the order a window read uses survives the role filter.
CREATE INDEX IF NOT EXISTS idx_task_messages_user_thread ON task_messages(thread_id, created_at) WHERE role = 'user';
CREATE INDEX IF NOT EXISTS idx_task_messages_user_task ON task_messages(task_id, created_at) WHERE role = 'user';
CREATE INDEX IF NOT EXISTS idx_task_threads_task ON task_threads(task_id);
`
