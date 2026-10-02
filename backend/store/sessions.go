package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

const sessionTable = `
CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    token      TEXT NOT NULL UNIQUE,
    csrf_token TEXT,
    token_hash TEXT,
    ip         TEXT,
    user_agent TEXT,
    last_seen  INTEGER,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
-- A presented token is matched on its digest and on nothing else.
CREATE INDEX IF NOT EXISTS idx_sessions_token_hash ON sessions(token_hash);
`

// sessionColumns lists the sessions columns in the order CreateSession writes them, and is every
// column the table has — the schema test compares this list against the DDL above.
const sessionColumns = `id, token, csrf_token, token_hash, ip, user_agent, last_seen, created_at, expires_at`

// lastSeenInterval is the minimum seconds between two last_seen writes for one
// session. The store serialises on a single connection, so an unconditional
// write would make every authenticated request a database write. The same interval throttles an
// API token's last_used_at, for the same reason.
const lastSeenInterval = 60

// hashToken returns the lookup key stored for a presented credential — a login session or an API
// token: hex(sha256(token)). Only the digest reaches the database, so a copy of kanban.db cannot be
// replayed as a live credential.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// initSessions creates the sessions table and its lookup index. It lives with the session accessors
// rather than in baseSchemaSQL, which holds no credential table.
func (s *Store) initSessions() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(sessionTable)
	return err
}

// NewSession carries the fields of a session row.
type NewSession struct {
	ID        string
	Token     string // raw bearer token — hashed, never persisted
	CSRFToken string
	IP        string
	UserAgent string
	ExpiresAt time.Time
}

// CreateSession inserts a new session row. The raw token is stored only as
// hex(sha256(token)), in `token_hash` (the index a lookup walks) and in `token`, whose
// NOT NULL UNIQUE constraint that digest therefore guards. No plaintext reaches the file.
func (s *Store) CreateSession(ctx context.Context, ses NewSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash := hashToken(ses.Token)
	now := time.Now().Unix()
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO sessions (`+sessionColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ses.ID, hash, ses.CSRFToken, hash, ses.IP, ses.UserAgent, now, now, ses.ExpiresAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// GetSessionByToken returns the session id, CSRF token and expiry for a
// presented token, or sql.ErrNoRows if nothing matches. The lookup is by digest
// only. It also stamps last_seen, throttled to one write per lastSeenInterval.
func (s *Store) GetSessionByToken(ctx context.Context, token string) (id, csrfToken string, expiresAt time.Time, err error) {
	if token == "" {
		return "", "", time.Time{}, sql.ErrNoRows
	}
	// Write lock: this read may refresh last_seen (see lastSeenInterval).
	s.mu.Lock()
	defer s.mu.Unlock()

	var csrf string
	var expiresAtUnix, lastSeen int64
	err = s.db.QueryRowContext(ctx,
		`SELECT id, COALESCE(csrf_token, ''), expires_at, COALESCE(last_seen, 0)
		   FROM sessions WHERE token_hash = ?`,
		hashToken(token)).
		Scan(&id, &csrf, &expiresAtUnix, &lastSeen)
	if err != nil {
		return "", "", time.Time{}, err
	}
	expiresAt = time.Unix(expiresAtUnix, 0)

	now := time.Now().Unix()
	if now-lastSeen >= lastSeenInterval {
		// A failed stamp is never fatal — activity tracking is best-effort.
		_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET last_seen = ? WHERE id = ?`, now, id)
	}
	return id, csrf, expiresAt, nil
}

// DeleteSession removes a session by (presented) token, looked up by digest.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions removes all sessions whose expires_at is in the past.
// Returns the number of sessions deleted.
func (s *Store) DeleteExpiredSessions(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// SessionInfo is one session row for the session-management API. It has no
// field for a token, token hash or CSRF token, so it cannot leak one.
type SessionInfo struct {
	ID        string
	CreatedAt time.Time
	LastSeen  time.Time // zero when the session has never been used
	ExpiresAt time.Time
	IP        string
	UserAgent string
}

// ListSessions returns all live sessions, newest first.
func (s *Store) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, created_at, COALESCE(last_seen, 0), expires_at, COALESCE(ip, ''), COALESCE(user_agent, '')
		   FROM sessions ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	sessions, err := scanRows(rows, scanSessionRow)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return sessions, nil
}

// scanSessionRow reads one session row. Timestamps are stored as unix seconds, and a
// session that has never been used has last_seen 0 — a zero time, not the epoch.
func scanSessionRow(r *sql.Rows) (SessionInfo, error) {
	var (
		id                string
		createdAt, seenAt int64
		expiresAt         int64
		ip, userAgent     string
	)
	if err := r.Scan(&id, &createdAt, &seenAt, &expiresAt, &ip, &userAgent); err != nil {
		return SessionInfo{}, fmt.Errorf("scan session row: %w", err)
	}
	info := SessionInfo{
		ID:        id,
		CreatedAt: time.Unix(createdAt, 0),
		ExpiresAt: time.Unix(expiresAt, 0),
		IP:        ip,
		UserAgent: userAgent,
	}
	if seenAt > 0 {
		info.LastSeen = time.Unix(seenAt, 0)
	}
	return info, nil
}

// DeleteSessionByID revokes a single session. Returns false when no session
// has that id.
func (s *Store) DeleteSessionByID(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	n, err := s.deleteRows(ctx, `DELETE FROM sessions WHERE id = ?`, "delete session by id", id)
	return n > 0, err
}

// DeleteOtherSessions revokes every session except currentID and returns the
// number deleted. An empty currentID deletes nothing — the caller's identity is
// unknown and deleting every session would be the wrong guess.
func (s *Store) DeleteOtherSessions(ctx context.Context, currentID string) (int, error) {
	if currentID == "" {
		return 0, nil
	}
	n, err := s.deleteRows(ctx, `DELETE FROM sessions WHERE id <> ?`, "delete other sessions", currentID)
	return int(n), err
}
