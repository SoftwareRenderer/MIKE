package auth

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mike/backend/store"
)

func testManager(t *testing.T) *SessionManager {
	t.Helper()
	m, _ := testManagerWithStore(t)
	return m
}

// testManagerWithStore also returns the store: a few tests inspect or backdate columns
// directly, and raw SQL belongs in a fixture rather than on the store's API.
func testManagerWithStore(t *testing.T) (*SessionManager, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	m, err := NewSessionManager(t.Context(), s)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	return m, s
}

// testIP / testUA are the client metadata StartSession records on a new session.
const (
	testIP = "203.0.113.9"
	testUA = "MIKE-test-agent/1.0"
)

// lastSeenOf reads the raw last_seen stamp of a session from the database.
func lastSeenOf(t *testing.T, db *store.Store, id string) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRow(`SELECT COALESCE(last_seen, 0) FROM sessions WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatalf("read last_seen for %s: %v", id, err)
	}
	return v
}

func TestStartSessionIssuesCredentials(t *testing.T) {
	m := testManager(t)

	id, token, csrfToken, err := m.StartSession(t.Context(), testIP, testUA)
	if err != nil {
		t.Fatalf("start session: %v", err)
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

// The id is listed by GET /api/sessions, so it must not share bytes with the token.
func TestSessionCredentialsShareNoBytes(t *testing.T) {
	m := testManager(t)

	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		id, token, csrfToken, err := m.StartSession(t.Context(), testIP, testUA)
		if err != nil {
			t.Fatalf("start session: %v", err)
		}
		if len(id) != 16 { // 8 bytes of hex, the shape stored ids already have
			t.Fatalf("session id %q: want 16 characters", id)
		}
		if strings.HasPrefix(token, id) {
			t.Fatalf("the session id is the front of the token: id=%s token=%s", id, token)
		}
		if strings.Contains(token, id) || strings.Contains(csrfToken, id) {
			t.Fatalf("the session id shares bytes with a credential: id=%s token=%s csrf=%s", id, token, csrfToken)
		}
		if seen[id] {
			t.Fatalf("session id %q handed out twice", id)
		}
		seen[id] = true
	}
}

func TestValidateValidToken(t *testing.T) {
	m := testManager(t)

	_, token, _, err := m.StartSession(t.Context(), testIP, testUA)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	id, csrfToken, err := m.Validate(t.Context(), token)
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

	_, _, err := m.Validate(t.Context(), "nonexistent-token")
	if err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken, got: %v", err)
	}
}

func TestValidateEmptyToken(t *testing.T) {
	m := testManager(t)

	_, _, err := m.Validate(t.Context(), "")
	if err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for empty token, got: %v", err)
	}
}

func TestLogout(t *testing.T) {
	m := testManager(t)

	_, token, _, err := m.StartSession(t.Context(), testIP, testUA)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	if err := m.Logout(t.Context(), token); err != nil {
		t.Fatalf("logout: %v", err)
	}

	// Token should be invalid after logout
	_, _, err = m.Validate(t.Context(), token)
	if err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken after logout, got: %v", err)
	}
}

func TestLogoutEmptyToken(t *testing.T) {
	m := testManager(t)

	err := m.Logout(t.Context(), "")
	if err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for empty token, got: %v", err)
	}
}

func TestMultipleSessions(t *testing.T) {
	m := testManager(t)

	_, token1, _, err := m.StartSession(t.Context(), testIP, testUA)
	if err != nil {
		t.Fatalf("login 1: %v", err)
	}
	_, token2, _, err := m.StartSession(t.Context(), testIP, testUA)
	if err != nil {
		t.Fatalf("login 2: %v", err)
	}

	if _, _, err := m.Validate(t.Context(), token1); err != nil {
		t.Errorf("token1 should be valid: %v", err)
	}
	if _, _, err := m.Validate(t.Context(), token2); err != nil {
		t.Errorf("token2 should be valid: %v", err)
	}

	// Logout token1 only
	if err := m.Logout(t.Context(), token1); err != nil {
		t.Fatalf("logout token1: %v", err)
	}

	// token1 invalid, token2 still valid
	_, _, err = m.Validate(t.Context(), token1)
	if err != ErrInvalidToken {
		t.Errorf("token1 should be invalid after logout, got: %v", err)
	}
	if _, _, err := m.Validate(t.Context(), token2); err != nil {
		t.Errorf("token2 should still be valid: %v", err)
	}
}

// TestStartSessionRecordsIPAndUserAgent checks the metadata the Sessions UI shows.
func TestStartSessionRecordsIPAndUserAgent(t *testing.T) {
	m, db := testManagerWithStore(t)

	id, _, _, err := m.StartSession(t.Context(), "198.51.100.7", "Mozilla/5.0 (Test Browser)")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	list, err := db.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("sessions = %d; want 1", len(list))
	}
	if list[0].ID != id {
		t.Fatalf("id = %q; want %q", list[0].ID, id)
	}
	if list[0].IP != "198.51.100.7" {
		t.Errorf("ip = %q; want 198.51.100.7", list[0].IP)
	}
	if list[0].UserAgent != "Mozilla/5.0 (Test Browser)" {
		t.Errorf("user_agent = %q", list[0].UserAgent)
	}
}

// TestValidateThrottlesLastSeen: activity is recorded, but at most once a
// minute — the store serialises on a single connection, so a write per request
// would be a write per page render.
func TestValidateThrottlesLastSeen(t *testing.T) {
	m, db := testManagerWithStore(t)
	id, token, _, err := m.StartSession(t.Context(), testIP, testUA)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	backdate := func(secs int64) {
		t.Helper()
		if _, err := db.Exec(`UPDATE sessions SET last_seen = ? WHERE id = ?`, time.Now().Unix()-secs, id); err != nil {
			t.Fatalf("backdate last_seen: %v", err)
		}
	}

	// Older than the interval → this validation refreshes the stamp.
	backdate(120)
	if _, _, err := m.Validate(t.Context(), token); err != nil {
		t.Fatalf("validate: %v", err)
	}
	refreshed := lastSeenOf(t, db, id)
	if time.Now().Unix()-refreshed > 5 {
		t.Fatalf("last_seen not refreshed: %d (now %d)", refreshed, time.Now().Unix())
	}

	// Inside the same minute → no further writes.
	backdate(0)
	for i := 0; i < 3; i++ {
		if _, _, err := m.Validate(t.Context(), token); err != nil {
			t.Fatalf("validate %d: %v", i, err)
		}
	}
	if got := lastSeenOf(t, db, id); got != refreshed {
		t.Fatalf("last_seen rewritten within the throttle window: %d != %d", got, refreshed)
	}

	// Once the interval has passed, validation stamps again.
	backdate(120)
	if stale := lastSeenOf(t, db, id); time.Now().Unix()-stale < 60 {
		t.Fatalf("backdate did not take effect: last_seen = %d", stale)
	}
	if _, _, err := m.Validate(t.Context(), token); err != nil {
		t.Fatalf("validate after interval: %v", err)
	}
	if got := lastSeenOf(t, db, id); time.Now().Unix()-got > 5 {
		t.Fatalf("last_seen was not restamped after the throttle window: %d (now %d)", got, time.Now().Unix())
	}
}
