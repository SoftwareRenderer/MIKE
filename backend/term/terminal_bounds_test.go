package term

// What a browser may make the terminal manager hold: live sockets. The count that needs bounding
// is connections, not PTYs — a session is per task or project and every client of it shares one
// shell, so connections are what a client can multiply.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mike/backend/types"
)

// waitForLiveConns waits for a client that was accepted to finish registering, so the refusal that
// follows is measured against a connection that is really attached.
func waitForLiveConns(t *testing.T, tm *TerminalManager, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for tm.liveConns() != want {
		if time.Now().After(deadline) {
			t.Fatalf("live terminal connections = %d, want %d", tm.liveConns(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestTerminalConnectionsStopAtTheAdmissionCap: past the cap the upgrade is refused with a 503, so
// the browser sees a refusal rather than a shell that never prints.
func TestTerminalConnectionsStopAtTheAdmissionCap(t *testing.T) {
	dir := t.TempDir()
	s, cs := newTestConfigStore(t)
	tm := NewTerminalManager(s, cs, newMockSandbox(t), nil, dangerZoneForTest)
	tm.maxConns = 1
	t.Cleanup(tm.Close)

	projID := "cap-proj"
	if err := s.CreateProject(&types.Project{ID: projID, Name: "Cap Project", Path: dir}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("projectID", projID)
		tm.HandleProjectTerminal(w, r)
	}))
	t.Cleanup(srv.Close)

	if code := wsUpgradeStatus(t, srv.URL, srv.URL); code != http.StatusSwitchingProtocols {
		t.Fatalf("the first connection was answered with %d, want 101", code)
	}
	waitForLiveConns(t, tm, 1)

	if code := wsUpgradeStatus(t, srv.URL, srv.URL); code != http.StatusServiceUnavailable {
		t.Fatalf("a connection at the cap of 1 was answered with %d, want a 503 before the upgrade", code)
	}
	if got := tm.liveConns(); got != 1 {
		t.Fatalf("refusing a connection changed the live count to %d, want 1", got)
	}
}
