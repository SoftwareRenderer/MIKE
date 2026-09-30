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

func TestHandleProjectDiscardPaths_RevertsModifiedFile(t *testing.T) {
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
func TestHandleProjectDiscardPaths_RevertsStagedAndUnstagedEdit(t *testing.T) {
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

func TestHandleProjectDiscardPaths_RestoresDeletedFile(t *testing.T) {
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

func TestHandleProjectDiscardPaths_DeletesUntrackedFile(t *testing.T) {
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
func TestHandleProjectDiscardPaths_DeletesStagedNewFile(t *testing.T) {
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

func TestHandleProjectDiscardPaths_RejectsPathOutsideRepo(t *testing.T) {
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

func TestHandleProjectDiscardPaths_RejectsMissingPath(t *testing.T) {
	mux, _ := discardFixture(t)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/projects/discard-proj/git/discard",
		strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// The panel may act on a status line a concurrent commit already cleared; that is a no-op, not a fault.
func TestHandleProjectDiscardPaths_CleanFileIsANoOp(t *testing.T) {
	mux, repoPath := discardFixture(t)

	rec := postDiscard(t, mux, "tracked.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := readFileOrFail(t, repoPath, "tracked.txt"); got != "committed\n" {
		t.Errorf("an unmodified file changed: %q", got)
	}
}

// One request carries the whole selection, and HEAD still decides revert from delete per path.
func TestHandleProjectDiscardPaths_RevertsAndDeletesTogether(t *testing.T) {
	mux, repoPath := discardFixture(t)
	testgit.WriteFile(t, repoPath, "tracked.txt", "edited on disk\n")
	testgit.WriteFile(t, repoPath, "scratch.txt", "never committed\n")

	rec := postDiscard(t, mux, "tracked.txt", "scratch.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	answer := decodeBatchAnswer(t, rec)
	if answer.Action != "discarded" {
		t.Errorf("action = %q, want discarded", answer.Action)
	}
	if strings.Join(answer.Applied, " ") != "tracked.txt scratch.txt" {
		t.Errorf("applied = %v", answer.Applied)
	}
	if len(answer.Failed) != 0 {
		t.Errorf("failed = %v", answer.Failed)
	}
	if got := readFileOrFail(t, repoPath, "tracked.txt"); got != "committed\n" {
		t.Errorf("worktree kept the edit: %q", got)
	}
	if fileExists(repoPath, "scratch.txt") {
		t.Error("the untracked file is still on disk")
	}
}

// Every path is validated before any is written, so a selection holding an escape writes nothing at all.
func TestHandleProjectDiscardPaths_RejectsTheBatchWhenOnePathEscapes(t *testing.T) {
	mux, repoPath := discardFixture(t)
	outside := filepath.Join(filepath.Dir(repoPath), "outside.txt")
	if err := os.WriteFile(outside, []byte("not yours\n"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	testgit.WriteFile(t, repoPath, "tracked.txt", "edited on disk\n")

	rec := postDiscard(t, mux, "tracked.txt", "../"+filepath.Base(outside))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := readFileOrFail(t, repoPath, "tracked.txt"); got != "edited on disk\n" {
		t.Errorf("a refused batch still wrote: %q", got)
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
