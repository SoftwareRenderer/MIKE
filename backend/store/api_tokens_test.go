package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// API token rows: what is stored, what a lookup returns, and what a revocation removes.

// newAPITokenRow writes a token with a caller-chosen secret so a test can present it back.
func newAPITokenRow(t *testing.T, s *Store, id, secret, label string) {
	t.Helper()
	if err := s.CreateAPIToken(context.Background(), NewAPIToken{ID: id, Token: secret, Label: label}); err != nil {
		t.Fatalf("create api token: %v", err)
	}
}

func TestAPITokenStoresOnlyTheDigest(t *testing.T) {
	s, dbPath := newStoreAt(t)
	const secret = "mike_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	newAPITokenRow(t, s, "tok-1", secret, "ci runner")

	sum := sha256.Sum256([]byte(secret))
	wantHash := hex.EncodeToString(sum[:])

	var stored string
	if err := s.QueryRow(`SELECT token_hash FROM api_tokens WHERE id = ?`, "tok-1").Scan(&stored); err != nil {
		t.Fatalf("read token_hash: %v", err)
	}
	if stored != wantHash {
		t.Errorf("token_hash = %q, want the digest %q", stored, wantHash)
	}

	// The secret must be absent from the bytes an attacker would actually steal, not merely
	// invisible to the accessor.
	if raw := string(dbBytes(t, s, dbPath)); strings.Contains(raw, secret) {
		t.Error("the plaintext token is present in the database file")
	}
}

func TestAPITokenLookupBySecret(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	newAPITokenRow(t, s, "tok-1", "mike_secret-under-test", "ci runner")

	info, err := s.GetAPITokenByToken(ctx, "mike_secret-under-test")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if info.ID != "tok-1" || info.Label != "ci runner" {
		t.Errorf("lookup returned %+v, want id tok-1 with label ci runner", info)
	}
	if info.CreatedAt.IsZero() {
		t.Error("a stored token reports no creation time")
	}

	for _, tc := range []struct{ name, secret string }{
		{"empty", ""},
		{"unknown", "mike_never-issued"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.GetAPITokenByToken(ctx, tc.secret); !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("lookup of %q returned %v, want sql.ErrNoRows", tc.secret, err)
			}
		})
	}
}

// A token is looked up on every request it authenticates, and the store is one serialised
// connection, so the activity stamp must not turn each request into a write.
func TestAPITokenLastUsedIsStampedOncePerInterval(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	newAPITokenRow(t, s, "tok-1", "mike_stamped", "ci runner")

	first, err := s.GetAPITokenByToken(ctx, "mike_stamped")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !first.LastUsed.IsZero() {
		t.Errorf("a never-used token reports last used %v", first.LastUsed)
	}

	second, err := s.GetAPITokenByToken(ctx, "mike_stamped")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if second.LastUsed.IsZero() {
		t.Fatal("the first use did not stamp last_used_at")
	}

	third, err := s.GetAPITokenByToken(ctx, "mike_stamped")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !third.LastUsed.Equal(second.LastUsed) {
		t.Errorf("a second use inside the interval moved last_used_at from %v to %v", second.LastUsed, third.LastUsed)
	}
}

func TestAPITokenListAndRevoke(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	newAPITokenRow(t, s, "tok-old", "mike_old", "older")
	newAPITokenRow(t, s, "tok-new", "mike_new", "newer")

	tokens, err := s.ListAPITokens(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("listed %d tokens, want 2: %+v", len(tokens), tokens)
	}
	if tokens[0].ID != "tok-new" {
		t.Errorf("newest first: got %q then %q", tokens[0].ID, tokens[1].ID)
	}

	found, err := s.DeleteAPITokenByID(ctx, "tok-new")
	if err != nil || !found {
		t.Fatalf("revoke tok-new = (%v, %v), want (true, nil)", found, err)
	}
	if _, err := s.GetAPITokenByToken(ctx, "mike_new"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("a revoked token still resolves: %v", err)
	}
	if found, err := s.DeleteAPITokenByID(ctx, "tok-new"); found || err != nil {
		t.Errorf("revoking an absent token = (%v, %v), want (false, nil)", found, err)
	}
	if found, err := s.DeleteAPITokenByID(ctx, ""); found || err != nil {
		t.Errorf("revoking a blank id = (%v, %v), want (false, nil)", found, err)
	}

	if remaining, err := s.ListAPITokens(ctx); err != nil || len(remaining) != 1 {
		t.Errorf("after revoking: %d tokens (%v), want 1", len(remaining), err)
	}
}

// The same secret twice would be two rows authenticating identically; the UNIQUE digest is what
// refuses it, and the caller must see the insert fail rather than a silent duplicate.
func TestAPITokenSecretIsUnique(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	newAPITokenRow(t, s, "tok-1", "mike_shared", "first")
	if err := s.CreateAPIToken(ctx, NewAPIToken{ID: "tok-2", Token: "mike_shared", Label: "second"}); err == nil {
		t.Fatal("the same secret was issued twice")
	}
}

// The table is created outside the user_version-gated probe chain, so a database written by an
// older build still gets it on reopen.
func TestAPITokenTableSurvivesReopen(t *testing.T) {
	s, dbPath := newStoreAt(t)
	newAPITokenRow(t, s, "tok-1", "mike_across-reopen", "kept")
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })

	info, err := reopened.GetAPITokenByToken(context.Background(), "mike_across-reopen")
	if err != nil {
		t.Fatalf("the row did not survive the reopen: %v", err)
	}
	if info.Label != "kept" {
		t.Errorf("label after reopen = %q", info.Label)
	}
}
