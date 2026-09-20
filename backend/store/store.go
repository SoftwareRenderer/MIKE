// Package store is the persistence layer: one SQLite database, its schema and migrations,
// and the typed accessors for tasks, messages, threads, projects, sessions, stats, config.
// The pool is pinned to one connection so the PRAGMAs apply everywhere; s.mu is the
// serialisation point. Migrations probe before they ALTER (migrations.go).
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"mike/backend/types"

	"mike/pkg/logging"

	_ "modernc.org/sqlite"
)

// Store provides thread-safe access to the SQLite database.
type Store struct {
	db *sql.DB
	// mu is the serialisation point AGENTS.md names: the pool is one connection, so this is what makes
	// "no other caller can interleave" true, and it must be held for a whole multi-statement section, not
	// per statement. It is not a substitute for tx — see tx for what a transaction adds on top.
	mu sync.RWMutex
	// configGen counts config-table writes, so a consumer that caches a parsed config can tell
	// "nothing changed" from "something changed". Bumped under mu.
	configGen atomic.Uint64
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
	if err := s.initAPITokens(); err != nil {
		return nil, fmt.Errorf("init api tokens: %w", err)
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
			logging.For(logging.Store).Warnf("could not restrict permissions of %s: %v", dbPath+suffix, err)
		}
	}
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// RawSQL exposes the three database primitives and nothing else, for packages that own their own
// tables — today only backend/extensions — which keeps raw SQL to one grep-able caller.
type RawSQL interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// RawSQL returns the store as raw SQL access. It stays behind the store's lock on purpose:
// several accessors hold that lock across statements in place of a transaction, and handing
// out *sql.DB directly would let raw callers interleave with them.
func (s *Store) RawSQL() RawSQL { return s }

// DBStats returns the handle's counters as a value, so a caller cannot reach the handle.
// WaitCount/WaitDuration say whether callers had to queue for the single connection - the
// only wait here that a context could cut short.
func (s *Store) DBStats() sql.DBStats {
	return s.db.Stats()
}

// Exec runs a raw SQL statement. Outside the store it is reachable only through RawSQL.
func (s *Store) Exec(query string, args ...any) (sql.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Exec(query, args...)
}

// Query runs a raw SQL query that returns rows. See Exec.
func (s *Store) Query(query string, args ...any) (*sql.Rows, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db.Query(query, args...)
}

// QueryRow runs a raw SQL query that returns a single row. See Exec.
func (s *Store) QueryRow(query string, args ...any) *sql.Row {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db.QueryRow(query, args...)
}

func (s *Store) initSchema() error {
	// Held across the whole chain, as it was before the chain was split: schema work is the one thing that
	// must not interleave with anything else this store does.
	s.mu.Lock()
	defer s.mu.Unlock()

	// Three layers, each idempotent for a different reason, and the whole chain gated by user_version
	// rather than run once per process: a database already at this build's schema version does not need
	// 53 column probes, and one stamped newer than this build must not be edited by it.
	if err := s.createBaseTables(); err != nil {
		return err
	}
	stored := s.userVersion()
	if stored >= schemaVersion {
		if stored > schemaVersion {
			logging.For(logging.Store).Infof("database schema version %d is newer than this build's %d — leaving the schema alone", stored, schemaVersion)
		}
		return nil
	}

	s.addMissingColumns()
	s.migrateTokenStats()
	s.zeroLegacySandboxPeaks()
	s.normalizeTimeText()
	s.clearDoneDiffSnapshotsIfOlderThan(stored)
	s.setUserVersion(schemaVersion)
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

// ptrTime formats a time pointer as RFC3339 UTC string, or nil if zero. UTC because the stored text is
// compared as text: task_threads.checkpoint_message_time is matched against a message's created_at
// rendered by the caller, and that one is UTC too (see messageTimeLayout).
func ptrTime(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
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
	var autoCommit, autoMerge, subtasksAutoMerge, titleGenerating, summarizing, hasMergeConflict, autostarted, memSupPaused int
	var memAlertAcked int
	var ackPeakMB, ackOOMKills, ackLimitMB int64
	var createdAt, updatedAt, movedAt string
	var parentTaskID, currentThreadID, reasoningEffort, baseRef, baseCommit, branchTip, assignedPortsJSON sql.NullString
	err := s.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Prompt, &t.Status,
		&t.SessionID, &t.Branch, &baseRef, &baseCommit, &branchTip, &t.Output, &t.ExecutionState, &hasMergeConflict,
		&t.LlamaServerURL, &t.ModelName, &reasoningEffort, &t.MaxConsecutiveFailedTurns,
		&autoCommit, &autoMerge, &subtasksAutoMerge, &t.OperatingMode, &parentTaskID, &autostarted, &titleGenerating, &summarizing, &currentThreadID, &createdAt, &updatedAt, &movedAt, &assignedPortsJSON, &t.SessionCost, &t.SandboxMemoryPeakMB, &t.SandboxOOMKills, &memSupPaused,
		&memAlertAcked, &ackPeakMB, &ackOOMKills, &ackLimitMB)
	if err != nil {
		return err
	}
	t.MemoryAlertAck = memoryAlertAck(memAlertAcked, ackPeakMB, ackOOMKills, ackLimitMB)
	t.AutoCommit = I2B(autoCommit)
	t.AutoMerge = I2B(autoMerge)
	t.SubtasksAutoMerge = I2B(subtasksAutoMerge)
	t.HasMergeConflict = I2B(hasMergeConflict)
	t.ParentTaskID = parentTaskID.String
	t.Autostarted = I2B(autostarted)
	t.TitleGenerating = I2B(titleGenerating)
	t.Summarizing = I2B(summarizing)
	t.MemorySupervisorPaused = I2B(memSupPaused)
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

// tx runs a multi-statement write as one transaction: every statement lands, or none does. The pool
// is one connection and s.mu is held across the section, so this buys failure atomicity, not
// isolation - a reader outside the section never sees a half-applied write, and never sees an
// intermediate state either.
func (s *Store) tx(fn func(tx *sql.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		// The rollback is not allowed to replace the failure that caused it: an operator reading
		// "rollback: ...", wants to know what broke, not that the cleanup also had opinions.
		if rbErr := tx.Rollback(); rbErr != nil {
			logging.For(logging.Store).Errorf("rollback after %v failed: %v", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// execer is satisfied by both *sql.DB and *sql.Tx for the three entry points this package uses. A
// multi-statement section hands it to its helpers, so the same query text runs either on the transaction
// or — outside one — on the pool, instead of the two copies that drift apart.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

var (
	_ execer = (*sql.DB)(nil)
	_ execer = (*sql.Tx)(nil)
)
