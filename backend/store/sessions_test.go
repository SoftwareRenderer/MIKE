package store

import (
	"context"
	"testing"
	"time"
)

func TestSessionCreateAndGet(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	id := "sess-1"
	token := "abc123token"
	csrfToken := "csrf123token"
	expiresAt := time.Now().Add(24 * time.Hour)

	if err := s.CreateSession(ctx, id, token, csrfToken, expiresAt); err != nil {
		t.Fatalf("create session: %v", err)
	}

	gotID, gotCsrf, gotExpires, err := s.GetSessionByToken(ctx, token)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if gotID != id {
		t.Errorf("id = %q; want %q", gotID, id)
	}
	if gotCsrf != csrfToken {
		t.Errorf("csrf_token = %q; want %q", gotCsrf, csrfToken)
	}
	if gotExpires.Unix() != expiresAt.Unix() {
		t.Errorf("expires_at = %v; want %v", gotExpires, expiresAt)
	}
}

func TestSessionGetNotFound(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	_, _, _, err := s.GetSessionByToken(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent token")
	}
}

func TestSessionDelete(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	id := "sess-2"
	token := "deleteme"
	csrfToken := "csrf-deleteme"
	expiresAt := time.Now().Add(24 * time.Hour)

	if err := s.CreateSession(ctx, id, token, csrfToken, expiresAt); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := s.DeleteSession(ctx, token); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	_, _, _, err := s.GetSessionByToken(ctx, token)
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestSessionDeleteExpired(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Create an expired session
	if err := s.CreateSession(ctx, "sess-expired", "expired-token", "csrf-expired", time.Now().Add(-1*time.Hour)); err != nil {
		t.Fatalf("create expired session: %v", err)
	}
	// Create a valid session
	if err := s.CreateSession(ctx, "sess-valid", "valid-token", "csrf-valid", time.Now().Add(24*time.Hour)); err != nil {
		t.Fatalf("create valid session: %v", err)
	}

	n, err := s.DeleteExpiredSessions(ctx)
	if err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted = %d; want 1", n)
	}

	// Expired session should be gone
	_, _, _, err = s.GetSessionByToken(ctx, "expired-token")
	if err == nil {
		t.Fatal("expected error for expired session")
	}

	// Valid session should still exist
	_, _, _, err = s.GetSessionByToken(ctx, "valid-token")
	if err != nil {
		t.Fatalf("valid session should still exist: %v", err)
	}
}

func TestSessionUniqueToken(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	token := "duplicate-token"
	csrfToken := "csrf-duplicate"
	expiresAt := time.Now().Add(24 * time.Hour)

	if err := s.CreateSession(ctx, "sess-a", token, csrfToken, expiresAt); err != nil {
		t.Fatalf("create first session: %v", err)
	}

	// Second session with same token should fail
	err := s.CreateSession(ctx, "sess-b", token, csrfToken, expiresAt)
	if err == nil {
		t.Fatal("expected error for duplicate token")
	}
}
