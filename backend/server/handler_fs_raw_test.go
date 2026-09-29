package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"mike/backend/exec"
	"mike/backend/types"
)

// One pixel of PNG: the bytes that come back are then recognisable as the file, not as padding.
var rawPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

// rawImageProject registers a project whose checkout holds the named files, all committed.
func rawImageProject(t *testing.T, s *Server, projectID string, files map[string][]byte) (string, string) {
	t.Helper()
	repo := t.TempDir()
	if err := s.store.CreateProject(&types.Project{ID: projectID, Name: projectID, Path: repo}); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, repo, "init")
	mustRunGit(t, repo, "config", "user.email", "test@test.com")
	mustRunGit(t, repo, "config", "user.name", "Test")
	names := make([]string, 0, len(files))
	for name, content := range files {
		full := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	for _, name := range names {
		mustRunGit(t, repo, "add", "--", name)
	}
	mustRunGit(t, repo, "commit", "-m", "files")
	return projectID, repo
}

// rawImageGet asks for one file, optionally from a ref and optionally under a task.
func rawImageGet(t *testing.T, s *Server, query url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := testRequest(http.MethodGet, "/api/fs/raw?"+query.Encode(), nil)
	return testResponseAuth(t, s, req)
}

func rawQuery(projectID, path, ref, task string) url.Values {
	q := url.Values{"project": {projectID}, "path": {path}}
	if ref != "" {
		q.Set("ref", ref)
	}
	if task != "" {
		q.Set("task", task)
	}
	return q
}

// TestFSRawServesAnImageFromTheWorktree: the bytes of a binary file, intact, with the Content-Type a
// browser paints. /api/fs/read cannot answer this — its JSON string replaces invalid bytes.
func TestFSRawServesAnImageFromTheWorktree(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	id, _ := rawImageProject(t, s, "p1", map[string][]byte{"docs/logo.png": rawPNG})

	rr := rawImageGet(t, s, rawQuery(id, "docs/logo.png", "", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if got := rr.Body.Bytes(); !bytes.Equal(got, rawPNG) {
		t.Errorf("body = %d bytes starting %x, want the file's own bytes", len(got), got[:min(8, len(got))])
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

// TestFSRawRefusesWhatItCannotPaint: the extension table is the permission, so a format that could run
// is refused by name. SVG is the row that matters — markup, a common repo asset, loaded by an <img>.
func TestFSRawRefusesWhatItCannotPaint(t *testing.T) {
	t.Parallel()
	body := []byte("<svg xmlns='http://www.w3.org/2000/svg' onload='alert(1)'></svg>")
	s := testServer(t)
	id, _ := rawImageProject(t, s, "p1", map[string][]byte{
		"icon.svg": body, "notes.txt": body, "README": body, "page.html": body, "script.png.sh": body,
	})

	for _, name := range []string{"icon.svg", "notes.txt", "README", "page.html", "script.png.sh"} {
		rr := rawImageGet(t, s, rawQuery(id, name, "", ""))
		if rr.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s: status = %d, want 415", name, rr.Code)
		}
		if bytes.Contains(rr.Body.Bytes(), body) {
			t.Errorf("%s: the refused response carried the file's content", name)
		}
	}
}

// TestFSRawRefusesAPathAboveTheProject covers an absolute path, a step above the root, a Windows drive
// path, and a symlink planted inside the repository — the last one because a lexical check reads right
// through it.
func TestFSRawRefusesAPathAboveTheProject(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "key.png")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, repo := rawImageProject(t, s, "p1", map[string][]byte{"link.png": rawPNG})
	full := filepath.Join(repo, "link.png")
	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, full); err != nil {
		t.Fatal(err)
	}

	rows := map[string]string{
		"absolute path":   secret,
		"parent step":     "../../" + filepath.Base(secretDir) + "/key.png",
		"symlink in repo": "link.png",
		"drive path":      "C:/Windows/win.ini",
	}
	for name, path := range rows {
		rr := rawImageGet(t, s, rawQuery(id, path, "", ""))
		if rr.Code >= 200 && rr.Code < 300 {
			t.Errorf("%s: status = %d, want a refusal", name, rr.Code)
		}
		if strings.Contains(rr.Body.String(), "SECRET") {
			t.Errorf("%s: the refusal carried the file's content", name)
		}
	}
}

// TestFSRawServesANameCarryingAColon: the refusal above is for a drive path, not for any name with a
// colon in it.
func TestFSRawServesANameCarryingAColon(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	id, _ := rawImageProject(t, s, "p1", map[string][]byte{"shots/a:b.png": rawPNG})

	rr := rawImageGet(t, s, rawQuery(id, "shots/a:b.png", "", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
}

// TestFSRawRefusesATaskOfAnotherProject: ?project and ?task arrive uncorrelated.
func TestFSRawRefusesATaskOfAnotherProject(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	a, _ := rawImageProject(t, s, "pa", map[string][]byte{"logo.png": rawPNG})
	b, _ := rawImageProject(t, s, "pb", map[string][]byte{"logo.png": rawPNG})
	if err := s.store.CreateTask(&types.Task{ID: "tb", ProjectID: b, Title: "tb", Status: types.StatusDone}); err != nil {
		t.Fatal(err)
	}

	rr := rawImageGet(t, s, rawQuery(a, "logo.png", "", "tb"))
	if rr.Code == http.StatusOK {
		t.Fatal("a task of another project was honoured")
	}
}

// TestFSRawServesAnImageReachedThroughAProjectSymlink: containment resolves the path before
// opening it, so a symlink filed inside the repository still answers — the swap detector reads the
// resolved name, not the link.
func TestFSRawServesAnImageReachedThroughAProjectSymlink(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	id, repo := rawImageProject(t, s, "p1", map[string][]byte{"vendor/logo.png": rawPNG, "link.png": rawPNG})
	full := filepath.Join(repo, "link.png")
	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("vendor/logo.png", full); err != nil {
		t.Fatal(err)
	}

	rr := rawImageGet(t, s, rawQuery(id, "link.png", "", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if !bytes.Equal(rr.Body.Bytes(), rawPNG) {
		t.Error("the symlink served something other than the linked image")
	}
}

// TestFSRawRefusesADirectoryNamedLikeAnImage: the extension check runs on the name, so a directory
// only meets the regular-file rule once the file is open. A FIFO would block the request on
// whatever a task writes into it, which is the same rule.
func TestFSRawRefusesADirectoryNamedLikeAnImage(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	id, repo := rawImageProject(t, s, "p1", map[string][]byte{"logo.png": rawPNG})
	if err := os.Mkdir(filepath.Join(repo, "traps.png"), 0o755); err != nil {
		t.Fatal(err)
	}

	rr := rawImageGet(t, s, rawQuery(id, "traps.png", "", ""))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Body.String(); strings.Contains(got, repo) {
		t.Errorf("the refusal named the host path: %s", got)
	}
}

// TestFSRawRefusalNamesNoHostPath: every caller answers a containment refusal with err.Error(), so
// the resolved target of a symlink committed into a repository must never reach that error — it
// would let a crafted repository read the server's directory layout back out.
func TestFSRawRefusalNamesNoHostPath(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	hidden := t.TempDir()
	target := filepath.Join(hidden, "id_ed25519.png")
	if err := os.WriteFile(target, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, repo := rawImageProject(t, s, "p1", map[string][]byte{"link.png": rawPNG})
	full := filepath.Join(repo, "link.png")
	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, full); err != nil {
		t.Fatal(err)
	}

	rr := rawImageGet(t, s, rawQuery(id, "link.png", "", ""))
	if rr.Code == http.StatusOK {
		t.Fatal("a symlink out of the project was served")
	}
	for _, leak := range []string{hidden, target, repo} {
		if strings.Contains(rr.Body.String(), leak) {
			t.Errorf("the refusal named the host path %q", leak)
		}
	}
}

// TestFSRawRefusesATaskOfAnotherProjectAtARef: the ref branch binds ?task= to ?project= on its own
// path through the handler, so the working-tree case above does not cover it.
func TestFSRawRefusesATaskOfAnotherProjectAtARef(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	a, _ := rawImageProject(t, s, "pa", map[string][]byte{"logo.png": rawPNG})
	b, _ := rawImageProject(t, s, "pb", map[string][]byte{"logo.png": rawPNG})
	if err := s.store.CreateTask(&types.Task{ID: "tb", ProjectID: b, Title: "tb", Status: types.StatusDone}); err != nil {
		t.Fatal(err)
	}

	rr := rawImageGet(t, s, rawQuery(a, "logo.png", "HEAD", "tb"))
	if rr.Code == http.StatusOK {
		t.Fatalf("a ref read honoured a task of another project: %s", rr.Body.String())
	}
}

// TestFSRawRefBranchRefusesAnOversizedBlob: the ref branch caps on `git cat-file -s` and again in
// the read, so a refusal must not cost the 20MB it refused.
func TestFSRawRefBranchRefusesAnOversizedBlob(t *testing.T) {
	// Serial: this measures the process-wide allocator (runtime.ReadMemStats), and a
	// concurrent test's allocations land in the same delta.
	s := testServer(t)
	id, repo := rawImageProject(t, s, "p1", map[string][]byte{"logo.png": rawPNG})
	big := bytes.Repeat([]byte{0x5a}, (exec.MaxUploadBytes)+(1<<20))
	if err := os.WriteFile(filepath.Join(repo, "big.png"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, repo, "add", "--", "big.png")
	mustRunGit(t, repo, "commit", "-m", "big")

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	rr := rawImageGet(t, s, rawQuery(id, "big.png", "HEAD", ""))
	runtime.ReadMemStats(&after)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", rr.Code, rr.Body.String())
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > allocBudget {
		t.Errorf("the ref refusal allocated %d bytes; the cap is meant to run before the read", grew)
	}
}

// TestFSRawImageIsNotBufferedOrRecompressed: a browser asks with Accept-Encoding, and CompressMiddleware
// buffers every GET body to hash an ETag. For an image that costs several times the file in memory and
// brotli-compresses bytes a codec already compressed, so the route's own cap stops being the bound.
func TestFSRawImageIsNotBufferedOrRecompressed(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	id, repo := rawImageProject(t, s, "p1", map[string][]byte{"logo.png": rawPNG})
	const size = 6 << 20
	payload := make([]byte, size) // incompressible, like a real image
	for i := range payload {
		payload[i] = byte(uint32(i*2654435761) >> 13)
	}
	if err := os.WriteFile(filepath.Join(repo, "shot.png"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	req := testRequest(http.MethodGet, "/api/fs/raw?"+rawQuery(id, "shot.png", "", "").Encode(), nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	rr := testResponseAuth(t, s, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q; an already-compressed image must go out as stored", got)
	}
	if got := rr.Header().Get("Content-Length"); got != strconv.Itoa(size) {
		t.Errorf("Content-Length = %q, want %d — the handler's own size reaches the client only unbuffered", got, size)
	}
	if rr.Body.Len() != size {
		t.Errorf("body = %d bytes, want %d", rr.Body.Len(), size)
	}
}

// TestFSRawPutsNoFileNameInAHeader: the file's name comes from the repository, so echoing it into
// Content-Disposition would make a crafted name a header writer.
func TestFSRawPutsNoFileNameInAHeader(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	id, _ := rawImageProject(t, s, "p1", map[string][]byte{"logo.png": rawPNG})

	rr := rawImageGet(t, s, rawQuery(id, "logo.png", "", ""))
	if got := rr.Header().Get("Content-Disposition"); got != "" {
		t.Errorf("Content-Disposition = %q, want unset", got)
	}
}

// TestFSRawReadsACommittedObject: the diff panel shows two sides of a changed image, and the old side
// is no longer on disk.
func TestFSRawReadsACommittedObject(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	id, repo := rawImageProject(t, s, "p1", map[string][]byte{"logo.png": rawPNG})
	head := strings.TrimSpace(mustRunGitOutput(t, repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "logo.png"), []byte("REPLACED"), 0o644); err != nil {
		t.Fatal(err)
	}

	rr := rawImageGet(t, s, rawQuery(id, "logo.png", head, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if !bytes.Equal(rr.Body.Bytes(), rawPNG) {
		t.Errorf("body = %q, want the committed bytes, not the working tree's", rr.Body.String())
	}

	// The same file spelled as a branch name, and an unknown object id.
	if got := rawImageGet(t, s, rawQuery(id, "logo.png", "HEAD", "")).Code; got != http.StatusOK {
		t.Errorf("HEAD: status = %d, want 200", got)
	}
	if got := rawImageGet(t, s, rawQuery(id, "logo.png", "0000000000000000000000000000000000000000", "")).Code; got == http.StatusOK {
		t.Errorf("unknown ref: status = %d, want a refusal", got)
	}
	if got := rawImageGet(t, s, rawQuery(id, "absent.png", "HEAD", "")).Code; got != http.StatusNotFound {
		t.Errorf("absent path: status = %d, want 404", got)
	}
	// An option-shaped ref is refused before it reaches git.
	if got := rawImageGet(t, s, rawQuery(id, "logo.png", "--output=/tmp/pwn", "")).Code; got != http.StatusBadRequest {
		t.Errorf("option ref: status = %d, want 400", got)
	}
}

// TestFSRawRefReadsATaskWhoseWorktreeIsGone: worktrees of a project share its object database, so a
// committed image is readable after commit-and-done deleted the worktree.
func TestFSRawRefReadsATaskWhoseWorktreeIsGone(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	id, _ := rawImageProject(t, s, "p1", map[string][]byte{"logo.png": rawPNG})
	if err := s.store.CreateTask(&types.Task{ID: "t1", ProjectID: id, Title: "t1", Status: types.StatusDone, Branch: "task/t1"}); err != nil {
		t.Fatal(err)
	}

	if got := rawImageGet(t, s, rawQuery(id, "logo.png", "HEAD", "t1")); got.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a task with no worktree: %s", got.Code, got.Body.String())
	}
	// The same request without a ref wants a working tree, and that tree does not exist.
	if got := rawImageGet(t, s, rawQuery(id, "logo.png", "", "t1")).Code; got == http.StatusOK {
		t.Error("a missing worktree answered a working-tree read")
	}
}

// TestFSRawRefusesAnOversizedImageBeforeReadingIt: the cap runs on the stat, so a refusal costs one stat
// and not the file. The sparse file is 32MB of hole, so what the request allocates is the only thing
// that tells the two orders apart.
func TestFSRawRefusesAnOversizedImageBeforeReadingIt(t *testing.T) {
	// Serial for the reason at TestFSRawRefBranchRefusesAnOversizedBlob.
	s := testServer(t)
	repo := t.TempDir()
	if err := s.store.CreateProject(&types.Project{ID: "p1", Name: "p1", Path: repo}); err != nil {
		t.Fatal(err)
	}
	sparseFile(t, filepath.Join(repo, "huge.png"), oversizedMiB)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	rr := rawImageGet(t, s, rawQuery("p1", "huge.png", "", ""))
	runtime.ReadMemStats(&after)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "20MB") {
		t.Errorf("refusal %q does not name the cap it applied", rr.Body.String())
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > allocBudget {
		t.Errorf("the refusal allocated %d bytes; the cap is meant to run before the read", grew)
	}
}
