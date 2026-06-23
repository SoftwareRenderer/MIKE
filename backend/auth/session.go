package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"kanban/backend/store"
)

const (
	sessionTokenBytes = 32
	csrfTokenBytes    = 16
	sessionDuration   = 30 * 24 * time.Hour // 30 days
	passwordEnv       = "KANBAN_PASSWORD"
)

var (
	ErrInvalidPassword = errors.New("invalid password")
	ErrInvalidToken    = errors.New("invalid session token")
	ErrExpiredToken    = errors.New("session token expired")
)

// SessionManager handles session lifecycle: login, validation, and logout.
type SessionManager struct {
	store    *store.Store
	password string
}

// NewSessionManager creates a new SessionManager.
// Returns an error if KANBAN_PASSWORD is not set.
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
		// best-effort, log would go here
		_ = n
	}
	return &SessionManager{store: s, password: password}, nil
}

// Login validates the password and creates a new session.
// Returns the session id, session token, and CSRF token on success.
func (m *SessionManager) Login(password string) (id, token, csrfToken string, err error) {
	if password != m.password {
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

	if err := m.store.CreateSession(context.Background(), id, token, csrfToken, expiresAt); err != nil {
		return "", "", "", fmt.Errorf("create session: %w", err)
	}
	return id, token, csrfToken, nil
}

// Validate checks a session token and returns the session id and CSRF token if valid.
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
