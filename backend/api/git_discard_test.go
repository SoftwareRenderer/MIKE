package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// A discard either reverts a path or deletes it, depending on its state in git, so every state is
// asserted here against a real repository. HEAD decides per path, and one request carries the whole
// selection.

// discardFixture serves the discard route over a repo holding one committed file, tracked.txt.
func discardFixture(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	h, s, _, _ := newTestGitHandler(t)
	repoPath := testgit.Repo(t)
	testgit.WriteFile(t, repoPath, "tracked.txt", "committed\n")
	testgit.Git(t, repoPath, "add", "tracked.txt")
	testgit.Git(t, repoPath, "commit", "-m", "track it")
	proj := &types.Project{ID: "discard-proj", Name: "discard-proj", Path: repoPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/git/discard", h.HandleProjectDiscardPaths)
	return mux, repoPath
}

// postDiscard names every path in one request, as the panel does.
func postDiscard(t *testing.T, mux *http.ServeMux, paths ...string) *httptest.ResponseRecorder {
	t.Helper()
	quoted := make([]string, 0, len(paths))
	for _, path := range paths {
		quoted = append(quoted, `"`+path+`"`)
	}
	req := httptest.NewRequest("POST", "/api/projects/discard-proj/git/discard",
		strings.NewReader(`{"paths": [`+strings.Join(quoted, ", ")+`]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func fileExists(repoPath, name string) bool {
	_, err := os.Lstat(filepath.Join(repoPath, name))
	return err == nil
}

// TestHandleProjectDiscardPaths_HeadDecidesRevertFromDelete covers the state machine one path can be
// in: an edit reverts, a delete comes back, an untracked file goes, and a staged entry has its index
// half removed before the file. A discard is one click, not "unstage, then revert", and the last row
// shows HEAD deciding per path inside one request rather than for the batch as a whole.
func TestHandleProjectDiscardPaths_HeadDecidesRevertFromDelete(t *testing.T) {
	for _, tt := range []struct {
		name string
		// plant puts the repository into the state the click reads.
		plant func(t *testing.T, repoPath string)
		paths []string
		// check reads the repository after the discard.
		check func(t *testing.T, repoPath string)
		// wantApplied is the answer's applied list; empty means "not asserted".
		wantApplied string
	}{
		{
			name:  "a modified tracked file reverts",
			plant: func(t *testing.T, repoPath string) { testgit.WriteFile(t, repoPath, "tracked.txt", "edited on disk\n") },
			paths: []string{"tracked.txt"},
			check: func(t *testing.T, repoPath string) {
				requireRevertedToCommit(t, repoPath)
			},
		},
		{
			name: "a staged and an unstaged half both go",
			plant: func(t *testing.T, repoPath string) {
				testgit.WriteFile(t, repoPath, "tracked.txt", "staged half\n")
				testgit.Git(t, repoPath, "add", "tracked.txt")
				testgit.WriteFile(t, repoPath, "tracked.txt", "plus an unstaged half\n")
			},
			paths: []string{"tracked.txt"},
			check: func(t *testing.T, repoPath string) {
				requireRevertedToCommit(t, repoPath)
			},
		},
		{
			name:  "an unstaged delete is restored",
			plant: func(t *testing.T, repoPath string) { removeFile(t, repoPath, "tracked.txt") },
			paths: []string{"tracked.txt"},
			check: func(t *testing.T, repoPath string) {
				requireRestored(t, repoPath)
			},
		},
		{
			name: "a staged delete is restored",
			plant: func(t *testing.T, repoPath string) {
				testgit.Git(t, repoPath, "rm", "--cached", "tracked.txt")
				removeFile(t, repoPath, "tracked.txt")
			},
			paths: []string{"tracked.txt"},
			check: func(t *testing.T, repoPath string) {
				requireRestored(t, repoPath)
			},
		},
		{
			name: "an untracked file is deleted",
			plant: func(t *testing.T, repoPath string) {
				testgit.WriteFile(t, repoPath, "scratch.txt", "never committed\n")
			},
			paths: []string{"scratch.txt"},
			check: func(t *testing.T, repoPath string) {
				if fileExists(repoPath, "scratch.txt") {
					t.Error("the untracked file is still on disk")
				}
				// A discard that reset the whole tree would show up here.
				if got := testgit.ReadFile(t, repoPath, "tracked.txt"); got != "committed\n" {
					t.Errorf("an untouched file changed: %q", got)
				}
			},
		},
		{
			name: "a staged new file loses its index entry before the file",
			plant: func(t *testing.T, repoPath string) {
				testgit.WriteFile(t, repoPath, "brand-new.txt", "staged as new\n")
				testgit.Git(t, repoPath, "add", "brand-new.txt")
			},
			paths: []string{"brand-new.txt"},
			check: func(t *testing.T, repoPath string) {
				if fileExists(repoPath, "brand-new.txt") {
					t.Error("the file is still on disk")
				}
				if staged := stagedFiles(t, repoPath); len(staged) != 0 {
					t.Errorf("the index still holds: %v", staged)
				}
			},
		},
		{
			// The panel may act on a status line a concurrent commit already cleared; that is a
			// no-op, not a fault.
			name:  "an unmodified file is a no-op",
			plant: func(t *testing.T, repoPath string) {},
			paths: []string{"tracked.txt"},
			check: func(t *testing.T, repoPath string) {
				if got := testgit.ReadFile(t, repoPath, "tracked.txt"); got != "committed\n" {
					t.Errorf("an unmodified file changed: %q", got)
				}
			},
		},
		{
			name: "one request reverts one path and deletes another",
			plant: func(t *testing.T, repoPath string) {
				testgit.WriteFile(t, repoPath, "tracked.txt", "edited on disk\n")
				testgit.WriteFile(t, repoPath, "scratch.txt", "never committed\n")
			},
			paths:       []string{"tracked.txt", "scratch.txt"},
			wantApplied: "tracked.txt scratch.txt",
			check: func(t *testing.T, repoPath string) {
				if got := testgit.ReadFile(t, repoPath, "tracked.txt"); got != "committed\n" {
					t.Errorf("worktree kept the edit: %q", got)
				}
				if fileExists(repoPath, "scratch.txt") {
					t.Error("the untracked file is still on disk")
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mux, repoPath := discardFixture(t)
			tt.plant(t, repoPath)

			rec := postDiscard(t, mux, tt.paths...)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if tt.wantApplied != "" {
				answer := decodeBatchAnswer(t, rec)
				if answer.Action != "discarded" {
					t.Errorf("action = %q, want discarded", answer.Action)
				}
				if strings.Join(answer.Applied, " ") != tt.wantApplied {
					t.Errorf("applied = %v, want %q", answer.Applied, tt.wantApplied)
				}
				if len(answer.Failed) != 0 {
					t.Errorf("failed = %v, want none", answer.Failed)
				}
			}
			tt.check(t, repoPath)
		})
	}
}

// requireRevertedToCommit asserts the tracked file is back to its committed content and the tree is
// clean — the half of a discard that a "revert" which left the index dirty would miss.
func requireRevertedToCommit(t *testing.T, repoPath string) {
	t.Helper()
	if got := testgit.ReadFile(t, repoPath, "tracked.txt"); got != "committed\n" {
		t.Errorf("worktree kept the edit: %q", got)
	}
	if out := testgit.GitOutput(t, repoPath, "status", "--porcelain"); out != "" {
		t.Errorf("status is not clean after the discard: %q", out)
	}
}

func requireRestored(t *testing.T, repoPath string) {
	t.Helper()
	if !fileExists(repoPath, "tracked.txt") {
		t.Fatal("the file is still gone")
	}
	if out := testgit.GitOutput(t, repoPath, "status", "--porcelain"); out != "" {
		t.Errorf("status is not clean after the discard: %q", out)
	}
}

func removeFile(t *testing.T, repoPath, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(repoPath, name)); err != nil {
		t.Fatalf("remove %s: %v", name, err)
	}
}

// With no commits there is nothing to revert to, so every listed entry is removed.
func TestHandleProjectDiscardPaths_NoCommitsYet(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)
	repoPath := testgit.EmptyRepo(t)
	testgit.WriteFile(t, repoPath, "untracked.txt", "a\n")
	testgit.WriteFile(t, repoPath, "staged.txt", "b\n")
	testgit.Git(t, repoPath, "add", "staged.txt")
	if err := s.CreateProject(&types.Project{ID: "discard-proj", Name: "discard-proj", Path: repoPath}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/git/discard", h.HandleProjectDiscardPaths)

	for _, name := range []string{"untracked.txt", "staged.txt"} {
		rec := postDiscard(t, mux, name)
		if rec.Code != http.StatusOK {
			t.Fatalf("discard %s: expected 200, got %d: %s", name, rec.Code, rec.Body.String())
		}
		if fileExists(repoPath, name) {
			t.Errorf("%s is still on disk", name)
		}
	}
	if staged := stagedFiles(t, repoPath); len(staged) != 0 {
		t.Errorf("the index still holds: %v", staged)
	}
}

// The panel never lists an ignored file; a request naming one must not delete it.
func TestHandleProjectDiscardPaths_LeavesIgnoredFileAlone(t *testing.T) {
	mux, repoPath := discardFixture(t)
	testgit.WriteFile(t, repoPath, ".gitignore", "build/\n")
	testgit.WriteFile(t, repoPath, "build/artifact.txt", "produced by the build\n")

	rec := postDiscard(t, mux, "build/artifact.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !fileExists(repoPath, "build/artifact.txt") {
		t.Error("git clean removed a file the repository ignores")
	}
}

// A path starting with '-' is a filename, not an option, because every path goes after `--`.
func TestHandleProjectDiscardPaths_DashPrefixedPathIsAFile(t *testing.T) {
	mux, repoPath := discardFixture(t)
	testgit.WriteFile(t, repoPath, "-dash.txt", "never committed\n")

	rec := postDiscard(t, mux, "-dash.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if fileExists(repoPath, "-dash.txt") {
		t.Error("the file is still on disk")
	}
}

// TestHandleProjectDiscardPaths_RefusesAPathOutsideTheRepo is the jail, and it is the whole batch's
// answer: every path is validated before any is written, so a selection holding an escape writes
// nothing at all — including the path in it that was legal.
func TestHandleProjectDiscardPaths_RefusesAPathOutsideTheRepo(t *testing.T) {
	for _, tt := range []struct {
		name  string
		paths []string
		// plant is the extra working-tree edit a legal path carries, which a refused batch must
		// leave exactly as it was.
		plant bool
	}{
		{name: "one escaping path", paths: []string{"../outside.txt"}},
		{name: "one legal path beside an escaping one", paths: []string{"tracked.txt", "../outside.txt"}, plant: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mux, repoPath := discardFixture(t)
			outside := filepath.Join(filepath.Dir(repoPath), "outside.txt")
			if err := os.WriteFile(outside, []byte("not yours\n"), 0o644); err != nil {
				t.Fatalf("write outside file: %v", err)
			}
			if tt.plant {
				testgit.WriteFile(t, repoPath, "tracked.txt", "edited on disk\n")
			}

			rec := postDiscard(t, mux, tt.paths...)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
			if _, err := os.Stat(outside); err != nil {
				t.Errorf("the file outside the repository was removed: %v", err)
			}
			if tt.plant {
				if got := testgit.ReadFile(t, repoPath, "tracked.txt"); got != "edited on disk\n" {
					t.Errorf("a refused batch still wrote: %q", got)
				}
			}
		})
	}
}

// An empty list is a caller error naming its field, not a write of nothing.
func TestHandleProjectDiscardPaths_RejectsEmptyPathList(t *testing.T) {
	mux, _ := discardFixture(t)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/projects/discard-proj/git/discard",
		strings.NewReader(`{"paths": []}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
