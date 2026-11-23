package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readKindGet asks GET /api/fs/read for one file as the kind named: the picture or the extension
// bytes. The query comes from readQuery, so every kind is asked about the same scope.
func readKindGet(t *testing.T, s *Server, kind string, query url.Values) *httptest.ResponseRecorder {
	t.Helper()
	return testResponseAuth(t, s, readKindRequest(kind, query))
}

// readKindRequest is readKindGet before it is answered, for a test that must set a request header first.
func readKindRequest(kind string, query url.Values) *http.Request {
	query.Set("kind", kind)
	return testRequest(http.MethodGet, "/api/fs/read?"+query.Encode(), nil)
}

// fsReadGet asks GET /api/fs/read for one repository-relative path, optionally as a commit holds it — the shape
// every caller sends, since the route reads only names the repository knows.
func fsReadGet(t *testing.T, s *Server, projectID, absPath, ref string) *httptest.ResponseRecorder {
	t.Helper()
	return fsReadGetEncoded(t, s, projectID, absPath, ref, "")
}

// fsReadGetEncoded is fsReadGet with Accept-Encoding set, which is what asks the server to build a
// compressor: a request that names none never pays for one, and no browser sends one.
func fsReadGetEncoded(t *testing.T, s *Server, projectID, absPath, ref, encoding string) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{"project": {projectID}, "path": {absPath}}
	if ref != "" {
		q.Set("ref", ref)
	}
	req := testRequest(http.MethodGet, "/api/fs/read?"+q.Encode(), nil)
	if encoding != "" {
		req.Header.Set("Accept-Encoding", encoding)
	}
	return testResponseAuth(t, s, req)
}

// The parameter picks a policy and never widens one, so a name outside the three is a refusal about the
// request rather than a read of some kind, and an absent parameter keeps the meaning the editor's call had
// before it existed.
func TestFSReadKindNamesAPolicy(t *testing.T) {
	s := testServer(t)
	id, _ := committedProject(t, s, "p1", map[string][]byte{"notes.md": []byte("# caf\u00E9\n")})

	for _, kind := range []string{"", "text"} {
		q := url.Values{"project": {id}, "path": {"notes.md"}}
		if kind != "" {
			q.Set("kind", kind)
		}
		rr := testResponseAuth(t, s, testRequest(http.MethodGet, "/api/fs/read?"+q.Encode(), nil))
		if rr.Code != http.StatusOK {
			t.Errorf("kind %q: expected 200, got %d: %s", kind, rr.Code, rr.Body.String())
		}
	}

	for _, kind := range []string{"IMAGE", "raw", "archive", "text; kind=bytes"} {
		rr := readKindGet(t, s, kind, url.Values{"project": {id}, "path": {"notes.md"}})
		if rr.Code != http.StatusBadRequest {
			t.Errorf("kind %q: expected 400, got %d: %s", kind, rr.Code, rr.Body.String())
		}
	}
}

// A file whose bytes are not text is refused, not answered: this route fills the editor's buffer, and bytes
// repaired on the way in are bytes a save writes back altered. The bytes themselves belong to
// `kind=image` and `kind=bytes`.
func TestFSReadRefusesBytesThatAreNotText(t *testing.T) {
	s := testServer(t)
	id, _ := committedProject(t, s, "p1", map[string][]byte{
		"logo.png":   pngFixture,
		"legacy.txt": {0x63, 0x61, 0x66, 0xE9, 0x0A},
		"notes.md":   []byte("# caf\u00E9\n"),
	})

	binary := []struct {
		name string
		path string
	}{
		{"a PNG", "logo.png"},
		{"text in another encoding", "legacy.txt"}, // Latin-1: no NUL, so only the UTF-8 half catches it
	}
	for _, tc := range binary {
		for _, ref := range []string{"", "HEAD"} {
			side := "on disk"
			if ref != "" {
				side = "at " + ref
			}
			rr := fsReadGet(t, s, id, tc.path, ref)
			if rr.Code != http.StatusUnsupportedMediaType {
				t.Errorf("%s %s: status = %d, want %d: %s", tc.name, side, rr.Code, http.StatusUnsupportedMediaType, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "not a text file") {
				t.Errorf("%s %s: the refusal said nothing useful: %s", tc.name, side, rr.Body.String())
			}
			if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
				t.Errorf("%s %s: Content-Type = %q, want JSON a client can parse", tc.name, side, ct)
			}
		}
	}

	// The control: text still reads, so the refusals above are the bytes and not a broken route.
	if rr := fsReadGet(t, s, id, "notes.md", ""); rr.Code != http.StatusOK {
		t.Errorf("an ordinary file was refused: %d %s", rr.Code, rr.Body.String())
	}
}

// The size cap outranks the text rule, which is the point of the cap: a request for a huge file costs one
// stat, not the read that would be needed to learn it is also binary.
func TestFSReadAnOversizedFileIsRefusedBySizeFirst(t *testing.T) {
	s := testServer(t)
	id, repo := committedProject(t, s, "p1", map[string][]byte{"logo.png": pngFixture})

	bigName := "huge.png"
	big := filepath.Join(repo, bigName)
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxFSReadBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	rr := fsReadGet(t, s, id, bigName, "")
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d: %s", rr.Code, http.StatusRequestEntityTooLarge, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "too large") {
		t.Errorf("the answer must say which limit it hit: %s", rr.Body.String())
	}
}

// An empty file is text: the editor has to open the file someone has just created.
func TestFSReadServesAnEmptyFile(t *testing.T) {
	s := testServer(t)
	id, _ := committedProject(t, s, "p1", map[string][]byte{"empty.txt": {}})

	if rr := fsReadGet(t, s, id, "empty.txt", ""); rr.Code != http.StatusOK {
		t.Errorf("an empty file: status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
}

// TestFSReadRefusesAFileInsideGit: the editor's buffer comes from the same reader as the picture and the
// extension bytes, so the repository's own directory is refused for text too. The jail would hand it over
// — .git sits under the project root — and a saved buffer writes it back.
func TestFSReadRefusesAFileInsideGit(t *testing.T) {
	s := testServer(t)
	id, repo := committedProject(t, s, "p1", map[string][]byte{"notes.md": []byte("# notes\n")})
	if err := os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, ref := range []string{"", "HEAD"} {
		rr := fsReadGet(t, s, id, ".git/hooks/pre-commit", ref)
		if rr.Code == http.StatusOK || !strings.Contains(rr.Body.String(), ".git") {
			t.Errorf("ref %q: status = %d, body = %s; want a refusal naming .git", ref, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "echo hi") {
			t.Errorf("ref %q: the refusal carried the file's content", ref)
		}
	}
	if rr := fsReadGet(t, s, id, ".git/config", ""); rr.Code == http.StatusOK {
		t.Error(".git/config was served to the editor")
	}
	// The control: an ordinary name still reads.
	if rr := fsReadGet(t, s, id, "notes.md", ""); rr.Code != http.StatusOK {
		t.Errorf("an ordinary file was refused: %d %s", rr.Code, rr.Body.String())
	}
}
