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

// taskUploadProject seeds an unsandboxed project with one task, with TMPDIR redirected to where
// that task's upload lands.
func taskUploadProject(t *testing.T, id string) *Server {
	t.Helper()
	s := testServer(t)
	t.Setenv("TMPDIR", t.TempDir())

	now := time.Now().UTC()
	sandboxFSProject(t, s, id, "Uploads "+id, t.TempDir(), false)
	if err := s.store.CreateTask(&types.Task{
		ID: "t1", ProjectID: id, Title: "t1", Prompt: "prompt", Status: types.StatusBacklog,
		ExecutionState: types.ExecStateIdle, CurrentThreadID: "main", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.store.CreateThread(&types.TaskThread{ID: "main", TaskID: "t1", Name: "Main", CreatedAt: now}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	return s
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
	s := taskUploadProject(t, "up-basic")

	rr := postTaskUpload(t, s, "up-basic", "t1", "report.pdf", []byte("%PDF-1.7\x00\xff"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	got := mustJSON[map[string]string](t, rr.Body)
	path := got["path"]
	if path == "" {
		t.Fatalf("no path in the response: %s", rr.Body.String())
	}
	if filepath.Base(path) != "report.pdf" {
		t.Errorf("path = %q, want the dropped file's own name", path)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the path in the response holds nothing readable: %v", err)
	}
	if string(written) != "%PDF-1.7\x00\xff" {
		t.Errorf("bytes changed on the way through the form: %q", written)
	}
}

func TestTaskUploadRefusesRequestsWithoutAFileOrWithoutATask(t *testing.T) {
	s := taskUploadProject(t, "up-refuse")

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
	s := taskUploadProject(t, "up-size")

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
	s := taskUploadProject(t, "up-csrf")

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
	if entries, _ := os.ReadDir(os.TempDir()); len(entries) != 0 {
		t.Errorf("the refused request still wrote: %v", entries)
	}
}
