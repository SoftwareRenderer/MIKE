package exec

import (
	"os"
	"strings"
	"testing"

	"kanban/backend/sandbox"
)

// TestMain opts this suite into host execution: execCommandStream is the host bash path and
// refuses without DANGER_ZONE=1. The refusal itself is pinned by
// TestExecCommandStreamRefusesWithoutDangerZone, which clears the variable again.
func TestMain(m *testing.M) {
	if os.Getenv(sandbox.DangerZoneEnv) == "" {
		_ = os.Setenv(sandbox.DangerZoneEnv, "1")
	}
	os.Exit(m.Run())
}

// A project with sandboxing disabled is created only with DANGER_ZONE=1, but the row is not
// the consent: a database restored from before that rule must not get a host shell from a
// server that never opted in, so the flag is checked where the command is spawned.
func TestExecCommandStreamRefusesWithoutDangerZone(t *testing.T) {
	t.Setenv(sandbox.DangerZoneEnv, "")
	dir := t.TempDir()

	streamed := 0
	out, err := execCommandStream(t.Context(), dir, "printf 'RAN ON THE HOST\\n' > ran.txt", 10, func(string) { streamed++ })
	if err == nil {
		t.Fatalf("a host tool command ran without DANGER_ZONE: %q", out)
	}
	if !strings.Contains(err.Error(), sandbox.DangerZoneEnv) {
		t.Errorf("the refusal must name %s, got: %v", sandbox.DangerZoneEnv, err)
	}
	if out != "" || streamed != 0 {
		t.Errorf("a refused command must produce nothing, got %q and %d streamed lines", out, streamed)
	}
	// And it refused before spawning: the command's own side effect is absent.
	if _, statErr := os.Stat(dir + "/ran.txt"); !os.IsNotExist(statErr) {
		t.Error("the command ran anyway — the check happens after the spawn")
	}

	// Opt-in: this is a rule, not a disabled tool.
	t.Setenv(sandbox.DangerZoneEnv, "1")
	out, err = execCommandStream(t.Context(), dir, "printf 'ok\\n'", 10, nil)
	if err != nil || strings.TrimSpace(out) != "ok" {
		t.Fatalf("with DANGER_ZONE=1 the command should run: %q, %v", out, err)
	}
}
