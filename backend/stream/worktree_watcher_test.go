package stream

import (
	"os"
	"path/filepath"
	"testing"

	"kanban/backend/git"
)

func TestResolveGitIndexDir_Worktree(t *testing.T) {
	dir := t.TempDir()

	// Simulate a worktree .git file
	gitFile := filepath.Join(dir, ".git")
	gitdirPath := "/home/user/projects/myrepo/.git/worktrees/abc-123-def"
	err := os.WriteFile(gitFile, []byte("gitdir: "+gitdirPath+"\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	result := resolveGitIndexDir(dir)
	if result != gitdirPath {
		t.Errorf("resolveGitIndexDir() = %q, want %q", result, gitdirPath)
	}
}

func TestResolveGitIndexDir_RegularRepo(t *testing.T) {
	dir := t.TempDir()

	// Simulate a regular repo where .git is a directory
	gitDir := filepath.Join(dir, ".git")
	err := os.Mkdir(gitDir, 0755)
	if err != nil {
		t.Fatal(err)
	}

	result := resolveGitIndexDir(dir)
	if result != "" {
		t.Errorf("resolveGitIndexDir() = %q, want empty string (regular repo)", result)
	}
}

func TestResolveGitIndexDir_NoGit(t *testing.T) {
	dir := t.TempDir()

	// No .git at all
	result := resolveGitIndexDir(dir)
	if result != "" {
		t.Errorf("resolveGitIndexDir() = %q, want empty string (no .git)", result)
	}
}

func TestResolveGitIndexDir_InvalidContent(t *testing.T) {
	dir := t.TempDir()

	// .git file with no gitdir: line
	gitFile := filepath.Join(dir, ".git")
	err := os.WriteFile(gitFile, []byte("some random content\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	result := resolveGitIndexDir(dir)
	if result != "" {
		t.Errorf("resolveGitIndexDir() = %q, want empty string (no gitdir line)", result)
	}
}

func TestResolveGitIndexDir_MultiLine(t *testing.T) {
	dir := t.TempDir()

	// .git file with extra lines before/after gitdir
	gitFile := filepath.Join(dir, ".git")
	gitdirPath := "/some/path/.git/worktrees/task-xyz"
	content := "# comment\ngitdir: " + gitdirPath + "\n# another comment\n"
	err := os.WriteFile(gitFile, []byte(content), 0644)
	if err != nil {
		t.Fatal(err)
	}

	result := resolveGitIndexDir(dir)
	if result != gitdirPath {
		t.Errorf("resolveGitIndexDir() = %q, want %q", result, gitdirPath)
	}
}

func TestCountManifestChanges(t *testing.T) {
	tests := []struct {
		name      string
		tracked   map[string]*git.TrackedDiffFile
		untracked []git.UntrackedFile
		wantAdd   int
		wantDel   int
	}{
		{
			name:      "empty",
			tracked:   nil,
			untracked: nil,
			wantAdd:   0,
			wantDel:   0,
		},
		{
			name: "single tracked file",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt": {Path: "a.txt", Added: 5, Removed: 3},
			},
			untracked: nil,
			wantAdd:   5,
			wantDel:   3,
		},
		{
			name: "multiple tracked files",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt": {Path: "a.txt", Added: 5, Removed: 3},
				"b.txt": {Path: "b.txt", Added: 2, Removed: 1},
			},
			untracked: nil,
			wantAdd:   7,
			wantDel:   4,
		},
		{
			name: "tracked with untracked",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt": {Path: "a.txt", Added: 3, Removed: 2},
			},
			untracked: []git.UntrackedFile{
				{Path: "new.txt", Lines: []string{"line1", "line2", "line3"}},
			},
			wantAdd:   6, // 3 tracked + 3 untracked
			wantDel:   2,
		},
		{
			name: "untracked with OrigAdded (truncated)",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt": {Path: "a.txt", Added: 1, Removed: 1},
			},
			untracked: []git.UntrackedFile{
				{Path: "new.txt", Lines: []string{"line1"}, OrigAdded: 100, Truncated: true},
			},
			wantAdd:   101, // 1 tracked + 100 untracked (OrigAdded)
			wantDel:   1,
		},
		{
			name: "truncated tracked file uses truncated counts",
			tracked: map[string]*git.TrackedDiffFile{
				// After truncation, Added/Removed reflect truncated lines
				// OrigAdded/OrigRemoved reflect original (full) lines
				"large.txt": {Path: "large.txt", Added: 6, Removed: 0, Truncated: true, OrigAdded: 90, OrigRemoved: 0},
			},
			untracked: nil,
			wantAdd:   6, // truncated count, NOT 90
			wantDel:   0,
		},
		{
			name: "excludes internal files from tracked",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt":      {Path: "a.txt", Added: 5, Removed: 3},
				".git/config": {Path: ".git/config", Added: 100, Removed: 200},
			},
			untracked: nil,
			wantAdd:   5,   // only a.txt, NOT .git/config
			wantDel:   3,
		},
		{
			name: "excludes internal files from untracked",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt": {Path: "a.txt", Added: 1, Removed: 1},
			},
			untracked: []git.UntrackedFile{
				{Path: "new.txt", Lines: []string{"line1"}},
				{Path: ".git/new", Lines: []string{"spam1", "spam2", "spam3", "spam4"}},
			},
			wantAdd:   2, // 1 tracked + 1 untracked (new.txt), NOT .git/new
			wantDel:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &git.TaskDiff{
				Tracked:   tt.tracked,
				Untracked: tt.untracked,
			}
			add, del := countManifestChanges(d)
			if add != tt.wantAdd || del != tt.wantDel {
				t.Errorf("countManifestChanges() = add=%d, del=%d, want add=%d, del=%d", add, del, tt.wantAdd, tt.wantDel)
			}
		})
	}
}

func TestCountManifestFiles(t *testing.T) {
	tests := []struct {
		name            string
		tracked         map[string]*git.TrackedDiffFile
		untracked       []git.UntrackedFile
		wantTracked     int
		wantUntracked   int
	}{
		{
			name:          "empty",
			tracked:       nil,
			untracked:     nil,
			wantTracked:   0,
			wantUntracked: 0,
		},
		{
			name: "mixed with internal files",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt":       {Path: "a.txt"},
				"b.txt":       {Path: "b.txt"},
				".git/config": {Path: ".git/config"},
				"kanban.trace": {Path: "kanban.trace"},
			},
			untracked: []git.UntrackedFile{
				{Path: "new.txt"},
				{Path: ".git/new"},
			},
			wantTracked:   2, // a.txt, b.txt (excludes .git/config, kanban.trace)
			wantUntracked: 1, // new.txt (excludes .git/new)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &git.TaskDiff{
				Tracked:   tt.tracked,
				Untracked: tt.untracked,
			}
			tracked, untracked := countManifestFiles(d)
			if tracked != tt.wantTracked || untracked != tt.wantUntracked {
				t.Errorf("countManifestFiles() = tracked=%d, untracked=%d, want tracked=%d, untracked=%d",
					tracked, untracked, tt.wantTracked, tt.wantUntracked)
			}
		})
	}
}
