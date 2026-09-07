package term

import (
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/sandbox"
	"kanban/backend/types"
)

// A project with sandboxing disabled is created only with DANGER_ZONE=1, but the row is not
// the consent: a database restored from before that rule must not hand out a host shell on a
// server that never opted in, so the flag is checked again where the PTY starts.

func TestHostTerminalNeedsDangerZone(t *testing.T) {
	t.Setenv("DANGER_ZONE", "")
	dir := t.TempDir()
	s, cs := newTestConfigStore(t)
	tm := NewTerminalManager(s, cs, newMockSandbox(t), nil)

	proj := &types.Project{ID: "legacy-unsandboxed", Name: "Legacy", Path: dir, SandboxEnabled: false}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	sess, err := tm.getOrCreate("legacy-unsandboxed", "legacy-unsandboxed", dir, proj.ID)
	if err == nil {
		if sess != nil {
			_ = sess.cmd.Process.Kill()
		}
		t.Fatal("a host shell was handed out without DANGER_ZONE")
	}
	if !strings.Contains(err.Error(), sandbox.DangerZoneEnv) {
		t.Errorf("the refusal must name %s, got: %v", sandbox.DangerZoneEnv, err)
	}

	// Opt-in: the same row works, so this is a rule and not a disabled feature.
	t.Setenv("DANGER_ZONE", "1")
	sess, err = tm.getOrCreate("legacy-unsandboxed-2", "legacy-unsandboxed-2", dir, proj.ID)
	if err != nil {
		t.Fatalf("with DANGER_ZONE=1 the terminal should start: %v", err)
	}
	if sess == nil || sess.cmd == nil {
		t.Fatal("no host session was created")
	}
	_ = sess.cmd.Process.Kill()
}

// The refusal must not have created the worktree directory either.
func TestHostTerminalRefusalCreatesNothing(t *testing.T) {
	t.Setenv("DANGER_ZONE", "")
	dir := t.TempDir()
	s, cs := newTestConfigStore(t)
	tm := NewTerminalManager(s, cs, newMockSandbox(t), nil)
	proj := &types.Project{ID: "refusal-proj", Name: "Refusal", Path: dir}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	if _, err := tm.getOrCreate("refusal-task", "refusal-task", dir, proj.ID); err == nil {
		t.Fatal("expected a refusal")
	}
	entries, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the refused terminal left %v behind", entries)
	}
}
