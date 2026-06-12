package store

import (
	"database/sql"
	"time"

	"kanban/backend/types"
)

// CreateTaskMessage inserts a task chat message.
func (s *Store) CreateTaskMessage(m *types.TaskMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO task_messages (id, task_id, thread_id, role, content, reasoning_content, tool_calls, tool_result, model_name, provider_name, prompt_tokens, predicted_tokens, cache_tokens, prompt_ms, predicted_ms, duration, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.TaskID, ptrStr(m.ThreadID), m.Role, m.Content, m.ReasoningContent, m.ToolCalls, m.ToolResult, m.ModelName, m.ProviderName, m.PromptTokens, m.PredictedTokens, m.CacheTokens, m.PromptMs, m.PredictedMs, m.Duration, m.CreatedAt.Format(time.RFC3339),
	)
	return err
}

// GetTaskMessages retrieves all messages for a task, optionally filtered by thread.
func (s *Store) GetTaskMessages(taskID, threadID string) ([]types.TaskMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var rows *sql.Rows
	var err error
	if threadID != "" {
		rows, err = s.db.Query(
			`SELECT id, task_id, thread_id, role, content, reasoning_content, tool_calls, tool_result, model_name, provider_name, prompt_tokens, predicted_tokens, cache_tokens, prompt_ms, predicted_ms, duration, created_at FROM task_messages WHERE task_id = ? AND thread_id = ? ORDER BY created_at ASC`,
			taskID, threadID,
		)
	} else {
		// For backward compatibility: get messages with no thread_id (legacy) or all messages
		rows, err = s.db.Query(
			`SELECT id, task_id, thread_id, role, content, reasoning_content, tool_calls, tool_result, model_name, provider_name, prompt_tokens, predicted_tokens, cache_tokens, prompt_ms, predicted_ms, duration, created_at FROM task_messages WHERE task_id = ? ORDER BY created_at ASC`,
			taskID,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []types.TaskMessage{}
	for rows.Next() {
		var m types.TaskMessage
		var createdAt string
		var threadID, modelName, providerName, toolCalls, toolResult, reasoningContent sql.NullString
		if err := rows.Scan(&m.ID, &m.TaskID, &threadID, &m.Role, &m.Content, &reasoningContent, &toolCalls, &toolResult, &modelName, &providerName, &m.PromptTokens, &m.PredictedTokens, &m.CacheTokens, &m.PromptMs, &m.PredictedMs, &m.Duration, &createdAt); err != nil {
			return nil, err
		}
		if threadID.Valid {
			m.ThreadID = threadID.String
		}
		if reasoningContent.Valid {
			m.ReasoningContent = reasoningContent.String
		}
		if modelName.Valid {
			m.ModelName = modelName.String
		}
		if providerName.Valid {
			m.ProviderName = providerName.String
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
	// Delete this message and all messages created after it (scoped to thread if provided)
	if threadID != "" {
		_, err = s.db.Exec(`DELETE FROM task_messages WHERE task_id = ? AND thread_id = ? AND created_at >= ?`, taskID, threadID, createdAt)
	} else {
		_, err = s.db.Exec(`DELETE FROM task_messages WHERE task_id = ? AND created_at >= ?`, taskID, createdAt)
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

// GetMessageCreatedAt returns the created_at time of a message by ID.
func (s *Store) GetMessageCreatedAt(messageID string) (*time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var createdAt string
	err := s.db.QueryRow(`SELECT created_at FROM task_messages WHERE id = ?`, messageID).Scan(&createdAt)
	if err != nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateHomeAgentMessage inserts a home agent message.
func (s *Store) CreateHomeAgentMessage(m *types.HomeAgentMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO home_agent_messages (id, project_id, role, content, created_at) VALUES (?, ?, ?, ?, ?)`,
		m.ID, m.ProjectID, m.Role, m.Content, m.CreatedAt.Format(time.RFC3339),
	)
	return err
}

// GetHomeAgentMessages retrieves all home agent messages for a project.
func (s *Store) GetHomeAgentMessages(projectID string) ([]types.HomeAgentMessage, error) {
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
	messages := []types.HomeAgentMessage{}
	for rows.Next() {
		var m types.HomeAgentMessage
		var createdAt string
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.Role, &m.Content, &createdAt); err != nil {
			return nil, err
		}
		m.CreatedAt = scanTime(createdAt)
		messages = append(messages, m)
	}
	return messages, rows.Err()
}
