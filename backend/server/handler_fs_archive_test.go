package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mike/backend/archive"
	"mike/backend/fsutil"
	"mike/backend/testutil/testgit"
	"mike/backend/types"
	"mike/pkg/logging"
)

// The members every fixture archive holds, and the row each must be listed as.
var archiveFixtureMembers = []struct {
	name string
	body string
	dir  bool
}{
	{name: "notes", dir: true},
	{name: "notes/todo.md", body: "# todo\n"},
	{name: "report.txt", body: "hello from the archive"},
	{name: "blob.bin", body: "\x00\x01\x02\x03not text"},
}

// archiveStamp is 2023-11-14T22:13:20Z, the stamp every fixture member carries.
var archiveFixtureStamp = time.Unix(1700000000, 0).UTC()

const archiveFixtureStampText = "2023-11-14T22:13:20Z"

func buildArchiveTarGz(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, m := range archiveFixtureMembers {
		name := m.name
		hdr := &tar.Header{Name: name, Mode: 0o644, ModTime: archiveFixtureStamp, Format: tar.FormatGNU}
		if m.dir {
			hdr.Typeflag, hdr.Name, hdr.Mode = tar.TypeDir, name+"/", 0o755
		} else {
			hdr.Typeflag, hdr.Size = tar.TypeReg, int64(len(m.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("header: %v", err)
		}
		if !m.dir {
			if _, err := tw.Write([]byte(m.body)); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func buildArchiveZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, m := range archiveFixtureMembers {
		name := m.name
		if m.dir {
			name += "/"
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: archiveFixtureStamp})
		if err != nil {
			t.Fatalf("zip header: %v", err)
		}
		if !m.dir {
			if _, err := w.Write([]byte(m.body)); err != nil {
				t.Fatalf("zip write: %v", err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// archiveProject registers a project whose checkout holds the named files, all committed, so both the
// file on disk and the object HEAD holds are there to ask for.
func archiveProject(t *testing.T, s *Server, projectID string, files map[string][]byte) (string, string) {
	t.Helper()
	repo := t.TempDir()
	if err := s.store.CreateProject(&types.Project{ID: projectID, Name: projectID, Path: repo}); err != nil {
		t.Fatal(err)
	}
	testgit.Git(t, repo, "init")
	testgit.Git(t, repo, "config", "user.email", "test@test.com")
	testgit.Git(t, repo, "config", "user.name", "Test")
	for name, content := range files {
		full := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Fatal(err)
		}
		testgit.Git(t, repo, "add", "--", name)
	}
	testgit.Git(t, repo, "commit", "-m", "files")
	return projectID, repo
}

// archiveAnswer is what GET /api/fs/archive answers, spelled out here rather than imported: the
// frontend reads these keys, so a Go tag renamed by accident has to fail a test.
type archiveAnswer struct {
	Format    string `json:"format"`
	Truncated bool   `json:"truncated"`
	Entries   []struct {
		Name    string `json:"name"`
		Size    int64  `json:"size"`
		ModTime string `json:"mtime"`
		IsDir   bool   `json:"isDir"`
	} `json:"entries"`
}

func archiveGet(t *testing.T, s *Server, cookie string, query url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := withAuth(testRequest(http.MethodGet, "/api/fs/archive?"+query.Encode(), nil), cookie)
	return testResponse(t, s, req)
}

func archiveEntryGet(t *testing.T, s *Server, cookie, route string, query url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := withAuth(testRequest(http.MethodGet, route+"?"+query.Encode(), nil), cookie)
	return testResponse(t, s, req)
}

func TestFSArchiveListsAnArchiveOnDisk(t *testing.T) {
	s := testServer(t)
	cookie, _ := loginTestCSRF(t, s)
	projectID, repo := archiveProject(t, s, "fs-archive-list", map[string][]byte{
		"bundle.tar.gz": buildArchiveTarGz(t),
		"bundle.zip":    buildArchiveZip(t),
	})

	for _, tc := range []struct{ file, format string }{
		{"bundle.tar.gz", archive.FormatTarGz},
		{"bundle.zip", archive.FormatZip},
	} {
		q := url.Values{"project": {projectID}, "path": {filepath.Join(repo, tc.file)}}
		rr := archiveGet(t, s, cookie, q)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.file, rr.Code, rr.Body.String())
		}
		got := mustJSON[archiveAnswer](t, rr.Body)
		if got.Format != tc.format {
			t.Errorf("%s: format = %q, want %q", tc.file, got.Format, tc.format)
		}
		if got.Truncated {
			t.Errorf("%s: a four-member archive is not truncated", tc.file)
		}
		want := []string{"notes", "notes/todo.md", "report.txt", "blob.bin"}
		if len(got.Entries) != len(want) {
			t.Fatalf("%s: entries = %+v, want %d rows", tc.file, got.Entries, len(want))
		}
		for i, name := range want {
			row := got.Entries[i]
			if row.Name != name {
				t.Errorf("%s: entry %d = %q, want %q", tc.file, i, row.Name, name)
			}
			if row.ModTime != archiveFixtureStampText {
				t.Errorf("%s: entry %q mtime = %q, want %q", tc.file, name, row.ModTime, archiveFixtureStampText)
			}
			if row.IsDir != (name == "notes") {
				t.Errorf("%s: entry %q isDir = %v", tc.file, name, row.IsDir)
			}
			if wantSize := len(archiveFixtureMembers[i].body); row.Size != int64(wantSize) {
				t.Errorf("%s: entry %q size = %d, want %d", tc.file, name, row.Size, wantSize)
			}
		}
	}
}

// The review panel asks for the archive a task committed, and a done task has no worktree left to name:
// a committed object is read from the project checkout, which still holds it.
func TestFSArchiveListsACommittedArchive(t *testing.T) {
	s := testServer(t)
	cookie, _ := loginTestCSRF(t, s)
	projectID, repo := archiveProject(t, s, "fs-archive-ref", map[string][]byte{"dist/release.tar.gz": buildArchiveTarGz(t)})
	if err := os.Remove(filepath.Join(repo, "dist", "release.tar.gz")); err != nil {
		t.Fatal(err)
	}

	// The same path with no ref is now a plain miss, which is what makes the ref answer below mean
	// something: the bytes are in the object database and nowhere else.
	miss := archiveGet(t, s, cookie, url.Values{"project": {projectID}, "path": {filepath.Join(repo, "dist", "release.tar.gz")}})
	if miss.Code != http.StatusNotFound {
		t.Fatalf("a deleted archive answered %d: %s", miss.Code, miss.Body.String())
	}

	q := url.Values{"project": {projectID}, "path": {filepath.Join(repo, "dist", "release.tar.gz")}, "ref": {"HEAD"}}
	rr := archiveGet(t, s, cookie, q)
	if rr.Code != http.StatusOK {
		t.Fatalf("ref listing: %d %s", rr.Code, rr.Body.String())
	}
	got := mustJSON[archiveAnswer](t, rr.Body)
	if len(got.Entries) != len(archiveFixtureMembers) {
		t.Fatalf("ref listing = %+v, want %d rows", got.Entries, len(archiveFixtureMembers))
	}
	if got.Entries[2].Name != "report.txt" {
		t.Errorf("entry 2 = %q", got.Entries[2].Name)
	}

	entry := archiveEntryGet(t, s, cookie, "/api/fs/archive/entry", url.Values{
		"project": {projectID}, "path": {filepath.Join(repo, "dist", "release.tar.gz")},
		"ref": {"HEAD"}, "entry": {"report.txt"},
	})
	if entry.Code != http.StatusOK {
		t.Fatalf("ref member: %d %s", entry.Code, entry.Body.String())
	}
	if body := mustJSON[fsReadResponse](t, entry.Body).Content; body != "hello from the archive" {
		t.Errorf("member = %q", body)
	}
}

// The diff panel names an archive relative to the repository root and reaches the same bytes the explorer
// names in full. Stepping out of the tree is refused like any other escape: the join happens before the
// jail, not instead of it.
func TestFSArchiveAcceptsARepositoryRelativePath(t *testing.T) {
	s := testServer(t)
	cookie, _ := loginTestCSRF(t, s)
	projectID, _ := archiveProject(t, s, "fs-archive-relative", map[string][]byte{"dist/bundle.tar.gz": buildArchiveTarGz(t)})

	for _, ref := range []string{"", "HEAD"} {
		q := url.Values{"project": {projectID}, "path": {"dist/bundle.tar.gz"}}
		if ref != "" {
			q.Set("ref", ref)
		}
		rr := archiveGet(t, s, cookie, q)
		if rr.Code != http.StatusOK {
			t.Fatalf("ref=%q: %d %s", ref, rr.Code, rr.Body.String())
		}
	}

	q := url.Values{"project": {projectID}, "path": {"../../../../etc/passwd"}}
	if rr := archiveGet(t, s, cookie, q); rr.Code != http.StatusBadRequest {
		t.Fatalf("a relative path out of the tree answered %d: %s", rr.Code, rr.Body.String())
	}
}

func TestFSArchiveEntryReadsOneMember(t *testing.T) {
	s := testServer(t)
	cookie, _ := loginTestCSRF(t, s)
	projectID, repo := archiveProject(t, s, "fs-archive-entry", map[string][]byte{"bundle.tar.gz": buildArchiveTarGz(t)})
	path := filepath.Join(repo, "bundle.tar.gz")

	for name, want := range map[string]string{
		"report.txt":    "hello from the archive",
		"notes/todo.md": "# todo\n",
	} {
		q := url.Values{"project": {projectID}, "path": {path}, "entry": {name}}
		rr := archiveEntryGet(t, s, cookie, "/api/fs/archive/entry", q)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, rr.Code, rr.Body.String())
		}
		if got := mustJSON[fsReadResponse](t, rr.Body).Content; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	// The spelling that reached the member is the spelling the listing showed: no directory, no prefix.
	for _, name := range []string{"notes", "notes/", "report", "../report.txt"} {
		q := url.Values{"project": {projectID}, "path": {path}, "entry": {name}}
		rr := archiveEntryGet(t, s, cookie, "/api/fs/archive/entry", q)
		if rr.Code != http.StatusNotFound {
			t.Errorf("%q: answered %d, want 404", name, rr.Code)
		}
	}
	if rr := archiveEntryGet(t, s, cookie, "/api/fs/archive/entry", url.Values{"project": {projectID}, "path": {path}}); rr.Code != http.StatusBadRequest {
		t.Errorf("an entry-less request answered %d, want 400", rr.Code)
	}
}

// An archive member is bounded by the same ceiling as a file the editor opens, and a member that is not
// text is refused rather than shown as replacement characters.
func TestFSArchiveEntryRefusesWhatTheEditorCannotShow(t *testing.T) {
	s := testServer(t)
	cookie, _ := loginTestCSRF(t, s)

	var big bytes.Buffer
	tw := tar.NewWriter(&big)
	body := strings.Repeat("a", int(maxFSReadBytes)+1)
	if err := tw.WriteHeader(&tar.Header{Name: "big.txt", Typeflag: tar.TypeReg, Size: int64(len(body)), ModTime: archiveFixtureStamp, Format: tar.FormatGNU}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	projectID, repo := archiveProject(t, s, "fs-archive-member", map[string][]byte{
		"bundle.tar.gz": buildArchiveTarGz(t),
		"oversized.tar": big.Bytes(),
	})
	path := filepath.Join(repo, "bundle.tar.gz")

	q := url.Values{"project": {projectID}, "path": {path}, "entry": {"blob.bin"}}
	if rr := archiveEntryGet(t, s, cookie, "/api/fs/archive/entry", q); rr.Code != http.StatusUnsupportedMediaType {
		t.Errorf("a binary member answered %d: %s", rr.Code, rr.Body.String())
	}

	q = url.Values{"project": {projectID}, "path": {filepath.Join(repo, "oversized.tar")}, "entry": {"big.txt"}}
	rr := archiveEntryGet(t, s, cookie, "/api/fs/archive/entry", q)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("an oversized member answered %d: %s", rr.Code, rr.Body.String())
	}
	if got := archiveErrText(t, rr); got != fsTooLargeMessage {
		t.Errorf("sentence = %q, want the one that names the cap it describes: %q", got, fsTooLargeMessage)
	}
}

// The refusals are the route's own vocabulary: a name that is not an archive, a name that is a directory,
// a name that is not a file at all, and a name that is not in this project.
func TestFSArchiveRefusals(t *testing.T) {
	s := testServer(t)
	cookie, _ := loginTestCSRF(t, s)
	projectID, repo := archiveProject(t, s, "fs-archive-refuse", map[string][]byte{
		"real.tar.gz": buildArchiveTarGz(t),
		"fake.tar.gz": []byte("a text file named as though it were an archive"),
	})
	if err := os.MkdirAll(filepath.Join(repo, "folder.tar.gz"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkFifo(t, filepath.Join(repo, "pipe"))

	outside := filepath.Join(filepath.Dir(repo), "elsewhere.tar.gz")
	if err := os.WriteFile(outside, buildArchiveTarGz(t), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		path string
		ref  string
		want int
		says string
	}{
		{name: "bytes that are no archive", path: "fake.tar.gz", want: http.StatusUnsupportedMediaType, says: archive.ErrNotArchive.Error()},
		{name: "a directory", path: "folder.tar.gz", want: http.StatusBadRequest, says: "path is a directory"},
		{name: "a FIFO", path: "pipe", want: http.StatusBadRequest},
		{name: "no such file", path: "gone.tar.gz", want: http.StatusNotFound},
		{name: "outside the project", path: outside, want: http.StatusBadRequest, says: fsutil.ErrOutside.Error()},
		{name: "inside .git", path: ".git/config.tar.gz", want: http.StatusBadRequest, says: fsutil.ErrGitPath.Error()},
		{name: "an option-shaped ref", path: "real.tar.gz", ref: "--output=/tmp/pwn", want: http.StatusBadRequest},
		{name: "a ref that names nothing", path: "real.tar.gz", ref: "no-such-branch", want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := url.Values{"project": {projectID}, "path": {tc.path}}
			if !filepath.IsAbs(tc.path) {
				query.Set("path", filepath.Join(repo, tc.path))
			}
			if tc.ref != "" {
				query.Set("ref", tc.ref)
			}
			// A FIFO is the one name whose open waits, so the answer has to arrive on its own.
			rr := answeredIn(t, 10*time.Second, "GET /api/fs/archive on "+tc.path, func() *httptest.ResponseRecorder {
				return archiveGet(t, s, cookie, query)
			})
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rr.Code, tc.want, rr.Body.String())
			}
			if tc.says != "" && !strings.Contains(archiveErrText(t, rr), tc.says) {
				t.Errorf("sentence = %q, want it to say %q", archiveErrText(t, rr), tc.says)
			}
		})
	}

	// The control: the same route, same project, real archive.
	if rr := archiveGet(t, s, cookie, url.Values{"project": {projectID}, "path": {filepath.Join(repo, "real.tar.gz")}}); rr.Code != http.StatusOK {
		t.Errorf("the real archive was refused: %d %s", rr.Code, rr.Body.String())
	}
}

// A path a request supplies reaches git only inside a spec, and a spec is built from the tree the
// request named: an absolute path that sits under it is the same object.
func TestFSArchiveRefusesAPathOutsideTheProjectWithARef(t *testing.T) {
	s := testServer(t)
	cookie, _ := loginTestCSRF(t, s)
	projectID, repo := archiveProject(t, s, "fs-archive-ref-jail", map[string][]byte{"bundle.tar.gz": buildArchiveTarGz(t)})

	q := url.Values{
		"project": {projectID},
		"path":    {filepath.Join(filepath.Dir(repo), "elsewhere", "bundle.tar.gz")},
		"ref":     {"HEAD"},
	}
	if rr := archiveGet(t, s, cookie, q); rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want the containment refusal (%s)", rr.Code, rr.Body.String())
	}
	// The control: the same ref over a path inside the project answers.
	q.Set("path", filepath.Join(repo, "bundle.tar.gz"))
	if rr := archiveGet(t, s, cookie, q); rr.Code != http.StatusOK {
		t.Errorf("the committed archive was refused: %d %s", rr.Code, rr.Body.String())
	}
}

// The refusal vocabulary is one switch, so it is pinned as one table: every error the reader can answer
// with has a status and a sentence a person can act on.
func TestFailArchiveVocabulary(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
		says string
	}{
		{err: archive.ErrNotArchive, want: http.StatusUnsupportedMediaType, says: archive.ErrNotArchive.Error()},
		{err: archive.ErrNoEntry, want: http.StatusNotFound, says: archive.ErrNoEntry.Error()},
		{err: archive.ErrEntryTooLarge, want: http.StatusRequestEntityTooLarge, says: fsTooLargeMessage},
		{err: archive.ErrBinaryEntry, want: http.StatusUnsupportedMediaType, says: archive.ErrBinaryEntry.Error()},
		{err: context.DeadlineExceeded, want: http.StatusGatewayTimeout, says: archiveWalkTimeoutMessage},
	} {
		rr := httptest.NewRecorder()
		failArchive(rr, tc.err)
		if rr.Code != tc.want {
			t.Errorf("%v: status = %d, want %d", tc.err, rr.Code, tc.want)
		}
		if got := archiveErrText(t, rr); !strings.Contains(got, tc.says) {
			t.Errorf("%v: sentence = %q, want it to contain %q", tc.err, got, tc.says)
		}
	}

	// A fault that is not the archive's own is the server's, and the sentence the browser gets is MIKE's.
	sink, restore := logging.TestSink()
	defer restore()
	rr := httptest.NewRecorder()
	failArchive(rr, errors.New("the disk is on fire"))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
	if got := archiveErrText(t, rr); strings.Contains(got, "on fire") {
		t.Errorf("the cause reached the browser: %q", got)
	}
	if !strings.Contains(sink.String(), "on fire") {
		t.Errorf("the cause did not reach the log: %s", sink.String())
	}
}

// archiveErrText reads the `error` field every file endpoint answers with.
func archiveErrText(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	return mustJSON[struct {
		Error string `json:"error"`
	}](t, rr.Body).Error
}
