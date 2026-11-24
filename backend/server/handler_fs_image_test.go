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

	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// One pixel of PNG: the bytes that come back are then recognisable as the file, not as padding.
var pngFixture = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

// committedProject registers a project whose checkout holds the named files, all committed.
func committedProject(t *testing.T, s *Server, projectID string, files map[string][]byte) (string, string) {
	t.Helper()
	repo := t.TempDir()
	if err := s.store.CreateProject(&types.Project{ID: projectID, Name: projectID, Path: repo}); err != nil {
		t.Fatal(err)
	}
	testgit.Git(t, repo, "init")
	testgit.Git(t, repo, "config", "user.email", "test@test.com")
	testgit.Git(t, repo, "config", "user.name", "Test")
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
		testgit.Git(t, repo, "add", "--", name)
	}
	testgit.Git(t, repo, "commit", "-m", "files")
	return projectID, repo
}

// imageGet asks GET /api/fs/read for one file's bytes as the picture kind.
func imageGet(t *testing.T, s *Server, query url.Values) *httptest.ResponseRecorder {
	t.Helper()
	return readKindGet(t, s, fsKindImage, query)
}

// readQuery is the scope every file read carries: which project, which name in it, and which tree —
// the working one or a commit's — the name is read from.
func readQuery(projectID, path, ref, task string) url.Values {
	q := url.Values{"project": {projectID}, "path": {path}}
	if ref != "" {
		q.Set("ref", ref)
	}
	if task != "" {
		q.Set("task", task)
	}
	return q
}

// TestFSImageServesAnImageFromTheWorktree: the bytes of a binary file, intact, with the Content-Type a
// browser paints. /api/fs/read cannot answer this — its JSON string replaces invalid bytes.
func TestFSImageServesAnImageFromTheWorktree(t *testing.T) {
	s := testServer(t)
	id, _ := committedProject(t, s, "p1", map[string][]byte{"docs/logo.png": pngFixture})

	rr := imageGet(t, s, readQuery(id, "docs/logo.png", "", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if got := rr.Body.Bytes(); !bytes.Equal(got, pngFixture) {
		t.Errorf("body = %d bytes starting %x, want the file's own bytes", len(got), got[:min(8, len(got))])
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

// TestFSImageRefusesWhatItCannotPaint: the extension table is the permission, so a format that could run
// is refused by name. SVG is the row that matters — markup, a common repo asset, loaded by an <img>.
func TestFSImageRefusesWhatItCannotPaint(t *testing.T) {
	body := []byte("<svg xmlns='http://www.w3.org/2000/svg' onload='alert(1)'></svg>")
	s := testServer(t)
	id, _ := committedProject(t, s, "p1", map[string][]byte{
		"icon.svg": body, "notes.txt": body, "README": body, "page.html": body, "script.png.sh": body,
	})

	for _, name := range []string{"icon.svg", "notes.txt", "README", "page.html", "script.png.sh"} {
		rr := imageGet(t, s, readQuery(id, name, "", ""))
		if rr.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s: status = %d, want 415", name, rr.Code)
		}
		if bytes.Contains(rr.Body.Bytes(), body) {
			t.Errorf("%s: the refused response carried the file's content", name)
		}
	}
}

// TestFSImageRefusesAPathAboveTheProject covers an absolute path, a step above the root, a Windows drive
// path, and a symlink planted inside the repository — the last one because a lexical check reads right
// through it.
func TestFSImageRefusesAPathAboveTheProject(t *testing.T) {
	s := testServer(t)
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "key.png")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, repo := committedProject(t, s, "p1", map[string][]byte{"link.png": pngFixture})
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
		rr := imageGet(t, s, readQuery(id, path, "", ""))
		if rr.Code >= 200 && rr.Code < 300 {
			t.Errorf("%s: status = %d, want a refusal", name, rr.Code)
		}
		if strings.Contains(rr.Body.String(), "SECRET") {
			t.Errorf("%s: the refusal carried the file's content", name)
		}
	}
}

// TestFSImageServesANameCarryingAColon: the refusal above is for a drive path, not for any name with a
// colon in it.
func TestFSImageServesANameCarryingAColon(t *testing.T) {
	s := testServer(t)
	id, _ := committedProject(t, s, "p1", map[string][]byte{"shots/a:b.png": pngFixture})

	rr := imageGet(t, s, readQuery(id, "shots/a:b.png", "", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
}

// TestFSImageRefusesATaskOfAnotherProject: ?project and ?task arrive uncorrelated.
func TestFSImageRefusesATaskOfAnotherProject(t *testing.T) {
	s := testServer(t)
	a, _ := committedProject(t, s, "pa", map[string][]byte{"logo.png": pngFixture})
	b, _ := committedProject(t, s, "pb", map[string][]byte{"logo.png": pngFixture})
	if err := s.store.CreateTask(&types.Task{ID: "tb", ProjectID: b, Title: "tb", Status: types.StatusDone}); err != nil {
		t.Fatal(err)
	}

	rr := imageGet(t, s, readQuery(a, "logo.png", "", "tb"))
	if rr.Code == http.StatusOK {
		t.Fatal("a task of another project was honoured")
	}
}

// TestFSImageServesAnImageReachedThroughAProjectSymlink: containment resolves the path before
// opening it, so a symlink filed inside the repository still answers — the swap detector reads the
// resolved name, not the link.
func TestFSImageServesAnImageReachedThroughAProjectSymlink(t *testing.T) {
	s := testServer(t)
	id, repo := committedProject(t, s, "p1", map[string][]byte{"vendor/logo.png": pngFixture, "link.png": pngFixture})
	full := filepath.Join(repo, "link.png")
	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("vendor/logo.png", full); err != nil {
		t.Fatal(err)
	}

	rr := imageGet(t, s, readQuery(id, "link.png", "", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if !bytes.Equal(rr.Body.Bytes(), pngFixture) {
		t.Error("the symlink served something other than the linked image")
	}
}

// TestFSImageRefusesADirectoryNamedLikeAnImage: the extension check runs on the name, so a directory only
// meets the regular-file rule after the jail.
func TestFSImageRefusesADirectoryNamedLikeAnImage(t *testing.T) {
	s := testServer(t)
	id, repo := committedProject(t, s, "p1", map[string][]byte{"logo.png": pngFixture})
	if err := os.Mkdir(filepath.Join(repo, "traps.png"), 0o755); err != nil {
		t.Fatal(err)
	}

	rr := imageGet(t, s, readQuery(id, "traps.png", "", ""))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Body.String(); strings.Contains(got, repo) {
		t.Errorf("the refusal named the host path: %s", got)
	}
}

// TestFSImageRefusalNamesNoHostPath: every caller answers a containment refusal with err.Error(), so
// the resolved target of a symlink committed into a repository must never reach that error — it
// would let a crafted repository read the server's directory layout back out.
func TestFSImageRefusalNamesNoHostPath(t *testing.T) {
	s := testServer(t)
	hidden := t.TempDir()
	target := filepath.Join(hidden, "id_ed25519.png")
	if err := os.WriteFile(target, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, repo := committedProject(t, s, "p1", map[string][]byte{"link.png": pngFixture})
	full := filepath.Join(repo, "link.png")
	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, full); err != nil {
		t.Fatal(err)
	}

	rr := imageGet(t, s, readQuery(id, "link.png", "", ""))
	if rr.Code == http.StatusOK {
		t.Fatal("a symlink out of the project was served")
	}
	for _, leak := range []string{hidden, target, repo} {
		if strings.Contains(rr.Body.String(), leak) {
			t.Errorf("the refusal named the host path %q", leak)
		}
	}
}

// TestFSImageRefBranchRefusesAnOversizedBlob: the ref branch caps on `git cat-file -s` and again in
// the read, so a refusal must not cost the 20MB it refused.
func TestFSImageRefBranchRefusesAnOversizedBlob(t *testing.T) {
	s := testServer(t)
	id, repo := committedProject(t, s, "p1", map[string][]byte{"logo.png": pngFixture})
	big := bytes.Repeat([]byte{0x5a}, int(fileLimit(s)+(1<<20)))
	if err := os.WriteFile(filepath.Join(repo, "big.png"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	testgit.Git(t, repo, "add", "--", "big.png")
	testgit.Git(t, repo, "commit", "-m", "big")

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	rr := imageGet(t, s, readQuery(id, "big.png", "HEAD", ""))
	runtime.ReadMemStats(&after)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", rr.Code, rr.Body.String())
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > allocBudget {
		t.Errorf("the ref refusal allocated %d bytes; the cap is meant to run before the read", grew)
	}
}

// TestFSImageImageIsNotBufferedOrRecompressed: a browser asks with Accept-Encoding, and CompressMiddleware
// buffers every GET body to hash an ETag. For an image that costs several times the file in memory and
// brotli-compresses bytes a codec already compressed, so the route's own cap stops being the bound.
func TestFSImageImageIsNotBufferedOrRecompressed(t *testing.T) {
	s := testServer(t)
	id, repo := committedProject(t, s, "p1", map[string][]byte{"logo.png": pngFixture})
	const size = 6 << 20
	payload := make([]byte, size) // incompressible, like a real image
	for i := range payload {
		payload[i] = byte(uint32(i*2654435761) >> 13)
	}
	if err := os.WriteFile(filepath.Join(repo, "shot.png"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	req := readKindRequest(fsKindImage, readQuery(id, "shot.png", "", ""))
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

// TestFSImagePutsNoFileNameInAHeader: the file's name comes from the repository, so echoing it into
// Content-Disposition would make a crafted name a header writer.
func TestFSImagePutsNoFileNameInAHeader(t *testing.T) {
	s := testServer(t)
	id, _ := committedProject(t, s, "p1", map[string][]byte{"logo.png": pngFixture})

	rr := imageGet(t, s, readQuery(id, "logo.png", "", ""))
	if got := rr.Header().Get("Content-Disposition"); got != "" {
		t.Errorf("Content-Disposition = %q, want unset", got)
	}
}

// TestFSImageReadsACommittedObject: the diff panel shows two sides of a changed image, and the old side
// is no longer on disk.
func TestFSImageReadsACommittedObject(t *testing.T) {
	s := testServer(t)
	id, repo := committedProject(t, s, "p1", map[string][]byte{"logo.png": pngFixture})
	head := strings.TrimSpace(testgit.GitOutput(t, repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "logo.png"), []byte("REPLACED"), 0o644); err != nil {
		t.Fatal(err)
	}

	rr := imageGet(t, s, readQuery(id, "logo.png", head, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if !bytes.Equal(rr.Body.Bytes(), pngFixture) {
		t.Errorf("body = %q, want the committed bytes, not the working tree's", rr.Body.String())
	}

	// The same file spelled as a branch name, and an unknown object id.
	if got := imageGet(t, s, readQuery(id, "logo.png", "HEAD", "")).Code; got != http.StatusOK {
		t.Errorf("HEAD: status = %d, want 200", got)
	}
	if got := imageGet(t, s, readQuery(id, "logo.png", "0000000000000000000000000000000000000000", "")).Code; got == http.StatusOK {
		t.Errorf("unknown ref: status = %d, want a refusal", got)
	}
	if got := imageGet(t, s, readQuery(id, "absent.png", "HEAD", "")).Code; got != http.StatusNotFound {
		t.Errorf("absent path: status = %d, want 404", got)
	}
	// An option-shaped ref is refused before it reaches git.
	if got := imageGet(t, s, readQuery(id, "logo.png", "--output=/tmp/pwn", "")).Code; got != http.StatusBadRequest {
		t.Errorf("option ref: status = %d, want 400", got)
	}
}

// TestFSImageRefReadsATaskWhoseWorktreeIsGone: worktrees of a project share its object database, so a
// committed image is readable after commit-and-done deleted the worktree.
func TestFSImageRefReadsATaskWhoseWorktreeIsGone(t *testing.T) {
	s := testServer(t)
	id, _ := committedProject(t, s, "p1", map[string][]byte{"logo.png": pngFixture})
	if err := s.store.CreateTask(&types.Task{ID: "t1", ProjectID: id, Title: "t1", Status: types.StatusDone, Branch: "task/t1"}); err != nil {
		t.Fatal(err)
	}

	if got := imageGet(t, s, readQuery(id, "logo.png", "HEAD", "t1")); got.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a task with no worktree: %s", got.Code, got.Body.String())
	}
	// The same request without a ref wants a working tree, and that tree does not exist.
	if got := imageGet(t, s, readQuery(id, "logo.png", "", "t1")).Code; got == http.StatusOK {
		t.Error("a missing worktree answered a working-tree read")
	}
}

// TestFSImageRefReadsTheTasksOwnHEAD: HEAD belongs to a worktree, and the diff panel asks for a committed
// picture by that name. Answered from the checkout it is "no such file".
func TestFSImageRefReadsTheTasksOwnHEAD(t *testing.T) {
	s := testServer(t)
	id, _ := committedProject(t, s, "p1", map[string][]byte{"README.md": []byte("hello\n")})
	taskID := createTestTask(t, s, id, "add a logo")
	wtPath, _, err := s.executor.WorktreeMgr().CreateWorktree(id, taskID, "task/"+taskID, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "logo.png"), pngFixture, 0o644); err != nil {
		t.Fatal(err)
	}
	testgit.Git(t, wtPath, "add", "--", "logo.png")
	testgit.Git(t, wtPath, "commit", "-m", "add a logo")

	rr := imageGet(t, s, readQuery(id, "logo.png", "HEAD", taskID))
	if rr.Code != http.StatusOK {
		t.Fatalf("HEAD of the task's branch: status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if !bytes.Equal(rr.Body.Bytes(), pngFixture) {
		t.Errorf("body = %d bytes, want the picture the task committed", rr.Body.Len())
	}

	// The same ref with no task still means the checkout, which holds no such picture.
	if got := imageGet(t, s, readQuery(id, "logo.png", "HEAD", "")).Code; got != http.StatusNotFound {
		t.Errorf("HEAD of the checkout: status = %d, want 404", got)
	}
}

// TestFSImageRefusesAnOversizedImageBeforeReadingIt: the cap runs on the stat, so a refusal costs one stat
// and not the file. The sparse file is 32MB of hole, so what the request allocates is the only thing
// that tells the two orders apart.
func TestFSImageRefusesAnOversizedImageBeforeReadingIt(t *testing.T) {
	s := testServer(t)
	repo := t.TempDir()
	if err := s.store.CreateProject(&types.Project{ID: "p1", Name: "p1", Path: repo}); err != nil {
		t.Fatal(err)
	}
	sparseFile(t, filepath.Join(repo, "huge.png"), oversizedMiB)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	rr := imageGet(t, s, readQuery("p1", "huge.png", "", ""))
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

// TestFSImageRefusesAFileInsideGit: a picture's whole permission is the suffix map, and a repository's
// own directory holds names that satisfy it. The jail lets .git through — it sits under the project root
// — so the name refusal is the only rule between a task's hooks and an <img> tag, and it has to hold on
// both halves: the committed read never touches a filesystem, so the relative name is the only place a
// .git row can be seen at all.
func TestFSImageRefusesAFileInsideGit(t *testing.T) {
	s := testServer(t)
	id, repo := committedProject(t, s, "p1", map[string][]byte{"docs/logo.png": pngFixture})
	hook := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hook, "pre-commit.png"), pngFixture, 0o755); err != nil {
		t.Fatal(err)
	}

	rr := imageGet(t, s, readQuery(id, ".git/hooks/pre-commit.png", "", ""))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("working tree: status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ".git") {
		t.Errorf("refusal %q does not say it refused a .git path", rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), pngFixture) {
		t.Error("the refusal carried the file's content")
	}
	if got := imageGet(t, s, readQuery(id, ".git/hooks/pre-commit.png", "HEAD", "")).Code; got == http.StatusOK {
		t.Errorf("committed: a .git name was served (%d)", got)
	}
}

// TestFSImageNamesACommittedObjectAsItsValidator: the committed half of every kind answers with the
// object's id, because an id never names different bytes — which is what makes a repeat view of the same
// picture cost a 304 and no object read at all.
func TestFSImageNamesACommittedObjectAsItsValidator(t *testing.T) {
	s := testServer(t)
	id, repo := committedProject(t, s, "p1", map[string][]byte{"docs/logo.png": pngFixture})
	head := strings.TrimSpace(testgit.GitOutput(t, repo, "rev-parse", "HEAD"))

	rr := imageGet(t, s, readQuery(id, "docs/logo.png", head, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if !bytes.Equal(rr.Body.Bytes(), pngFixture) {
		t.Errorf("the streamed bytes are not the file: %d B, want %d B", rr.Body.Len(), len(pngFixture))
	}
	if got := rr.Header().Get("ETag"); got != `"`+head+`"` {
		t.Errorf("ETag = %q, want the object id %q", got, head)
	}
	if got := rr.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q, want one an object id can stand behind", got)
	}

	req := readKindRequest(fsKindImage, readQuery(id, "docs/logo.png", head, ""))
	req.Header.Set("If-None-Match", `"`+head+`"`)
	cond := testResponseAuth(t, s, req)
	if cond.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match: status = %d, want 304", cond.Code)
	}
	if cond.Body.Len() != 0 {
		t.Errorf("a 304 carried %d bytes", cond.Body.Len())
	}

	// The same name in the working tree is nobody's cache key: the tree changes under the reader.
	live := imageGet(t, s, readQuery(id, "docs/logo.png", "", ""))
	if got := live.Header().Get("ETag"); got != "" {
		t.Errorf("ETag = %q on a working-tree read, want none", got)
	}
}
