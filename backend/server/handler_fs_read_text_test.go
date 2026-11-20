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

// fsReadGet asks GET /api/fs/read for one absolute path, optionally as a commit holds it — the shape the
// explorer sends, since its listing hands over absolute paths.
func fsReadGet(t *testing.T, s *Server, projectID, absPath, ref string) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{"project": {projectID}, "path": {absPath}}
	if ref != "" {
		q.Set("ref", ref)
	}
	req := testRequest(http.MethodGet, "/api/fs/read?"+q.Encode(), nil)
	return testResponseAuth(t, s, req)
}

// A file whose bytes are not text is refused, not answered: this route fills the editor's buffer, and bytes
// repaired on the way in are bytes a save writes back altered. The bytes themselves belong to
// GET /api/fs/raw.
func TestFSReadRefusesBytesThatAreNotText(t *testing.T) {
	s := testServer(t)
	id, repo := rawImageProject(t, s, "p1", map[string][]byte{
		"logo.png":   rawPNG,
		"legacy.txt": {0x63, 0x61, 0x66, 0xE9, 0x0A},
		"notes.md":   []byte("# caf\u00E9\n"),
	})

	binary := []struct {
		name string
		path string
	}{
		{"a PNG", filepath.Join(repo, "logo.png")},
		{"text in another encoding", filepath.Join(repo, "legacy.txt")}, // Latin-1: no NUL, so only the UTF-8 half catches it
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
	if rr := fsReadGet(t, s, id, filepath.Join(repo, "notes.md"), ""); rr.Code != http.StatusOK {
		t.Errorf("an ordinary file was refused: %d %s", rr.Code, rr.Body.String())
	}
}

// The size cap outranks the text rule, which is the point of the cap: a request for a huge file costs one
// stat, not the read that would be needed to learn it is also binary.
func TestFSReadAnOversizedFileIsRefusedBySizeFirst(t *testing.T) {
	s := testServer(t)
	id, repo := rawImageProject(t, s, "p1", map[string][]byte{"logo.png": rawPNG})

	big := filepath.Join(repo, "huge.png")
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

	rr := fsReadGet(t, s, id, big, "")
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
	id, repo := rawImageProject(t, s, "p1", map[string][]byte{"empty.txt": {}})

	if rr := fsReadGet(t, s, id, filepath.Join(repo, "empty.txt"), ""); rr.Code != http.StatusOK {
		t.Errorf("an empty file: status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
}
