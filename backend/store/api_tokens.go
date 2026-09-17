package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// API tokens are the bearer credentials a headless client presents in
// `Authorization: Bearer`. A row keeps only hex(sha256(token)) — the same rule as a login
// session — so a copied kanban.db cannot be replayed as a live credential, and the plaintext
// exists only in the response that creates the row. The table is created on every start like the
// sessions table, not by the probe chain: CREATE TABLE IF NOT EXISTS cannot disturb the rows an
// existing database already has.
const apiTokenTable = `
CREATE TABLE IF NOT EXISTS api_tokens (
    id           TEXT PRIMARY KEY,
    label        TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER
);
`

// initAPITokens creates the api_tokens table if it does not exist. The UNIQUE token_hash is the
// lookup index; a second index on it would be a second copy of the same b-tree.
func (s *Store) initAPITokens() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(apiTokenTable)
	return err
}

// NewAPIToken carries the fields of an api_tokens row.
type NewAPIToken struct {
	ID    string
	Token string // raw bearer token — hashed, never persisted
	Label string
	// CreatedAt is when the token was issued; the zero value means now. The caller that shows the
	// token to a person passes the time it already reported, so the row and the answer agree.
	CreatedAt time.Time
}

// CreateAPIToken inserts a new token row. Only the digest of the presented secret is stored; the
// UNIQUE constraint on it is what makes the same secret impossible to issue twice.
func (s *Store) CreateAPIToken(ctx context.Context, t NewAPIToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	createdAt := t.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_tokens (id, label, token_hash, created_at) VALUES (?, ?, ?, ?)`,
		t.ID, t.Label, hashToken(t.Token), createdAt.Unix())
	if err != nil {
		return fmt.Errorf("create api token: %w", err)
	}
	return nil
}

// APITokenInfo is one api_tokens row for the management API and the auth path. It has no field for
// a token or a token hash, so neither can leak by accident.
type APITokenInfo struct {
	ID        string
	Label     string
	CreatedAt time.Time
	LastUsed  time.Time // zero when the token has never authenticated a request
}

// GetAPITokenByToken returns the token a presented secret belongs to, or sql.ErrNoRows when
// nothing matches. The lookup is by digest only. It stamps last_used_at, throttled by the same
// interval as a session's last_seen.
func (s *Store) GetAPITokenByToken(ctx context.Context, token string) (APITokenInfo, error) {
	var info APITokenInfo
	if token == "" {
		return info, sql.ErrNoRows
	}
	// Write lock: this read may refresh last_used_at (see lastSeenInterval).
	s.mu.Lock()
	defer s.mu.Unlock()

	var createdAt, lastUsed int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, label, created_at, COALESCE(last_used_at, 0) FROM api_tokens WHERE token_hash = ?`,
		hashToken(token)).
		Scan(&info.ID, &info.Label, &createdAt, &lastUsed)
	if err != nil {
		return APITokenInfo{}, err
	}
	info.CreatedAt = time.Unix(createdAt, 0)
	if lastUsed > 0 {
		info.LastUsed = time.Unix(lastUsed, 0)
	}

	if now := time.Now().Unix(); now-lastUsed >= lastSeenInterval {
		// Activity tracking is best-effort: a failed stamp must not refuse a valid token.
		_, _ = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, now, info.ID)
	}
	return info, nil
}

// ListAPITokens returns every token, newest first.
func (s *Store) ListAPITokens(ctx context.Context) ([]APITokenInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, label, created_at, COALESCE(last_used_at, 0)
		   FROM api_tokens ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	tokens, err := scanRows(rows, scanAPITokenRow)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	return tokens, nil
}

// scanAPITokenRow reads one api_tokens row. Timestamps are unix seconds, and a token that has
// never been used has last_used_at 0 — a zero time, not the epoch.
func scanAPITokenRow(r *sql.Rows) (APITokenInfo, error) {
	var (
		info      APITokenInfo
		createdAt int64
		lastUsed  int64
	)
	if err := r.Scan(&info.ID, &info.Label, &createdAt, &lastUsed); err != nil {
		return APITokenInfo{}, fmt.Errorf("scan api token row: %w", err)
	}
	info.CreatedAt = time.Unix(createdAt, 0)
	if lastUsed > 0 {
		info.LastUsed = time.Unix(lastUsed, 0)
	}
	return info, nil
}

// DeleteAPITokenByID revokes a single token. Returns false when no token has that id.
func (s *Store) DeleteAPITokenByID(ctx context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		return false, nil
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("delete api token by id: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
