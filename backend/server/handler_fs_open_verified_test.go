package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// answeredIn fails rather than hanging: a handler that opens the name again instead of the file the jail
// checked never answers when that name is a FIFO, so the wait is the measurement.
func answeredIn(t *testing.T, within time.Duration, what string, run func() *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- run() }()
	select {
	case rr := <-done:
		return rr
	case <-time.After(within):
		t.Fatalf("%s never answered within %s: it opened the name, and the name is a FIFO", what, within)
		return nil
	}
}

// mkFifo plants a FIFO — the one file whose open waits. Skips where the filesystem refuses one.
func mkFifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Skipf("this filesystem cannot hold a FIFO, so an irregular name cannot be planted: %v", err)
	}
}

// TestFSReadRefusesANameThatIsNotARegularFile: the working-tree read is a host read of a project-scoped
// request, and an irregular name is a refusal, never a request parked on the task's next write.
func TestFSReadRefusesANameThatIsNotARegularFile(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)
	projectID, seed := fsSizeProject(t, s, "fs-irregular-name", "seed.txt", 4)
	pipe := filepath.Join(filepath.Dir(seed), "pipe")
	mkFifo(t, pipe)

	q := url.Values{"path": {pipe}, "project": {projectID}}
	req := testRequest(http.MethodGet, "/api/fs/read?"+q.Encode(), nil)
	withAuthCSRF(req, cookie, csrf)
	rr := answeredIn(t, 10*time.Second, "GET /api/fs/read on a FIFO", func() *httptest.ResponseRecorder {
		return testResponse(t, s, req)
	})
	if rr.Code == http.StatusOK {
		t.Errorf("a FIFO was served as file content: %s", rr.Body.String())
	}

	// The control: an ordinary file still reads, so the refusal above is the FIFO and not a broken route.
	q = url.Values{"path": {seed}, "project": {projectID}}
	seedReq := testRequest(http.MethodGet, "/api/fs/read?"+q.Encode(), nil)
	withAuthCSRF(seedReq, cookie, csrf)
	if rr := testResponse(t, s, seedReq); rr.Code != http.StatusOK {
		t.Errorf("an ordinary file was refused: %d %s", rr.Code, rr.Body.String())
	}
}

// TestPreviewAssetRefusesANameThatIsNotARegularFile: a preview asset is streamed straight to the
// response, and every other failure of that read is already a 404.
func TestPreviewAssetRefusesANameThatIsNotARegularFile(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, dir := previewTestProject(t, s)
	mkFifo(t, filepath.Join(dir, "style.css"))

	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
		"content": "<html></html>",
		"project": projectID,
		"path":    "page.html",
	})

	rr := answeredIn(t, 10*time.Second, "the preview asset read", func() *httptest.ResponseRecorder {
		return getPreview(t, s, "/api/preview/"+token+"/style.css")
	})
	if rr.Code != http.StatusNotFound {
		t.Errorf("a FIFO as a preview asset: got %d, want 404 %s", rr.Code, rr.Body.String())
	}
}
