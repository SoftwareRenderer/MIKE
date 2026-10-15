package server

import (
	"os"
	"path/filepath"
	"testing"

	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// TestFSListingMarksIgnoredRows: the dimming is git's answer and nobody else's — a committed file
// matching a later rule, and a build directory no rule mentions, both stay unmarked.
func TestFSListingMarksIgnoredRows(t *testing.T) {
	s := testServer(t)
	repo := t.TempDir()
	const projectID = "fs-ignored"

	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "*.log\n/build/\n!keep.log\n")
	write("sub/.gitignore", "secret.txt\n")
	write("README.md", "# readme\n")
	testgit.Git(t, repo, "init")
	testgit.Git(t, repo, "config", "user.email", "test@test.com")
	testgit.Git(t, repo, "config", "user.name", "Test")
	testgit.Git(t, repo, "add", ".gitignore", "sub/.gitignore", "README.md")
	testgit.Git(t, repo, "commit", "-m", "init")
	write("tracked.log", "tracked\n")
	testgit.Git(t, repo, "add", "-f", "tracked.log")
	testgit.Git(t, repo, "commit", "-m", "add tracked.log")

	write("app.log", "x\n")
	write("keep.log", "x\n")
	write("build/out.js", "x\n")
	write("node_modules/pkg/index.js", "x\n")
	write("sub/secret.txt", "x\n")
	write("sub/visible.txt", "x\n")

	if err := s.store.CreateProject(&types.Project{ID: projectID, Name: projectID, Path: repo}); err != nil {
		t.Fatal(err)
	}

	_, root := fsListing(t, s, projectID, repo)
	want := map[string]bool{
		"README.md": false, ".gitignore": false, "keep.log": false, "tracked.log": false,
		"node_modules": false, // no rule mentions it; the listing no longer judges names
		"app.log":      true,
		"build":        true,
		"sub":          false,
	}
	for name, ignored := range want {
		row, ok := root[name]
		if !ok {
			t.Errorf("%s: not listed at all", name)
		} else if row.ignored != ignored {
			t.Errorf("%s: ignored=%v, want %v", name, row.ignored, ignored)
		}
	}

	// A nested .gitignore is judged by the directory that holds it.
	_, sub := fsListing(t, s, projectID, filepath.Join(repo, "sub"))
	if row, ok := sub["secret.txt"]; !ok {
		t.Error("sub/secret.txt: not listed at all")
	} else if !row.ignored {
		t.Error("sub/secret.txt: not marked, though sub/.gitignore excludes it")
	}
	if row, ok := sub["visible.txt"]; !ok {
		t.Error("sub/visible.txt: not listed at all")
	} else if row.ignored {
		t.Error("sub/visible.txt: marked though nothing excludes it")
	}
	// The parent row is judged by whatever lists it, never here: `..` is a fatal "outside the
	// repository" to check-ignore, which would blank the whole batch.
	if row, ok := sub[".."]; !ok {
		t.Error("a subdirectory listing offers no parent row")
	} else if row.ignored {
		t.Error("the parent row is marked ignored")
	}
}

// TestFSListingOutsideARepositoryMarksNothing: with no repository there is no rule to dim by.
func TestFSListingOutsideARepositoryMarksNothing(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	const projectID = "fs-ignored-plain"
	for _, name := range []string{"notes.log", "build"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.CreateProject(&types.Project{ID: projectID, Name: projectID, Path: dir}); err != nil {
		t.Fatal(err)
	}

	_, listing := fsListing(t, s, projectID, dir)
	for _, name := range []string{"notes.log", "build"} {
		row, ok := listing[name]
		if !ok {
			t.Fatalf("%s: not listed", name)
		}
		if row.ignored {
			t.Errorf("%s: marked ignored with no repository to decide it", name)
		}
	}
}
