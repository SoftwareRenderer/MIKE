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
	"time"

	"mike/backend/store"
	"mike/pkg/logging"
)

const (
	sessionTokenBytes = 32
	csrfTokenBytes    = 16
	sessionIDBytes    = 8
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

// NewSessionManager creates a new SessionManager from the resolved master password (the value
// startup.Config.MasterPassword holds). Returns an error if it is empty: an empty password would make
// every session a share of nothing. The ctx is the caller's, and the startup sweep below runs on it.
func NewSessionManager(ctx context.Context, s *store.Store, password string) (*SessionManager, error) {
	if password == "" {
		return nil, fmt.Errorf("%s environment variable is not set", passwordEnv)
	}
	// Clean up expired sessions on startup
	if n, err := s.DeleteExpiredSessions(ctx); err != nil {
		return nil, fmt.Errorf("cleanup expired sessions: %w", err)
	} else if n > 0 {
		logging.For(logging.Auth).Infof("cleaned up %d expired session(s)", n)
	}
	return &SessionManager{store: s, passwordHash: sha256.Sum256([]byte(password))}, nil
}

// Login validates the password and creates a session, recording the caller's IP
// and User-Agent on it. Returns the session id, session token and CSRF token. The
// token reaches the caller only here — the store keeps just its hash.
//
// The ctx belongs to the caller: a session is state this leaves behind, so the login route passes a
// detached one and a tab closing does not decide whether the user is signed in.
func (m *SessionManager) Login(ctx context.Context, password, ip, userAgent string) (id, token, csrfToken string, err error) {
	presented := sha256.Sum256([]byte(password))
	if subtle.ConstantTimeCompare(presented[:], m.passwordHash[:]) != 1 {
		return "", "", "", ErrInvalidPassword
	}

	draw := make([]byte, sessionTokenBytes+csrfTokenBytes+sessionIDBytes)
	if _, err := rand.Read(draw); err != nil {
		return "", "", "", fmt.Errorf("generate session credentials: %w", err)
	}
	token = hex.EncodeToString(draw[:sessionTokenBytes])
	csrfToken = hex.EncodeToString(draw[sessionTokenBytes : sessionTokenBytes+csrfTokenBytes])
	id = hex.EncodeToString(draw[sessionTokenBytes+csrfTokenBytes:])

	expiresAt := time.Now().Add(sessionDuration)

	if err := m.store.CreateSession(ctx, store.NewSession{
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
//
// The request's ctx is the right one here: the only write is that one throttled stamp, and a cancel
// leaves at worst a stale last_seen, which no reader depends on.
func (m *SessionManager) Validate(ctx context.Context, token string) (id, csrfToken string, err error) {
	if token == "" {
		return "", "", ErrInvalidToken
	}

	id, csrfToken, expiresAt, err := m.store.GetSessionByToken(ctx, token)
	if err != nil {
		return "", "", ErrInvalidToken
	}

	if time.Now().After(expiresAt) {
		// Session expired — clean up
		_ = m.store.DeleteSession(ctx, token)
		return "", "", ErrExpiredToken
	}

	return id, csrfToken, nil
}

// Logout deletes a session by token. Detached like Login: a delete that does not land leaves the
// session live, which is the opposite of what was asked for.
func (m *SessionManager) Logout(ctx context.Context, token string) error {
	if token == "" {
		return ErrInvalidToken
	}
	return m.store.DeleteSession(ctx, token)
}
