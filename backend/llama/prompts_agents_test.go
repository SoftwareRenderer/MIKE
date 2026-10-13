package llama

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAgentsFileStaysInsideTheWorktree(t *testing.T) {
	worktree := t.TempDir()
	home := t.TempDir()
	outside := filepath.Join(home, "secrets.md")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(worktree, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if got := readAgentsFile(worktree); got != "" {
		t.Errorf("a worktree link out of the jail read %q, want nothing", got)
	}
}

// An instruction file is small; pasting an oversized one costs the operator on every call.
func TestReadAgentsFileCapsWhatItHandsToThePrompt(t *testing.T) {
	worktree := t.TempDir()
	body := strings.Repeat("rule\n", agentsFileLimit)
	if err := os.WriteFile(filepath.Join(worktree, "AGENTS.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readAgentsFile(worktree)
	if len(got) != agentsFileLimit {
		t.Errorf("read %d bytes, want the cap of %d", len(got), agentsFileLimit)
	}
}

func TestReadAgentsFileOfAnAbsentWorktreeReadsNothing(t *testing.T) {
	// An empty path would otherwise read the server's own checkout rather than the task's.
	if got := readAgentsFile(""); got != "" {
		t.Errorf("empty worktree read %d bytes of the server's own checkout", len(got))
	}
	if got := readAgentsFile(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Errorf("missing worktree read %q, want nothing", got)
	}
}
