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

	"mike/backend/testutil/testgit"
)

// uploadArgs describes one /api/fs/upload request the way the browser's drop zone
// sends it: the target directory and the relative path are form fields, the bytes
// are the "file" part. An empty relpath leaves the part's filename as the only
// name, which is the shape a single-file drop arrives in.
type uploadArgs struct {
	projectID string
	taskID    string
	dir       string
	relpath   string
	filename  string
	content   []byte
	overwrite bool
}

func uploadFile(t *testing.T, s *Server, a uploadArgs) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("path", a.dir); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("relpath", a.relpath); err != nil {
		t.Fatal(err)
	}
	if a.overwrite {
		if err := w.WriteField("overwrite", "1"); err != nil {
			t.Fatal(err)
		}
	}
	if a.filename != "" {
		part, err := w.CreateFormFile("file", a.filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(a.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	target := "/api/fs/upload"
	if a.projectID != "" {
		target += "?project=" + a.projectID
		if a.taskID != "" {
			target += "&task=" + a.taskID
		}
	}
	req := httptest.NewRequest(http.MethodPost, target, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return testResponseAuth(t, s, req)
}

// uploadProject is the shared fixture: one unsandboxed project rooted at a temp dir.
func uploadProject(t *testing.T, id string) (*Server, string) {
	t.Helper()
	s := testServer(t)
	dir := t.TempDir()
	sandboxFSProject(t, s, id, "Upload "+id, dir, false)
	return s, dir
}

// TestFSUploadWritesFiles covers what the drop zone needs the endpoint to be:
// binary-safe, able to create a directory tree, and unable to escape the project.
func TestFSUploadWritesFiles(t *testing.T) {
	s, dir := uploadProject(t, "upload-basic")

	t.Run("a plain file lands byte-for-byte", func(t *testing.T) {
		rr := uploadFile(t, s, uploadArgs{projectID: "upload-basic",
			dir:       dir,
			relpath:   "notes.txt",
			filename:  "notes.txt",
			content:   []byte("hello from a drop\n"),
			overwrite: false,
		})
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		data, err := os.ReadFile(filepath.Join(dir, "notes.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "hello from a drop\n" {
			t.Errorf("content = %q", data)
		}
	})

	// The reason /api/fs/write could not be reused: its `content` is a JSON string,
	// so a PNG arriving through it is UTF-8-mangled before it is written.
	t.Run("binary bytes survive the transport", func(t *testing.T) {
		raw := make([]byte, 256)
		for i := range raw {
			raw[i] = byte(i)
		}
		raw = append(raw, raw...) // include every byte value twice
		rr := uploadFile(t, s, uploadArgs{projectID: "upload-basic",
			dir:       dir,
			relpath:   "assets/img/logo.png",
			filename:  "logo.png",
			content:   raw,
			overwrite: false,
		})
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		data, err := os.ReadFile(filepath.Join(dir, "assets", "img", "logo.png"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, raw) {
			t.Errorf("binary content changed: %d bytes, first diff at %d", len(data), bytes.Index(data, raw))
		}
	})

	t.Run("a new file is 0644 and leaves no temporary behind", func(t *testing.T) {
		fi, err := os.Stat(filepath.Join(dir, "assets", "img", "logo.png"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0644 {
			t.Errorf("mode = %v, want -rw-r--r--", fi.Mode().Perm())
		}
		entries, err := os.ReadDir(filepath.Join(dir, "assets", "img"))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Errorf("unexpected files next to the upload: %v", entries)
		}
	})

	// A folder drop arrives as many requests; a part whose filename lost its
	// directories (Go strips them) is the single-file case, so `relpath` is optional.
	t.Run("relpath is optional and falls back to the part filename", func(t *testing.T) {
		rr := uploadFile(t, s, uploadArgs{projectID: "upload-basic",
			dir:       dir,
			relpath:   "",
			filename:  "fallback.txt",
			content:   []byte("x"),
			overwrite: false,
		})
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		if _, err := os.Stat(filepath.Join(dir, "fallback.txt")); err != nil {
			t.Errorf("file not written: %v", err)
		}
	})

	t.Run("a missing part or field is refused", func(t *testing.T) {
		rr := uploadFile(t, s, uploadArgs{projectID: "upload-basic",
			dir:       dir,
			relpath:   "no-bytes.txt",
			filename:  "",
			content:   nil,
			overwrite: false,
		})
		if rr.Code != http.StatusBadRequest {
			t.Errorf("no file part: got %d, want 400", rr.Code)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/fs/upload?project=upload-basic", strings.NewReader("not multipart"))
		req.Header.Set("Content-Type", "text/plain")
		if rr := testResponseAuth(t, s, req); rr.Code != http.StatusBadRequest {
			t.Errorf("not multipart: got %d, want 400", rr.Code)
		}
	})

	t.Run("a project-less upload is refused", func(t *testing.T) {
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		_ = w.WriteField("path", dir)
		part, _ := w.CreateFormFile("file", "orphan.txt")
		_, _ = part.Write([]byte("x"))
		_ = w.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/fs/upload", &body)
		req.Header.Set("Content-Type", w.FormDataContentType())
		if rr := testResponseAuth(t, s, req); rr.Code != http.StatusBadRequest {
			t.Errorf("no project: got %d, want 400", rr.Code)
		}
	})

	t.Run("GET is not an upload", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/fs/upload?project=upload-basic", nil)
		if rr := testResponseAuth(t, s, req); rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET: got %d, want 405", rr.Code)
		}
	})
}

// TestFSUploadRejectsEscapes is the containment proof: `relpath` is the one new
// client-supplied path component, and every way out of the project must be refused
// before anything is created.
func TestFSUploadRejectsEscapes(t *testing.T) {
	s, dir := uploadProject(t, "upload-escape")
	outside := t.TempDir()

	rejected := map[string]string{
		"parent segment":        "../escaped.txt",
		"nested parent segment": "a/b/../../../escaped.txt",
		"absolute":              filepath.Join(outside, "escaped.txt"),
		"backslash separator":   `a\..\escaped.txt`,
		"empty segment":         "a//b.txt",
		"dot segment":           "./ok.txt/../escaped.txt",
		"trailing slash":        "adir/",
		"reserved temp name":    ".mike-tmp-upload.txt",
		"control character":     "escaped\n.txt",
	}
	for name, rel := range rejected {
		rr := uploadFile(t, s, uploadArgs{projectID: "upload-escape",
			dir:       dir,
			relpath:   rel,
			filename:  "ignored.txt",
			content:   []byte("nope"),
			overwrite: false,
		})
		if rr.Code == http.StatusOK {
			t.Errorf("%s: upload of %q succeeded", name, rel)
			continue
		}
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d for %q, want 400 (%s)", name, rr.Code, rel, rr.Body.String())
		}
	}

	// The escape was aimed at a sibling temp dir: nothing may exist there, and the
	// project must not have gained the tree either.
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("escaped writes landed outside the project: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(dir, "escaped.txt")); err == nil {
		t.Error("a rejected relpath still created its file")
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); err == nil {
		t.Error("a rejected relpath still created its directories")
	}
}

// TestFSUploadAsksBeforeOverwriting: replacing a file is the user's decision, so the
// default answer is 409 and the client only sends overwrite after the dialog.
func TestFSUploadAsksBeforeOverwriting(t *testing.T) {
	s, dir := uploadProject(t, "upload-overwrite")
	target := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(target, []byte("original"), 0755); err != nil {
		t.Fatal(err)
	}

	rr := uploadFile(t, s, uploadArgs{projectID: "upload-overwrite",
		dir:       dir,
		relpath:   "keep.txt",
		filename:  "keep.txt",
		content:   []byte("replacement"),
		overwrite: false,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("first upload: got %d, want 409 (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "keep.txt") {
		t.Errorf("the 409 does not name the file: %s", rr.Body.String())
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Errorf("a refused overwrite changed the file: %q", data)
	}

	rr = uploadFile(t, s, uploadArgs{projectID: "upload-overwrite",
		dir:       dir,
		relpath:   "keep.txt",
		filename:  "keep.txt",
		content:   []byte("replacement"),
		overwrite: true,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("confirmed overwrite: got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "replacement" {
		t.Errorf("content after confirmed overwrite = %q, err %v", data, err)
	}
	// WriteFileAtomic keeps the mode of the file it replaced: an executable script
	// dropped over another must stay runnable.
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0755 {
		t.Errorf("mode after overwrite = %v, want -rwxr-xr-x (err %v)", fi.Mode().Perm(), err)
	}
}

// TestFSUploadRefusesDirectoryNames: a directory is not an overwrite candidate, and
// replacing one would hide whatever is inside it.
func TestFSUploadRefusesDirectoryNames(t *testing.T) {
	s, dir := uploadProject(t, "upload-dir")
	if err := os.Mkdir(filepath.Join(dir, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	rr := uploadFile(t, s, uploadArgs{projectID: "upload-dir",
		dir:       dir,
		relpath:   "src",
		filename:  "src",
		content:   []byte("now a file?"),
		overwrite: true,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (%s)", rr.Code, rr.Body.String())
	}
	info, err := os.Stat(filepath.Join(dir, "src"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Error("a directory was replaced by a file")
	}
}

// TestFSUploadCapsFileSize proves the 20 MB limit is enforced on the wire rather
// than trusted from the client's File.size.
func TestFSUploadCapsFileSize(t *testing.T) {
	s, dir := uploadProject(t, "upload-size")

	chunk := make([]byte, 1<<20)
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("path", dir)
	part, err := w.CreateFormFile("file", "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxUploadSize/(1<<20)+2; i++ {
		if _, err := part.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/fs/upload?project=upload-size", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("got %d, want 413 (%s)", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "big.bin")); err == nil {
		t.Error("an oversized file was written anyway")
	}
}

// TestFSUploadNeedsCSRF: the endpoint writes inside the project, so a cross-site
// POST must not be able to plant a file there.
func TestFSUploadNeedsCSRF(t *testing.T) {
	s, dir := uploadProject(t, "upload-csrf")

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("path", dir)
	part, _ := w.CreateFormFile("file", "csrf.txt")
	_, _ = part.Write([]byte("planted"))
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/fs/upload?project=upload-csrf", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	cookie, _ := loginTestCSRF(t, s)
	withAuth(req, cookie) // session, but no X-CSRF-Token header

	rr := testResponse(t, s, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (%s)", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "csrf.txt")); err == nil {
		t.Error("a CSRF-less upload wrote into the project")
	}
}

// TestFSUploadRejectsSymlinkEscape holds the same line /api/fs/write holds: a link
// planted inside the project must not turn an upload into a write outside it.
func TestFSUploadRejectsSymlinkEscape(t *testing.T) {
	s, dir := uploadProject(t, "upload-symlink")
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("do not touch"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}

	rr := uploadFile(t, s, uploadArgs{projectID: "upload-symlink",
		dir:       dir,
		relpath:   "link.txt",
		filename:  "link.txt",
		content:   []byte("through the link"),
		overwrite: true,
	})
	if rr.Code == http.StatusOK {
		t.Fatal("an upload wrote through a symlink planted in the project")
	}
	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "do not touch" {
		t.Errorf("the symlink target changed: %q", data)
	}
}

// TestFSUploadIntoTaskWorktree is the task explorer's path: the same endpoint, the
// `task` parameter deciding which checkout the drop lands in. The project's own
// directory must stay untouched — a drop in a task's file list is not a change to
// the branch the project is on.
func TestFSUploadIntoTaskWorktree(t *testing.T) {
	s := testServer(t)
	repo := t.TempDir()
	const projID = "upload-worktree"
	sandboxFSProject(t, s, projID, "Upload Worktree", repo, false)
	testgit.Git(t, repo, "init")
	testgit.Git(t, repo, "config", "user.email", "test@test.com")
	testgit.Git(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testgit.Git(t, repo, "add", "seed.txt")
	testgit.Git(t, repo, "commit", "-m", "seed")

	taskID := createTestTask(t, s, projID, "upload task")
	worktreePath, _, err := s.executor.WorktreeMgr().CreateWorktree(projID, taskID, "task/"+taskID, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	rr := uploadFile(t, s, uploadArgs{projectID: projID, taskID: taskID, dir: worktreePath, relpath: "fixtures/dropped.json", filename: "dropped.json", content: []byte(`{"a":1}`)})
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(worktreePath, "fixtures", "dropped.json"))
	if err != nil {
		t.Fatalf("upload did not reach the worktree: %v", err)
	}
	if string(data) != `{"a":1}` {
		t.Errorf("content = %q", data)
	}
	if _, err := os.Stat(filepath.Join(repo, "fixtures")); !os.IsNotExist(err) {
		t.Error("a task-scoped upload wrote into the project checkout as well")
	}
}
