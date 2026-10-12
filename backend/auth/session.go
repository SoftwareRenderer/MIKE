// Package auth holds the session tokens that authenticate API and WebSocket requests, the passkey
// ceremonies that open one, and the pairing codes that authorize a passkey. Session tokens are stored
// hashed, so a copied kanban.db cannot be replayed as a live session, and a pairing code is spent on the
// registration it authorizes, so nothing outlives the ceremony it was typed for.
package auth

import (
	"context"
	"crypto/rand"
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
)

var (
	ErrInvalidToken = errors.New("invalid session token")
	ErrExpiredToken = errors.New("session token expired")
)

// SessionManager handles session lifecycle: start, validation, and logout.
type SessionManager struct {
	store *store.Store
}

func NewSessionManager(ctx context.Context, s *store.Store) (*SessionManager, error) {
	// Clean up expired sessions on startup
	if n, err := s.DeleteExpiredSessions(ctx); err != nil {
		return nil, fmt.Errorf("cleanup expired sessions: %w", err)
	} else if n > 0 {
		logging.For(logging.Auth).Infof("cleaned up %d expired session(s)", n)
	}
	return &SessionManager{store: s}, nil
}

func (m *SessionManager) StartSession(ctx context.Context, ip, userAgent string) (id, token, csrfToken string, err error) {
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

func (m *SessionManager) Logout(ctx context.Context, token string) error {
	if token == "" {
		return ErrInvalidToken
	}
	return m.store.DeleteSession(ctx, token)
}
