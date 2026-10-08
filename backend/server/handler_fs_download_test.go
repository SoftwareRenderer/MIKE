package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"mike/backend/fsutil"
	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

/**
 * The download route answers two different questions with one URL, so what is held here is which answer
 * came back and what was refused before any byte: a file's own bytes under its own name, or one archive
 * holding a whole selection. The container-path half of the jail is `fsScopeTarget`'s and is held by
 * handler_fs_sandbox_test.go where that translation lives; these tests run against a host checkout,
 * which is where the bytes of either tree sit.
 */

// downloadProject registers a project whose checkout holds the named files. The repository is
// initialised because the route refuses a `.git`, and that needs a real one.
func downloadProject(t *testing.T, s *Server, projectID string, files map[string]string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	if err := s.store.CreateProject(&types.Project{ID: projectID, Name: projectID, Path: repo}); err != nil {
		t.Fatal(err)
	}
	testgit.Git(t, repo, "init")
	for name, content := range files {
		full := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return projectID, repo
}

// downloadAsk asks for the named paths the way the Explorer does — one `path` key, repeated.
func downloadAsk(t *testing.T, s *Server, projectID, task string, paths ...string) *httptest.ResponseRecorder {
	t.Helper()
	query := url.Values{"project": {projectID}}
	if task != "" {
		query.Set("task", task)
	}
	for _, p := range paths {
		query.Add("path", p)
	}
	req := testRequest(http.MethodGet, "/api/fs/download?"+query.Encode(), nil)
	return testResponseAuth(t, s, req)
}

// unpackArchive reads a response body as a gzip'd tar and returns each entry's content.
func unpackArchive(t *testing.T, body io.Reader) map[string]string {
	t.Helper()
	zr, err := gzip.NewReader(body)
	if err != nil {
		t.Fatalf("the response is not gzip: %v", err)
	}
	tr := tar.NewReader(zr)
	got := map[string]string{}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return got
		}
		if err != nil {
			t.Fatalf("reading the archive: %v", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("reading %s: %v", header.Name, err)
		}
		got[header.Name] = string(data)
	}
}

func TestFSDownloadSendsOneFileAsItself(t *testing.T) {
	s := testServer(t)
	id, repo := downloadProject(t, s, "dl-file", map[string]string{"notes.md": "# notes\n"})

	rr := downloadAsk(t, s, id, "", filepath.Join(repo, "notes.md"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "# notes\n" {
		t.Errorf("body = %q, want the file's own bytes", rr.Body.String())
	}
	if got := rr.Header().Get("Content-Disposition"); got != `attachment; filename=notes.md` {
		t.Errorf("Content-Disposition = %q", got)
	}
	if got := rr.Header().Get("Content-Length"); got != strconv.Itoa(len("# notes\n")) {
		t.Errorf("Content-Length = %q, want %d", got, len("# notes\n"))
	}
	if got := rr.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want octet-stream so nothing is shown rather than saved", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: the next edit changes what this answers", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestFSDownloadPacksOneFolderAsTarGz(t *testing.T) {
	s := testServer(t)
	id, repo := downloadProject(t, s, "dl-dir", map[string]string{
		"src/main.go":     "package main",
		"src/deep/x.txt":  "deep body",
		"README.md":       "top level, not asked for",
		"other/y.txt":     "also not asked for",
		"src/.git/config": "[remote \"origin\"]\nurl = https://tok:secret@example.com/r.git\n",
	})

	rr := downloadAsk(t, s, id, "", filepath.Join(repo, "src"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/gzip" {
		t.Errorf("Content-Type = %q, want application/gzip", got)
	}
	if got := rr.Header().Get("Content-Disposition"); got != `attachment; filename=src.tar.gz` {
		t.Errorf("Content-Disposition = %q, want the folder's name plus the archive extension", got)
	}
	if got := rr.Header().Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q; a streamed archive has no size before the walk ends", got)
	}

	entries := unpackArchive(t, rr.Body)
	if entries["src/main.go"] != "package main" {
		t.Errorf("src/main.go = %q", entries["src/main.go"])
	}
	if entries["src/deep/x.txt"] != "deep body" {
		t.Errorf("src/deep/x.txt = %q", entries["src/deep/x.txt"])
	}
	for name := range entries {
		if strings.HasPrefix(name, "README") || strings.HasPrefix(name, "other/") {
			t.Errorf("the archive carries %s, which was not asked for", name)
		}
		if strings.Contains(name, fsutil.GitDirName) {
			t.Errorf("the archive carries %s, whose config can hold a credential", name)
		}
	}
}

func TestFSDownloadPacksASelectionIntoOneArchiveNamedAfterItsFolder(t *testing.T) {
	s := testServer(t)
	id, repo := downloadProject(t, s, "dl-multi", map[string]string{
		"a.txt":       "first",
		"b.txt":       "second",
		"src/main.go": "package main",
	})

	rr := downloadAsk(t, s, id, "", filepath.Join(repo, "a.txt"), filepath.Join(repo, "src"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	want := filepath.Base(repo) + ".tar.gz"
	if got := rr.Header().Get("Content-Disposition"); !strings.Contains(got, want) {
		t.Errorf("Content-Disposition = %q, want it named after %s — the folder the rows sit in", got, want)
	}

	entries := unpackArchive(t, rr.Body)
	if entries["a.txt"] != "first" {
		t.Errorf("a.txt = %q", entries["a.txt"])
	}
	if entries["src/main.go"] != "package main" {
		t.Errorf("src/main.go = %q", entries["src/main.go"])
	}
	if _, seen := entries["b.txt"]; seen {
		t.Error("a row that was not selected is in the archive")
	}
}

// A path above the folder the archive is built from would unpack outside the folder that was clicked,
// so the request is refused rather than quietly re-rooted.
func TestFSDownloadRefusesAPathAboveTheArchiveFolder(t *testing.T) {
	s := testServer(t)
	id, repo := downloadProject(t, s, "dl-split", map[string]string{"a.txt": "first", "src/deep/b.txt": "second"})

	rr := downloadAsk(t, s, id, "",
		filepath.Join(repo, "src", "deep", "b.txt"),
		filepath.Join(repo, "a.txt"),
	)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "must sit in") {
		t.Errorf("body = %q, want the sentence naming the rule", rr.Body.String())
	}
}

func TestFSDownloadRefusesAPathOutsideTheProject(t *testing.T) {
	s := testServer(t)
	id, repo := downloadProject(t, s, "dl-escape", map[string]string{"a.txt": "first"})
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "secret.txt"), []byte("host secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.Join(elsewhere, "secret.txt"),
		filepath.Join(repo, "..", "..", "etc", "passwd"),
	} {
		rr := downloadAsk(t, s, id, "", path)
		if rr.Code == http.StatusOK {
			t.Fatalf("%s was served: %s", path, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "host secret") || strings.Contains(rr.Body.String(), "root:") {
			t.Fatalf("%s leaked the file it refused: %s", path, rr.Body.String())
		}
	}
}

// A `.git` path is inside the project, so containment alone does not refuse it.
func TestFSDownloadRefusesAGitPath(t *testing.T) {
	s := testServer(t)
	id, repo := downloadProject(t, s, "dl-git", map[string]string{"a.txt": "first"})

	rr := downloadAsk(t, s, id, "", filepath.Join(repo, ".git", "config"))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), fsutil.ErrGitPath.Error()) {
		t.Errorf("body = %q, want the batch routes' sentence for a .git path", rr.Body.String())
	}
}

// One `.git` row refuses the whole selection, the way one outside path does.
func TestFSDownloadRefusesASelectionOfGitAndGoodPaths(t *testing.T) {
	s := testServer(t)
	id, repo := downloadProject(t, s, "dl-git-mixed", map[string]string{"a.txt": "first"})

	rr := downloadAsk(t, s, id, "", filepath.Join(repo, "a.txt"), filepath.Join(repo, ".git", "config"))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for the whole request: %s", rr.Code, rr.Body.String())
	}
	if rr.Body.Len() == 0 {
		t.Error("a refused batch said nothing")
	}
}

func TestFSDownloadRefusesATaskOfAnotherProject(t *testing.T) {
	s := testServer(t)
	a, repoA := downloadProject(t, s, "dl-own", map[string]string{"a.txt": "first"})
	b, _ := downloadProject(t, s, "dl-other", map[string]string{"b.txt": "second"})
	foreign := createTestTask(t, s, b, "a task of the other project")

	rr := downloadAsk(t, s, a, foreign, filepath.Join(repoA, "a.txt"))
	if rr.Code == http.StatusOK {
		t.Fatalf("a task of another project was honoured: %s", rr.Body.String())
	}
}

func TestFSDownloadReportsAMissingPath(t *testing.T) {
	s := testServer(t)
	id, repo := downloadProject(t, s, "dl-gone", map[string]string{"a.txt": "first"})

	rr := downloadAsk(t, s, id, "", filepath.Join(repo, "gone.txt"))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rr.Code, rr.Body.String())
	}
}

// A row that vanished since the listing is stale rather than missing: the rest still goes out.
func TestFSDownloadSkipsAVanishedRowOfASelection(t *testing.T) {
	s := testServer(t)
	id, repo := downloadProject(t, s, "dl-partial", map[string]string{"a.txt": "first", "b.txt": "second"})
	if err := os.Remove(filepath.Join(repo, "a.txt")); err != nil {
		t.Fatal(err)
	}

	rr := downloadAsk(t, s, id, "", filepath.Join(repo, "a.txt"), filepath.Join(repo, "b.txt"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	entries := unpackArchive(t, rr.Body)
	if entries["b.txt"] != "second" {
		t.Errorf("entries = %v, want b.txt still archived", entries)
	}
}

// The Explorer's batch bound, asked for over a URL rather than a body.
func TestFSDownloadRefusesTooManyPaths(t *testing.T) {
	s := testServer(t)
	id, repo := downloadProject(t, s, "dl-many", map[string]string{"a.txt": "first"})

	paths := make([]string, 0, maxFSOpsBatch+1)
	for i := 0; i <= maxFSOpsBatch; i++ {
		paths = append(paths, filepath.Join(repo, "a.txt"))
	}
	rr := downloadAsk(t, s, id, "", paths...)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), strconv.Itoa(maxFSOpsBatch)) {
		t.Errorf("body = %q, want it to name the limit", rr.Body.String())
	}
}

// A file name is repository content and this route must put it in a header, so the encoding is what
// holds: RFC 2231 percent-encoding, plus an ASCII twin.
func TestFSDownloadNamesTheFileWithoutLettingTheNameWriteAHeader(t *testing.T) {
	names := []string{`we"ird.txt`, "two words.txt", "süß.txt", "line\nbreak.txt"}
	s := testServer(t)
	id, repo := downloadProject(t, s, "dl-names", nil)
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("body"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, name := range names {
		rr := downloadAsk(t, s, id, "", filepath.Join(repo, name))
		if rr.Code != http.StatusOK {
			t.Fatalf("%q: status = %d, want 200: %s", name, rr.Code, rr.Body.String())
		}
		disposition := rr.Header().Get("Content-Disposition")
		if strings.ContainsAny(disposition, "\r\n") {
			t.Errorf("%q: the name wrote a header line: %q", name, disposition)
		}
		if !strings.HasPrefix(disposition, "attachment;") {
			t.Errorf("%q: Content-Disposition = %q, want an attachment", name, disposition)
		}
		// The name the browser saves has to still read like the file it is.
		if !strings.Contains(disposition, ".txt") {
			t.Errorf("%q: Content-Disposition = %q, want a readable file name in it", name, disposition)
		}
	}
}

// CompressMiddleware buffers every GET body to hash an ETag, so the absence of one is the observable
// that this response streamed past it.
func TestFSDownloadIsNotBufferedForAnETag(t *testing.T) {
	s := testServer(t)
	id, repo := downloadProject(t, s, "dl-stream", map[string]string{"src/main.go": "package main"})

	for _, ask := range []struct {
		name  string
		paths []string
	}{
		{"a file", []string{filepath.Join(repo, "src", "main.go")}},
		{"a folder", []string{filepath.Join(repo, "src")}},
	} {
		req := testRequest(http.MethodGet, "/api/fs/download?"+url.Values{
			"project": {id},
			"path":    ask.paths,
		}.Encode(), nil)
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
		rr := testResponseAuth(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d: %s", ask.name, rr.Code, rr.Body.String())
		}
		if got := rr.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("%s: Content-Encoding = %q; the response must leave as the handler wrote it", ask.name, got)
		}
		if got := rr.Header().Get("ETag"); got != "" {
			t.Errorf("%s: ETag = %q; an ETag is what a buffered GET leaves behind", ask.name, got)
		}
	}
}

// A folder's bytes must arrive intact through the middleware chain, because nothing reads them twice.
func TestFSDownloadArchiveOfALargerFileIsIntact(t *testing.T) {
	s := testServer(t)
	id, repo := downloadProject(t, s, "dl-big", nil)
	payload := bytes.Repeat([]byte{0x00, 0xff, 0x0f, 0xf0, 0x5a, 0xa5}, (2<<20)/6)
	if err := os.MkdirAll(filepath.Join(repo, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "assets", "blob.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	rr := downloadAsk(t, s, id, "", filepath.Join(repo, "assets"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	entries := unpackArchive(t, rr.Body)
	if len(entries["assets/blob.bin"]) != len(payload) {
		t.Errorf("the archive carries %d bytes of a %d byte file", len(entries["assets/blob.bin"]), len(payload))
	}
}
