package store

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"mike/backend/types"
)

// taskMessageCols is the column list every task_messages reader must use, kept next to
// scanTaskMessage so a new column cannot be selected in one query and missed in another.
const taskMessageCols = `id, task_id, thread_id, role, severity, content, reasoning_content, tool_calls, tool_result, attachments, model_name, provider_name, prompt_tokens, predicted_tokens, cache_tokens, prompt_ms, predicted_ms, duration, git_commit_hash, is_loop_hint, assistant_msg_id, kind, created_at`

// messageTimeLayout is the storage form of a task message's created_at, and the reason chat reads back
// in the order it was written. SQL orders these rows and cuts them at a message by comparing that text,
// and time.RFC3339Nano trims the zeros off the end of the fraction: 'Z' (0x5A) sorts above every digit,
// so the earlier "…:05.3892121Z" sorts *after* the later "…:05.38921216Z". A fixed-width fraction makes
// the text order the time order — for text in one zone, which is why the instant is stored in UTC: an
// hour of wall-clock time repeats at a fall-back, and two offsets would order it backwards.
const messageTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// formatMessageTime renders a timestamp for task_messages.created_at.
func formatMessageTime(t time.Time) string { return t.UTC().Format(messageTimeLayout) }

// messageOrder lists messages oldest first. rowid breaks a tie inside one turn, where an assistant row
// and its tool_call rows share a created_at.
const messageOrder = "ORDER BY created_at ASC, rowid ASC"

// newestFirstOrder is the same walk backwards, which is how a window is cut: SQL drops the oldest rows
// at the LIMIT.
const newestFirstOrder = "ORDER BY created_at DESC, rowid DESC"

// A window counts answers, not rows: an assistant row and the tool_call rows under it are one answer.
// MessageRowCeiling bounds one response when the newest answers are long.
const (
	MessageWindowDefault = 5
	MessageWindowMax     = 25
	MessageRowCeiling    = 500
)

// A turn read answers where the prompts are, which is how far back the chat's sticky header can name a
// step up. TurnLabelChars bounds the quoted line: a prompt that is a pasted file must not ride the
// response just to name itself.
const (
	TurnWindowDefault = 10
	TurnWindowMax     = 50
	TurnLabelChars    = 200
)

// MessagePage is one window of a conversation: its newest rows, oldest first.
type MessagePage struct {
	Messages []types.TaskMessage
	// Before is the cursor for the next-older window, empty when this window reaches the first row.
	Before  string
	HasMore bool
}

// UserTurn is one prompt's position in the conversation and the line that names it. A loop hint is a
// user-role row the transcript draws among the prompts, so it is a turn here too.
type UserTurn struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
	// Text is the head of the prompt, bounded by TurnLabelChars — enough to name the turn, and a client
	// shows its first line.
	Text string `json:"text"`
	// Cursor is this prompt's own position, in the encoding the message window's `from` takes.
	Cursor string `json:"cursor"`
}

// UserTurnPage is the newest few prompts of a conversation, oldest first, with the cursor for the prompts
// above them.
type UserTurnPage struct {
	Turns []UserTurn
	// Before is the cursor for the next-older prompts, empty when this page reaches the first prompt.
	Before  string
	HasMore bool
}

// userTurnRow pairs a turn with the position its cursor names.
type userTurnRow struct {
	UserTurn
	key messageKey
}

func scanUserTurn(r *sql.Rows) (userTurnRow, error) {
	var t userTurnRow
	if err := r.Scan(&t.ID, &t.CreatedAt, &t.key.rowid, &t.Text); err != nil {
		return t, err
	}
	// A cursor is the pair the ORDER BY compares, so the row's id and role are not part of it.
	t.key.at = t.CreatedAt
	return t, nil
}

// messageKey is a row's position in the conversation order: the values the ORDER BY compares.
type messageKey struct {
	id    string
	at    string
	rowid int64
	role  string
}

// encodeMessageCursor renders a cursor. It is opaque: a position in this table's order, not a value a
// caller composes.
func encodeMessageCursor(k messageKey) string {
	return base64.RawURLEncoding.EncodeToString([]byte(k.at + "|" + strconv.FormatInt(k.rowid, 10)))
}

func decodeMessageCursor(s string) (messageKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return messageKey{}, ErrMessageCursor
	}
	at, rest, ok := strings.Cut(string(raw), "|")
	if !ok || at == "" {
		return messageKey{}, ErrMessageCursor
	}
	rowid, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return messageKey{}, ErrMessageCursor
	}
	return messageKey{at: at, rowid: rowid}, nil
}

// conversationWhere is the WHERE of a conversation read: one thread when one is named, otherwise the
// whole task, cut at the caller's cursor and stopped at its floor. A created_at alone cannot cut a turn —
// its rows share one instant — so both comparisons are the pair the ORDER BY uses.
func conversationWhere(taskID, threadID string, cursor, floor *messageKey) (string, []any) {
	var sb strings.Builder
	var args []any
	if threadID != "" {
		sb.WriteString("thread_id = ?")
		args = append(args, threadID)
	} else {
		sb.WriteString("task_id = ?")
		args = append(args, taskID)
	}
	if cursor != nil {
		sb.WriteString(" AND (created_at, rowid) < (?, ?)")
		args = append(args, cursor.at, cursor.rowid)
	}
	if floor != nil {
		sb.WriteString(" AND (created_at, rowid) >= (?, ?)")
		args = append(args, floor.at, floor.rowid)
	}
	return sb.String(), args
}

// messageKeyCols names the columns the key walk reads. The (scope, created_at) index carries the order but
// neither id nor role, so the walk looks every position up in the table — measured 3x the cost of the same
// walk over created_at and rowid alone. role is what newestUnits cuts an answer on, and id is what the row
// fetch below is keyed by.
const messageKeyCols = `SELECT id, created_at, rowid, role FROM task_messages WHERE `

func (s *Store) messageKeys(taskID, threadID string, cursor, floor *messageKey, n int) ([]messageKey, error) {
	where, args := conversationWhere(taskID, threadID, cursor, floor)
	rows, err := s.db.Query(messageKeyCols+where+` `+newestFirstOrder+` LIMIT ?`,
		append(append([]any{}, args...), n)...)
	if err != nil {
		return nil, err
	}
	return scanRows(rows, scanMessageKey)
}

// startsUnit is true for a row that opens an answer. A tool_call row belongs to the assistant row that
// announced it, so a window must not cut between the two.
func startsUnit(role string) bool { return role != "tool_call" }

// newestUnits keeps the newest `units` answers of a newest-first list of positions, cutting above the row
// that opens the oldest answer held. An answer longer than the read's bound is cut inside, so the page
// opens on a tool_call row and its opening rows arrive on the next page.
func newestUnits(keys []messageKey, units int) []messageKey {
	seen := 0
	for i, k := range keys {
		if !startsUnit(k.role) {
			continue
		}
		seen++
		if seen == units {
			return keys[:i+1]
		}
	}
	return keys
}

// GetTaskMessagePage answers one window: the newest `limit` answers, oldest first, with before naming a
// cursor an earlier call returned and `from` an optional floor — the oldest row this page may answer.
// Positions are read before rows, so the cut and the sort run over index entries rather than the
// conversation's bytes.
//
// Together the pages hand over every row of the thread exactly once, which is why the cursor names the
// oldest row this page holds. A floor stops a page there; the rows below it come on a later page.
func (s *Store) GetTaskMessagePage(taskID, threadID string, limit int, before, from string) (*MessagePage, error) {
	// The route refuses a window outside 1..MessageWindowMax; this covers a caller that is not a request.
	if limit <= 0 || limit > MessageWindowMax {
		limit = MessageWindowDefault
	}
	cursor, err := messageCursor(before, ErrMessageCursor)
	if err != nil {
		return nil, err
	}
	floor, err := messageCursor(from, ErrMessageFloor)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	page := &MessagePage{}
	keys, err := s.messageKeys(taskID, threadID, cursor, floor, MessageRowCeiling)
	if err != nil {
		return nil, err
	}
	keys = newestUnits(keys, limit)
	if len(keys) == 0 {
		return page, nil
	}
	// The oldest row here starts the next window: it answers both the cursor and whether one exists.
	oldest := keys[len(keys)-1]
	olderWhere, olderArgs := conversationWhere(taskID, threadID, &oldest, nil)
	var more int
	if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM task_messages WHERE `+olderWhere+`)`, olderArgs...).Scan(&more); err != nil {
		return nil, err
	}
	page.HasMore = more == 1
	if page.HasMore {
		page.Before = encodeMessageCursor(oldest)
	}

	placeholders := make([]string, len(keys))
	rowids := make([]any, len(keys))
	for i, k := range keys {
		placeholders[i] = "?"
		rowids[i] = k.rowid
	}
	rows, err := s.db.Query(`SELECT `+taskMessageCols+` FROM task_messages WHERE rowid IN (`+strings.Join(placeholders, ",")+`)`, rowids...)
	if err != nil {
		return nil, err
	}
	found, err := scanRows(rows, func(r *sql.Rows) (types.TaskMessage, error) { return scanTaskMessage(r) })
	if err != nil {
		return nil, err
	}
	byID := make(map[string]types.TaskMessage, len(found))
	for _, m := range found {
		byID[m.ID] = m
	}
	page.Messages = make([]types.TaskMessage, 0, len(keys))
	for i := len(keys) - 1; i >= 0; i-- {
		if m, ok := byID[keys[i].id]; ok {
			page.Messages = append(page.Messages, m)
		}
	}
	return page, nil
}

// messageCursor decodes a page cursor; an empty one means "from this end of the conversation". `bad` is
// the error to answer with, so a token sent in the wrong parameter names that parameter.
func messageCursor(before string, bad error) (*messageKey, error) {
	if before == "" {
		return nil, nil
	}
	c, err := decodeMessageCursor(before)
	if err != nil {
		return nil, bad
	}
	return &c, nil
}

// userTurnCols names the columns of a prompt read: the position a cursor names, beside the head of the
// prompt. The substr bound is the first placeholder, so a prompt is bounded before its bytes are read.
const userTurnCols = `SELECT id, created_at, rowid, substr(content, 1, ?) FROM task_messages WHERE `

// userTurnOnly is the partial user-row indexes' predicate: a read carrying it walks prompts, and the
// answers between them stay unread.
const userTurnOnly = ` AND role = 'user'`

// GetUserTurnPage answers where the prompts are without reading the answers under them. The chat's sticky
// header names the prompt above the rows it holds, and a turn counted in answers keeps that prompt out of
// the window. `role = 'user'` is the partial user-row index's predicate, so the read costs the prompts it
// returns rather than the rows between them.
func (s *Store) GetUserTurnPage(taskID, threadID string, limit int, before string) (*UserTurnPage, error) {
	// The route refuses a page outside 1..TurnWindowMax; this covers a caller that is not a request.
	if limit <= 0 || limit > TurnWindowMax {
		limit = TurnWindowDefault
	}
	cursor, err := messageCursor(before, ErrMessageCursor)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	page := &UserTurnPage{}
	where, args := conversationWhere(taskID, threadID, cursor, nil)
	// substr's bound is the first placeholder; the where's args follow it, the limit last.
	args = append(append([]any{TurnLabelChars}, args...), limit)
	rows, err := s.db.Query(userTurnCols+where+userTurnOnly+` `+newestFirstOrder+` LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	found, err := scanRows(rows, scanUserTurn)
	if err != nil {
		return nil, err
	}
	if len(found) > 0 {
		oldest := found[len(found)-1]
		olderWhere, olderArgs := conversationWhere(taskID, threadID, &oldest.key, nil)
		var more int
		if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM task_messages WHERE `+olderWhere+userTurnOnly+`)`, olderArgs...).Scan(&more); err != nil {
			return nil, err
		}
		page.HasMore = more == 1
		if page.HasMore {
			page.Before = encodeMessageCursor(oldest.key)
		}
	}
	// A page is an array even when the conversation holds no prompt: the route answers with this slice.
	page.Turns = make([]UserTurn, 0, len(found))
	for i := len(found) - 1; i >= 0; i-- {
		turn := found[i].UserTurn
		turn.Cursor = encodeMessageCursor(found[i].key)
		page.Turns = append(page.Turns, turn)
	}
	return page, nil
}

func scanMessageKey(r *sql.Rows) (messageKey, error) {
	var k messageKey
	if err := r.Scan(&k.id, &k.at, &k.rowid, &k.role); err != nil {
		return k, err
	}
	return k, nil
}

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
		m.ID, m.TaskID, ptrStr(m.ThreadID), m.Role, ptrStr(m.Severity), m.Content, m.ReasoningContent, m.ToolCalls, m.ToolResult, ptrStr(m.Attachments), m.ModelName, m.ProviderName, m.PromptTokens, m.PredictedTokens, m.CacheTokens, m.PromptMs, m.PredictedMs, m.Duration, ptrStr(m.GitCommitHash), B2I(m.IsLoopHint), ptrStr(m.AssistantMsgID), ptrStr(m.Kind), formatMessageTime(m.CreatedAt),
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

// Named so the plan test explains the text these reads run.
const (
	taskThreadMessagesSQL = `SELECT ` + taskMessageCols + ` FROM task_messages WHERE task_id = ? AND thread_id = ? ` + messageOrder
	threadMessagesSQL     = `SELECT ` + taskMessageCols + ` FROM task_messages WHERE thread_id = ? ` + messageOrder
	taskMessagesAllSQL    = `SELECT ` + taskMessageCols + ` FROM task_messages WHERE task_id = ? ` + messageOrder
)

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
			rows, err = s.db.Query(taskThreadMessagesSQL, taskID, threadID)
		} else {
			rows, err = s.db.Query(threadMessagesSQL, threadID)
		}
	} else {
		// For backward compatibility: get messages with no thread_id (legacy) or all messages
		rows, err = s.db.Query(taskMessagesAllSQL, taskID)
	}
	if err != nil {
		return nil, err
	}
	return scanRows(rows, func(r *sql.Rows) (types.TaskMessage, error) {
		return scanTaskMessage(r)
	})
}

// CountTaskMessages counts a task's conversation rows. A write no durability seam sees — a fork copying a
// thread's rows — moves this count and no revision.
func (s *Store) CountTaskMessages(taskID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM task_messages WHERE task_id = ?`, taskID).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// GetTaskMessageByID retrieves a single message by its ID.
func (s *Store) GetTaskMessageByID(messageID string) (*types.TaskMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row := s.db.QueryRow(`SELECT `+taskMessageCols+` FROM task_messages WHERE id = ?`, messageID)
	m, err := scanTaskMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMessageNotFound
	}
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

// DeleteTaskMessagesAfter deletes the message with the given ID and all messages after it, optionally
// scoped to a thread, and reports the ids of the deleted rows that held a worktree checkpoint.
//
// The ids come from the delete's own predicate. Which rows go is a created_at/role rule that spares
// summarization rows and breaks ties by rowid, so a caller that re-derived the set could release a
// snapshot a surviving row still names — one git is then free to prune.
func (s *Store) DeleteTaskMessagesAfter(taskID, messageID, threadID string) ([]string, error) {
	var checkpointed []string
	err := s.tx(func(tx *sql.Tx) error {
		var err error
		checkpointed, err = s.deleteTaskMessagesAfterLocked(tx, taskID, messageID, threadID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return checkpointed, nil
}

// deleteTaskMessagesAfterLocked reads the checkpointed ids and deletes the rows against one handle, so the
// set the caller is told to release is the set the delete removed: read on one statement and delete on the
// next, with a failure between them, the caller would be handed ids for rows that are still there (or left
// holding ids for rows that are not), and the snapshot a surviving row names is a snapshot git can prune.
func (s *Store) deleteTaskMessagesAfterLocked(db execer, taskID, messageID, threadID string) ([]string, error) {
	// Get the created_at of the message to delete
	var createdAt string
	if err := db.QueryRow(`SELECT created_at FROM task_messages WHERE id = ?`, messageID).Scan(&createdAt); err != nil {
		return nil, err
	}
	// The message itself plus everything after it in the thread. Matching the ID explicitly covers
	// timestamps shared inside one turn.
	where := `task_id = ? AND (id = ? OR (created_at >= ? AND role != 'summarization'))`
	args := []any{taskID, messageID, createdAt}
	if threadID != "" {
		where = `task_id = ? AND thread_id = ? AND (id = ? OR (created_at >= ? AND role != 'summarization'))`
		args = []any{taskID, threadID, messageID, createdAt}
	}
	rows, err := db.Query(`SELECT id, git_commit_hash FROM task_messages WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	// Collected and closed before the DELETE: the pool is one connection, and the
	// checkpointed ids must all be known before any row that names them is gone.
	checkpointedRows, err := scanRows(rows, func(r *sql.Rows) (checkpointRef, error) {
		var ref checkpointRef
		err := r.Scan(&ref.id, &ref.hash)
		return ref, err
	})
	if err != nil {
		return nil, err
	}
	var checkpointed []string
	for _, ref := range checkpointedRows {
		if ref.hash.String != "" {
			checkpointed = append(checkpointed, ref.id.String)
		}
	}

	if _, err := db.Exec(`DELETE FROM task_messages WHERE `+where, args...); err != nil {
		return nil, err
	}
	return checkpointed, nil
}

// checkpointRef is one row's id plus the snapshot its git_commit_hash names.
type checkpointRef struct {
	id, hash sql.NullString
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
		ORDER BY created_at DESC, rowid DESC
		LIMIT 1
	`, threadID).Scan(&id)
	return id, err
}

// lastSummarizationSQL is the summary row in effect for a thread: the newest one. It is the SQL twin of
// extractPrevSummary in backend/exec, which must name the same row.
const lastSummarizationSQL = `SELECT id FROM task_messages WHERE thread_id = ? AND role = 'summarization' ` + newestFirstOrder + ` LIMIT 1`

// LastSummarizationMessageID returns the id of the newest summarization row of a thread, or "" when the
// thread has none.
func (s *Store) LastSummarizationMessageID(threadID string) (string, error) {
	if threadID == "" {
		return "", nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var id string
	err := s.db.QueryRow(lastSummarizationSQL, threadID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// lastMessageInThreadSQL is the newest row of a thread.
const lastMessageInThreadSQL = `SELECT ` + taskMessageCols + ` FROM task_messages WHERE thread_id = ? ` + newestFirstOrder + ` LIMIT 1`

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
	m, err := scanTaskMessage(s.db.QueryRow(lastMessageInThreadSQL, threadID))
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
		t, err = time.Parse(time.RFC3339, createdAt)
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

// FixPostSummaryTimestamps moves messages created between anchorTime and summaryTime to
// sort after the summary: they were sent during the summarization window, so their
// created_at predates the summary but they belong after it.
func (s *Store) FixPostSummaryTimestamps(taskID, threadID string, anchorTime, summaryTime time.Time) error {
	newTime := summaryTime.Add(time.Millisecond)
	_, err := s.db.Exec(
		`UPDATE task_messages SET created_at = ? WHERE task_id = ? AND thread_id = ? AND created_at > ? AND created_at < ?`,
		formatMessageTime(newTime), taskID, threadID,
		formatMessageTime(anchorTime), formatMessageTime(summaryTime),
	)
	if err != nil {
		return fmt.Errorf("fix post-summary timestamps: %w", err)
	}
	return nil
}
