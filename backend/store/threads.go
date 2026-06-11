package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"kanban/backend/types"
)

// CreateThread inserts a new thread for a task.
func (s *Store) CreateThread(th *types.TaskThread) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO task_threads (id, task_id, parent_thread_id, name, is_active, git_branch, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		th.ID, th.TaskID, ptrStr(th.ParentThreadID), th.Name, b2i(th.IsActive), ptrStr(th.GitBranch), th.CreatedAt.Format(time.RFC3339),
	)
	return err
}

// GetThreadsByTask returns all threads for a task ordered by creation time.
func (s *Store) GetThreadsByTask(taskID string) ([]types.TaskThread, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(
		`SELECT id, task_id, parent_thread_id, name, is_active, git_branch, created_at FROM task_threads WHERE task_id = ? ORDER BY created_at ASC`,
		taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	threads := []types.TaskThread{}
	for rows.Next() {
		var th types.TaskThread
		if err := scanTaskThread(rows, &th); err != nil {
			return nil, err
		}
		threads = append(threads, th)
	}
	return threads, rows.Err()
}

// GetThread retrieves a single thread by ID.
func (s *Store) GetThread(threadID string) (*types.TaskThread, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row := s.db.QueryRow(
		`SELECT id, task_id, parent_thread_id, name, is_active, git_branch, created_at FROM task_threads WHERE id = ?`,
		threadID,
	)
	var th types.TaskThread
	err := scanTaskThread(row, &th)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("thread not found")
	}
	return &th, err
}

// SetActiveThread marks a thread as active and deactivates all other threads for the same task.
func (s *Store) SetActiveThread(threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Get the task_id for this thread
	var taskID string
	err := s.db.QueryRow(`SELECT task_id FROM task_threads WHERE id = ?`, threadID).Scan(&taskID)
	if err != nil {
		return fmt.Errorf("get thread task: %w", err)
	}
	// Deactivate all threads for this task
	_, err = s.db.Exec(`UPDATE task_threads SET is_active = 0 WHERE task_id = ?`, taskID)
	if err != nil {
		return err
	}
	// Activate the target thread
	_, err = s.db.Exec(`UPDATE task_threads SET is_active = 1 WHERE id = ?`, threadID)
	return err
}

// copyMsg holds the fields needed to copy a message.
type copyMsg struct {
	role, content, createdAt       string
	reasoningContent, toolCalls    *string
	toolResult, modelName          *string
	providerName                   *string
	promptTokens, predictedTokens  int
	cacheTokens                    int
	promptMs, predictedMs          int
	duration                       int64
}

// CopyMessagesToThread copies messages from a source thread up to and including a specific message into a target thread.
func (s *Store) CopyMessagesToThread(sourceThreadID, targetThreadID, upToMessageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Get the created_at of the cutoff message
	var cutoffTime string
	err := s.db.QueryRow(`SELECT created_at FROM task_messages WHERE id = ?`, upToMessageID).Scan(&cutoffTime)
	if err != nil {
		return fmt.Errorf("get cutoff message time: %w", err)
	}
	// Get the task_id of the source thread
	var taskID string
	err = s.db.QueryRow(`SELECT task_id FROM task_threads WHERE id = ?`, sourceThreadID).Scan(&taskID)
	if err != nil {
		return fmt.Errorf("get source thread task: %w", err)
	}
	// Fetch all source messages into memory first (SQLite doesn't allow writes with open read cursors)
	sourceRows, err := s.db.Query(`
		SELECT role, content, reasoning_content, tool_calls, tool_result, model_name, provider_name, prompt_tokens, predicted_tokens, cache_tokens, prompt_ms, predicted_ms, duration, created_at
		FROM task_messages
		WHERE thread_id = ? AND created_at <= ?
		ORDER BY created_at ASC
	`, sourceThreadID, cutoffTime)
	if err != nil {
		return fmt.Errorf("query source messages: %w", err)
	}

	var messages []copyMsg
	for sourceRows.Next() {
		var m copyMsg
		var rc, tc, tr, mn, pn sql.NullString
		if err := sourceRows.Scan(&m.role, &m.content, &rc, &tc, &tr, &mn, &pn, &m.promptTokens, &m.predictedTokens, &m.cacheTokens, &m.promptMs, &m.predictedMs, &m.duration, &m.createdAt); err != nil {
			sourceRows.Close()
			return fmt.Errorf("scan source message: %w", err)
		}
		m.reasoningContent = nullStr(rc)
		m.toolCalls = nullStr(tc)
		m.toolResult = nullStr(tr)
		m.modelName = nullStr(mn)
		m.providerName = nullStr(pn)
		messages = append(messages, m)
	}
	sourceRows.Close()
	if err := sourceRows.Err(); err != nil {
		return fmt.Errorf("iterating source messages: %w", err)
	}

	// Now insert into target thread
	for _, m := range messages {
		newID := uuid.New().String()
		_, err := s.db.Exec(`
			INSERT INTO task_messages (id, task_id, thread_id, role, content, reasoning_content, tool_calls, tool_result, model_name, provider_name, prompt_tokens, predicted_tokens, cache_tokens, prompt_ms, predicted_ms, duration, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, newID, taskID, targetThreadID, m.role, m.content,
			m.reasoningContent, m.toolCalls, m.toolResult,
			m.modelName, m.providerName,
			m.promptTokens, m.predictedTokens, m.cacheTokens, m.promptMs, m.predictedMs, m.duration, m.createdAt)
		if err != nil {
			return fmt.Errorf("insert copied message: %w", err)
		}
	}
	return nil
}

func nullStr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	return &n.String
}
