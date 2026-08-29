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
`

// lastSeenInterval is the minimum number of seconds between two last_seen
// writes for the same session. The store serialises all access on a single
// connection, so an unconditional write per request would turn every
// authenticated request into a database write.
const lastSeenInterval = 60

// HashSessionToken returns the lookup key stored for a session token:
// hex(sha256(token)). Only this digest ever reaches the database, so a copy of
// kanban.db cannot be replayed as a live session.
func HashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// initSessions creates the sessions table if it does not exist, and migrates
// existing tables with the columns added after the first release.
func (s *Store) initSessions() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(sessionTable)
	if err != nil {
		return err
	}
	// Migrations for existing DBs. SQLite has no "ADD COLUMN IF NOT EXISTS":
	// re-adding an existing column errors, which is ignored. The token column
	// keeps its NOT NULL UNIQUE constraint (it now holds the digest, or the
	// "revoked-<id>" placeholder written by BackfillSessionTokens).
	for _, stmt := range []string{
		`ALTER TABLE sessions ADD COLUMN csrf_token TEXT`,
		`ALTER TABLE sessions ADD COLUMN token_hash TEXT`,
		`ALTER TABLE sessions ADD COLUMN ip TEXT`,
		`ALTER TABLE sessions ADD COLUMN user_agent TEXT`,
		`ALTER TABLE sessions ADD COLUMN last_seen INTEGER`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_token_hash ON sessions(token_hash)`,
	} {
		_, _ = s.db.Exec(stmt)
	}
	return nil
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
// hex(sha256(token)); the legacy `token` column (NOT NULL UNIQUE) receives the
// same digest so the uniqueness constraint keeps guarding distinct sessions
// while no plaintext token lands on disk.
func (s *Store) CreateSession(ctx context.Context, ses NewSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash := HashSessionToken(ses.Token)
	now := time.Now().Unix()
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO sessions (id, token, csrf_token, token_hash, ip, user_agent, last_seen, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ses.ID, hash, ses.CSRFToken, hash, ses.IP, ses.UserAgent, now, now, ses.ExpiresAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// GetSessionByToken returns the session id, CSRF token, and expiry time for a
// presented token, or sql.ErrNoRows if no session matches. The lookup goes by
// digest only — the plaintext token is never compared against the database.
//
// It also stamps last_seen, throttled to one write per lastSeenInterval.
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
		HashSessionToken(token)).
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
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, HashSessionToken(token))
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

// BackfillSessionTokens migrates sessions created before tokens were hashed.
// For every row without a token_hash it derives hex(sha256(token)) in Go
// (SQLite has no sha256), stores it, and overwrites the legacy plaintext with
// the non-secret placeholder 'revoked-' || id — which keeps the column's
// NOT NULL UNIQUE constraint intact instead of colliding on an empty string.
//
// No session is invalidated: lookups go through the digest, which is derived
// from the very token the client already holds. The migration is idempotent —
// rows already migrated have a token_hash and are skipped. Returns the number
// of rows migrated.
func (s *Store) BackfillSessionTokens(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	type legacyRow struct{ id, token string }
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, token FROM sessions WHERE token_hash IS NULL OR token_hash = ''`)
	if err != nil {
		return 0, fmt.Errorf("scan legacy sessions: %w", err)
	}
	var pending []legacyRow
	for rows.Next() {
		var r legacyRow
		if err := rows.Scan(&r.id, &r.token); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan legacy session row: %w", err)
		}
		if r.token == "" {
			continue // nothing to derive a digest from; already unusable
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("scan legacy sessions: %w", err)
	}
	rows.Close()

	n := 0
	for _, r := range pending {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE sessions SET token_hash = ?, token = 'revoked-' || id WHERE id = ?`,
			HashSessionToken(r.token), r.id); err != nil {
			return n, fmt.Errorf("backfill session %s: %w", r.id, err)
		}
		n++
	}
	if n > 0 {
		// The overwritten plaintext still sits in freed page space until the file
		// is rebuilt; VACUUM rewrites it so a stolen copy of the database leaks
		// nothing (best-effort — a busy database keeps the slack until the next
		// startup). In WAL mode that new image lands in the -wal first, so it is
		// checkpointed and the sidecar truncated: otherwise the pre-migration
		// pages — plaintext token and all — survive in the main file and in the
		// sidecar until some later checkpoint.
		_, _ = s.db.ExecContext(ctx, `VACUUM`)
		_, _ = s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	return n, nil
}

// SessionInfo is one session row for the session-management API. It carries no
// credential material by construction (there is no field for a token, a token
// hash, or a CSRF token).
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
	defer rows.Close()

	var out []SessionInfo
	for rows.Next() {
		var (
			id                string
			createdAt, seenAt int64
			expiresAt         int64
			ip, userAgent     string
		)
		if err := rows.Scan(&id, &createdAt, &seenAt, &expiresAt, &ip, &userAgent); err != nil {
			return nil, fmt.Errorf("scan session row: %w", err)
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
		out = append(out, info)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return out, nil
}

// DeleteSessionByID revokes a single session. Returns false when no session
// has that id.
func (s *Store) DeleteSessionByID(ctx context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		return false, nil
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("delete session by id: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteOtherSessions revokes every session except currentID and returns the
// number deleted. An empty currentID deletes nothing: the caller's identity is
// unknown, and nuking every session would be the wrong guess.
func (s *Store) DeleteOtherSessions(ctx context.Context, currentID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if currentID == "" {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id <> ?`, currentID)
	if err != nil {
		return 0, fmt.Errorf("delete other sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
