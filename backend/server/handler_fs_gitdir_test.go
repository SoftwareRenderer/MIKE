package server

import (
	"os"
	"path/filepath"
	"testing"

	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// TestFSListingOffersNoGitRow: no listing carries a `.git` row — the directory of a checkout, the gitfile
// of a linked worktree, or a folder of that name no git made.
func TestFSListingOffersNoGitRow(t *testing.T) {
	s := testServer(t)
	base := t.TempDir()

	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct{ name, content string }{
		{"README.md", "# repo\n"},
		{"src/main.go", "package main\n"},
	} {
		if err := os.WriteFile(filepath.Join(repo, f.name), []byte(f.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	testgit.Git(t, repo, "init")
	testgit.Git(t, repo, "add", "-A")
	testgit.Git(t, repo, "commit", "-m", "init")

	// A linked worktree: its .git is a pointer file, not a folder.
	wt := filepath.Join(base, "wt")
	testgit.Git(t, repo, "worktree", "add", "-q", "-b", "side", wt)
	if info, err := os.Lstat(filepath.Join(wt, ".git")); err != nil {
		t.Fatalf("worktree fixture: %v", err)
	} else if info.IsDir() {
		t.Fatal("git worktree add wrote a .git directory, not the pointer file this case is about")
	}

	// A folder named .git that no git ever made.
	plain := filepath.Join(base, "plain")
	if err := os.MkdirAll(filepath.Join(plain, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plain, "notes.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, p := range []struct{ id, path string }{
		{"fs-gitdir-repo", repo},
		{"fs-gitdir-wt", wt},
		{"fs-gitdir-plain", plain},
	} {
		if err := s.store.CreateProject(&types.Project{ID: p.id, Name: p.id, Path: p.path}); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name, project, dir string
		// Rows this listing must still carry, name -> isDir: a listing that answered nothing would
		// also answer "no .git".
		want map[string]bool
	}{
		{"a checkout's .git directory", "fs-gitdir-repo", repo,
			map[string]bool{"README.md": false, "src": true}},
		{"a linked worktree's .git gitfile", "fs-gitdir-wt", wt,
			map[string]bool{"README.md": false, "src": true}},
		{"a .git folder no repository made", "fs-gitdir-plain", plain,
			map[string]bool{"notes.txt": false}},
		// The add-project picker, whose request names no project.
		{"the directory picker, which names no project", "", repo,
			map[string]bool{"README.md": false, "src": true}},
	}
	for _, c := range cases {
		_, rows := fsListing(t, s, c.project, c.dir)
		if _, ok := rows[".git"]; ok {
			t.Errorf("%s: listed as a row", c.name)
		}
		for name, isDir := range c.want {
			row, ok := rows[name]
			if !ok {
				t.Errorf("%s: %s not listed, so the answer says nothing about .git", c.name, name)
				continue
			}
			if row.isDir != isDir {
				t.Errorf("%s: %s isDir=%v, want %v", c.name, name, row.isDir, isDir)
			}
		}
	}
}
