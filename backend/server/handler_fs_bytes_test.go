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

	"mike/backend/extensions"
	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// bytesFixture holds a NUL and bytes above 0x7f, so a response that came back through a JSON string
// is distinguishable from one that carried the file.
var bytesFixture = []byte{0x00, 0x01, 'M', 'I', 'K', 'E', 0xff, 0xfe, 0x00, 'b', 'i', 'n', 0x80, 0x7f}

// declareSuffixes installs a UI extension row declaring these suffixes. It is the whole grant a byte
// read has, so every test that expects bytes needs one, and every test that expects a refusal removes it.
func declareSuffixes(t *testing.T, s *Server, name string, enabled bool, suffixes ...string) {
	t.Helper()
	ext := &extensions.Extension{
		ID:      "ext-" + name,
		Name:    name,
		Author:  "TestDev",
		Version: "1.0.0",
		Type:    extensions.ExtensionTypeUI,
		Enabled: enabled,
		Manifest: extensions.ExtensionManifest{
			DisplayName: name,
			Capabilities: &extensions.Capabilities{
				UI: &extensions.UIConfig{
					InjectionPoints: []string{"fileEditor"},
					FileSuffixes:    suffixes,
				},
			},
		},
	}
	if err := s.extStore.Create(ext); err != nil {
		t.Fatalf("create extension %s: %v", name, err)
	}
}

// bytesGet asks GET /api/fs/read for one file's bytes as the extension kind.
func bytesGet(t *testing.T, s *Server, query url.Values) *httptest.ResponseRecorder {
	t.Helper()
	return readKindGet(t, s, fsKindBytes, query)
}

// TestFSBytesServesADeclaredSuffixAsTheFilesOwnBytes: the extension frame parses these itself, so a
// response that decoded them as text would arrive with the NUL and the high bytes replaced.
func TestFSBytesServesADeclaredSuffixAsTheFilesOwnBytes(t *testing.T) {
	s := testServer(t)
	declareSuffixes(t, s, "viewer", true, "zbin")
	id, _ := committedProject(t, s, "p1", map[string][]byte{"mesh/part.zbin": bytesFixture})

	rr := bytesGet(t, s, readQuery(id, "mesh/part.zbin", "", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", got)
	}
	if !bytes.Equal(rr.Body.Bytes(), bytesFixture) {
		t.Errorf("body = %x, want the file's own bytes", rr.Body.Bytes())
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store for a working tree that can change under the reader", got)
	}
	if got := rr.Header().Get("Content-Disposition"); got != "" {
		t.Errorf("Content-Disposition = %q, want unset — a preview is not a saved file", got)
	}
}

// TestFSBytesMatchesTheSuffixWithoutCareForCase: the declared list is lowercased by validation and the
// file's extension is lowercased here, so one declaration covers PART.ZBIN and part.zbin alike.
func TestFSBytesMatchesTheSuffixWithoutCareForCase(t *testing.T) {
	s := testServer(t)
	declareSuffixes(t, s, "viewer", true, "zbin")
	id, _ := committedProject(t, s, "p1", map[string][]byte{"mesh/PART.ZBIN": bytesFixture})

	if got := bytesGet(t, s, readQuery(id, "mesh/PART.ZBIN", "", "")).Code; got != http.StatusOK {
		t.Errorf("status = %d, want 200 for an uppercased file name", got)
	}
}

// TestFSBytesRefusesAFileNoExtensionDeclared: only "some installed extension renders it" is a grant.
func TestFSBytesRefusesAFileNoExtensionDeclared(t *testing.T) {
	s := testServer(t)
	declareSuffixes(t, s, "viewer", true, "other")
	id, _ := committedProject(t, s, "p1", map[string][]byte{"mesh/part.zbin": bytesFixture})

	rr := bytesGet(t, s, readQuery(id, "mesh/part.zbin", "", ""))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rr.Code, rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), bytesFixture) {
		t.Error("the refusal carried the file's content")
	}
	if !strings.Contains(rr.Body.String(), "zbin") {
		t.Errorf("refusal %q does not name the suffix it refused", rr.Body.String())
	}
}

// TestFSBytesRefusesADisabledExtensionsSuffix: the row's Enabled column is the switch, so an extension
// the operator turned off must not keep reading files.
func TestFSBytesRefusesADisabledExtensionsSuffix(t *testing.T) {
	s := testServer(t)
	declareSuffixes(t, s, "viewer", false, "zbin")
	id, _ := committedProject(t, s, "p1", map[string][]byte{"mesh/part.zbin": bytesFixture})

	if got := bytesGet(t, s, readQuery(id, "mesh/part.zbin", "", "")).Code; got != http.StatusForbidden {
		t.Errorf("status = %d, want 403 while the declaring row is disabled", got)
	}
}

// TestFSBytesRefusesANameWithNoSuffix: an empty suffix would match whatever the route is pointed at.
func TestFSBytesRefusesANameWithNoSuffix(t *testing.T) {
	s := testServer(t)
	declareSuffixes(t, s, "viewer", true, "zbin")
	id, _ := committedProject(t, s, "p1", map[string][]byte{"Makefile": bytesFixture})

	if got := bytesGet(t, s, readQuery(id, "Makefile", "", "")).Code; got == http.StatusOK {
		t.Error("a file with no suffix was served")
	}
}

// TestFSBytesRefusesAFileInsideGit: the byte route serves any declared suffix, so the .git refusal that
// the picture kind never needs (it only ever serves images) is load-bearing here.
func TestFSBytesRefusesAFileInsideGit(t *testing.T) {
	s := testServer(t)
	declareSuffixes(t, s, "viewer", true, "zbin")
	id, repo := committedProject(t, s, "p1", map[string][]byte{"mesh/part.zbin": bytesFixture})
	hook := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hook, "pre-commit.zbin"), bytesFixture, 0o755); err != nil {
		t.Fatal(err)
	}

	// A declared suffix inside .git is the case only the .git refusal stops: the jail would let it
	// through, because .git sits under the project root.
	rr := bytesGet(t, s, readQuery(id, ".git/hooks/pre-commit.zbin", "", ""))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ".git") {
		t.Errorf("refusal %q does not say it refused a .git path", rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), bytesFixture) {
		t.Error("the refusal carried the file's content")
	}
	// An undeclared name under .git is refused too — by whichever rule gets there first.
	if got := bytesGet(t, s, readQuery(id, ".git/config", "", "")).Code; got == http.StatusOK {
		t.Error(".git/config was served")
	}
}

// TestFSBytesRefusesAPathAboveTheProject: the jail is the one the write endpoints use.
func TestFSBytesRefusesAPathAboveTheProject(t *testing.T) {
	s := testServer(t)
	declareSuffixes(t, s, "viewer", true, "zbin")
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "id_ed25519.zbin")
	if err := os.WriteFile(secret, bytesFixture, 0o600); err != nil {
		t.Fatal(err)
	}
	id, repo := committedProject(t, s, "p1", map[string][]byte{"link.zbin": bytesFixture})
	full := filepath.Join(repo, "link.zbin")
	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, full); err != nil {
		t.Fatal(err)
	}

	rows := map[string]string{
		"absolute path":   secret,
		"parent step":     "../../" + filepath.Base(secretDir) + "/id_ed25519.zbin",
		"symlink in repo": "link.zbin",
		"drive path":      "C:/Windows/win.zbin",
	}
	for name, path := range rows {
		rr := bytesGet(t, s, readQuery(id, path, "", ""))
		if rr.Code == http.StatusOK {
			t.Errorf("%s: status = %d, want a refusal", name, rr.Code)
		}
		if bytes.Contains(rr.Body.Bytes(), bytesFixture) {
			t.Errorf("%s: the refusal carried the file's content", name)
		}
		for _, leak := range []string{secretDir, repo} {
			if strings.Contains(rr.Body.String(), leak) {
				t.Errorf("%s: the refusal named the host path %q", name, leak)
			}
		}
	}
}

// TestFSBytesRefusesATaskOfAnotherProject: ?project and ?task arrive uncorrelated.
func TestFSBytesRefusesATaskOfAnotherProject(t *testing.T) {
	s := testServer(t)
	declareSuffixes(t, s, "viewer", true, "zbin")
	a, _ := committedProject(t, s, "pa", map[string][]byte{"part.zbin": bytesFixture})
	b, _ := committedProject(t, s, "pb", map[string][]byte{"part.zbin": bytesFixture})
	if err := s.store.CreateTask(&types.Task{ID: "tb", ProjectID: b, Title: "tb", Status: types.StatusDone}); err != nil {
		t.Fatal(err)
	}

	if got := bytesGet(t, s, readQuery(a, "part.zbin", "", "tb")).Code; got == http.StatusOK {
		t.Error("a task of another project was honoured")
	}
}

// TestFSBytesRefusesAnOversizedFileBeforeReadingIt: the cap runs on the stat. The sparse file is 32MB of
// hole, so what the request allocates is what tells the two orders apart.
func TestFSBytesRefusesAnOversizedFileBeforeReadingIt(t *testing.T) {
	s := testServer(t)
	declareSuffixes(t, s, "viewer", true, "zbin")
	repo := t.TempDir()
	if err := s.store.CreateProject(&types.Project{ID: "p1", Name: "p1", Path: repo}); err != nil {
		t.Fatal(err)
	}
	sparseFile(t, filepath.Join(repo, "huge.zbin"), oversizedMiB)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	rr := bytesGet(t, s, readQuery("p1", "huge.zbin", "", ""))
	runtime.ReadMemStats(&after)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", rr.Code, rr.Body.String())
	}
	if want := strconv.FormatInt(s.settings.FileSizeLimitMegabytes(), 10) + "MB"; !strings.Contains(rr.Body.String(), want) {
		t.Errorf("refusal %q does not name the cap it applied, want it to say %s", rr.Body.String(), want)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > allocBudget {
		t.Errorf("the refusal allocated %d bytes; the cap is meant to run before the read", grew)
	}
}

// TestFSBytesRefusesAnOversizedBlob: the committed half caps on `git cat-file -s` and again in the
// read.
func TestFSBytesRefusesAnOversizedBlob(t *testing.T) {
	s := testServer(t)
	declareSuffixes(t, s, "viewer", true, "zbin")
	id, repo := committedProject(t, s, "p1", map[string][]byte{"part.zbin": bytesFixture})
	big := bytes.Repeat([]byte{0x5a}, int(fileLimit(s)+(1<<20)))
	if err := os.WriteFile(filepath.Join(repo, "big.zbin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	testgit.Git(t, repo, "add", "--", "big.zbin")
	testgit.Git(t, repo, "commit", "-m", "big")

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	rr := bytesGet(t, s, readQuery(id, "big.zbin", "HEAD", ""))
	runtime.ReadMemStats(&after)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", rr.Code, rr.Body.String())
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > allocBudget {
		t.Errorf("the ref refusal allocated %d bytes; the cap is meant to run before the read", grew)
	}
}

// TestFSBytesNamesACommittedObjectAsItsValidator: an object id is the only spelling of a ref that can
// never name different bytes. A worktree read gets no validator, because there the bytes can change.
func TestFSBytesNamesACommittedObjectAsItsValidator(t *testing.T) {
	s := testServer(t)
	declareSuffixes(t, s, "viewer", true, "zbin")
	id, repo := committedProject(t, s, "p1", map[string][]byte{"part.zbin": bytesFixture})
	head := strings.TrimSpace(testgit.GitOutput(t, repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "part.zbin"), []byte("REPLACED"), 0o644); err != nil {
		t.Fatal(err)
	}

	rr := bytesGet(t, s, readQuery(id, "part.zbin", head, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if !bytes.Equal(rr.Body.Bytes(), bytesFixture) {
		t.Errorf("body = %q, want the committed bytes, not the working tree's", rr.Body.String())
	}
	if got := rr.Header().Get("ETag"); got != `"`+head+`"` {
		t.Errorf("ETag = %q, want the object id %q", got, head)
	}
	if got := rr.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q, want one an object id can stand behind", got)
	}

	// The match is decided before a byte is read: a 304 carries no body.
	req := readKindRequest(fsKindBytes, readQuery(id, "part.zbin", head, ""))
	req.Header.Set("If-None-Match", `"`+head+`"`)
	cond := testResponseAuth(t, s, req)
	if cond.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match: status = %d, want 304", cond.Code)
	}
	if cond.Body.Len() != 0 {
		t.Errorf("a 304 carried %d bytes", cond.Body.Len())
	}

	// A worktree read of the same name is the other answer: live bytes are nobody's cache key.
	live := bytesGet(t, s, readQuery(id, "part.zbin", "", ""))
	if got := live.Header().Get("ETag"); got != "" {
		t.Errorf("ETag = %q on a working-tree read, want none", got)
	}
}

// TestFSBytesCachesOnlyWhatAnObjectIDCanStandBehind: ?ref=HEAD with no ?task names a moving name rather
// than an object, so a stored answer could never be asked about again — and a year of immutability would
// keep a preview showing bytes the commit no longer holds.
func TestFSBytesCachesOnlyWhatAnObjectIDCanStandBehind(t *testing.T) {
	s := testServer(t)
	declareSuffixes(t, s, "viewer", true, "zbin")
	id, _ := committedProject(t, s, "p1", map[string][]byte{"part.zbin": bytesFixture})

	moving := bytesGet(t, s, readQuery(id, "part.zbin", "HEAD", ""))
	if moving.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", moving.Code, moving.Body.String())
	}
	if got := moving.Header().Get("ETag"); got != "" {
		t.Errorf("ETag = %q for a ref that named no object", got)
	}
	if got := moving.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q for a ref that named no object, want no-store", got)
	}

	// The same name is cacheable once a worktree resolves it to an object.
	if err := s.store.CreateTask(&types.Task{ID: "t1", ProjectID: id, Title: "t1", Status: types.StatusDone, Branch: "task/t1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.executor.WorktreeMgr().CreateWorktree(id, "t1", "task/t1", "HEAD"); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	resolved := bytesGet(t, s, readQuery(id, "part.zbin", "HEAD", "t1"))
	if got := resolved.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q for a ref a worktree resolved, want the object id's answer", got)
	}
	if got := resolved.Header().Get("ETag"); got == "" {
		t.Error("a ref that resolved to an object got no validator")
	}
}

// TestFSBytesAnswersARefThatNamedNoObject: a read that names no task keeps `HEAD` as written, so it names no
// object — the board's own diff, whose panel carries no task. The answer is still the object's bytes at the
// probe's length; what the missing id costs is the cache validator.
func TestFSBytesAnswersARefThatNamedNoObject(t *testing.T) {
	s := testServer(t)
	declareSuffixes(t, s, "viewer", true, "zbin")
	// Over the object-id reading's buffers so holding cannot hide inside them; under the file limit so the
	// request is answered at all.
	size := allocBudget + (1 << 20)
	id, repo := committedProject(t, s, "p1", map[string][]byte{"part.zbin": bytesFixture})
	big := bytes.Repeat([]byte{0x5a}, int(size))
	if err := os.WriteFile(filepath.Join(repo, "mesh.zbin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	testgit.Git(t, repo, "add", "--", "mesh.zbin")
	testgit.Git(t, repo, "commit", "-m", "big")

	// What the answer must be, streaming or not: the object's bytes, the probe's length, no validator.
	moving := bytesGet(t, s, readQuery(id, "mesh.zbin", "HEAD", ""))
	if moving.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", moving.Code, moving.Body.String())
	}
	if !bytes.Equal(moving.Body.Bytes(), big) {
		t.Errorf("body = %d bytes, want the object's %d", moving.Body.Len(), len(big))
	}
	if got := moving.Header().Get("Content-Length"); got != strconv.Itoa(int(size)) {
		t.Errorf("Content-Length = %q, want the probe's %d — the promised length is what lets the read stream", got, size)
	}
	if got := moving.Header().Get("ETag"); got != "" {
		t.Errorf("ETag = %q for a ref that named no object", got)
	}
	if got := moving.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q for a ref that named no object, want no-store", got)
	}

	// The two spellings of the one object answer the same bytes; only the validator differs.
	head := strings.TrimSpace(testgit.GitOutput(t, repo, "rev-parse", "HEAD"))
	pinned := bytesGet(t, s, readQuery(id, "mesh.zbin", head, ""))
	if pinned.Code != http.StatusOK {
		t.Fatalf("pinned read: status = %d, want 200: %s", pinned.Code, pinned.Body.String())
	}
	if !bytes.Equal(pinned.Body.Bytes(), moving.Body.Bytes()) {
		t.Error("the object id and the name no tree resolved answered different bytes for one object")
	}
	if got := pinned.Header().Get("ETag"); got != `"`+head+`"` {
		t.Errorf("ETag = %q, want the object id %q", got, head)
	}
}

// TestFSBytesIsNotBufferedOrRecompressed: CompressMiddleware buffers every GET body to hash an ETag,
// which for raw bytes costs several times the file and makes the route's own cap not the bound.
func TestFSBytesIsNotBufferedOrRecompressed(t *testing.T) {
	s := testServer(t)
	declareSuffixes(t, s, "viewer", true, "zbin")
	id, repo := committedProject(t, s, "p1", map[string][]byte{"part.zbin": bytesFixture})
	const size = 6 << 20
	payload := make([]byte, size) // incompressible, like a mesh
	for i := range payload {
		payload[i] = byte(uint32(i*2654435761) >> 13)
	}
	if err := os.WriteFile(filepath.Join(repo, "heavy.zbin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	req := readKindRequest(fsKindBytes, readQuery(id, "heavy.zbin", "", ""))
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	rr := testResponseAuth(t, s, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want the bytes as stored", got)
	}
	if got := rr.Header().Get("Content-Length"); got != strconv.Itoa(size) {
		t.Errorf("Content-Length = %q, want %d — the handler's own size reaches the client only unbuffered", got, size)
	}
	if rr.Body.Len() != size {
		t.Errorf("body = %d bytes, want %d", rr.Body.Len(), size)
	}
}

// TestFSBytesRefBranchRefusesAnOptionShapedRef: the ref reaches git, so a value that reads like a flag
// is refused before it is passed.
func TestFSBytesRefBranchRefusesAnOptionShapedRef(t *testing.T) {
	s := testServer(t)
	declareSuffixes(t, s, "viewer", true, "zbin")
	id, _ := committedProject(t, s, "p1", map[string][]byte{"part.zbin": bytesFixture})

	if got := bytesGet(t, s, readQuery(id, "part.zbin", "--output=/tmp/pwn", "")).Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an option-shaped ref", got)
	}
}

// TestFSBytesRefBranchReadsATaskWhoseWorktreeIsGone: worktrees of a project share its object database,
// so the old side of a diff is readable after commit-and-done deleted the tree.
func TestFSBytesRefBranchReadsATaskWhoseWorktreeIsGone(t *testing.T) {
	s := testServer(t)
	declareSuffixes(t, s, "viewer", true, "zbin")
	id, _ := committedProject(t, s, "p1", map[string][]byte{"part.zbin": bytesFixture})
	if err := s.store.CreateTask(&types.Task{ID: "t1", ProjectID: id, Title: "t1", Status: types.StatusDone, Branch: "task/t1"}); err != nil {
		t.Fatal(err)
	}

	if got := bytesGet(t, s, readQuery(id, "part.zbin", "HEAD", "t1")).Code; got != http.StatusOK {
		t.Errorf("status = %d, want 200 for a task with no worktree", got)
	}
	// The same request without a ref wants a working tree, and that tree does not exist.
	if got := bytesGet(t, s, readQuery(id, "part.zbin", "", "t1")).Code; got == http.StatusOK {
		t.Error("a missing worktree answered a working-tree read")
	}
}
