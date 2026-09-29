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
// asserted here against a real repository.

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
	mux.HandleFunc("POST /api/projects/{projectID}/git/discard", h.HandleProjectDiscardFile)
	return mux, repoPath
}

func postDiscard(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/projects/discard-proj/git/discard",
		strings.NewReader(`{"path": "`+path+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func readFileOrFail(t *testing.T, repoPath, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoPath, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func fileExists(repoPath, name string) bool {
	_, err := os.Lstat(filepath.Join(repoPath, name))
	return err == nil
}

func TestHandleProjectDiscardFile_RevertsModifiedFile(t *testing.T) {
	t.Parallel()
	mux, repoPath := discardFixture(t)
	testgit.WriteFile(t, repoPath, "tracked.txt", "edited on disk\n")

	rec := postDiscard(t, mux, "tracked.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := readFileOrFail(t, repoPath, "tracked.txt"); got != "committed\n" {
		t.Errorf("worktree kept the edit: %q", got)
	}
	if out := testgit.GitOutput(t, repoPath, "status", "--porcelain"); out != "" {
		t.Errorf("status is not clean after the discard: %q", out)
	}
}

// Discard is one click, not "unstage, then revert".
func TestHandleProjectDiscardFile_RevertsStagedAndUnstagedEdit(t *testing.T) {
	t.Parallel()
	mux, repoPath := discardFixture(t)
	testgit.WriteFile(t, repoPath, "tracked.txt", "staged half\n")
	testgit.Git(t, repoPath, "add", "tracked.txt")
	testgit.WriteFile(t, repoPath, "tracked.txt", "plus an unstaged half\n")

	rec := postDiscard(t, mux, "tracked.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := readFileOrFail(t, repoPath, "tracked.txt"); got != "committed\n" {
		t.Errorf("worktree kept the edit: %q", got)
	}
	if out := testgit.GitOutput(t, repoPath, "status", "--porcelain"); out != "" {
		t.Errorf("status is not clean after the discard: %q", out)
	}
}

func TestHandleProjectDiscardFile_RestoresDeletedFile(t *testing.T) {
	t.Parallel()
	for _, stage := range []bool{false, true} {
		name := "unstaged delete"
		if stage {
			name = "staged delete"
		}
		t.Run(name, func(t *testing.T) {
			mux, repoPath := discardFixture(t)
			if stage {
				testgit.Git(t, repoPath, "rm", "--cached", "tracked.txt")
			}
			if err := os.Remove(filepath.Join(repoPath, "tracked.txt")); err != nil {
				t.Fatalf("remove: %v", err)
			}

			rec := postDiscard(t, mux, "tracked.txt")
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if !fileExists(repoPath, "tracked.txt") {
				t.Fatal("the file is still gone")
			}
			if out := testgit.GitOutput(t, repoPath, "status", "--porcelain"); out != "" {
				t.Errorf("status is not clean after the discard: %q", out)
			}
		})
	}
}

func TestHandleProjectDiscardFile_DeletesUntrackedFile(t *testing.T) {
	t.Parallel()
	mux, repoPath := discardFixture(t)
	testgit.WriteFile(t, repoPath, "scratch.txt", "never committed\n")

	rec := postDiscard(t, mux, "scratch.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if fileExists(repoPath, "scratch.txt") {
		t.Error("the untracked file is still on disk")
	}
	// A discard that reset the whole tree would show up here.
	if got := readFileOrFail(t, repoPath, "tracked.txt"); got != "committed\n" {
		t.Errorf("an untouched file changed: %q", got)
	}
}

// The index entry has to go before the file, or the discard leaves a staged addition behind.
func TestHandleProjectDiscardFile_DeletesStagedNewFile(t *testing.T) {
	t.Parallel()
	mux, repoPath := discardFixture(t)
	testgit.WriteFile(t, repoPath, "brand-new.txt", "staged as new\n")
	testgit.Git(t, repoPath, "add", "brand-new.txt")

	rec := postDiscard(t, mux, "brand-new.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if fileExists(repoPath, "brand-new.txt") {
		t.Error("the file is still on disk")
	}
	if staged := stagedFiles(t, repoPath); len(staged) != 0 {
		t.Errorf("the index still holds: %v", staged)
	}
}

// With no commits there is nothing to revert to, so every listed entry is removed.
func TestHandleProjectDiscardFile_NoCommitsYet(t *testing.T) {
	t.Parallel()
	h, s, _, _ := newTestGitHandler(t)
	repoPath := testgit.EmptyRepo(t)
	testgit.WriteFile(t, repoPath, "untracked.txt", "a\n")
	testgit.WriteFile(t, repoPath, "staged.txt", "b\n")
	testgit.Git(t, repoPath, "add", "staged.txt")
	if err := s.CreateProject(&types.Project{ID: "discard-proj", Name: "discard-proj", Path: repoPath}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/git/discard", h.HandleProjectDiscardFile)

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
func TestHandleProjectDiscardFile_LeavesIgnoredFileAlone(t *testing.T) {
	t.Parallel()
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
func TestHandleProjectDiscardFile_DashPrefixedPathIsAFile(t *testing.T) {
	t.Parallel()
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

func TestHandleProjectDiscardFile_RejectsPathOutsideRepo(t *testing.T) {
	t.Parallel()
	mux, repoPath := discardFixture(t)
	outside := filepath.Join(filepath.Dir(repoPath), "outside.txt")
	if err := os.WriteFile(outside, []byte("not yours\n"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	rec := postDiscard(t, mux, "../"+filepath.Base(outside))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !fileExists(filepath.Dir(repoPath), filepath.Base(outside)) {
		t.Error("the file outside the repository was removed")
	}
}

func TestHandleProjectDiscardFile_RejectsMissingPath(t *testing.T) {
	t.Parallel()
	mux, _ := discardFixture(t)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/projects/discard-proj/git/discard",
		strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// The panel may act on a status line a concurrent commit already cleared; that is a no-op, not a fault.
func TestHandleProjectDiscardFile_CleanFileIsANoOp(t *testing.T) {
	t.Parallel()
	mux, repoPath := discardFixture(t)

	rec := postDiscard(t, mux, "tracked.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := readFileOrFail(t, repoPath, "tracked.txt"); got != "committed\n" {
		t.Errorf("an unmodified file changed: %q", got)
	}
}
