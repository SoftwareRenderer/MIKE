package server

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"mike/backend/fsutil"
	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

/*
 * What the Explorer's Info menu reads: which facts a row answers, and what is refused before any stat
 * runs — a folder's figure is a floor once its walk budget runs out, a path gone since the listing was
 * read stays a row, and a symbolic link is named without naming where it points.
 */

// Spelled out here rather than imported: a field renamed or dropped changes the wire the Explorer reads.
type statWireRow struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Bytes    *int64 `json:"bytes"`
	Items    *int   `json:"items"`
	Partial  bool   `json:"partial"`
	Mode     string `json:"mode"`
	Modified string `json:"modified"`
	MIME     string `json:"mime"`
}

type statWireAnswer struct {
	Entries []statWireRow `json:"entries"`
}

// statProject registers a project whose checkout holds `files`, each key a repository-relative path.
// The directory is a committed repository: a `.git` has to exist to be refused, and a task's worktree
// needs a HEAD.
func statProject(t *testing.T, s *Server, id string, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		// chmod rather than the create mode, so the harness's umask cannot change a row's permissions.
		if err := os.Chmod(full, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.CreateProject(&types.Project{ID: id, Name: id, Path: repo}); err != nil {
		t.Fatal(err)
	}
	testgit.RepoAtMessage(t, repo, "Initial")
	return repo
}

// statAsk asks the route the way the Explorer does — one `path` key, repeated — and hands back the
// status and body too, so a test can expect a refusal and read what came back.
func statAsk(t *testing.T, s *Server, project, task string, paths ...string) (int, statWireAnswer, string) {
	t.Helper()
	query := url.Values{"project": {project}}
	if task != "" {
		query.Set("task", task)
	}
	for _, p := range paths {
		query.Add("path", p)
	}
	req := testRequest(http.MethodGet, "/api/fs/stat?"+query.Encode(), nil)
	rr := testResponseAuth(t, s, req)
	var answer statWireAnswer
	if rr.Code == http.StatusOK {
		answer = mustJSON[statWireAnswer](t, rr.Body)
	}
	return rr.Code, answer, rr.Body.String()
}

// TestFSStatAnswersAFilesSizeTimesAndType: the four facts the listing leaves out. An unknown suffix
// answers no type.
func TestFSStatAnswersAFilesSizeTimesAndType(t *testing.T) {
	s := testServer(t)
	repo := statProject(t, s, "fs-stat-file", map[string]string{
		"notes/report.txt": "hello world",
		"data.zzqx":        "\x00\x01\x02",
	})

	status, answer, _ := statAsk(t, s, "fs-stat-file", "", filepath.Join(repo, "notes/report.txt"), filepath.Join(repo, "data.zzqx"))
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if len(answer.Entries) != 2 {
		t.Fatalf("entries = %+v, want one per asked path", answer.Entries)
	}

	text := answer.Entries[0]
	want := statWireRow{Name: "report.txt", Path: filepath.Join(repo, "notes/report.txt"), Kind: "file", Mode: "0640"}
	if text.Name != want.Name || text.Path != want.Path || text.Kind != want.Kind || text.Mode != want.Mode {
		t.Fatalf("row = %+v, want %v", text, want)
	}
	if text.Bytes == nil || *text.Bytes != 11 {
		t.Fatalf("bytes = %v, want 11", text.Bytes)
	}
	if !strings.HasPrefix(text.MIME, "text/plain") {
		t.Fatalf("mime = %q, want the type a .txt file is known by", text.MIME)
	}
	modified, err := time.Parse(time.RFC3339, text.Modified)
	if err != nil {
		t.Fatalf("modified %q is not fixed-width UTC: %v", text.Modified, err)
	}
	if age := time.Since(modified); age > 10*time.Minute || age < -10*time.Minute {
		t.Fatalf("modified %v is not a moment around now", modified)
	}

	unknown := answer.Entries[1]
	if unknown.Kind != "file" || unknown.MIME != "" {
		t.Fatalf("row = %+v, want a file with no type claimed for a suffix nothing knows", unknown)
	}
}

// TestFSStatAnswersAFoldersItemCountAndByteTotal: a folder carries both how many and how much, and the
// folder itself is not one of the things.
func TestFSStatAnswersAFoldersItemCountAndByteTotal(t *testing.T) {
	s := testServer(t)
	repo := statProject(t, s, "fs-stat-dir", map[string]string{
		"src/a.txt":     strings.Repeat("a", 100),
		"src/sub/b.txt": strings.Repeat("b", 40),
	})

	status, answer, _ := statAsk(t, s, "fs-stat-dir", "", filepath.Join(repo, "src"))
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	row := answer.Entries[0]
	if row.Kind != "dir" || row.Items == nil || *row.Items != 3 || row.Bytes == nil || *row.Bytes != 140 {
		t.Fatalf("row = %+v, want 3 entries below the folder and their 140 bytes", row)
	}
	if row.Partial {
		t.Fatal("a folder walked whole is reported as a partial figure")
	}
	if row.MIME != "" {
		t.Fatalf("mime = %q, want none claimed for a folder", row.MIME)
	}
}

// TestFSStatReportsAPartialFolderWhenTheWalkRunsOut: a tree past the budget answers what was counted and
// says so — an unmarked floor reads as a total.
func TestFSStatReportsAPartialFolderWhenTheWalkRunsOut(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("0123456789"), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	// Two entries of budget: the folder itself and one file, in the walk's own lexical order.
	row := statRow(dir, dir, fsutil.WalkBudget{MaxEntries: 2})
	if !row.Partial {
		t.Fatalf("row = %+v, want the walk to admit it stopped early", row)
	}
	if row.Bytes == nil || *row.Bytes != 10 || row.Items == nil || *row.Items != 1 {
		t.Fatalf("row = %+v, want the 10 bytes of the one entry the budget let it price", row)
	}
}

// TestFSStatPricesNothingForAnIrregularName: a FIFO, socket or device is named as what it is and priced
// as nothing; a type claimed from its suffix would be invented.
func TestFSStatPricesNothingForAnIrregularName(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	row := statRow(fifo, fifo, fsStatWalkBudget())
	if row.Kind != "other" || row.Bytes != nil || row.Items != nil || row.MIME != "" {
		t.Fatalf("row = %+v, want the irregular name named and priced as nothing", row)
	}
	if row.Mode != "0644" || row.Modified == "" {
		t.Fatalf("row = %+v, want the name's own permissions and time", row)
	}
}

// TestFSStatClaimsNothingAboutANameItCannotState: the jail accepted this name and the kernel will not
// describe it — a cause for the log and an empty row, not a missing file.
func TestFSStatClaimsNothingAboutANameItCannotState(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "locked")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(blocked, "wedged.txt")
	if err := os.WriteFile(name, []byte("bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })
	if _, err := os.Lstat(name); err == nil {
		t.Skip("this host stats through a 0o000 directory anyway (running as root)")
	}

	row := statRow(name, name, fsStatWalkBudget())
	if row.Kind != "unknown" || row.Name != "wedged.txt" || row.Path != name {
		t.Fatalf("row = %+v, want the asked name called unknown", row)
	}
	if row.Bytes != nil || row.Items != nil || row.Mode != "" || row.Modified != "" || row.MIME != "" {
		t.Fatalf("row = %+v, want nothing claimed about a name that cannot be stated", row)
	}
}

// TestFSStatHoldsAGitDirectoryOutOfAFoldersFigure: no listing offers a row for .git, so the figure the
// dialog draws beside that listing holds none of it either.
func TestFSStatHoldsAGitDirectoryOutOfAFoldersFigure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(strings.Repeat("a", 20)), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "objects", "pack"), make([]byte, 4096), 0o640); err != nil {
		t.Fatal(err)
	}

	row := statRow(dir, dir, fsStatWalkBudget())
	if row.Bytes == nil || *row.Bytes != 20 || row.Items == nil || *row.Items != 1 {
		t.Fatalf("row = %+v, want the 20 bytes and 1 entry the listing shows", row)
	}
}

// TestFSStatAnswersAMissingPathAsARow: a selection can be a listing a task has since emptied, and one
// gone row must not take the rest of the answer with it.
func TestFSStatAnswersAMissingPathAsARow(t *testing.T) {
	s := testServer(t)
	repo := statProject(t, s, "fs-stat-gone", map[string]string{"here.txt": "here"})

	status, answer, _ := statAsk(t, s, "fs-stat-gone", "", filepath.Join(repo, "deleted.txt"), filepath.Join(repo, "here.txt"))
	if status != http.StatusOK {
		t.Fatalf("status %d, body gone: a missing path is a row, not a refusal", status)
	}
	if len(answer.Entries) != 2 {
		t.Fatalf("entries = %+v, want one per asked path", answer.Entries)
	}
	gone := answer.Entries[0]
	if gone.Kind != "missing" || gone.Name != "deleted.txt" {
		t.Fatalf("row = %+v, want the gone row named and called missing", gone)
	}
	if gone.Bytes != nil || gone.Items != nil || gone.Mode != "" || gone.Modified != "" {
		t.Fatalf("row = %+v, want nothing claimed about a file that is not there", gone)
	}
	if answer.Entries[1].Kind != "file" {
		t.Fatalf("row = %+v, want the row that is still there answered in full", answer.Entries[1])
	}
}

// TestFSStatRefusesAPathOutsideTheProject: a request aimed at the host beyond the checkout is refused
// before anything is stated about it — by the lexical path, by a step above the root, and by the
// symlink-resolved path a task planted inside the tree. The refusal is ErrOutside alone: the host path a
// name resolves to is the server's to read, not the requester's.
func TestFSStatRefusesAPathOutsideTheProject(t *testing.T) {
	s := testServer(t)
	repo := statProject(t, s, "fs-stat-jailed", map[string]string{"here.txt": "here"})
	host := t.TempDir()
	outside := filepath.Join(host, "outside-secret.txt")
	target := filepath.Join(host, "host-secret.txt")
	for _, name := range []string{outside, target} {
		if err := os.WriteFile(name, []byte("not the project's"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(repo, "points-away")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	for _, tc := range []struct{ what, path string }{
		{"a host file outside the checkout", outside},
		{"a step above the root", filepath.Join(repo, "..", t.TempDir(), "anything.txt")},
		{"a link the task planted that leaves the tree", link},
	} {
		status, _, body := statAsk(t, s, "fs-stat-jailed", "", tc.path)
		if status != http.StatusBadRequest {
			t.Fatalf("status %d for %s, want 400: %s", status, tc.what, body)
		}
		for _, leak := range []string{host, "outside-secret", "host-secret", "points-away", "resolves to"} {
			if strings.Contains(body, leak) {
				t.Errorf("the refusal for %s repeats %q: %s", tc.what, leak, body)
			}
		}
	}
}

// TestFSStatRefusesAGitSegment: a repository's own files are not what these endpoints describe.
func TestFSStatRefusesAGitSegment(t *testing.T) {
	s := testServer(t)
	repo := statProject(t, s, "fs-stat-gitdir", map[string]string{"here.txt": "here"})

	status, _, body := statAsk(t, s, "fs-stat-gitdir", "", filepath.Join(repo, ".git/config"))
	if status != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", status, body)
	}
}

// TestFSStatNeedsAPathAProjectAndACappedBatch: the three shapes of a malformed request, each answered
// before any path is resolved.
func TestFSStatNeedsAPathAProjectAndACappedBatch(t *testing.T) {
	s := testServer(t)
	repo := statProject(t, s, "fs-stat-batch", map[string]string{"here.txt": "here"})

	if rr := testResponseAuth(t, s, testRequest(http.MethodGet, "/api/fs/stat?project=fs-stat-batch", nil)); rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d with no path, want 400: %s", rr.Code, rr.Body.String())
	}
	if rr := testResponseAuth(t, s, testRequest(http.MethodGet, "/api/fs/stat?path="+filepath.Join(repo, "here.txt"), nil)); rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d with no project, want 400: %s", rr.Code, rr.Body.String())
	}

	query := url.Values{"project": {"fs-stat-batch"}}
	for range maxFSOpsBatch + 1 {
		query.Add("path", filepath.Join(repo, "here.txt"))
	}
	rr := testResponseAuth(t, s, testRequest(http.MethodGet, "/api/fs/stat?"+query.Encode(), nil))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "max") {
		t.Fatalf("status %d over the batch cap, want 400 naming it: %s", rr.Code, rr.Body.String())
	}
}

// TestFSStatNamesANameThatIsASymbolicLinkAsOne: the row says so and stops — the target's size would be an
// answer about a file the request never named.
func TestFSStatNamesANameThatIsASymbolicLinkAsOne(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a-host-name-nobody-would-ask-for.txt")
	if err := os.WriteFile(target, []byte(strings.Repeat("x", 4096)), 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "swapped-in")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	row := statRow(link, link, fsutil.WalkBudget{})
	if row.Kind != "link" {
		t.Fatalf("row = %+v, want a link named as one", row)
	}
	if row.Bytes != nil || row.Items != nil || row.MIME != "" {
		t.Fatalf("row = %+v, want nothing priced about a name that is a link", row)
	}
}

// TestFSStatForATasksWorktree: ?task= scopes the read, so the file named is the one in that task's tree
// rather than the one of the same name in the checkout.
func TestFSStatForATasksWorktree(t *testing.T) {
	s := testServer(t)
	repo := statProject(t, s, "fs-stat-worktree", map[string]string{"notes.md": "checkout"})
	taskID := createTestTask(t, s, "fs-stat-worktree", "stat the worktree")
	worktree, _, err := s.executor.WorktreeMgr().CreateWorktree("fs-stat-worktree", taskID, "task/"+taskID, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "notes.md"), []byte("written by the task"), 0o640); err != nil {
		t.Fatal(err)
	}

	inCheckout, inWorktree := filepath.Join(repo, "notes.md"), filepath.Join(worktree, "notes.md")
	status, answer, body := statAsk(t, s, "fs-stat-worktree", taskID, inCheckout, inWorktree)
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	if len(answer.Entries) != 2 {
		t.Fatalf("entries = %+v, want one per asked path", answer.Entries)
	}
	// The checkout's own bytes, and the different ones the task wrote beside them.
	if answer.Entries[0].Bytes == nil || *answer.Entries[0].Bytes != 8 {
		t.Fatalf("checkout row = %+v, want the file the commit holds", answer.Entries[0])
	}
	if answer.Entries[1].Bytes == nil || *answer.Entries[1].Bytes != 19 {
		t.Fatalf("worktree row = %+v, want the 19 bytes the task wrote", answer.Entries[1])
	}
	if answer.Entries[1].Path != inWorktree {
		t.Fatalf("path = %q, want the spelling the request used", answer.Entries[1].Path)
	}
}
