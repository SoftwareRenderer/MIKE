package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/types"
)

// secretMarker is the content of the file planted outside the project; seeing it in
// a response is the failure these tests look for.
const secretMarker = "DO-NOT-LEAK-0d30f2"

// TestProjectDiffFilePathContainment is the jail for the one git route that reads a
// working-tree file on the host: GET /api/projects/{id}/git/diff/file.
//
// `../` was never the interesting case — git refuses a pathspec outside the
// repository, so the handler failed before reading anything. A symlink INSIDE the
// repository is: git answers the diff from the object database, then the handler
// read the file through the link, which resolves against the host. A cloned repo
// shipping `key.pem -> ../../../home/user/.ssh/id_rsa` therefore read any file the
// server user could open, and so did the same route through a symlinked directory.
func TestProjectDiffFilePathContainment(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	if err := s.CreateProject(&types.Project{ID: "jail-proj", Name: "jail-proj", Path: projPath}); err != nil {
		t.Fatal(err)
	}

	// A secret outside the project, and a directory of them.
	outside := filepath.Dir(projPath)
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte(secretMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secretDir := filepath.Join(outside, "secretdir")
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretDir, "key.pem"), []byte(secretMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A normal file inside the project, which must keep working.
	createFile(t, projPath, "inside.txt", "harmless\n")
	mustRun(t, projPath, "git", "add", "-A")
	mustRun(t, projPath, "git", "commit", "-m", "inside")

	// A tracked symlink, an untracked one, and a symlinked directory.
	if err := os.Symlink(secret, filepath.Join(projPath, "link-tracked.txt")); err != nil {
		t.Fatal(err)
	}
	mustRun(t, projPath, "git", "add", "link-tracked.txt")
	mustRun(t, projPath, "git", "commit", "-m", "tracked link")
	if err := os.Symlink(secret, filepath.Join(projPath, "link-untracked.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretDir, filepath.Join(projPath, "dirlink")); err != nil {
		t.Fatal(err)
	}

	mux := newTestGitMux(h)
	const prefix = "/api/projects/jail-proj/git/diff/file?path="

	tests := []struct {
		name   string
		path   string
		wantOK bool
	}{
		{name: "file inside the project", path: "inside.txt", wantOK: true},
		{name: "parent traversal", path: "../secret.txt"},
		{name: "deep parent traversal", path: "sub/../../secret.txt"},
		{name: "absolute path", path: secret},
		{name: "tracked symlink out of the project", path: "link-tracked.txt"},
		{name: "untracked symlink out of the project", path: "link-untracked.txt"},
		{name: "symlinked directory", path: "dirlink/key.pem"},
	}
	for _, tt := range tests {
		for _, staged := range []bool{false, true} {
			name, target := tt.name, prefix+tt.path
			if staged {
				name += " (staged)"
				target += "&staged=true"
			}
			t.Run(name, func(t *testing.T) {
				rr := doRequest(t, mux, http.MethodGet, target)
				body := rr.Body.String()
				if strings.Contains(body, secretMarker) {
					t.Fatalf("path %q leaked the file outside the project: %.200s", tt.path, body)
				}
				want := http.StatusBadRequest
				if tt.wantOK {
					want = http.StatusOK
				}
				if rr.Code != want {
					t.Fatalf("path %q: got %d, want %d (%.200s)", tt.path, rr.Code, want, body)
				}
				if !tt.wantOK {
					return
				}
				var resp map[string]any
				if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
					t.Fatalf("response is not JSON: %v", err)
				}
				if lines, _ := resp["lines"].([]any); len(lines) != 1 || lines[0] != "harmless" {
					t.Errorf("the in-project file changed shape: %.200s", body)
				}
			})
		}
	}
}

// TestRepoFilePathJailsSymlinks pins the helper directly: a link standing where the
// file would be created, a link that resolves out, and a path that only escapes
// after cleaning.
func TestRepoFilePathJailsSymlinks(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secret, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}

	if got, err := repoFilePath(repo, "a/b.txt"); err != nil {
		t.Errorf("a file inside the repo was refused: %v", err)
	} else if want := filepath.Join(repo, "a/b.txt"); got != want {
		t.Errorf("repoFilePath = %q, want %q", got, want)
	}
	for _, bad := range []string{"../secret.txt", "sub/../link", "/etc/passwd", "link", "sub/../../secret.txt"} {
		if got, err := repoFilePath(repo, bad); err == nil {
			t.Errorf("repoFilePath(%q) = %q, want an error", bad, got)
		}
	}
}
