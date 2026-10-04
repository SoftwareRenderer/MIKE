package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func addPasskey(t *testing.T, s *Store, id, rpID string, createdAt time.Time) {
	t.Helper()
	err := s.CreatePasskey(context.Background(), NewPasskey{
		CredentialID: id,
		RPID:         rpID,
		Name:         "device " + id,
		Record:       []byte(`{"id":"` + id + `","pk":"key"}`),
		CreatedAt:    createdAt,
	})
	if err != nil {
		t.Fatalf("create passkey %s: %v", id, err)
	}
}

func passkeyIDs(t *testing.T, s *Store) []string {
	t.Helper()
	list, err := s.ListPasskeys(context.Background())
	if err != nil {
		t.Fatalf("list passkeys: %v", err)
	}
	ids := make([]string, 0, len(list))
	for _, p := range list {
		ids = append(ids, p.CredentialID)
	}
	return ids
}

func TestPasskeyRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	addPasskey(t, s, "cred-a", "localhost", time.Now().Add(-time.Hour))

	count, err := s.PasskeyCount(ctx)
	if err != nil || count != 1 {
		t.Fatalf("count = %d, %v; want 1, nil", count, err)
	}

	list, err := s.ListPasskeys(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v; want one row", list, err)
	}
	got := list[0]
	if got.Name != "device cred-a" || got.RPID != "localhost" {
		t.Errorf("row = %+v; want the stored name and rp_id", got)
	}
	if !got.LastUsedAt.IsZero() {
		t.Errorf("last_used_at = %v; want the zero time for a credential that never signed", got.LastUsedAt)
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at is zero")
	}

	renamed, err := s.RenamePasskey(ctx, "cred-a", "work laptop")
	if err != nil || !renamed {
		t.Fatalf("rename = %v, %v; want true, nil", renamed, err)
	}
	renamed, err = s.RenamePasskey(ctx, "nope", "ghost")
	if err != nil || renamed {
		t.Fatalf("rename of an unknown id = %v, %v; want false, nil", renamed, err)
	}

	if err := s.UsePasskey(ctx, "cred-a", []byte(`{"id":"cred-a","pk":"key","signCount":7}`)); err != nil {
		t.Fatalf("use: %v", err)
	}
	records, err := s.ListPasskeyRecords(ctx, "localhost")
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %v, %v; want one row", records, err)
	}
	if string(records[0].Record) != `{"id":"cred-a","pk":"key","signCount":7}` {
		t.Errorf("record = %s; want what the ceremony wrote back", records[0].Record)
	}

	list, err = s.ListPasskeys(ctx)
	if err != nil || list[0].LastUsedAt.IsZero() {
		t.Fatalf("last_used_at not recorded: %v, %v", list, err)
	}
}

// A credential id is an authenticator's identity for one relying party. Two rows for one id would be two
// counters for one key, and the counter is what reports a cloned authenticator.
func TestCreatePasskeyRejectsADuplicateCredentialID(t *testing.T) {
	s := testStore(t)
	addPasskey(t, s, "cred-dup", "localhost", time.Now())
	err := s.CreatePasskey(context.Background(), NewPasskey{
		CredentialID: "cred-dup", RPID: "elsewhere", Record: []byte("{}"),
	})
	if err == nil {
		t.Fatal("a second row for one credential id was accepted")
	}
}

func TestListPasskeyRecordsIsScopedToItsRelyingParty(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	addPasskey(t, s, "on-localhost", "localhost", time.Now())
	addPasskey(t, s, "on-lan", "192.168.1.10", time.Now())

	for _, tc := range []struct{ rpID, want string }{
		{"localhost", "on-localhost"},
		{"192.168.1.10", "on-lan"},
	} {
		records, err := s.ListPasskeyRecords(ctx, tc.rpID)
		if err != nil {
			t.Fatalf("list for %s: %v", tc.rpID, err)
		}
		if len(records) != 1 || records[0].CredentialID != tc.want {
			t.Errorf("rp_id %s offered %v; want only %s", tc.rpID, records, tc.want)
		}
	}
	if records, err := s.ListPasskeyRecords(ctx, "kanban.example.com"); err != nil || len(records) != 0 {
		t.Errorf("an rp_id nobody registered for offered %v, %v; want nothing", records, err)
	}
}

func TestListPasskeysIsOldestFirst(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	addPasskey(t, s, "third", "localhost", now.Add(-3*time.Minute))
	addPasskey(t, s, "first", "localhost", now.Add(-time.Minute))
	addPasskey(t, s, "second", "localhost", now.Add(-2*time.Minute))

	ids := passkeyIDs(t, s)
	want := []string{"third", "second", "first"}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("order = %v; want %v", ids, want)
		}
	}
}

func TestDeletePasskeyRefusesTheLastOne(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	addPasskey(t, s, "only", "localhost", time.Now())

	deleted, err := s.DeletePasskey(ctx, "only")
	if !errors.Is(err, ErrLastPasskey) || deleted {
		t.Fatalf("delete of the last passkey = %v, %v; want false, ErrLastPasskey", deleted, err)
	}
	if ids := passkeyIDs(t, s); len(ids) != 1 {
		t.Fatalf("the refusal took the row anyway: %v", ids)
	}

	addPasskey(t, s, "second", "localhost", time.Now())
	deleted, err = s.DeletePasskey(ctx, "only")
	if err != nil || !deleted {
		t.Fatalf("delete with two rows = %v, %v; want true, nil", deleted, err)
	}
	if deleted, err := s.DeletePasskey(ctx, "never-existed"); err != nil || deleted {
		t.Errorf("delete of an unknown id = %v, %v; want false, nil", deleted, err)
	}
	// One row is left, and the refusal holds again.
	if _, err := s.DeletePasskey(ctx, "second"); !errors.Is(err, ErrLastPasskey) {
		t.Errorf("delete that would leave nothing = %v; want ErrLastPasskey", err)
	}
}

func TestResetPasskeys(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	addPasskey(t, s, "cred-a", "localhost", time.Now())
	addPasskey(t, s, "cred-b", "localhost", time.Now())
	seedSession(t, s, "session-1")
	seedSession(t, s, "session-2")

	passkeys, sessions, err := s.ResetPasskeys(ctx)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if passkeys != 2 || sessions != 2 {
		t.Errorf("reset removed %d passkeys and %d sessions; want 2 and 2", passkeys, sessions)
	}
	if n, _ := s.PasskeyCount(ctx); n != 0 {
		t.Errorf("%d passkeys left", n)
	}
	if live, _ := s.ListSessions(ctx); len(live) != 0 {
		t.Errorf("%d sessions left; a live session can mint a pairing code, so it must not survive a reset", len(live))
	}
}

// The one that pins the change: a reset that fails half way must not take the credentials and leave the
// sessions — the surviving sessions would be the very access the reset was for.
func TestResetPasskeysThatFailsRemovesNothing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	addPasskey(t, s, "cred-a", "localhost", time.Now())
	seedSession(t, s, "session-1")

	if _, err := s.db.Exec(`CREATE TRIGGER abort_session_delete BEFORE DELETE ON sessions
		BEGIN SELECT RAISE(ABORT, 'injected: the session row cannot go'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if _, _, err := s.ResetPasskeys(ctx); err == nil {
		t.Fatal("ResetPasskeys reported success against a trigger that aborts the session delete")
	}
	if n, _ := s.PasskeyCount(ctx); n != 1 {
		t.Errorf("%d passkeys left; a failed reset took the credential and left the session it was revoking", n)
	}
	if live, _ := s.ListSessions(ctx); len(live) != 1 {
		t.Errorf("%d sessions left; want the one row untouched", len(live))
	}
}

func seedSession(t *testing.T, s *Store, token string) {
	t.Helper()
	err := s.CreateSession(context.Background(), NewSession{
		ID: "id-" + token, Token: token, CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
}
