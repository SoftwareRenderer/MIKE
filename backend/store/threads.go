package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"mike/backend/types"
)

// CreateThread inserts a new thread for a task, minting its ID the way CreateTask does.
func (s *Store) CreateThread(th *types.TaskThread) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if th.ID == "" {
		th.ID = s.newThreadID()
	}
	_, err := s.db.Exec(
		`INSERT INTO task_threads (`+threadColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		th.ID, th.TaskID, ptrStr(th.ParentThreadID), th.Name, B2I(th.IsActive), ptrStr(th.GitBranch), ptrStr(th.CheckpointMessageID), ptrTime(th.CheckpointMessageTime), ptrStr(th.CheckpointCommitHash), ptrStr(th.WorktreeStashHash), th.CreatedAt.Format(time.RFC3339),
	)
	return err
}

// threadColumns lists the task_threads columns in the exact order scanTaskThread
// reads them. Keep the two in step (see projectColumns for why that matters).
const threadColumns = `id, task_id, parent_thread_id, name, is_active, git_branch, checkpoint_message_id, checkpoint_message_time, checkpoint_commit_hash, worktree_stash_hash, created_at`

// queryThreads reads every thread matching a `WHERE <where>` clause. Callers must
// hold s.mu.
func (s *Store) queryThreads(where string, args ...any) ([]types.TaskThread, error) {
	rows, err := s.db.Query(`SELECT `+threadColumns+` FROM task_threads WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	return scanRows(rows, scanInto(scanTaskThread))
}

// GetThreadsByTask returns all threads for a task ordered by creation time.
func (s *Store) GetThreadsByTask(taskID string) ([]types.TaskThread, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryThreads(`task_id = ? ORDER BY created_at ASC`, taskID)
}

// NewThreadID mints a thread ID for a caller that must name a git branch by it before the row exists.
func (s *Store) NewThreadID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.newThreadID()
}

// newThreadID returns a thread ID no row holds. The caller holds s.mu, which keeps another writer
// from claiming the ID before its insert.
func (s *Store) newThreadID() string {
	return newID(types.GenerateThreadID, func(id string) bool {
		_, err := s.getThreadLocked(s.db, id)
		return err == nil
	})
}

// GetThread retrieves a single thread by ID.
func (s *Store) GetThread(threadID string) (*types.TaskThread, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getThreadLocked(s.db, threadID)
}

// getThreadLocked reads one thread assuming the caller already holds the lock.
// sync.RWMutex is not reentrant, so a method that holds it must call this instead.
func (s *Store) getThreadLocked(db execer, threadID string) (*types.TaskThread, error) {
	var th types.TaskThread
	err := scanTaskThread(db.QueryRow(`SELECT `+threadColumns+` FROM task_threads WHERE id = ?`, threadID), &th)
	if err == sql.ErrNoRows {
		return nil, ErrThreadNotFound
	}
	return &th, err
}

// DeleteThreadWithMessages removes a thread and its messages as one locked sequence, and
// returns the deleted row (nil, nil if there was nothing to delete).
// The row is read first and returned so the caller can clean up the git branch it names.
// Deleting an absent thread is not an error: the endpoint may be replayed.
func (s *Store) DeleteThreadWithMessages(threadID string) (*types.TaskThread, error) {
	var thread *types.TaskThread
	err := s.tx(func(tx *sql.Tx) error {
		var err error
		thread, err = s.deleteThreadWithMessagesLocked(tx, threadID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return thread, nil
}

func (s *Store) deleteThreadWithMessagesLocked(db execer, threadID string) (*types.TaskThread, error) {
	thread, err := s.getThreadLocked(db, threadID)
	if err == ErrThreadNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`DELETE FROM task_messages WHERE thread_id = ?`, threadID); err != nil {
		return nil, fmt.Errorf("delete thread messages: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM task_threads WHERE id = ?`, threadID); err != nil {
		return nil, fmt.Errorf("delete thread: %w", err)
	}
	return thread, nil
}

// SetActiveThread makes one thread the active one for its task. Exactly one row is active when this
// returns, and none of it lands when it fails — a task with no active thread has no board to show.
func (s *Store) SetActiveThread(threadID string) error {
	return s.tx(func(tx *sql.Tx) error {
		var taskID string
		if err := tx.QueryRow(`SELECT task_id FROM task_threads WHERE id = ?`, threadID).Scan(&taskID); err != nil {
			return fmt.Errorf("get thread task: %w", err)
		}
		if _, err := tx.Exec(`UPDATE task_threads SET is_active = 0 WHERE task_id = ?`, taskID); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE task_threads SET is_active = 1 WHERE id = ?`, threadID)
		return err
	})
}

// GetThreadsByCheckpointMessageID returns all threads whose checkpoint message matches the given ID.
func (s *Store) GetThreadsByCheckpointMessageID(messageID string) ([]types.TaskThread, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryThreads(`checkpoint_message_id = ?`, messageID)
}

// DeleteThread removes a thread from the database.
func (s *Store) DeleteThread(threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM task_threads WHERE id = ?`, threadID)
	return err
}

// GetAllThreadIDs returns every thread id across all tasks. A row that will not scan
// fails the call rather than being skipped: the branch GC reads a missing id as an
// orphan and deletes the branch.
func (s *Store) GetAllThreadIDs() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT id FROM task_threads`)
	if err != nil {
		return nil, err
	}
	return scanColumn[string](rows)
}

// UpdateThreadWorktreeStashHash saves the worktree stash hash for a thread.
func (s *Store) UpdateThreadWorktreeStashHash(threadID, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE task_threads SET worktree_stash_hash = ? WHERE id = ?`, hash, threadID)
	return err
}

// GetThreadsByCheckpointMessageTime returns all threads whose checkpoint message time matches the given time.
// This is used for cleanup when a copied checkpoint message (with a new ID but same created_at) is deleted.
func (s *Store) GetThreadsByCheckpointMessageTime(messageTime string) ([]types.TaskThread, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryThreads(`checkpoint_message_time = ?`, messageTime)
}

// CopiedCheckpoint is a checkpoint the fork inherited: MessageID is the fork's new row, CommitHash the
// snapshot it names. The caller re-pins that snapshot under SnapshotRef(taskID, MessageID) (see
// backend/git/snapshots.go), or it stays alive only as long as the parent thread's own rows do.
type CopiedCheckpoint struct {
	MessageID  string
	CommitHash string
}

// copyMsg holds the fields needed to copy a message.
type copyMsg struct {
	sourceID, role, content, createdAt    string
	severity, reasoningContent, toolCalls *string
	toolResult, modelName                 *string
	providerName                          *string
	attachments, kind                     *string
	assistantMsgID                        string
	isLoopHint                            bool
	promptTokens, predictedTokens         int
	cacheTokens                           int
	promptMs, predictedMs                 int
	duration                              int64
	gitCommitHash                         *string
}

// entryRow is a row of a thread's history, in display order — all the entry-boundary walk looks at.
type entryRow struct {
	id             string
	role           string
	createdAt      string
	assistantMsgID string
}

// entryCutoffLocked returns the created_at of the last row of the entry that holds upToMessageID —
// the boundary a fork copies up to. An assistant row and the tool calls it issued are separate
// rows that both readers show as one entry, so a fork cuts between entries, never through one.
//
// A tool_call row belongs to its assistant_msg_id owner, or, when that is empty (legacy rows), to
// the nearest preceding assistant row; every other role is its own entry. Callers hold s.mu and read
// through `db`: the store has one connection, so a second handle would wait for the transaction running
// this. See docs/storage.md.
func (s *Store) entryCutoffLocked(db execer, sourceThreadID, upToMessageID string) (string, error) {
	rows, err := db.Query(`
		SELECT id, role, created_at, COALESCE(assistant_msg_id, '')
		FROM task_messages
		WHERE thread_id = ?
		ORDER BY created_at ASC, rowid ASC
	`, sourceThreadID)
	if err != nil {
		return "", fmt.Errorf("list thread messages: %w", err)
	}

	threadRows, err := scanRows(rows, func(r *sql.Rows) (entryRow, error) {
		var row entryRow
		err := r.Scan(&row.id, &row.role, &row.createdAt, &row.assistantMsgID)
		return row, err
	})
	if err != nil {
		return "", fmt.Errorf("list thread messages: %w", err)
	}

	idx := -1
	for i, r := range threadRows {
		if r.id == upToMessageID {
			idx = i
			break
		}
	}
	if idx < 0 {
		// The row is not in this thread (deleted, or the wrong source thread was passed).
		// Fall back to its own timestamp rather than inventing a boundary.
		var own string
		if err := db.QueryRow(`SELECT created_at FROM task_messages WHERE id = ?`, upToMessageID).Scan(&own); err != nil {
			return "", fmt.Errorf("get cutoff message time: %w", err)
		}
		return own, nil
	}

	ownerIdx := idx
	switch threadRows[idx].role {
	case "assistant":
	case "tool_call":
		ownerIdx = -1
		if id := threadRows[idx].assistantMsgID; id != "" {
			for i, r := range threadRows {
				if r.id == id && r.role == "assistant" {
					ownerIdx = i
					break
				}
			}
		}
		if ownerIdx < 0 { // legacy row without an owner id: the nearest assistant above it
			for i := idx - 1; i >= 0; i-- {
				if threadRows[i].role == "assistant" {
					ownerIdx = i
					break
				}
			}
		}
		if ownerIdx < 0 {
			return threadRows[idx].createdAt, nil // an orphan tool_call row is its own entry
		}
	default:
		return threadRows[idx].createdAt, nil
	}

	ownerID := threadRows[ownerIdx].id
	boundary := threadRows[ownerIdx].createdAt
	for i := ownerIdx + 1; i < len(threadRows); i++ {
		r := threadRows[i]
		if r.role != "tool_call" {
			break
		}
		if r.assistantMsgID != "" && r.assistantMsgID != ownerID {
			break // a call of a different assistant starts a different entry
		}
		boundary = r.createdAt
	}
	// Rows this assistant owns anywhere in the thread still belong to the entry (a loop hint can land
	// between the assistant row and its tool calls).
	for _, r := range threadRows {
		if r.role == "tool_call" && r.assistantMsgID == ownerID && r.createdAt > boundary {
			boundary = r.createdAt
		}
	}
	// The entry can never be cut before the row the fork was asked for.
	if boundary < threadRows[idx].createdAt {
		boundary = threadRows[idx].createdAt
	}
	return boundary, nil
}

// CopyMessagesToThread copies the parent conversation into a fork: every row up to and including the
// entry that holds upToMessageID (see entryCutoffLocked), same payloads, new IDs. Owner ids are remapped to
// the fork's own copies, and it reports the checkpoints the copy inherited so the caller can pin them.
//
// Dropping a column changes what the copy is: a tool_call row without its payload renders as a blank card
// and reaches the model as an empty turn, a loop hint without its flag becomes real conversation input,
// and rows without a checkpoint hash leave the fork with no recorded file state.
func (s *Store) CopyMessagesToThread(sourceThreadID, targetThreadID, upToMessageID string) ([]CopiedCheckpoint, error) {
	var inherited []CopiedCheckpoint
	err := s.tx(func(tx *sql.Tx) error {
		var err error
		inherited, err = s.copyMessagesToThreadLocked(tx, sourceThreadID, targetThreadID, upToMessageID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return inherited, nil
}

// copyMessagesToThreadLocked runs the whole copy against one handle. A fork is read out of one snapshot and
// written into the same transaction: a copy that stopped half-way would be a conversation with rows missing
// and no error left to explain it, and the checkpoint list the caller pins would name rows that were never
// written.
func (s *Store) copyMessagesToThreadLocked(db execer, sourceThreadID, targetThreadID, upToMessageID string) ([]CopiedCheckpoint, error) {
	// Copy up to the end of the checkpoint's entry, not up to the checkpoint row itself.
	cutoffTime, err := s.entryCutoffLocked(db, sourceThreadID, upToMessageID)
	if err != nil {
		return nil, err
	}
	// Get the task_id of the source thread
	var taskID string
	err = db.QueryRow(`SELECT task_id FROM task_threads WHERE id = ?`, sourceThreadID).Scan(&taskID)
	if err != nil {
		return nil, fmt.Errorf("get source thread task: %w", err)
	}
	// Fetch all source messages into memory first (SQLite doesn't allow writes with open read cursors)
	sourceRows, err := db.Query(`
		SELECT id, role, severity, content, reasoning_content, tool_calls, tool_result, attachments, model_name, provider_name, prompt_tokens, predicted_tokens, cache_tokens, prompt_ms, predicted_ms, duration, git_commit_hash, is_loop_hint, assistant_msg_id, kind, created_at
		FROM task_messages
		WHERE thread_id = ? AND created_at <= ?
		ORDER BY created_at ASC, rowid ASC
	`, sourceThreadID, cutoffTime)
	if err != nil {
		return nil, fmt.Errorf("query source messages: %w", err)
	}

	var messages []copyMsg
	for sourceRows.Next() {
		var m copyMsg
		var rc, tc, tr, att, mn, pn, gch, sev, kind, owner sql.NullString
		var isLoopHint int
		if err := sourceRows.Scan(&m.sourceID, &m.role, &sev, &m.content, &rc, &tc, &tr, &att, &mn, &pn, &m.promptTokens, &m.predictedTokens, &m.cacheTokens, &m.promptMs, &m.predictedMs, &m.duration, &gch, &isLoopHint, &owner, &kind, &m.createdAt); err != nil {
			sourceRows.Close()
			return nil, fmt.Errorf("scan source message: %w", err)
		}
		m.reasoningContent = nullStr(rc)
		m.severity = nullStr(sev)
		// Everything that gives the row its meaning travels with it, including the tool invocation
		// and its result: the fork shows the parent conversation, and a tool_call row stripped of
		// its payload renders as a blank card and reaches the model as an empty turn.
		m.toolCalls = nullStr(tc)
		m.toolResult = nullStr(tr)
		m.attachments = nullStr(att)
		m.kind = nullStr(kind)
		m.assistantMsgID = owner.String
		m.isLoopHint = isLoopHint != 0
		m.modelName = nullStr(mn)
		m.providerName = nullStr(pn)
		m.gitCommitHash = nullStr(gch)
		messages = append(messages, m)
	}
	sourceRows.Close()
	if err := sourceRows.Err(); err != nil {
		return nil, fmt.Errorf("iterating source messages: %w", err)
	}

	// Insert in display order: an assistant row is always written before the tool_call rows it owns, so
	// newIDs can remap owner ids as it goes.
	newIDs := make(map[string]string)
	var inherited []CopiedCheckpoint
	for _, m := range messages {
		newID := uuid.New().String()
		// A copied tool_call row must point at the copied assistant row. An owner that was not part
		// of the copy stays NULL: exec.toolCallRowsByOwner discards a row whose owner lives in
		// another thread, and falls back to adjacency for a row with no owner at all.
		assistantMsgID := (*string)(nil)
		if m.assistantMsgID != "" {
			if mapped, ok := newIDs[m.assistantMsgID]; ok {
				assistantMsgID = ptrStr(mapped)
			}
		}
		_, err := db.Exec(`
			INSERT INTO task_messages (id, task_id, thread_id, role, severity, content, reasoning_content, tool_calls, tool_result, attachments, model_name, provider_name, prompt_tokens, predicted_tokens, cache_tokens, prompt_ms, predicted_ms, duration, git_commit_hash, is_loop_hint, assistant_msg_id, kind, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, newID, taskID, targetThreadID, m.role, m.severity, m.content,
			m.reasoningContent, m.toolCalls, m.toolResult, m.attachments,
			m.modelName, m.providerName,
			m.promptTokens, m.predictedTokens, m.cacheTokens, m.promptMs, m.predictedMs, m.duration,
			m.gitCommitHash, B2I(m.isLoopHint), assistantMsgID, m.kind, m.createdAt)
		if err != nil {
			return nil, fmt.Errorf("insert copied message: %w", err)
		}
		if m.gitCommitHash != nil && *m.gitCommitHash != "" {
			inherited = append(inherited, CopiedCheckpoint{MessageID: newID, CommitHash: *m.gitCommitHash})
		}
		if m.role == "assistant" {
			newIDs[m.sourceID] = newID
		}
	}
	return inherited, nil
}

func nullStr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	return &n.String
}
