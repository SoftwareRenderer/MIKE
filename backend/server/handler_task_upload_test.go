package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/types"
	"kanban/pkg/bridge/protocol"
)

// taskUploadProject seeds an unsandboxed project whose directory is a real git repository, one task, and
// that task's worktree — the directory an upload lands in, and the one whose git state is part of the claim.
func taskUploadProject(t *testing.T, id string) (*Server, string) {
	t.Helper()
	s := testServer(t)
	t.Setenv("TMPDIR", t.TempDir())

	repo := t.TempDir()
	sandboxFSProject(t, s, id, "Uploads "+id, repo, false)
	mustRunGit(t, repo, "init")
	mustRunGit(t, repo, "config", "user.email", "test@test.com")
	mustRunGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# up\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, repo, "add", "README.md")
	mustRunGit(t, repo, "commit", "-m", "Initial")

	now := time.Now().UTC()
	// The branch is set because the deletion path takes the worktree only when the row names a branch.
	if err := s.store.CreateTask(&types.Task{
		ID: "t1", ProjectID: id, Title: "t1", Prompt: "prompt", Status: types.StatusBacklog,
		Branch: "task/t1", ExecutionState: types.ExecStateIdle, CurrentThreadID: "main", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.store.CreateThread(&types.TaskThread{ID: "main", TaskID: "t1", Name: "Main", CreatedAt: now}); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	wtPath, _, err := s.executor.WorktreeMgr().CreateWorktree(id, "t1", "task/t1", "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	return s, wtPath
}

// postTaskUpload sends one "file" part; nil content leaves the part out entirely.
func postTaskUpload(t *testing.T, s *Server, projectID, taskID, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if content != nil {
		part, err := w.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/uploads", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return testResponseAuth(t, s, req)
}

func TestTaskUploadAnswersThePathItWrote(t *testing.T) {
	s, wtPath := taskUploadProject(t, "up-basic")

	rr := postTaskUpload(t, s, "up-basic", "t1", "report.pdf", []byte("%PDF-1.7\x00\xff"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	got := mustJSON[map[string]string](t, rr.Body)
	path := got["path"]
	if path == "" {
		t.Fatalf("no path in the response: %s", rr.Body.String())
	}
	if want := filepath.Join(wtPath, ".mike-uploads", "report.pdf"); path != want {
		t.Errorf("path = %q, want the task's own worktree: %q", path, want)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the path in the response holds nothing readable: %v", err)
	}
	if string(written) != "%PDF-1.7\x00\xff" {
		t.Errorf("bytes changed on the way through the form: %q", written)
	}
	// The upload is git's to discard if it shows in the task's status, so it must not.
	if out := strings.TrimSpace(mustRunGitOutput(t, wtPath, "status", "--porcelain")); out != "" {
		t.Errorf("the upload shows in the task's git status:\n%s", out)
	}
}

// The worktree is where the file lands, and preparing an environment is not this call's side effect.
func TestTaskUploadRefusesATaskWithNoWorktree(t *testing.T) {
	s, wtPath := taskUploadProject(t, "up-noworktree")
	if err := os.RemoveAll(wtPath); err != nil {
		t.Fatal(err)
	}

	rr := postTaskUpload(t, s, "up-noworktree", "t1", "report.pdf", []byte("x"))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "start the task") {
		t.Errorf("refusal %q does not say what to do about it", rr.Body.String())
	}
}

func TestTaskUploadRefusesRequestsWithoutAFileOrWithoutATask(t *testing.T) {
	s, _ := taskUploadProject(t, "up-refuse")

	if rr := postTaskUpload(t, s, "up-refuse", "t1", "", nil); rr.Code != http.StatusBadRequest {
		t.Errorf("no file part: status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	// Go's multipart reader strips directories off a filename, so `../x` arrives as `x`; the
	// one-segment rule is held at the executor, and a long name is the case here.
	if rr := postTaskUpload(t, s, "up-refuse", "t1", strings.Repeat("x", 300)+".txt", []byte("x")); rr.Code != http.StatusBadRequest {
		t.Errorf("name over the filesystem's limit: status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if rr := postTaskUpload(t, s, "up-refuse", "no-such-task", "note.txt", []byte("x")); rr.Code != http.StatusNotFound {
		t.Errorf("unknown task: status = %d, want 404: %s", rr.Code, rr.Body.String())
	}
}

func TestTaskUploadRefusesAFileOverTheLimit(t *testing.T) {
	s, _ := taskUploadProject(t, "up-size")

	oversize := bytes.Repeat([]byte("x"), protocol.MaxWriteBytes+1)
	rr := postTaskUpload(t, s, "up-size", "t1", "big.bin", oversize)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "MB") {
		t.Errorf("refusal %q does not name the limit the user is over", rr.Body.String())
	}
}

// A methodless route would escape CSRFMiddleware and take a cross-site form post.
func TestTaskUploadRequiresCSRF(t *testing.T) {
	s, wtPath := taskUploadProject(t, "up-csrf")

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("x"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/up-csrf/tasks/t1/uploads", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	cookie, _ := loginTestCSRF(t, s)
	withAuth(req, cookie) // the session, without the token

	rr := httptest.NewRecorder()
	s.CORSMiddleware(s.SessionMiddleware(s.CSRFMiddleware(CompressMiddleware(s.mux)))).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403: %s", rr.Code, rr.Body.String())
	}
	if entries, err := os.ReadDir(filepath.Join(wtPath, ".mike-uploads")); err == nil && len(entries) != 0 {
		t.Errorf("the refused request still wrote: %v", entries)
	}
}

// An upload is deleted with its task and with nothing else. Every other transition that stops the executor
// or the sandbox must leave the file where the message says it is — including the sandbox stop, which used
// to take the task's uploads with it.
func TestUploadedFileSurvivesEverythingExceptDeletingTheTask(t *testing.T) {
	s, wtPath := taskUploadProject(t, "up-keep")

	rr := postTaskUpload(t, s, "up-keep", "t1", "report.pdf", []byte("%PDF"))
	if rr.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", rr.Code, rr.Body.String())
	}
	upload := filepath.Join(wtPath, ".mike-uploads", "report.pdf")

	keep := func(what string) {
		t.Helper()
		if _, err := os.Stat(upload); err != nil {
			t.Errorf("%s deleted the task's upload: %v", what, err)
		}
	}

	s.executor.StopTask("t1")
	keep("stopping the task")

	s.executor.StopSandbox("t1")
	keep("stopping the task's sandbox")

	rr = testResponseAuth(t, s, testRequest(http.MethodPut, "/api/projects/up-keep/tasks/t1", map[string]any{"operating_mode": "plan"}))
	if rr.Code != http.StatusOK {
		t.Fatalf("mode change = %d: %s", rr.Code, rr.Body.String())
	}
	keep("changing the mount mode")
}

func TestDeletingTheTaskTakesItsUploads(t *testing.T) {
	s, wtPath := taskUploadProject(t, "up-delete")

	rr := postTaskUpload(t, s, "up-delete", "t1", "report.pdf", []byte("%PDF"))
	if rr.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", rr.Code, rr.Body.String())
	}
	upload := filepath.Join(wtPath, ".mike-uploads", "report.pdf")
	if _, err := os.Stat(upload); err != nil {
		t.Fatalf("the upload did not land: %v", err)
	}

	if rr := testResponseAuth(t, s, testRequest(http.MethodDelete, "/api/projects/up-delete/tasks/t1", nil)); rr.Code != http.StatusNoContent {
		t.Fatalf("delete task = %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(upload); !os.IsNotExist(err) {
		t.Errorf("the deleted task's upload survived: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("the worktree that holds the uploads survived: %v", err)
	}
}
