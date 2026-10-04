package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"mike/backend/store"
)

func testTokenManager(t *testing.T) (*APITokenManager, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return NewAPITokenManager(s), s
}

func TestAPITokenIssueAndValidate(t *testing.T) {
	m, s := testTokenManager(t)
	ctx := context.Background()

	issued, secret, err := m.Issue(ctx, "  nightly importer  ")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if issued.ID == "" {
		t.Fatal("issue returned no id")
	}
	if !strings.HasPrefix(secret, TokenPrefix) {
		t.Errorf("token %q does not start with %q", secret, TokenPrefix)
	}
	if len(secret) != len(TokenPrefix)+2*tokenSecretBytes {
		t.Errorf("token length = %d, want %d", len(secret), len(TokenPrefix)+2*tokenSecretBytes)
	}
	if strings.Contains(issued.ID, secret) || strings.Contains(secret, issued.ID) {
		t.Error("the listed id overlaps the secret that authenticates")
	}

	info, err := m.Validate(ctx, secret)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if info.ID != issued.ID {
		t.Errorf("validate resolved %q, want %q", info.ID, issued.ID)
	}
	if info.Label != "nightly importer" {
		t.Errorf("label = %q, want the trimmed label", info.Label)
	}
	if !info.CreatedAt.Equal(issued.CreatedAt.Truncate(time.Second)) {
		t.Errorf("the row reports %v and the issued row reported %v; the screen shows one and the list the other",
			info.CreatedAt, issued.CreatedAt)
	}

	// Only the digest is on disk, so the row cannot be replayed as the credential.
	var stored string
	if err := s.QueryRow(`SELECT token_hash FROM api_tokens WHERE id = ?`, issued.ID).Scan(&stored); err != nil {
		t.Fatalf("read row: %v", err)
	}
	sum := sha256.Sum256([]byte(secret))
	if stored != hex.EncodeToString(sum[:]) {
		t.Errorf("stored %q, want the token digest", stored)
	}
}

func TestAPITokenValidateRefuses(t *testing.T) {
	m, s := testTokenManager(t)
	ctx := context.Background()

	_, secret, err := m.Issue(ctx, "runner")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	if _, err := m.Validate(ctx, ""); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("an empty token = %v, want ErrInvalidToken", err)
	}
	if _, err := m.Validate(ctx, TokenPrefix+"never-issued"); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("an unknown token = %v, want ErrInvalidToken", err)
	}

	// Revocation is the row's absence: the same secret is then worth nothing.
	if _, err := s.DeleteAPITokenByID(ctx, mustValidateID(t, m, ctx, secret)); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := m.Validate(ctx, secret); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a revoked token = %v, want ErrInvalidToken", err)
	}
}

// A session token and an API token are different credentials over the same alphabet: neither may
// authenticate as the other, and both live in one database.
func TestAPITokenAndSessionTokensAreNotInterchangeable(t *testing.T) {
	sessions, s := testManagerWithStore(t)
	ctx := context.Background()

	_, sessionToken, _, err := sessions.StartSession(ctx, testIP, testUA)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, _, err := sessions.Validate(ctx, sessionToken); err != nil {
		t.Fatalf("the session token should authenticate a session: %v", err)
	}

	tokens := NewAPITokenManager(s)
	if _, err := tokens.Validate(ctx, sessionToken); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a session token authenticated an API request: %v", err)
	}

	_, apiSecret, err := tokens.Issue(ctx, "runner")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, _, err := sessions.Validate(ctx, apiSecret); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("an API token authenticated a session: %v", err)
	}
}

func TestAPITokenLabelRules(t *testing.T) {
	m, _ := testTokenManager(t)
	ctx := context.Background()

	for _, tc := range []struct{ name, label string }{
		{"empty", ""},
		{"whitespace only", "   \t "},
		{"too long", strings.Repeat("x", MaxTokenLabelLen+1)},
		{"embedded newline", "ci\nrunner"},
		{"trailing control", "ci\x07"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := m.Issue(ctx, tc.label); !errors.Is(err, ErrInvalidLabel) {
				t.Errorf("issue(%q) = %v, want ErrInvalidLabel", tc.label, err)
			}
		})
	}

	if _, secret, err := m.Issue(ctx, strings.Repeat("x", MaxTokenLabelLen)); err != nil || secret == "" {
		t.Errorf("a label at the limit was refused: %v", err)
	}
}

func mustValidateID(t *testing.T, m *APITokenManager, ctx context.Context, secret string) string {
	t.Helper()
	info, err := m.Validate(ctx, secret)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	return info.ID
}
