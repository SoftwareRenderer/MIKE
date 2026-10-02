package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newStoreAt creates a Store at an explicit path so tests can inspect the
// database file itself, not just its contents through SQL.
func newStoreAt(t *testing.T) (*Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dbPath
}

// dbBytesRaw returns the bytes of the database plus any sidecars, exactly as
// they sit on disk right now.
func dbBytesRaw(t *testing.T, dbPath string) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		data, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		buf.Write(data)
	}
	return buf.Bytes()
}

// dbBytes checkpoints the WAL into the main file and returns the bytes of the
// database plus any sidecars. Used to prove a secret is not merely invisible to
// SQL but absent from the file an attacker would actually steal.
func dbBytes(t *testing.T, s *Store, dbPath string) []byte {
	t.Helper()
	if _, err := s.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	return dbBytesRaw(t, dbPath)
}

func TestSessionCreateAndGet(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	id := "sess-1"
	token := "abc123token"
	csrfToken := "csrf123token"
	expiresAt := time.Now().Add(24 * time.Hour)

	sess := NewSession{ID: id, Token: token, CSRFToken: csrfToken, ExpiresAt: expiresAt}
	if err := s.CreateSession(ctx, sess); err != nil {
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

// TestSessionStoresHashOnly asserts the core of the session-hash migration:
// lookups resolve the presented token through hex(sha256(token)), the plaintext
// token never reaches the file, and the stored digest is not itself a usable
// credential (a stolen database cannot be replayed).
func TestSessionStoresHashOnly(t *testing.T) {
	s, dbPath := newStoreAt(t)
	ctx := context.Background()

	const rawToken = "5f4dcc3b5aa765d61d8327deb882cf99a1b0c3f0e9d2b4a5c6d7e8f901234567"
	if err := s.CreateSession(ctx, NewSession{
		ID:        "sess-hash",
		Token:     rawToken,
		CSRFToken: "csrf-hash",
		IP:        "203.0.113.7",
		UserAgent: "MIKE-test/1.0",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// The raw token the client holds still validates.
	gotID, gotCsrf, _, err := s.GetSessionByToken(ctx, rawToken)
	if err != nil {
		t.Fatalf("presented raw token must validate: %v", err)
	}
	if gotID != "sess-hash" || gotCsrf != "csrf-hash" {
		t.Fatalf("got (%q, %q); want (sess-hash, csrf-hash)", gotID, gotCsrf)
	}

	// The row holds the digest and nothing else.
	sum := sha256.Sum256([]byte(rawToken))
	wantHash := hex.EncodeToString(sum[:])
	var storedToken, storedHash string
	if err := s.QueryRow(`SELECT token, token_hash FROM sessions WHERE id = ?`, "sess-hash").
		Scan(&storedToken, &storedHash); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if storedHash != wantHash {
		t.Errorf("token_hash = %q; want %q", storedHash, wantHash)
	}
	if storedToken == rawToken {
		t.Error("the plaintext token was persisted")
	}
	if storedToken == "" {
		t.Error("token column is empty; it must keep a non-secret value to satisfy NOT NULL UNIQUE")
	}

	// Nothing that looks like the token is left in the database file.
	if raw := dbBytes(t, s, dbPath); bytes.Contains(raw, []byte(rawToken)) {
		t.Error("the raw token appears in the database file")
	}

	// The digest is not a credential: replaying it authenticates nothing.
	if _, _, _, err := s.GetSessionByToken(ctx, wantHash); err == nil {
		t.Error("presenting the stored hash validated a session; lookups must be one-way")
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

	token := "deleteme"
	if err := s.CreateSession(ctx, NewSession{ID: "sess-2", Token: token, CSRFToken: "csrf-deleteme", ExpiresAt: time.Now().Add(24 * time.Hour)}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := s.DeleteSession(ctx, token); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	if _, _, _, err := s.GetSessionByToken(ctx, token); err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestSessionDeleteExpired(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.CreateSession(ctx, NewSession{ID: "sess-expired", Token: "expired-token", CSRFToken: "csrf-expired", ExpiresAt: time.Now().Add(-1 * time.Hour)}); err != nil {
		t.Fatalf("create expired session: %v", err)
	}
	if err := s.CreateSession(ctx, NewSession{ID: "sess-valid", Token: "valid-token", CSRFToken: "csrf-valid", ExpiresAt: time.Now().Add(24 * time.Hour)}); err != nil {
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
	if _, _, _, err = s.GetSessionByToken(ctx, "expired-token"); err == nil {
		t.Fatal("expected error for expired session")
	}

	// Valid session should still exist
	if _, _, _, err = s.GetSessionByToken(ctx, "valid-token"); err != nil {
		t.Fatalf("valid session should still exist: %v", err)
	}
}

func TestSessionUniqueToken(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	token := "duplicate-token"
	csrfToken := "csrf-duplicate"
	expiresAt := time.Now().Add(24 * time.Hour)

	if err := s.CreateSession(ctx, NewSession{ID: "sess-a", Token: token, CSRFToken: csrfToken, ExpiresAt: expiresAt}); err != nil {
		t.Fatalf("create first session: %v", err)
	}

	// Second session with same token should fail
	err := s.CreateSession(ctx, NewSession{ID: "sess-b", Token: token, CSRFToken: csrfToken, ExpiresAt: expiresAt})
	if err == nil {
		t.Fatal("expected error for duplicate token")
	}
}

func TestSessionLastSeenThrottle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.CreateSession(ctx, NewSession{ID: "sess-seen", Token: "seen-token", CSRFToken: "csrf", ExpiresAt: time.Now().Add(24 * time.Hour)}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	lastSeen := func() int64 {
		t.Helper()
		var v int64
		if err := s.QueryRow(`SELECT COALESCE(last_seen, 0) FROM sessions WHERE id = ?`, "sess-seen").Scan(&v); err != nil {
			t.Fatalf("read last_seen: %v", err)
		}
		return v
	}

	// Backdate the stamp so the next validation is allowed to write.
	stale := time.Now().Add(-2 * time.Minute).Unix()
	if _, err := s.Exec(`UPDATE sessions SET last_seen = ? WHERE id = ?`, stale, "sess-seen"); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if _, _, _, err := s.GetSessionByToken(ctx, "seen-token"); err != nil {
		t.Fatalf("validate: %v", err)
	}
	refreshed := lastSeen()
	if refreshed <= stale {
		t.Fatalf("last_seen was not refreshed: %d (stale %d)", refreshed, stale)
	}

	// Repeated validations inside the same minute must not write again —
	// the store runs on a single connection, so per-request writes are costly.
	for i := 0; i < 3; i++ {
		if _, _, _, err := s.GetSessionByToken(ctx, "seen-token"); err != nil {
			t.Fatalf("validate %d: %v", i, err)
		}
	}
	if got := lastSeen(); got != refreshed {
		t.Fatalf("last_seen written within the throttle window: %d != %d", got, refreshed)
	}
}

func TestSessionListAndDeleteByID(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for _, id := range []string{"s-a", "s-b", "s-c"} {
		suffix := id[len(id)-1:]
		if err := s.CreateSession(ctx, NewSession{
			ID:        id,
			Token:     "tok-" + id,
			CSRFToken: "csrf-" + id,
			IP:        "10.0.0." + suffix,
			UserAgent: "agent-" + id,
			ExpiresAt: time.Now().Add(24 * time.Hour),
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	list, err := s.ListSessions(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len(list) = %d; want 3", len(list))
	}
	byID := map[string]SessionInfo{}
	for _, se := range list {
		byID[se.ID] = se
	}
	got := byID["s-b"]
	if got.IP != "10.0.0.b" {
		t.Errorf("ip = %q; want 10.0.0.b", got.IP)
	}
	if got.UserAgent != "agent-s-b" {
		t.Errorf("user_agent = %q; want agent-s-b", got.UserAgent)
	}
	if got.CreatedAt.IsZero() || got.ExpiresAt.IsZero() {
		t.Errorf("created_at/expires_at not parsed: %+v", got)
	}
	if got.LastSeen.IsZero() {
		t.Errorf("last_seen should be stamped at creation: %+v", got)
	}
	if _, leaked := byID[""]; leaked {
		t.Error("a session without an id was listed")
	}

	// Delete one by id.
	ok, err := s.DeleteSessionByID(ctx, "s-b")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !ok {
		t.Fatal("DeleteSessionByID reported nothing deleted")
	}
	if ok, err = s.DeleteSessionByID(ctx, "s-b"); err != nil || ok {
		t.Fatalf("second delete = (%v, %v); want (false, nil)", ok, err)
	}
	if ok, err = s.DeleteSessionByID(ctx, ""); err != nil || ok {
		t.Fatalf("empty id delete = (%v, %v); want (false, nil)", ok, err)
	}

	// Revoke everyone else, sparing the current session.
	n, err := s.DeleteOtherSessions(ctx, "s-a")
	if err != nil {
		t.Fatalf("delete others: %v", err)
	}
	if n != 1 {
		t.Fatalf("revoked = %d; want 1", n)
	}
	list, err = s.ListSessions(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != "s-a" {
		t.Fatalf("remaining sessions = %+v; want just s-a", list)
	}

	// An unknown current id must not nuke the whole table.
	if n, err := s.DeleteOtherSessions(ctx, ""); err != nil || n != 0 {
		t.Fatalf("delete others with empty id = (%d, %v); want (0, nil)", n, err)
	}
}
