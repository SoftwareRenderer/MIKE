package store

import (
	"context"
	"fmt"
	"time"
)

const sessionTable = `
CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    token      TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
`

// initSessions creates the sessions table if it does not exist.
func (s *Store) initSessions() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(sessionTable)
	return err
}

// CreateSession inserts a new session row and returns its id.
func (s *Store) CreateSession(ctx context.Context, id, token string, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO sessions (id, token, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		id, token, time.Now().Unix(), expiresAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// GetSessionByToken returns the session id and expiry time for a given token,
// or sql.ErrNoRows if the token does not exist.
func (s *Store) GetSessionByToken(ctx context.Context, token string) (id string, expiresAt time.Time, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var createdAt, expiresAtUnix int64
	err = s.db.QueryRowContext(ctx, `SELECT id, created_at, expires_at FROM sessions WHERE token = ?`, token).
		Scan(&id, &createdAt, &expiresAtUnix)
	if err != nil {
		return "", time.Time{}, err
	}
	return id, time.Unix(expiresAtUnix, 0), nil
}

// DeleteSession removes a session by token.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
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
