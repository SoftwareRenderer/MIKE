package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"kanban/backend/types"
)

// taskMessageCols is the column list every task_messages reader must use, kept next to
// scanTaskMessage so a new column cannot be selected in one query and missed in another.
const taskMessageCols = `id, task_id, thread_id, role, severity, content, reasoning_content, tool_calls, tool_result, attachments, model_name, provider_name, prompt_tokens, predicted_tokens, cache_tokens, prompt_ms, predicted_ms, duration, git_commit_hash, is_loop_hint, assistant_msg_id, kind, created_at`

// optStr is the inverse of nullStr: the empty string for a NULL column.
func optStr(n sql.NullString) string {
	if n.Valid {
		return n.String
	}
	return ""
}

// scanTaskMessage scans one row of taskMessageCols into a TaskMessage.
func scanTaskMessage(s interface{ Scan(...any) error }) (types.TaskMessage, error) {
	var m types.TaskMessage
	var createdAt string
	var threadID, modelName, providerName, toolCalls, toolResult, reasoningContent, gitCommitHash, attachments, severity, assistantMsgID, kind sql.NullString
	if err := s.Scan(&m.ID, &m.TaskID, &threadID, &m.Role, &severity, &m.Content, &reasoningContent, &toolCalls, &toolResult, &attachments, &modelName, &providerName, &m.PromptTokens, &m.PredictedTokens, &m.CacheTokens, &m.PromptMs, &m.PredictedMs, &m.Duration, &gitCommitHash, &m.IsLoopHint, &assistantMsgID, &kind, &createdAt); err != nil {
		return m, err
	}
	m.ThreadID = optStr(threadID)
	m.Severity = optStr(severity)
	m.ReasoningContent = optStr(reasoningContent)
	m.ModelName = optStr(modelName)
	m.ProviderName = optStr(providerName)
	m.ToolCalls = optStr(toolCalls)
	m.ToolResult = optStr(toolResult)
	m.GitCommitHash = optStr(gitCommitHash)
	m.Attachments = optStr(attachments)
	m.AssistantMsgID = optStr(assistantMsgID)
	m.Kind = optStr(kind)
	m.CreatedAt = scanTime(createdAt)
	return m, nil
}

// CreateTaskMessage inserts a task chat message and accumulates token stats.
func (s *Store) CreateTaskMessage(m *types.TaskMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO task_messages (id, task_id, thread_id, role, severity, content, reasoning_content, tool_calls, tool_result, attachments, model_name, provider_name, prompt_tokens, predicted_tokens, cache_tokens, prompt_ms, predicted_ms, duration, git_commit_hash, is_loop_hint, assistant_msg_id, kind, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.TaskID, ptrStr(m.ThreadID), m.Role, ptrStr(m.Severity), m.Content, m.ReasoningContent, m.ToolCalls, m.ToolResult, ptrStr(m.Attachments), m.ModelName, m.ProviderName, m.PromptTokens, m.PredictedTokens, m.CacheTokens, m.PromptMs, m.PredictedMs, m.Duration, ptrStr(m.GitCommitHash), B2I(m.IsLoopHint), ptrStr(m.AssistantMsgID), ptrStr(m.Kind), m.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return err
	}
	// Accumulate token stats for assistant/title messages into the cumulative table.
	if m.Role == "assistant" || m.Role == "title" {
		_, _ = s.db.Exec(`
			INSERT INTO project_token_stats (project_id, model_name, provider_name, prompt_tokens, cache_tokens, predicted_tokens, prompt_ms, predicted_ms)
			SELECT t.project_id, ?, ?, ?, ?, ?, ?, ?
			FROM tasks t WHERE t.id = ?
			ON CONFLICT(project_id, model_name, provider_name)
			DO UPDATE SET
				prompt_tokens = project_token_stats.prompt_tokens + excluded.prompt_tokens,
				cache_tokens = project_token_stats.cache_tokens + excluded.cache_tokens,
				predicted_tokens = project_token_stats.predicted_tokens + excluded.predicted_tokens,
				prompt_ms = project_token_stats.prompt_ms + excluded.prompt_ms,
				predicted_ms = project_token_stats.predicted_ms + excluded.predicted_ms
		`, m.ModelName, m.ProviderName, m.PromptTokens, m.CacheTokens, m.PredictedTokens, m.PromptMs, m.PredictedMs, m.TaskID)
		// Track this task in the cumulative task counter (idempotent per task).
		_, _ = s.db.Exec(`
			INSERT INTO project_token_tasks (project_id, model_name, provider_name, task_id)
			SELECT t.project_id, ?, ?, ?
			FROM tasks t WHERE t.id = ?
			ON CONFLICT(project_id, model_name, provider_name, task_id) DO NOTHING
		`, m.ModelName, m.ProviderName, m.TaskID, m.TaskID)
	}
	return nil
}

// GetTaskMessages retrieves all messages for a task, optionally filtered by thread.
func (s *Store) GetTaskMessages(taskID, threadID string) ([]types.TaskMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var rows *sql.Rows
	var err error
	if threadID != "" {
		// A thread belongs to exactly one task, so thread_id alone is sufficient to
		// identify its messages. When taskID is empty (e.g. getLastCommitHashInThread),
		// omit the task_id filter so the lookup isn't restricted to task_id = ''. When
		// taskID is provided, keep both filters for callers that expect them.
		if taskID != "" {
			rows, err = s.db.Query(
				`SELECT `+taskMessageCols+` FROM task_messages WHERE task_id = ? AND thread_id = ? ORDER BY created_at ASC`,
				taskID, threadID,
			)
		} else {
			rows, err = s.db.Query(
				`SELECT `+taskMessageCols+` FROM task_messages WHERE thread_id = ? ORDER BY created_at ASC`,
				threadID,
			)
		}
	} else {
		// For backward compatibility: get messages with no thread_id (legacy) or all messages
		rows, err = s.db.Query(
			`SELECT `+taskMessageCols+` FROM task_messages WHERE task_id = ? ORDER BY created_at ASC`,
			taskID,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []types.TaskMessage{}
	for rows.Next() {
		m, err := scanTaskMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// GetTaskMessageByID retrieves a single message by its ID.
func (s *Store) GetTaskMessageByID(messageID string) (*types.TaskMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row := s.db.QueryRow(`SELECT `+taskMessageCols+` FROM task_messages WHERE id = ?`, messageID)
	m, err := scanTaskMessage(row)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// DeleteTaskMessageByID deletes a single message by its ID.
func (s *Store) DeleteTaskMessageByID(messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM task_messages WHERE id = ?`, messageID)
	return err
}

// DeleteTaskMessagesAfter deletes the message with the given ID and all messages after it, optionally scoped to a thread.
func (s *Store) DeleteTaskMessagesAfter(taskID, messageID, threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Get the created_at of the message to delete
	var createdAt string
	err := s.db.QueryRow(`SELECT created_at FROM task_messages WHERE id = ?`, messageID).Scan(&createdAt)
	if err != nil {
		return err
	}
	// Delete this message and all messages created after it (scoped to thread if provided).
	// Explicitly match the message ID to handle any timestamp precision edge cases.
	if threadID != "" {
		_, err = s.db.Exec(`DELETE FROM task_messages WHERE task_id = ? AND thread_id = ? AND (id = ? OR (created_at >= ? AND role != 'summarization'))`, taskID, threadID, messageID, createdAt)
	} else {
		_, err = s.db.Exec(`DELETE FROM task_messages WHERE task_id = ? AND (id = ? OR (created_at >= ? AND role != 'summarization'))`, taskID, messageID, createdAt)
	}
	return err
}

// UpdateTaskMessage updates the content of a message.
func (s *Store) UpdateTaskMessage(messageID, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE task_messages SET content = ? WHERE id = ?`, content, messageID)
	return err
}

// GetLastUserMessageInThread returns the ID of the last user message in a thread.
func (s *Store) GetLastUserMessageInThread(threadID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var id string
	err := s.db.QueryRow(`
		SELECT id FROM task_messages
		WHERE thread_id = ? AND role = 'user'
		ORDER BY created_at DESC
		LIMIT 1
	`, threadID).Scan(&id)
	return id, err
}

// GetLastMessageInThread returns the newest row of a thread, or nil when it has none.
// It ignores role, so callers can ask "has anything happened since this message?". rowid
// breaks ties inside one turn, where an assistant row and its tool_call rows share a
// created_at.
func (s *Store) GetLastMessageInThread(threadID string) (*types.TaskMessage, error) {
	if threadID == "" {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, err := scanTaskMessage(s.db.QueryRow(
		`SELECT `+taskMessageCols+` FROM task_messages WHERE thread_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`,
		threadID,
	))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &m, nil
}

// GetMessageCreatedAt returns the created_at time of a message by ID.
func (s *Store) GetMessageCreatedAt(messageID string) (*time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var createdAt string
	err := s.db.QueryRow(`SELECT created_at FROM task_messages WHERE id = ?`, messageID).Scan(&createdAt)
	if err != nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		t, err = time.Parse(time.RFC3339, createdAt) // fallback for legacy data
		if err != nil {
			return nil, err
		}
	}
	return &t, nil
}

// UpdateTaskMessageCommitHash updates the git_commit_hash of a message.
func (s *Store) UpdateTaskMessageCommitHash(messageID, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE task_messages SET git_commit_hash = ? WHERE id = ?`, hash, messageID)
	return err
}

// UpdateTaskMessageToolResult updates the tool_result of a tool_call message.
func (s *Store) UpdateTaskMessageToolResult(messageID, toolResult string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE task_messages SET tool_result = ? WHERE id = ?`, toolResult, messageID)
	return err
}

// GetLastCommitHashBeforeMessage walks backward from the given message to find
// the last message (in any role) with a non-empty git_commit_hash.
// Returns empty string if no commit hash is found.
func (s *Store) GetLastCommitHashBeforeMessage(messageID, threadID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Get the created_at of the target message
	var cutoffTime string
	err := s.db.QueryRow(`SELECT created_at FROM task_messages WHERE id = ?`, messageID).Scan(&cutoffTime)
	if err != nil {
		return "", err
	}
	// Find the last message with a commit hash at or before the cutoff
	var hash string
	query := `SELECT git_commit_hash FROM task_messages WHERE task_id = (SELECT task_id FROM task_messages WHERE id = ?) AND thread_id = ? AND created_at < ? AND git_commit_hash IS NOT NULL AND git_commit_hash != '' ORDER BY created_at DESC LIMIT 1`
	err = s.db.QueryRow(query, messageID, threadID, cutoffTime).Scan(&hash)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return hash, err
}

// FixPostSummaryTimestamps updates messages created between anchorTime
// and summaryTime to sort after the summary. This fixes ordering for
// messages sent during a summarization window, which have earlier
// created_at than the summary but semantically belong after it.
func (s *Store) FixPostSummaryTimestamps(taskID, threadID string, anchorTime, summaryTime time.Time) error {
	newTime := summaryTime.Add(time.Millisecond)
	_, err := s.db.Exec(
		`UPDATE task_messages SET created_at = ? WHERE task_id = ? AND thread_id = ? AND created_at > ? AND created_at < ?`,
		newTime.Format(time.RFC3339Nano), taskID, threadID,
		anchorTime.Format(time.RFC3339Nano), summaryTime.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("fix post-summary timestamps: %w", err)
	}
	return nil
}
