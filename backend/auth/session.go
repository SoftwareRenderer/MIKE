// Package auth holds the master password digest and the session tokens that authenticate
// API and WebSocket requests. The password itself is never retained - only its SHA-256
// digest, compared in constant time - and tokens are stored hashed, so a copied kanban.db
// cannot be replayed as a live session.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"kanban/backend/store"
	"kanban/pkg/logging"
)

const (
	sessionTokenBytes = 32
	csrfTokenBytes    = 16
	sessionDuration   = 30 * 24 * time.Hour // 30 days
	passwordEnv       = "MIKE_LOGIN_PASSWORD"
)

var (
	ErrInvalidPassword = errors.New("invalid password")
	ErrInvalidToken    = errors.New("invalid session token")
	ErrExpiredToken    = errors.New("session token expired")
)

// SessionManager handles session lifecycle: login, validation, and logout.
type SessionManager struct {
	store *store.Store
	// passwordHash is sha256(MIKE_LOGIN_PASSWORD); the plaintext is not retained.
	// Logins compare digests, so both sides are always 32 bytes and the
	// constant-time comparison can neither short-circuit on length nor leak the
	// password's length.
	passwordHash [sha256.Size]byte
}

// NewSessionManager creates a new SessionManager.
// Returns an error if MIKE_LOGIN_PASSWORD is not set.
func NewSessionManager(s *store.Store) (*SessionManager, error) {
	password := os.Getenv(passwordEnv)
	if password == "" {
		return nil, fmt.Errorf("%s environment variable is not set", passwordEnv)
	}
	// Clean up expired sessions on startup
	ctx := context.Background()
	if n, err := s.DeleteExpiredSessions(ctx); err != nil {
		return nil, fmt.Errorf("cleanup expired sessions: %w", err)
	} else if n > 0 {
		logging.For(logging.Auth).Infof("cleaned up %d expired session(s)", n)
	}
	// One-time migration for sessions created before tokens were hashed. Safe to run
	// every startup: rows that already carry a hash are skipped and no live session
	// is invalidated.
	if n, err := s.BackfillSessionTokens(ctx); err != nil {
		return nil, fmt.Errorf("backfill session token hashes: %w", err)
	} else if n > 0 {
		logging.For(logging.Auth).Infof("hashed %d legacy session token(s); plaintext tokens removed from the database", n)
	}
	return &SessionManager{store: s, passwordHash: sha256.Sum256([]byte(password))}, nil
}

// Login validates the password and creates a session, recording the caller's IP
// and User-Agent on it. Returns the session id, session token and CSRF token. The
// token reaches the caller only here — the store keeps just its hash.
func (m *SessionManager) Login(password, ip, userAgent string) (id, token, csrfToken string, err error) {
	presented := sha256.Sum256([]byte(password))
	if subtle.ConstantTimeCompare(presented[:], m.passwordHash[:]) != 1 {
		return "", "", "", ErrInvalidPassword
	}

	tokenBytes := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", "", "", fmt.Errorf("generate token: %w", err)
	}
	token = hex.EncodeToString(tokenBytes)

	csrfBytes := make([]byte, csrfTokenBytes)
	if _, err := rand.Read(csrfBytes); err != nil {
		return "", "", "", fmt.Errorf("generate csrf token: %w", err)
	}
	csrfToken = hex.EncodeToString(csrfBytes)

	id = hex.EncodeToString(tokenBytes[:8]) // short id from first 8 bytes
	expiresAt := time.Now().Add(sessionDuration)

	if err := m.store.CreateSession(context.Background(), store.NewSession{
		ID:        id,
		Token:     token,
		CSRFToken: csrfToken,
		IP:        ip,
		UserAgent: userAgent,
		ExpiresAt: expiresAt,
	}); err != nil {
		return "", "", "", fmt.Errorf("create session: %w", err)
	}
	return id, token, csrfToken, nil
}

// Validate checks a session token and returns the session id and CSRF token if
// valid. The store looks the session up by token hash and stamps last_seen
// (throttled) as a side effect.
func (m *SessionManager) Validate(token string) (id, csrfToken string, err error) {
	if token == "" {
		return "", "", ErrInvalidToken
	}

	id, csrfToken, expiresAt, err := m.store.GetSessionByToken(context.Background(), token)
	if err != nil {
		return "", "", ErrInvalidToken
	}

	if time.Now().After(expiresAt) {
		// Session expired — clean up
		_ = m.store.DeleteSession(context.Background(), token)
		return "", "", ErrExpiredToken
	}

	return id, csrfToken, nil
}

// Logout deletes a session by token.
func (m *SessionManager) Logout(token string) error {
	if token == "" {
		return ErrInvalidToken
	}
	return m.store.DeleteSession(context.Background(), token)
}
