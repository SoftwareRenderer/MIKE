package auth

import (
	"os"
	"path/filepath"
	"testing"

	"kanban/backend/store"
)

func testManager(t *testing.T) *SessionManager {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Set password env var for the duration of the test
	old := os.Getenv("KANBAN_PASSWORD")
	t.Cleanup(func() { os.Setenv("KANBAN_PASSWORD", old) })
	os.Setenv("KANBAN_PASSWORD", "test-password")

	m, err := NewSessionManager(s)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	return m
}

func TestNewSessionManagerNoPassword(t *testing.T) {
	old := os.Getenv("KANBAN_PASSWORD")
	t.Cleanup(func() { os.Setenv("KANBAN_PASSWORD", old) })
	os.Unsetenv("KANBAN_PASSWORD")

	dir := t.TempDir()
	s, err := store.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	_, err = NewSessionManager(s)
	if err == nil {
		t.Fatal("expected error when KANBAN_PASSWORD is not set")
	}
}

func TestLoginValidPassword(t *testing.T) {
	m := testManager(t)

	id, token, csrfToken, err := m.Login("test-password")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty session id")
	}
	if token == "" {
		t.Error("expected non-empty token")
	}
	if len(token) != 64 { // 32 bytes hex-encoded = 64 chars
		t.Errorf("token length = %d; want 64", len(token))
	}
	if csrfToken == "" {
		t.Error("expected non-empty csrf token")
	}
	if len(csrfToken) != 32 { // 16 bytes hex-encoded = 32 chars
		t.Errorf("csrf token length = %d; want 32", len(csrfToken))
	}
}

func TestLoginInvalidPassword(t *testing.T) {
	m := testManager(t)

	_, _, _, err := m.Login("wrong-password")
	if err != ErrInvalidPassword {
		t.Fatalf("expected ErrInvalidPassword, got: %v", err)
	}
}

func TestValidateValidToken(t *testing.T) {
	m := testManager(t)

	_, token, _, err := m.Login("test-password")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	id, csrfToken, err := m.Validate(token)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty session id from validate")
	}
	if csrfToken == "" {
		t.Error("expected non-empty csrf token from validate")
	}
}

func TestValidateInvalidToken(t *testing.T) {
	m := testManager(t)

	_, _, err := m.Validate("nonexistent-token")
	if err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken, got: %v", err)
	}
}

func TestValidateEmptyToken(t *testing.T) {
	m := testManager(t)

	_, _, err := m.Validate("")
	if err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for empty token, got: %v", err)
	}
}

func TestLogout(t *testing.T) {
	m := testManager(t)

	_, token, _, err := m.Login("test-password")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	if err := m.Logout(token); err != nil {
		t.Fatalf("logout: %v", err)
	}

	// Token should be invalid after logout
	_, _, err = m.Validate(token)
	if err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken after logout, got: %v", err)
	}
}

func TestLogoutEmptyToken(t *testing.T) {
	m := testManager(t)

	err := m.Logout("")
	if err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for empty token, got: %v", err)
	}
}

func TestMultipleSessions(t *testing.T) {
	m := testManager(t)

	_, token1, _, err := m.Login("test-password")
	if err != nil {
		t.Fatalf("login 1: %v", err)
	}
	_, token2, _, err := m.Login("test-password")
	if err != nil {
		t.Fatalf("login 2: %v", err)
	}

	// Both should be valid
	if _, _, err := m.Validate(token1); err != nil {
		t.Errorf("token1 should be valid: %v", err)
	}
	if _, _, err := m.Validate(token2); err != nil {
		t.Errorf("token2 should be valid: %v", err)
	}

	// Logout token1 only
	if err := m.Logout(token1); err != nil {
		t.Fatalf("logout token1: %v", err)
	}

	// token1 invalid, token2 still valid
	_, _, err = m.Validate(token1)
	if err != ErrInvalidToken {
		t.Errorf("token1 should be invalid after logout, got: %v", err)
	}
	if _, _, err := m.Validate(token2); err != nil {
		t.Errorf("token2 should still be valid: %v", err)
	}
}
