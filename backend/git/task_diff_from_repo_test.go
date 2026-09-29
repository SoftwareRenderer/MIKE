package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/config"
	"mike/backend/store"
)

// newBareDiffTestRepo creates a temp git repo with an initial commit and
// returns the repo dir, the worktree manager, and a helper to read HEAD.
func newBareDiffTestRepo(t *testing.T) (string, *WorktreeManager, func() string) {
	t.Helper()
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	os.MkdirAll(repoDir, 0o755)
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	writeFile(t, repoDir, "README.md", "# Test\nOld line\n")
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	cfgStore := config.NewConfigStore(s, dbPath, "test", nil)
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if err := cfgStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	wm := NewWorktreeManager(s, cfgStore)

	head := func() string {
		out, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").CombinedOutput()
		if err != nil {
			t.Fatalf("rev-parse HEAD: %s", string(out))
		}
		return strings.TrimSpace(string(out))
	}
	return repoDir, wm, head
}

func TestTaskDiffFromRepo_NormalDiff(t *testing.T) {
	repoDir, wm, head := newBareDiffTestRepo(t)
	base := head()

	// README started as "# Test\nOld line\n" — replacement yields one added
	// and one removed line.
	writeFile(t, repoDir, "README.md", "# Test\nChanged line\n")
	writeFile(t, repoDir, "new.txt", "hello\nworld\n")
	runGit(t, repoDir, "add", "-A")
	runGit(t, repoDir, "commit", "-m", "change")
	tip := head()

	res, err := wm.TaskDiffFromRepo(repoDir, base, tip, false)
	if err != nil {
		t.Fatalf("TaskDiffFromRepo: %v", err)
	}
	if res.Diff == "" {
		t.Fatal("expected non-empty diff")
	}
	if len(res.Tracked) != 2 {
		t.Fatalf("expected 2 tracked files, got %d: %v", len(res.Tracked), keysOf(res.Tracked))
	}
	readme := res.Tracked["README.md"]
	if readme == nil || readme.Added != 1 || readme.Removed != 1 {
		t.Fatalf("unexpected README.md stats: %+v", readme)
	}
	newFile := res.Tracked["new.txt"]
	if newFile == nil || newFile.Added != 2 || newFile.Removed != 0 {
		t.Fatalf("unexpected new.txt stats: %+v", newFile)
	}
	if len(res.Untracked) != 0 {
		t.Fatalf("expected no untracked files, got %d", len(res.Untracked))
	}
	if res.ShortStat == "" {
		t.Fatal("expected non-empty short stat")
	}
}

func TestTaskDiffFromRepo_EmptyDiff(t *testing.T) {
	repoDir, wm, head := newBareDiffTestRepo(t)
	sha := head()

	res, err := wm.TaskDiffFromRepo(repoDir, sha, sha, false)
	if err != nil {
		t.Fatalf("TaskDiffFromRepo: %v", err)
	}
	if res.Diff != "" {
		t.Fatalf("expected empty diff, got %q", res.Diff)
	}
	if len(res.Tracked) != 0 {
		t.Fatalf("expected no tracked files, got %d", len(res.Tracked))
	}
	if len(res.Untracked) != 0 {
		t.Fatalf("expected empty untracked slice, got %d", len(res.Untracked))
	}
}

func TestTaskDiffFromRepo_BinaryFile(t *testing.T) {
	repoDir, wm, head := newBareDiffTestRepo(t)
	base := head()

	// NUL bytes make git classify the file as binary.
	if err := os.WriteFile(filepath.Join(repoDir, "blob.bin"), []byte{0x00, 0x01, 0x02, 0x00}, 0o644); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	runGit(t, repoDir, "add", "blob.bin")
	runGit(t, repoDir, "commit", "-m", "add binary")
	tip := head()

	res, err := wm.TaskDiffFromRepo(repoDir, base, tip, false)
	if err != nil {
		t.Fatalf("TaskDiffFromRepo: %v", err)
	}
	blob := res.Tracked["blob.bin"]
	if blob == nil {
		t.Fatalf("expected blob.bin in tracked files, got %v", keysOf(res.Tracked))
	}
	if !blob.IsBinary {
		t.Fatal("expected blob.bin to be marked binary")
	}
	if blob.FileSize == nil || *blob.FileSize != 4 {
		t.Fatalf("expected file size 4, got %v", blob.FileSize)
	}
}

func TestTaskDiffFromRepo_TruncatesLargeDiff(t *testing.T) {
	repoDir, wm, head := newBareDiffTestRepo(t)
	base := head()

	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString("line\n")
	}
	writeFile(t, repoDir, "big.txt", b.String())
	runGit(t, repoDir, "add", "big.txt")
	runGit(t, repoDir, "commit", "-m", "add big file")
	tip := head()

	res, err := wm.TaskDiffFromRepo(repoDir, base, tip, false)
	if err != nil {
		t.Fatalf("TaskDiffFromRepo: %v", err)
	}
	big := res.Tracked["big.txt"]
	if big == nil {
		t.Fatalf("expected big.txt in tracked files, got %v", keysOf(res.Tracked))
	}
	if !big.Truncated {
		t.Fatal("expected big.txt to be truncated")
	}
	if big.OrigAdded != 100 {
		t.Fatalf("expected OrigAdded=100, got %d", big.OrigAdded)
	}
	if len(res.TruncatedFiles) == 0 {
		t.Fatal("expected big.txt in TruncatedFiles")
	}
	// skipTruncate keeps all rows
	full, err := wm.TaskDiffFromRepo(repoDir, base, tip, true)
	if err != nil {
		t.Fatalf("TaskDiffFromRepo(full): %v", err)
	}
	fullBig := full.Tracked["big.txt"]
	if fullBig == nil || fullBig.Truncated || len(fullBig.Rows) != 100 {
		t.Fatalf("expected full untruncated big.txt, got %+v", fullBig)
	}
}

func TestTaskDiffFromRepo_InvalidArgs(t *testing.T) {
	repoDir, wm, _ := newBareDiffTestRepo(t)
	if _, err := wm.TaskDiffFromRepo("", "abc", "def", false); err == nil {
		t.Fatal("expected error for empty repoPath")
	}
	if _, err := wm.TaskDiffFromRepo(repoDir, "abc", "", false); err == nil {
		t.Fatal("expected error for empty headRef")
	}
}

func keysOf(m map[string]*TrackedDiffFile) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
