package auth

// API tokens are the bearer credentials a headless client presents instead of a login cookie.
// Like a session, the secret is only ever a digest in the database, so neither a copied kanban.db
// nor a leaked backup yields a usable token. Unlike a session, a token is scoped to the routes
// backend/server lists as token-accessible and never expires on its own — revoking its row is the
// control.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"mike/backend/store"
)

const (
	// TokenPrefix marks a MIKE API token. It is what a secret scanner matches on, and a
	// client may rely on it to tell its own credentials apart in a log line.
	TokenPrefix = "mike_"

	tokenSecretBytes = 32
	tokenIDBytes     = 8

	// MaxTokenLabelLen bounds a label by runes, not bytes: it is display text, and the limit
	// exists so one row stays readable in Settings → About and in a log line.
	MaxTokenLabelLen = 80
)

// ErrInvalidLabel refuses a label that would render as an unnamed or unlocatable row.
var ErrInvalidLabel = errors.New("token label must be 1-80 characters with no control characters")

// APITokenManager issues and validates API tokens. Revocation and listing stay on the store, the
// way session revocation does: this type owns the credential, not the row.
type APITokenManager struct {
	store *store.Store
}

// NewAPITokenManager returns a manager over the shared store.
func NewAPITokenManager(s *store.Store) *APITokenManager {
	return &APITokenManager{store: s}
}

// Issue creates a token and returns its row together with the plaintext secret. This is the only
// moment the secret exists outside the caller's request: the row keeps hex(sha256(secret)), and the
// timestamp the response reports is the one the row was stamped with.
//
// The id and the secret come from one crypto/rand draw but from disjoint bytes of it — the id is
// listed in Settings and in every log line about the token, so it must never be a slice of the
// credential that authenticates.
func (m *APITokenManager) Issue(ctx context.Context, label string) (store.APITokenInfo, string, error) {
	label, err := NormalizeTokenLabel(label)
	if err != nil {
		return store.APITokenInfo{}, "", err
	}

	draw := make([]byte, tokenSecretBytes+tokenIDBytes)
	if _, err := rand.Read(draw); err != nil {
		return store.APITokenInfo{}, "", fmt.Errorf("generate api token: %w", err)
	}
	secret := TokenPrefix + hex.EncodeToString(draw[:tokenSecretBytes])
	id := hex.EncodeToString(draw[tokenSecretBytes:])
	createdAt := time.Now().UTC()

	if err := m.store.CreateAPIToken(ctx, store.NewAPIToken{
		ID: id, Token: secret, Label: label, CreatedAt: createdAt,
	}); err != nil {
		return store.APITokenInfo{}, "", fmt.Errorf("create api token: %w", err)
	}
	return store.APITokenInfo{ID: id, Label: label, CreatedAt: createdAt}, secret, nil
}

// Validate resolves a presented secret to its row, or reports ErrInvalidToken for anything that is
// not a live token: empty, unknown, or revoked (a revoked row is gone, so "unknown" covers it).
func (m *APITokenManager) Validate(ctx context.Context, token string) (store.APITokenInfo, error) {
	if token == "" {
		return store.APITokenInfo{}, ErrInvalidToken
	}
	info, err := m.store.GetAPITokenByToken(ctx, token)
	if err != nil {
		return store.APITokenInfo{}, ErrInvalidToken
	}
	return info, nil
}

// NormalizeTokenLabel trims a label and refuses the shapes that would make a row unreadable:
// nothing at all, more than MaxTokenLabelLen runes, or an embedded control character, which a
// terminal or a log line would render as its own content.
func NormalizeTokenLabel(label string) (string, error) {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" || len([]rune(trimmed)) > MaxTokenLabelLen || strings.IndexFunc(trimmed, unicode.IsControl) >= 0 {
		return "", ErrInvalidLabel
	}
	return trimmed, nil
}
