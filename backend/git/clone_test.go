package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeCloneSource creates a temporary non-bare git repo with `commits`
// commits of a growing file (enough objects for git to emit
// "Receiving objects" progress lines).
func makeCloneSource(t *testing.T, path string, commits int) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	runGit(t, path, "init")
	runGit(t, path, "config", "user.email", "test@test.com")
	runGit(t, path, "config", "user.name", "Test")
	for i := 0; i < commits; i++ {
		// ~1KB per commit with unique content so the pack is large enough
		// for git to emit "Receiving objects" progress lines.
		line := fmt.Sprintf("line %d %s", i, strings.Repeat("x", 1020))
		writeFile(t, path, "data.txt", line)
		runGit(t, path, "add", "data.txt")
		runGit(t, path, "commit", "-m", "commit")
	}
}

func TestCloneWithProgress(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "source")
	makeCloneSource(t, srcDir, 20)
	// Use a file:// URL: cloning a plain local path uses git's hardlink
	// optimization (no pack transfer, no progress output), while file://
	// goes through the normal upload-pack protocol.
	src := "file://" + srcDir
	dst := filepath.Join(tmp, "workspace", "source")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	var events []int
	out, err := CloneWithProgress(src, dst, func(pct int) {
		events = append(events, pct)
	})
	if err != nil {
		t.Fatalf("CloneWithProgress: %v (output: %s)", err, out)
	}
	if !IsGitRepo(dst) {
		t.Fatal("clone target is not a git repository")
	}
	if len(events) == 0 {
		t.Fatalf("no progress events reported (output: %q)", out)
	}
	for i, pct := range events {
		if pct < 0 || pct > 100 {
			t.Fatalf("progress out of range: %d", pct)
		}
		if i > 0 && pct <= events[i-1] {
			t.Fatalf("progress not strictly increasing: %v", events)
		}
	}
	if events[len(events)-1] != 100 {
		t.Fatalf("final progress = %d, want 100 (events: %v)", events[len(events)-1], events)
	}
}

func TestCloneWithProgress_NilCallback(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "source")
	makeCloneSource(t, src, 3)
	dst := filepath.Join(tmp, "workspace", "source")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	if _, err := CloneWithProgress(src, dst, nil); err != nil {
		t.Fatalf("CloneWithProgress with nil callback: %v", err)
	}
	if !IsGitRepo(dst) {
		t.Fatal("clone target is not a git repository")
	}
}

func TestCloneWithProgress_Error(t *testing.T) {
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "does-not-exist")

	out, err := CloneWithProgress(missing, filepath.Join(tmp, "dst"), nil)
	if err == nil {
		t.Fatal("expected error cloning a missing source")
	}
	if !strings.Contains(out, "does-not-exist") && !strings.Contains(out, "fatal") {
		t.Fatalf("error output does not mention the failure: %q", out)
	}
}
