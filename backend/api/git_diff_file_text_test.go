package api

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf8"

	"mike/backend/git"
	"mike/backend/types"
)

// Both file routes carry a file's own bytes as JSON text, and both repair what the encoder would refuse.
// A picture is a different answer; its refusal is handler_fs_read_text_test.go.

// latinBytes is "café\nnew\n" in Latin-1: no NUL, so only the UTF-8 rule can catch it.
var latinBytes = []byte{0x63, 0x61, 0x66, 0xE9, 0x0A, 0x6E, 0x65, 0x77, 0x0A}

var pngBytes = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

func writeWorktreeBytes(t *testing.T, dir, rel string, content []byte) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// decodeJSONBody fails the test rather than returning an error: a body that is not there is the whole
// regression.
func decodeJSONBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	if len(body) == 0 {
		t.Fatal("the route answered with no body at all")
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("the body is not JSON a client can read: %v (%q)", err, body)
	}
	return got
}

// The untracked re-read (`full=1`) takes the file off disk, so it is the path that meets the bytes
// themselves.
func TestTaskDiffFileAnswersAFileWhoseBytesAreNotText(t *testing.T) {
	h, mux, wtPath := taskDiffFileFixture(t, "difftext-proj", "difftext-task")
	writeWorktreeBytes(t, wtPath, "latin.txt", latinBytes)
	h.diffCache.put("difftext-task", &git.TaskDiff{
		Tracked:   map[string]*git.TrackedDiffFile{},
		Untracked: []git.UntrackedFile{{Path: "latin.txt", Truncated: true}},
	})

	rr := doRequest(t, mux, http.MethodGet,
		"/api/projects/difftext-proj/tasks/difftext-task/diff/file?path=latin.txt&full=1")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	body := decodeJSONBody(t, rr.Body.Bytes())
	lines, ok := body["lines"].([]any)
	if !ok || len(lines) != 2 {
		t.Fatalf("lines = %v, want the file's two lines", body["lines"])
	}
	for _, line := range lines {
		text, _ := line.(string)
		if !utf8.ValidString(text) {
			t.Errorf("line %q is not valid UTF-8, so nothing can marshal it", text)
		}
	}
	if first, _ := lines[1].(string); first != "new" {
		t.Errorf("the readable line was lost in the repair: %q", first)
	}
}

// An image is still a binary file with a size and no rows, which is what the panel's notice and its picture
// control read.
func TestTaskDiffFileStillAnswersAnImageAsBinary(t *testing.T) {
	h, mux, wtPath := taskDiffFileFixture(t, "difftext-img", "difftext-img-task")
	writeWorktreeBytes(t, wtPath, "logo.png", pngBytes)
	h.diffCache.put("difftext-img-task", &git.TaskDiff{
		Tracked:   map[string]*git.TrackedDiffFile{},
		Untracked: []git.UntrackedFile{{Path: "logo.png", IsBinary: true, FileSize: int64(len(pngBytes))}},
	})

	rr := doRequest(t, mux, http.MethodGet,
		"/api/projects/difftext-img/tasks/difftext-img-task/diff/file?path=logo.png")
	body := decodeJSONBody(t, rr.Body.Bytes())
	if body["isBinary"] != true {
		t.Errorf("isBinary = %v, want true so the panel says what the file is", body["isBinary"])
	}
	if body["fileSize"] == nil {
		t.Error("fileSize is missing, so the notice has no size to name")
	}
	if lines, _ := body["lines"].([]any); len(lines) != 0 {
		t.Errorf("an image answered %d lines of text", len(lines))
	}
}

// The project-level route answers the patch text as well as the rows, and `git diff` puts the file's bytes
// into it verbatim; the panel reads this body whole, so one unencodable field would take every other file's
// rows down with it.
func TestProjectDiffFileAnswersForeignBytesInPatchAndStatus(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	if err := s.CreateProject(&types.Project{ID: "p", Name: "p", Path: projPath}); err != nil {
		t.Fatal(err)
	}
	writeWorktreeBytes(t, projPath, "latin.txt", []byte{0x63, 0x61, 0x66, 0xE9, 0x0A})
	writeWorktreeBytes(t, projPath, "logo.png", pngBytes)
	mustRun(t, projPath, "git", "add", "--", "latin.txt", "logo.png")
	mustRun(t, projPath, "git", "commit", "-m", "base")
	// Change both, unstaged: the text one puts a foreign byte in the patch, the image one is git's binary case.
	writeWorktreeBytes(t, projPath, "latin.txt", []byte{0x63, 0x61, 0x66, 0xE9, 0x0A, 0x6E, 0x65, 0x77, 0x0A})
	writeWorktreeBytes(t, projPath, "logo.png", append(append([]byte{}, pngBytes...), 0x00, 0x01))

	mux := newTestGitMux(h)
	// The status line is the third body that carries repository bytes, so this asks for it too.
	mux.HandleFunc("GET /api/projects/{projectID}/git/status", h.HandleStatus)

	text := decodeJSONBody(t, doRequest(t, mux, http.MethodGet,
		"/api/projects/p/git/diff/file?path=latin.txt").Body.Bytes())
	if patch, _ := text["diff"].(string); patch == "" {
		t.Error("the patch text was dropped, so the panel has nothing to draw")
	} else if !utf8.ValidString(patch) {
		t.Errorf("the patch is not valid UTF-8: %q", patch)
	}
	if lines, _ := text["lines"].([]any); len(lines) != 2 {
		t.Errorf("lines = %v, want the file's two lines", text["lines"])
	}

	image := decodeJSONBody(t, doRequest(t, mux, http.MethodGet,
		"/api/projects/p/git/diff/file?path=logo.png").Body.Bytes())
	if image["isBinary"] != true {
		t.Errorf("isBinary = %v, want true for a PNG", image["isBinary"])
	}
	if size, _ := image["file_size"].(float64); size == 0 {
		t.Errorf("file_size = %v, want the image's size so the notice can name it", image["file_size"])
	}

	status := decodeJSONBody(t, doRequest(t, mux, http.MethodGet, "/api/projects/p/git/status").Body.Bytes())
	if line, _ := status["status"].(string); !utf8.ValidString(line) {
		t.Errorf("git status answered unencodable text: %q", line)
	}
}

// The listing asks this route what a file is before it asks for rows, and the names it can be handed are not
// only files that exist: a row the watcher left behind names one git still holds, and a dropped file names one
// the worktree no longer has. Neither is a server fault.
func TestProjectDiffFileMetaAnswersEveryStateAListingCanName(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	if err := s.CreateProject(&types.Project{ID: "p", Name: "p", Path: projPath}); err != nil {
		t.Fatal(err)
	}
	writeWorktreeBytes(t, projPath, "committed.txt", []byte("one\ntwo\n"))
	writeWorktreeBytes(t, projPath, "gone.txt", []byte("was here\n"))
	writeWorktreeBytes(t, projPath, "logo.png", pngBytes)
	mustRun(t, projPath, "git", "add", "--", "committed.txt", "gone.txt", "logo.png")
	mustRun(t, projPath, "git", "commit", "-m", "base")
	if err := os.Remove(filepath.Join(projPath, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeWorktreeBytes(t, projPath, "fresh.txt", []byte("untracked\n"))

	mux := newTestGitMux(h)

	for _, tc := range []struct {
		path       string
		whatCall   string
		wantBinary bool
	}{
		{path: "committed.txt", whatCall: "a file the working tree holds"},
		{path: "gone.txt", whatCall: "a file only the commit holds"},
		{path: "fresh.txt", whatCall: "a file no commit has yet"},
		{path: "logo.png", whatCall: "an image", wantBinary: true},
		{path: "never-existed.txt", whatCall: "a name nothing holds"},
	} {
		rr := doRequest(t, mux, http.MethodGet, "/api/projects/p/git/diff/file?path="+tc.path+"&meta=1")
		if rr.Code >= http.StatusInternalServerError {
			t.Errorf("%s (%s): status = %d, want a refusal rather than a server fault: %s",
				tc.path, tc.whatCall, rr.Code, rr.Body.String())
			continue
		}
		if rr.Code != http.StatusOK {
			t.Errorf("%s (%s): status = %d, want 200: %s", tc.path, tc.whatCall, rr.Code, rr.Body.String())
			continue
		}
		meta := decodeJSONBody(t, rr.Body.Bytes())
		if binary, _ := meta["isBinary"].(bool); binary != tc.wantBinary {
			t.Errorf("%s (%s): isBinary = %v, want %v", tc.path, tc.whatCall, binary, tc.wantBinary)
		}
	}
}
