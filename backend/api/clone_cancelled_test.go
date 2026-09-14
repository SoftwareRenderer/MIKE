package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kanban/backend/git"
	"kanban/backend/types"
	"kanban/pkg/logging"
)

// Phase 9's acceptance specs, written in Phase 3h. A clone is the one long operation that *is* the
// browser's to stop — `CloneContext` runs on the request context, so closing the tab stops it, by design.
// The UX claim that has to hold is therefore the other one: a stopped clone leaves nothing a later request
// can mistake for a project. A half-written checkout with a row beside it is a project whose files are not
// there, and one without a row is a directory no request can name; both look like success from the board.

func TestCloneCancelledByClientLeavesNoProjectAndNoCheckout(t *testing.T) {
	h, _, st := testProjectHandlerWithStore(t)

	buf, restore := logging.TestSink()
	defer restore()

	url, fetching, requests := dumbHTTPGitServer(t)
	// `path` is the parent; the checkout lands in a subdirectory of it named after the repository, which
	// is why the assertion below is about the parent being empty rather than about one known name.
	parent := t.TempDir()

	body, _ := json.Marshal(types.CreateProjectRequest{Path: parent, GitURL: url})
	// The handler returning is the only honest signal that the cleanup has run.
	handlerDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		h.HandleClone(w, r)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/projects/clone", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	// Wait until the server says it is cloning, so the cancel lands while `git clone` is fetching the
	// pack — the fixture stalls every pack request, so the fetch is provably still in flight.
	scanner := bufio.NewScanner(resp.Body)
	sawClone := false
	var streamed []string
	for scanner.Scan() {
		line := scanner.Text()
		streamed = append(streamed, line)
		if strings.Contains(line, "Cloning") {
			sawClone = true
			break
		}
	}
	if !sawClone {
		_ = resp.Body.Close()
		t.Fatalf("the clone never reported itself in progress; stream: %q log: %q", streamed, buf.String())
	}
	// Now the fetch is under way and the checkout directory exists: a cancellation that arrived earlier
	// would prove nothing, because there would be nothing yet to leave behind.
	select {
	case <-fetching:
	case <-time.After(10 * time.Second):
		_ = resp.Body.Close()
		t.Fatalf("git never asked for the pack, so the cancel would not have landed mid-fetch; requests: %v log: %q", requests(), buf.String())
	}
	if entries, err := os.ReadDir(parent); err != nil || len(entries) == 0 {
		_ = resp.Body.Close()
		t.Fatalf("nothing was on disk to cancel mid-way (ReadDir: %v)", err)
	}
	cancel()
	_ = resp.Body.Close()

	select {
	case <-handlerDone:
	case <-time.After(20 * time.Second):
		t.Fatalf("the handler never returned after the client left; log:\n%s", buf.String())
	}

	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", parent, err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the stopped clone left %v behind in %s", names, parent)
	}

	projects, err := st.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	for _, p := range projects {
		if strings.HasPrefix(p.Path, parent) {
			t.Errorf("a project row exists for the clone that never finished: %+v", p)
		}
	}

	// The user's own navigation is not a git failure. Putting it in the error log is how a cancelled clone
	// becomes the report nobody reads when a real clone failure arrives.
	if log := buf.String(); strings.Contains(log, "git clone failed") {
		t.Errorf("a cancelled clone was logged as a git failure:\n%s", log)
	}
}

// dumbHTTPGitServer serves a one-commit repository over the git dumb HTTP protocol and stalls every pack
// request, which is what makes "cancel while it is fetching" a fact about the test rather than a hope.
// Serving the parent directory is what makes the repo reachable at /repo.git.
func dumbHTTPGitServer(t *testing.T) (url string, fetching <-chan struct{}, requests func() []string) {
	t.Helper()
	root := t.TempDir()

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := git.Command(dir, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}

	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	run(src, "init", "-q", ".")
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte(strings.Repeat("fixture payload\n", 200)), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	run(src, "add", "-A")
	run(src, "-c", "user.email=fixture@mike.test", "-c", "user.name=MIKE Fixture", "commit", "-qm", "one")
	run(root, "clone", "-q", "--bare", "src", "repo.git")
	// A freshly cloned bare repo can hold its objects loose, and the dumb protocol then fetches them by
	// path instead of asking for a pack — which is the request this fixture stalls.
	run(filepath.Join(root, "repo.git"), "repack", "-a", "-d", "-q")
	// The dumb protocol reads these two listings instead of talking to upload-pack.
	run(filepath.Join(root, "repo.git"), "update-server-info")

	started := make(chan struct{})
	var once sync.Once
	var seen []string
	var seenMu sync.Mutex
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		seenMu.Unlock()

		// Once is enough: the first pack request is the fetch, and stalling every later one would only
		// multiply the test's runtime.
		if strings.Contains(r.URL.Path, ".pack") {
			once.Do(func() { close(started) })
			time.Sleep(3 * time.Second)
		}
		// Stripped of the mount point, so what is served is the bare repository itself — pointing the
		// file server at `root` would answer /repo.git/info/refs with 404 and git would call that
		// "repository not found" without ever asking for a pack.
		http.StripPrefix("/repo.git/", http.FileServer(http.Dir(filepath.Join(root, "repo.git")))).ServeHTTP(w, r)
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	seenRequests := func() []string {
		seenMu.Lock()
		defer seenMu.Unlock()
		return append([]string(nil), seen...)
	}
	return srv.URL + "/repo.git", started, seenRequests
}
