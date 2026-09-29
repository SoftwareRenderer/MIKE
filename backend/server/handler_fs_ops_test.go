package server

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/types"
)

// fsOpsProject creates a non-sandboxed project at <temp>/repo holding `files`, whose keys are
// repository-relative paths.
func fsOpsProject(t *testing.T, s *Server, id string, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.CreateProject(&types.Project{ID: id, Name: "FS Ops Project", Path: repo}); err != nil {
		t.Fatal(err)
	}
	return repo
}

// fsOpsPost runs one of the batch routes and decodes the answer. It returns the status so a test
// can expect a failure.
func fsOpsPost(t *testing.T, s *Server, route, project, task string, body any) (int, fsOpsResponse) {
	t.Helper()
	cookie, csrf := loginTestCSRF(t, s)
	q := url.Values{"project": {project}}
	if task != "" {
		q.Set("task", task)
	}
	req := testRequest(http.MethodPost, "/api/fs/"+route+"?"+q.Encode(), body)
	withAuthCSRF(req, cookie, csrf)
	rr := testResponse(t, s, req)
	var res fsOpsResponse
	if rr.Code == http.StatusOK {
		res = mustJSON[fsOpsResponse](t, rr.Body)
	}
	return rr.Code, res
}

// fsRenamePost runs POST /api/fs/rename, whose answer is one path rather than a batch.
func fsRenamePost(t *testing.T, s *Server, project, path, name string) (int, string) {
	t.Helper()
	cookie, csrf := loginTestCSRF(t, s)
	req := testRequest(http.MethodPost, "/api/fs/rename?project="+project, map[string]string{
		"path": path, "name": name,
	})
	withAuthCSRF(req, cookie, csrf)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		return rr.Code, rr.Body.String()
	}
	return rr.Code, mustJSON[map[string]string](t, rr.Body)["path"]
}

func fsOpsWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fsOpsGitRepo is a project row over a real git checkout, so a worktree can be created from it.
func fsOpsGitRepo(t *testing.T, s *Server, id string, files map[string]string) string {
	t.Helper()
	repo := fsOpsProject(t, s, id, files)
	mustRunGit(t, repo, "init")
	mustRunGit(t, repo, "config", "user.email", "test@test.com")
	mustRunGit(t, repo, "config", "user.name", "Test")
	mustRunGit(t, repo, "add", ".")
	mustRunGit(t, repo, "commit", "-m", "commit")
	return repo
}

func TestFSDeleteRemovesFilesAndDirectories(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	repo := fsOpsProject(t, s, "fs-ops-del", map[string]string{
		"a.txt":          "a",
		"keep.txt":       "keep",
		"dir/b.txt":      "b",
		"dir/deep/c.txt": "c",
	})

	status, res := fsOpsPost(t, s, "delete", "fs-ops-del", "", map[string]any{
		"paths": []string{filepath.Join(repo, "a.txt"), filepath.Join(repo, "dir")},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if len(res.Done) != 2 || len(res.Failed) != 0 {
		t.Fatalf("done/failed = %+v / %+v", res.Done, res.Failed)
	}
	for _, gone := range []string{"a.txt", "dir"} {
		if _, err := os.Lstat(filepath.Join(repo, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived the delete", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "keep.txt")); err != nil {
		t.Errorf("a path outside the batch was removed: %v", err)
	}
}

func TestFSDeleteRefusesTreeRootsAndGitPaths(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	repo := fsOpsProject(t, s, "fs-ops-del-root", map[string]string{
		"a.txt":       "a",
		".git/config": "[core]",
	})

	// One item failing is still a successful request: 200, with the reason beside that path.
	status, res := fsOpsPost(t, s, "delete", "fs-ops-del-root", "", map[string]any{
		"paths": []string{repo, filepath.Join(repo, ".git")},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if len(res.Done) != 0 || len(res.Failed) != 2 {
		t.Fatalf("done/failed = %+v / %+v", res.Done, res.Failed)
	}
	if !strings.Contains(res.Failed[0].Reason, "cannot be deleted") {
		t.Errorf("root refusal said %q", res.Failed[0].Reason)
	}
	if !strings.Contains(res.Failed[1].Reason, ".git") {
		t.Errorf("git refusal said %q", res.Failed[1].Reason)
	}
	if _, err := os.Stat(filepath.Join(repo, "a.txt")); err != nil {
		t.Errorf("the checkout was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "config")); err != nil {
		t.Errorf("the git directory was removed: %v", err)
	}
}

func TestFSDeleteStaysInsideTheProject(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	repo := fsOpsProject(t, s, "fs-ops-del-escape", map[string]string{"a.txt": "a"})
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	fsOpsWrite(t, secret, "host secret")
	link := filepath.Join(repo, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{secret, filepath.Join(link, "secret.txt"), filepath.Join(repo, "..")} {
		req := testRequest(http.MethodPost, "/api/fs/delete?project=fs-ops-del-escape", map[string]any{
			"paths": []string{path},
		})
		cookie, csrf := loginTestCSRF(t, s)
		withAuthCSRF(req, cookie, csrf)
		if rr := testResponse(t, s, req); rr.Code != http.StatusBadRequest {
			t.Errorf("delete of %q: status %d, want 400: %s", path, rr.Code, rr.Body.String())
		}
	}
	if _, err := os.Stat(secret); err != nil {
		t.Errorf("a delete reached outside the project: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "a.txt")); err != nil {
		t.Errorf("the project was removed by a refused request: %v", err)
	}
}

// TestFSDeleteRefusesWorktreeGitPointer: a worktree's .git is a pointer file, and deleting it
// breaks every worktree of the repository.
func TestFSDeleteRefusesWorktreeGitPointer(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	projID := "fs-ops-del-pointer"
	fsOpsGitRepo(t, s, projID, map[string]string{"f.txt": "base"})

	taskID := createTestTask(t, s, projID, "delete pointer")
	worktree, _, err := s.executor.WorktreeMgr().CreateWorktree(projID, taskID, "task/"+taskID, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	_, res := fsOpsPost(t, s, "delete", projID, taskID, map[string]any{
		"paths": []string{filepath.Join(worktree, ".git")},
	})
	if len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Reason, ".git") {
		t.Fatalf("failed = %+v, want the .git refusal", res.Failed)
	}
	if _, err := os.Stat(filepath.Join(worktree, ".git")); err != nil {
		t.Fatalf("the worktree's .git pointer was deleted: %v", err)
	}
}

func TestFSCopyAutoRenamesAndReportsIt(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	repo := fsOpsProject(t, s, "fs-ops-copy", map[string]string{
		"report.txt":      "one",
		"report copy.txt": "two",
	})

	status, res := fsOpsPost(t, s, "copy", "fs-ops-copy", "", map[string]any{
		"paths": []string{filepath.Join(repo, "report.txt")},
		"dest":  repo,
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if len(res.Done) != 1 || len(res.Failed) != 0 {
		t.Fatalf("done/failed = %+v / %+v", res.Done, res.Failed)
	}
	want := filepath.Join(repo, "report copy2.txt")
	if res.Done[0].To != want || !res.Done[0].Renamed {
		t.Errorf("to = %q (renamed %v), want %q", res.Done[0].To, res.Done[0].Renamed, want)
	}
	if got, err := os.ReadFile(want); err != nil || string(got) != "one" {
		t.Errorf("renamed copy content = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "report.txt")); err != nil {
		t.Errorf("a copy removed its source: %v", err)
	}
}

func TestFSCopyIntoAnotherFolder(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	repo := fsOpsProject(t, s, "fs-ops-copy-dir", map[string]string{
		"note.md":   "note",
		"docs/x.md": "x",
	})

	status, res := fsOpsPost(t, s, "copy", "fs-ops-copy-dir", "", map[string]any{
		"paths": []string{filepath.Join(repo, "note.md")},
		"dest":  filepath.Join(repo, "docs"),
	})
	if status != http.StatusOK || len(res.Done) != 1 || res.Done[0].Renamed {
		t.Fatalf("status %d, done/failed = %+v / %+v", status, res.Done, res.Failed)
	}
	if res.Done[0].To != filepath.Join(repo, "docs", "note.md") {
		t.Errorf("to = %q", res.Done[0].To)
	}
	if got, err := os.ReadFile(filepath.Join(repo, "docs", "note.md")); err != nil || string(got) != "note" {
		t.Errorf("copied content = %q, %v", got, err)
	}
}

func TestFSMoveStaysInsideTheProject(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	repo := fsOpsProject(t, s, "fs-ops-move-escape", map[string]string{"a.txt": "a"})
	outside := t.TempDir()
	fsOpsWrite(t, filepath.Join(outside, "b.txt"), "b")

	// The source outside the project, and a destination outside it.
	for _, body := range []map[string]any{
		{"paths": []string{filepath.Join(outside, "b.txt")}, "dest": repo},
		{"paths": []string{filepath.Join(repo, "a.txt")}, "dest": outside},
	} {
		if status, _ := fsOpsPost(t, s, "move", "fs-ops-move-escape", "", body); status != http.StatusBadRequest {
			t.Errorf("move %+v: status %d, want 400", body, status)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "b.txt")); err != nil {
		t.Errorf("a file outside the project was moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "a.txt")); err != nil {
		t.Errorf("the refused request moved the in-project file: %v", err)
	}
}

// TestFSMoveRefusesBeforeTouchingAnything: one path outside the project refuses the whole request,
// so a move never lands half-done.
func TestFSMoveRefusesBeforeTouchingAnything(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	repo := fsOpsProject(t, s, "fs-ops-atomic", map[string]string{"one.txt": "1", "two.txt": "2"})
	other := t.TempDir()
	fsOpsWrite(t, filepath.Join(other, "ghost.txt"), "ghost")

	status, _ := fsOpsPost(t, s, "move", "fs-ops-atomic", "", map[string]any{
		"paths": []string{filepath.Join(repo, "one.txt"), filepath.Join(other, "ghost.txt"), filepath.Join(repo, "two.txt")},
		"dest":  repo,
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		if _, err := os.Stat(filepath.Join(repo, name)); err != nil {
			t.Errorf("%s moved although the batch was refused: %v", name, err)
		}
	}
}

func TestFSMoveIntoItsOwnFolderChangesNothing(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	repo := fsOpsProject(t, s, "fs-ops-move-here", map[string]string{"a.txt": "a"})

	status, res := fsOpsPost(t, s, "move", "fs-ops-move-here", "", map[string]any{
		"paths": []string{filepath.Join(repo, "a.txt")},
		"dest":  repo,
	})
	if status != http.StatusOK || len(res.Done) != 1 || res.Done[0].Renamed {
		t.Fatalf("status %d, done/failed = %+v / %+v", status, res.Done, res.Failed)
	}
	if _, err := os.Stat(filepath.Join(repo, "a.txt")); err != nil {
		t.Errorf("a no-op move lost the file: %v", err)
	}
	if entries, err := os.ReadDir(repo); err != nil || len(entries) != 1 {
		t.Errorf("a no-op move made %v entries: %v", entries, err)
	}
}

// TestFSMoveBetweenCheckoutAndWorktree: a request naming a task may touch the project checkout and
// that task's worktree, so moving a file between them is allowed.
func TestFSMoveBetweenCheckoutAndWorktree(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	projID := "fs-ops-wt-move"
	repo := fsOpsGitRepo(t, s, projID, map[string]string{"seed.txt": "seed", "notes.md": "notes"})
	taskID := createTestTask(t, s, projID, "cross-tree move")
	worktree, _, err := s.executor.WorktreeMgr().CreateWorktree(projID, taskID, "task/"+taskID, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	status, res := fsOpsPost(t, s, "move", projID, taskID, map[string]any{
		"paths": []string{filepath.Join(repo, "notes.md")},
		"dest":  worktree,
	})
	if status != http.StatusOK || len(res.Done) != 1 {
		t.Fatalf("status %d, done/failed = %+v / %+v", status, res.Done, res.Failed)
	}
	if _, err := os.Stat(filepath.Join(worktree, "notes.md")); err != nil {
		t.Errorf("the file did not arrive in the worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "notes.md")); !os.IsNotExist(err) {
		t.Errorf("the checkout still holds the moved file: %v", err)
	}
}

// TestFSMoveNeedsItsOwnTask: a task belonging to another project cannot point this request at its
// worktree.
func TestFSMoveNeedsItsOwnTask(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	repo := fsOpsProject(t, s, "fs-ops-foreign", map[string]string{"a.txt": "a"})
	fsOpsProject(t, s, "fs-ops-other", map[string]string{"b.txt": "b"})
	foreign := createTestTask(t, s, "fs-ops-other", "not mine")

	status, _ := fsOpsPost(t, s, "move", "fs-ops-foreign", foreign, map[string]any{
		"paths": []string{filepath.Join(repo, "a.txt")},
		"dest":  repo,
	})
	if status == http.StatusOK {
		t.Fatal("a move scoped to a task the project does not own succeeded")
	}
}

func TestFSRename(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	repo := fsOpsProject(t, s, "fs-ops-rename", map[string]string{"old.txt": "body", "taken.txt": "t"})

	status, newPath := fsRenamePost(t, s, "fs-ops-rename", filepath.Join(repo, "old.txt"), "new.txt")
	if status != http.StatusOK {
		t.Fatalf("rename: %d %s", status, newPath)
	}
	if newPath != filepath.Join(repo, "new.txt") {
		t.Errorf("path = %q", newPath)
	}
	if got, err := os.ReadFile(filepath.Join(repo, "new.txt")); err != nil || string(got) != "body" {
		t.Errorf("renamed content = %q, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(repo, "old.txt")); !os.IsNotExist(err) {
		t.Errorf("the old name survived: %v", err)
	}

	// A name already taken is refused, not replaced with a generated one.
	status, _ = fsRenamePost(t, s, "fs-ops-rename", filepath.Join(repo, "new.txt"), "taken.txt")
	if status != http.StatusConflict {
		t.Errorf("rename onto a taken name: status %d, want 409", status)
	}
	if _, err := os.Stat(filepath.Join(repo, "new.txt")); err != nil {
		t.Errorf("a refused rename moved the file: %v", err)
	}

	for _, name := range []string{"", ".", "..", "a/b", "sub/../x"} {
		if status, _ := fsRenamePost(t, s, "fs-ops-rename", filepath.Join(repo, "new.txt"), name); status == http.StatusOK {
			t.Errorf("rename to %q succeeded", name)
		}
	}
}

func TestFSRenameStaysInsideTheProject(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	repo := fsOpsProject(t, s, "fs-ops-rename-escape", map[string]string{"a.txt": "a"})
	outside := t.TempDir()
	fsOpsWrite(t, filepath.Join(outside, "secret.txt"), "host secret")
	link := filepath.Join(repo, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	if status, _ := fsRenamePost(t, s, "fs-ops-rename-escape", filepath.Join(link, "secret.txt"), "renamed.txt"); status != http.StatusBadRequest {
		t.Errorf("rename through a symlink: status %d, want 400", status)
	}
	if _, err := os.Stat(filepath.Join(outside, "secret.txt")); err != nil {
		t.Errorf("a rename reached outside the project: %v", err)
	}
}

func TestFSCopyRefusesOversizedFile(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	repo := fsOpsProject(t, s, "fs-ops-big", map[string]string{"small.txt": "small"})
	big := filepath.Join(repo, "big.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(64<<20 + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	_, res := fsOpsPost(t, s, "copy", "fs-ops-big", "", map[string]any{
		"paths": []string{big},
		"dest":  repo,
	})
	if len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Reason, "too large") {
		t.Fatalf("failed = %+v, want the size refusal", res.Failed)
	}
	if _, err := os.Lstat(filepath.Join(repo, "big copy.bin")); !os.IsNotExist(err) {
		t.Fatal("an oversized copy was made anyway")
	}
}

func TestFSOpsRequestShape(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	repo := fsOpsProject(t, s, "fs-ops-shape", map[string]string{"a.txt": "a"})
	repoFile := filepath.Join(repo, "a.txt")

	t.Run("an empty batch is refused", func(t *testing.T) {
		if status, _ := fsOpsPost(t, s, "delete", "fs-ops-shape", "", map[string]any{"paths": []string{}}); status != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", status)
		}
	})

	t.Run("a batch larger than the cap is refused", func(t *testing.T) {
		paths := make([]string, 0, maxFSOpsBatch+1)
		for i := 0; i <= maxFSOpsBatch; i++ {
			paths = append(paths, repoFile)
		}
		if status, _ := fsOpsPost(t, s, "delete", "fs-ops-shape", "", map[string]any{"paths": paths}); status != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", status)
		}
		if _, err := os.Stat(repoFile); err != nil {
			t.Errorf("the refused batch removed the file: %v", err)
		}
	})

	t.Run("a paste needs a destination", func(t *testing.T) {
		if status, _ := fsOpsPost(t, s, "copy", "fs-ops-shape", "", map[string]any{"paths": []string{repoFile}}); status != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", status)
		}
	})

	t.Run("a destination that is a file is refused", func(t *testing.T) {
		if status, _ := fsOpsPost(t, s, "copy", "fs-ops-shape", "", map[string]any{
			"paths": []string{repoFile}, "dest": repoFile,
		}); status != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", status)
		}
	})

	t.Run("a file operation without a project is refused", func(t *testing.T) {
		cookie, csrf := loginTestCSRF(t, s)
		req := testRequest(http.MethodPost, "/api/fs/delete", map[string]any{"paths": []string{repoFile}})
		withAuthCSRF(req, cookie, csrf)
		if rr := testResponse(t, s, req); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400: %s", rr.Code, rr.Body.String())
		}
		if _, err := os.Stat(repoFile); err != nil {
			t.Errorf("a request without a project removed a file: %v", err)
		}
	})
}
