package api

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"mike/backend/fsutil"
	"mike/backend/git"
	"mike/backend/store"
	"mike/backend/testutil/teststore"
	"mike/backend/types"
	"mike/pkg/subproc"

	"mike/backend/sandbox"
)

// createBareRepo creates a bare git repository at the given path to act as a
// clone source for tests.
func createBareRepo(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command("git", "init", "--bare", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v (%s)", err, out)
	}
}

// TestRepoNameFromGitURL verifies repository name extraction for both SSH and
// HTTPS git URLs, plus local paths.
func TestRepoNameFromGitURL(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://github.com/org/repo.git", "repo"},
		{"https://github.com/org/repo", "repo"},
		{"https://github.com/org/my.repo.git", "my.repo"},
		{"git@github.com:org/repo.git", "repo"},
		{"ssh://git@github.com/org/repo.git", "repo"},
		{filepath.Join("local", "path", "to", "repo"), "repo"},
	}
	for _, c := range cases {
		got, err := repoNameFromGitURL(c.url)
		if err != nil {
			t.Fatalf("repoNameFromGitURL(%q) error: %v", c.url, err)
		}
		if got != c.want {
			t.Fatalf("repoNameFromGitURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
	if _, err := repoNameFromGitURL(""); err == nil {
		t.Fatal("expected error for empty URL")
	}
	if _, err := repoNameFromGitURL("/"); err == nil {
		t.Fatal("expected error for root URL")
	}
}

// testProjectHandlerGranted is the fixture carrying the DANGER_ZONE grant — the permission to create
// a project with sandboxing disabled. Production decides it once, at boot (startup.Config.DangerZone,
// injected into NewProjectHandler), so a test names the process it is pretending to have booted
// instead of editing this one's environment. The refusal is what a closed grant produces, and that is
// pinned by TestSandboxCreateDisabledBlockedWithoutDangerZone.
func testProjectHandlerGranted(t *testing.T) (*ProjectHandler, string) {
	t.Helper()
	h, projID := testProjectHandler(t)
	h.dangerZone = sandbox.DangerZone(true)
	return h, projID
}

// TestHandleCreateClone verifies that HandleCreate clones a repo into a
// subdirectory of the target parent and creates the project from it.
func TestHandleCreateClone(t *testing.T) {
	h, _ := testProjectHandlerGranted(t)
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "my-remote-repo")
	createBareRepo(t, remote)
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	body, _ := json.Marshal(types.CreateProjectRequest{
		Path:   workspace,
		GitURL: remote,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	h.HandleCreate(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var proj types.Project
	if err := json.Unmarshal(rr.Body.Bytes(), &proj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	expectedPath := filepath.Join(workspace, "my-remote-repo")
	if proj.Path != expectedPath {
		t.Fatalf("expected path %q, got %q", expectedPath, proj.Path)
	}
	if proj.Name != "my-remote-repo" {
		t.Fatalf("expected name %q, got %q", "my-remote-repo", proj.Name)
	}
	if !git.IsGitRepo(expectedPath) {
		t.Fatal("cloned path is not a git repository")
	}
}

// makeCommittedRepo creates a non-bare git repo with `commits` commits at
// path. The file:// form of its URL is returned: cloning a plain local path
// uses git's hardlink optimization (no pack transfer, no progress output),
// while file:// goes through the upload-pack protocol and reports progress.
func makeCommittedRepo(t *testing.T, path string, commits int) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	runGit("init")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")
	for i := 0; i < commits; i++ {
		content := fmt.Sprintf("line %d %s", i, strings.Repeat("x", 512))
		if err := os.WriteFile(filepath.Join(path, "data.txt"), []byte(content), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		runGit("add", "data.txt")
		runGit("commit", "-m", "commit")
	}
	return "file://" + path
}

// TestHandleCloneStreamsProgress verifies that HandleClone streams clone
// progress events and finishes with a done event carrying the new project.
func TestHandleCloneStreamsProgress(t *testing.T) {
	h, _ := testProjectHandlerGranted(t)
	tmp := t.TempDir()
	src := makeCommittedRepo(t, filepath.Join(tmp, "my-remote-repo"), 20)
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	body, _ := json.Marshal(types.CreateProjectRequest{Path: workspace, GitURL: src})
	req := httptest.NewRequest(http.MethodPost, "/api/projects/clone", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	h.HandleClone(rr, req)

	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	events := readSSEEvents(t, rr.Body)
	if len(events) == 0 {
		t.Fatal("no SSE events")
	}

	sawProgress := false
	var lastPct int
	var statuses []string
	for i, ev := range events {
		switch ev["type"] {
		case "progress":
			sawProgress = true
			pct, ok := ev["percent"].(float64)
			if !ok {
				t.Fatalf("event %d: percent not a number: %v", i, ev)
			}
			if pct < 0 || pct > 100 {
				t.Fatalf("event %d: percent out of range: %v", i, ev)
			}
			if int(pct) <= lastPct {
				t.Fatalf("progress not strictly increasing at event %d: %v", i, events)
			}
			lastPct = int(pct)
		case "status":
			if m, ok := ev["message"].(string); ok {
				statuses = append(statuses, m)
			}
		case "done":
		case "error":
			t.Fatalf("unexpected error event: %v", ev)
		default:
			t.Fatalf("unknown event type: %v", ev)
		}
	}
	if !sawProgress {
		t.Fatal("no progress events")
	}
	// A percentage says how far, never what for: the phase line the percentage came from has to
	// reach the reader as its own frame, or the board can only show a bar with no words.
	if !slices.Contains(statuses, "Receiving objects...") {
		t.Errorf("no status frame named the phase the clone was in (statuses: %v)", statuses)
	}
	if lastPct != 100 {
		t.Fatalf("final progress = %d, want 100", lastPct)
	}
	last := events[len(events)-1]
	if last["type"] != "done" {
		t.Fatalf("last event = %v, want done", last)
	}
	proj, ok := last["project"].(map[string]any)
	if !ok {
		t.Fatalf("done event without project: %v", last)
	}
	if proj["name"] != "my-remote-repo" {
		t.Fatalf("project name = %v", proj["name"])
	}
	wantPath := filepath.Join(workspace, "my-remote-repo")
	if proj["path"] != wantPath {
		t.Fatalf("project path = %v, want %v", proj["path"], wantPath)
	}
	if !git.IsGitRepo(wantPath) {
		t.Fatal("clone target is not a git repository")
	}
	// The project must be persisted.
	if _, err := h.store.GetProject(proj["id"].(string)); err != nil {
		t.Fatalf("project not persisted: %v", err)
	}
}

// A clone git itself refuses has to be readable, so the error frame carries git's own lines kept apart.
func TestHandleCloneFailureAnswersWithGitsOwnLines(t *testing.T) {
	h, _ := testProjectHandlerGranted(t)
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	body, _ := json.Marshal(types.CreateProjectRequest{Path: workspace, GitURL: "file://" + filepath.Join(tmp, "no-such-repo")})
	req := httptest.NewRequest(http.MethodPost, "/api/projects/clone", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	h.HandleClone(rr, req)

	events := readSSEEvents(t, rr.Body)
	last := events[len(events)-1]
	if last["type"] != "error" {
		t.Fatalf("last event = %v, want an error", last)
	}
	detail, _ := last["detail"].(string)
	if !strings.Contains(detail, "does not appear to be a git repository") {
		t.Errorf("detail = %q, want git's own reason", detail)
	}
	if !strings.Contains(detail, "\n") {
		t.Errorf("detail = %q, want git's lines kept apart", detail)
	}
	projects, err := h.store.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	for _, p := range projects {
		if strings.HasPrefix(p.Path, workspace) {
			t.Errorf("a failed clone stored a project row for %s", p.Path)
		}
	}
}

// The detail is subprocess bytes into a browser: a typed credential is not echoed back, and a verbose
// failure is bounded.
func TestCloneFailureDetailRedactsAndBoundsGitsOutput(t *testing.T) {
	out := "Cloning into '/tmp/w/x'...\n" +
		"fatal: unable to access 'https://someone:sekret@github.com/someone/x/': Authentication failed\n" +
		"some noise\n" +
		"fatal: repository 'https://github.com/someone/x/' not found\n"
	detail := cloneFailureDetail(out)
	if strings.Contains(detail, "sekret") {
		t.Errorf("the credential in the address was echoed back: %q", detail)
	}
	if !strings.Contains(detail, "github.com/someone/x") {
		t.Errorf("the address itself did not survive: %q", detail)
	}
	if !strings.HasSuffix(detail, "not found") {
		t.Errorf("the line that explains the failure did not survive: %q", detail)
	}

	long := strings.Repeat("remote: Counting objects endlessly\n", 4000)
	if got := cloneFailureDetail(long); len(got) > subproc.AnswerTailMax+len("…") {
		t.Errorf("detail = %d bytes, want at most %d", len(got), subproc.AnswerTailMax)
	}

	if got := cloneFailureDetail(""); got != "" {
		t.Errorf("detail of nothing = %q, want nothing", got)
	}
}

// A clone that cannot run answers with one terminal error event naming the cause, and leaves no row
// behind: the board must never hold a project whose files are not there.
func TestHandleCloneRefusesWithOneTerminalErrorNamingTheCause(t *testing.T) {
	cases := []struct {
		name string
		// setup puts the workspace in the state the refusal is about and returns the URL to clone.
		setup func(t *testing.T, tmp, workspace string) string
		want  string // what the error event says
	}{
		{
			name: "the target directory is already there",
			setup: func(t *testing.T, tmp, workspace string) string {
				src := makeCommittedRepo(t, filepath.Join(tmp, "my-remote-repo"), 3)
				if err := os.MkdirAll(filepath.Join(workspace, "my-remote-repo"), 0o755); err != nil {
					t.Fatal(err)
				}
				return src
			},
			want: "already exists",
		},
		{
			name:  "the URL is a git flag",
			setup: func(t *testing.T, tmp, workspace string) string { return "-evil-flag" },
			want:  "dash",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := testProjectHandlerGranted(t)
			tmp := t.TempDir()
			workspace := filepath.Join(tmp, "workspace")
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			url := tt.setup(t, tmp, workspace)

			body, _ := json.Marshal(types.CreateProjectRequest{Path: workspace, GitURL: url})
			req := httptest.NewRequest(http.MethodPost, "/api/projects/clone", strings.NewReader(string(body)))
			rr := httptest.NewRecorder()
			h.HandleClone(rr, req)

			events := readSSEEvents(t, rr.Body)
			if len(events) != 1 || events[0]["type"] != "error" {
				t.Fatalf("events = %v, want one terminal error event", events)
			}
			msg, _ := events[0]["message"].(string)
			if !strings.Contains(msg, tt.want) {
				t.Errorf("message = %q, want it to say %q", msg, tt.want)
			}
			projects, err := h.store.ListProjects()
			if err != nil {
				t.Fatalf("list projects: %v", err)
			}
			for _, p := range projects {
				if strings.HasPrefix(p.Path, workspace) {
					t.Errorf("a refused clone stored a project row for %s", p.Path)
				}
			}
		})
	}
}

// floatApprox checks if two floats are approximately equal within a tolerance.
func floatApprox(t *testing.T, got, want, tol float64) {
	t.Helper()
	if got < want-tol || got > want+tol {
		t.Fatalf("float not approximately equal: got %.6f, want %.6f (tol %.6f)", got, want, tol)
	}
}

// testProjectHandler creates a ProjectHandler with a temporary database for testing.
func testProjectHandler(t *testing.T) (*ProjectHandler, string) {
	t.Helper()
	h, projID, _ := testProjectHandlerWithStore(t)
	return h, projID
}

// testProjectHandlerWithStore also returns the store, for tests that seed tasks and
// messages next to the project.
func testProjectHandlerWithStore(t *testing.T) (*ProjectHandler, string, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	worktreesDir := filepath.Join(dir, "worktrees")
	os.MkdirAll(worktreesDir, 0o755)

	s := teststore.StoreIn(t, dbPath)

	cfgStore := teststore.ConfigStore(t, s, dbPath)

	h := &ProjectHandler{
		store:       s,
		configStore: cfgStore,
		// The fixtures are t.TempDir() directories under the system temp dir, so declare
		// it as a root the way a real install declares its workspace dir.
		hostRoot: fsutil.NewHostRoot(true, os.TempDir()),
	}

	// Create a test project
	proj := &types.Project{
		ID:   "test-proj",
		Name: "test",
		Path: dir,
	}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	return h, "test-proj", s
}

// createTestTaskWithMessages creates a task with assistant messages for stats testing.
func createTestTaskWithMessages(t *testing.T, s *store.Store, projID, taskID, modelName, providerName string,
	promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs int,
) {
	t.Helper()
	task := &types.Task{
		ID:        taskID,
		ProjectID: projID,
		Title:     "test task",
		Status:    types.StatusBacklog,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	msg := &types.TaskMessage{
		ID:              taskID + "-msg-1",
		TaskID:          taskID,
		Role:            "assistant",
		Content:         "test response",
		ModelName:       modelName,
		ProviderName:    providerName,
		PromptTokens:    promptTokens,
		PredictedTokens: predictedTokens,
		CacheTokens:     cacheTokens,
		PromptMs:        promptMs,
		PredictedMs:     predictedMs,
		CreatedAt:       time.Now(),
	}
	if err := s.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}
}

// doSandboxBuildRequest sends a POST request through a mux serving
// HandleSandboxBuild and returns the response recorder.
func doSandboxBuildRequest(t *testing.T, mux *http.ServeMux, projectID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/sandbox/build", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// TestHandleSandboxBuildNoDockerfilePath verifies the build endpoint rejects
// builds when no Dockerfile path is configured. The frontend must persist the
// selected Dockerfile path before triggering a build; otherwise the user sees
// this exact error.
func TestHandleSandboxBuildNoDockerfilePath(t *testing.T) {
	h, projID := testProjectHandler(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects/{projectID}/sandbox/build", h.HandleSandboxBuild)

	rr := doSandboxBuildRequest(t, mux, projID)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rr.Body.String(), "no Dockerfile path configured for this project") {
		t.Fatalf("body = %q, want error message", rr.Body.String())
	}
}

// recordingSandbox answers the project handler's sandbox calls with the batches it was handed, and a build with
// the result or the failure the test put in it.
type recordingSandbox struct {
	stoppedProjects []string
	result          *sandbox.BuildResult
	err             error
	// watching says whether a preparation is in flight. watch is the queue a viewer is handed.
	watching bool
	watch    chan types.SandboxImageStatus
	// cancelled is what a cancel click is answered with, and cancelCalls counts the clicks.
	cancelled   bool
	cancelCalls int
}

func (r *recordingSandbox) BuildProjectImage(context.Context, *types.Project, func(string)) (*sandbox.BuildResult, error) {
	return r.result, r.err
}

func (r *recordingSandbox) WatchImagePreparation(context.Context, *types.Project) (<-chan types.SandboxImageStatus, bool) {
	if !r.watching {
		return nil, false
	}
	return r.watch, true
}

func (r *recordingSandbox) CancelImagePreparation(*types.Project) bool {
	r.cancelCalls++
	return r.cancelled
}

func (r *recordingSandbox) StopProjectContainers(projectID string, _ []string) {
	r.stoppedProjects = append(r.stoppedProjects, projectID)
}

// sseDataFrames is the payload of every `data:` frame of an SSE body, in the order they were written.
func sseDataFrames(body string) []string {
	var frames []string
	for _, line := range strings.Split(body, "\n") {
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			frames = append(frames, strings.TrimPrefix(value, " "))
		}
	}
	return frames
}

// readSSEEvents parses the "data: <json>" frames of an SSE response body.
func readSSEEvents(t *testing.T, body io.Reader) []map[string]any {
	t.Helper()
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read SSE: %v", err)
	}
	var events []map[string]any
	for _, frame := range sseDataFrames(string(raw)) {
		var ev map[string]any
		if err := json.Unmarshal([]byte(frame), &ev); err != nil {
			t.Fatalf("bad SSE frame %q: %v", frame, err)
		}
		events = append(events, ev)
	}
	return events
}

// The build stream is plain lines with one JSON frame at the end, and the reader can only end its
// wait by recognizing that frame. Both endings are asserted here: a build that succeeded must be
// told by a frame the reader can pick out (flattened, carrying the build's own fields), and a build
// that failed must be told by one `error: ` line with no JSON frame after it — a stream that ends
// without either reads as one that never answered, which is how a person is shown "Build completed
// without result" over an image that exists.
func TestSandboxBuildStreamEndsEitherWithAFrameOrWithAnErrorLine(t *testing.T) {
	for _, tt := range []struct {
		name string
		// outcome is what the sandbox answers the build with.
		outcome func() (*sandbox.BuildResult, error)
		// check reads the stream's last frame.
		check func(*testing.T, string, []string)
	}{
		{
			name: "a build that produced an image",
			outcome: func() (*sandbox.BuildResult, error) {
				return &sandbox.BuildResult{Image: "localhost/mike-test:abc", Commit: "abc", Files: 2}, nil
			},
			check: func(t *testing.T, last string, frames []string) {
				var result map[string]any
				if err := json.Unmarshal([]byte(last), &result); err != nil {
					t.Fatalf("last frame %q is not the JSON result: %v", last, err)
				}
				if status, ok := result["status"].(string); !ok || status == "" {
					t.Fatalf("result frame %q carries no status string for the reader to pick it out by", last)
				}
				// The result is flattened, not nested: the reader's type is the build's own field list.
				if result["image"] != "localhost/mike-test:abc" || result["files"] != float64(2) {
					t.Fatalf("result frame %q does not carry the build's own fields flattened", last)
				}
			},
		},
		{
			name: "a build that failed",
			outcome: func() (*sandbox.BuildResult, error) {
				return nil, errors.New("build docker.io/library/node:22: exit status 125")
			},
			check: func(t *testing.T, last string, frames []string) {
				if !strings.HasPrefix(last, "error: ") {
					t.Fatalf("last frame %q is not the error line the reader is shown", last)
				}
				for _, frame := range frames {
					if strings.HasPrefix(frame, "{") {
						t.Fatalf("a failed build wrote a result frame: %q", frame)
					}
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, projID, s := testProjectHandlerWithStore(t)
			if err := s.UpdateProjectSandboxDockerfilePath(projID, "Dockerfile"); err != nil {
				t.Fatal(err)
			}
			result, buildErr := tt.outcome()
			h.sandboxMgr = &recordingSandbox{result: result, err: buildErr}

			mux := http.NewServeMux()
			mux.HandleFunc("/api/projects/{projectID}/sandbox/build", h.HandleSandboxBuild)
			rr := doSandboxBuildRequest(t, mux, projID)

			frames := sseDataFrames(rr.Body.String())
			if len(frames) == 0 {
				t.Fatalf("stream wrote no frame: %q", rr.Body.String())
			}
			tt.check(t, frames[len(frames)-1], frames)
		})
	}
}

// TestRejectBuildQuery checks the build route's one control: a key it cannot honour is refused by name, and an
// unrelated parameter is left alone.
func TestRejectBuildQuery(t *testing.T) {
	// An absent query, and an unrelated parameter, are nobody's business.
	for _, query := range []string{"", "other=1"} {
		if err := rejectBuildQuery(buildRequestWithQuery(t, query)); err != nil {
			t.Errorf("query %q refused: %v", query, err)
		}
	}

	// There is no build cache to skip, so the key is refused by name — by presence, not by value: a request that
	// sent it asked for something.
	for _, gone := range []string{"no_cache=", "no_cache=1", "no_cache=maybe"} {
		err := rejectBuildQuery(buildRequestWithQuery(t, gone))
		if err == nil || !strings.Contains(err.Error(), "no_cache") {
			t.Errorf("%q: err = %v, want a refusal naming no_cache", gone, err)
		}
	}
}

// buildRequestWithQuery is the build route with a query string, which every option test reads.
func buildRequestWithQuery(t *testing.T, query string) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodPost, "/api/projects/p1/sandbox/build?"+query, nil)
}

// A sandbox image that names no registry ("ubuntu:24.04") breaks every later sandbox start, and a
// rejected write must not half-update the row, so both write paths answer the same way: 400 naming the
// rule, and the row left holding what it had.
func TestProjectWritesRefuseAnUnqualifiedSandboxImage(t *testing.T) {
	isolation := false
	const usable = "docker.io/library/ubuntu:24.04"

	cases := []struct {
		name   string
		seeded string // what the row holds before the request
		wantOK int    // the status a usable ref earns on this path
		write  func(t *testing.T, image string) (int, string, string)
	}{
		{
			name:   "create",
			seeded: "",
			wantOK: http.StatusCreated,
			write: func(t *testing.T, image string) (int, string, string) {
				h, _ := testProjectHandler(t)
				repo := t.TempDir()
				createBareRepo(t, repo)
				proj, code, msg := h.createProjectFromRequest(types.CreateProjectRequest{
					Path:                    repo,
					Name:                    "img-proj",
					SandboxEnabled:          true,
					SandboxNetworkIsolation: &isolation,
					SandboxImage:            image,
				})
				stored := ""
				if proj != nil {
					stored = proj.SandboxImage
				}
				return code, msg, stored
			},
		},
		{
			name:   "update",
			seeded: usable,
			wantOK: http.StatusOK,
			write: func(t *testing.T, image string) (int, string, string) {
				h, projID := testProjectHandler(t)
				if err := h.store.UpdateProjectSandboxImage(projID, usable); err != nil {
					t.Fatalf("seed image: %v", err)
				}
				mux := http.NewServeMux()
				mux.HandleFunc("/api/projects/{projectID}", h.HandleByID)
				body, err := json.Marshal(map[string]string{"sandbox_image": image})
				if err != nil {
					t.Fatal(err)
				}
				rr := httptest.NewRecorder()
				mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/api/projects/"+projID, strings.NewReader(string(body))))
				p, err := h.store.GetProject(projID)
				if err != nil {
					t.Fatalf("read the row back: %v", err)
				}
				return rr.Code, rr.Body.String(), p.SandboxImage
			},
		},
	}

	refs := []struct {
		image string
		ok    bool
	}{
		{"ubuntu:24.04", false},
		{"alpine/git", false},
		{"sandbox-getmike.dev", false},
		{usable, true},
		{"quay.io/o/r:1", true},
	}
	for _, tc := range cases {
		for _, ref := range refs {
			t.Run(tc.name+"/"+ref.image, func(t *testing.T) {
				code, msg, stored := tc.write(t, ref.image)
				if ref.ok {
					if code != tc.wantOK {
						t.Fatalf("usable ref %q: status = %d (%s), want %d", ref.image, code, msg, tc.wantOK)
					}
					if stored != ref.image {
						t.Errorf("stored image = %q, want the configured ref %q", stored, ref.image)
					}
					return
				}
				if code != http.StatusBadRequest {
					t.Fatalf("unusable ref %q: status = %d (%s), want 400", ref.image, code, msg)
				}
				if !strings.Contains(msg, "fully qualified") {
					t.Errorf("message = %q, want it to say the image must be fully qualified", msg)
				}
				if stored != tc.seeded {
					t.Errorf("a refused %q left the row holding %q, want %q", ref.image, stored, tc.seeded)
				}
			})
		}
	}
}

// A forwarded port's bind address is checked for shape and canonicalized by the one validator both write
// paths call: 0.0.0.0 is stored as "", because that is what "every interface" means everywhere else, and a
// refused value leaves the row exactly as it was.
func TestProjectWritesValidateAndCanonicalizeForwardPorts(t *testing.T) {
	isolation := false
	seededPorts := []types.PortForward{{ContainerPort: 3000}}

	cases := []struct {
		name   string
		seeded []types.PortForward
		wantOK int
		write  func(t *testing.T, ports []types.PortForward) (int, string, []types.PortForward)
	}{
		{
			name:   "create",
			seeded: nil,
			wantOK: http.StatusCreated,
			write: func(t *testing.T, ports []types.PortForward) (int, string, []types.PortForward) {
				h, _ := testProjectHandler(t)
				repo := t.TempDir()
				createBareRepo(t, repo)
				proj, code, msg := h.createProjectFromRequest(types.CreateProjectRequest{
					Path:                    repo,
					Name:                    "fwd-proj",
					SandboxEnabled:          true,
					SandboxNetworkIsolation: &isolation,
					SandboxForwardPorts:     ports,
				})
				if proj == nil {
					return code, msg, nil
				}
				return code, msg, proj.ForwardPorts
			},
		},
		{
			name:   "update",
			seeded: seededPorts,
			wantOK: http.StatusOK,
			write: func(t *testing.T, ports []types.PortForward) (int, string, []types.PortForward) {
				h, projID := testProjectHandler(t)
				if err := h.store.UpdateProjectSandboxForwardPorts(projID, seededPorts); err != nil {
					t.Fatalf("seed forward ports: %v", err)
				}
				mux := http.NewServeMux()
				mux.HandleFunc("/api/projects/{projectID}", h.HandleByID)
				body, err := json.Marshal(map[string]any{"sandbox_forward_ports": ports})
				if err != nil {
					t.Fatal(err)
				}
				rr := httptest.NewRecorder()
				mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/api/projects/"+projID, strings.NewReader(string(body))))
				p, err := h.store.GetProject(projID)
				if err != nil {
					t.Fatalf("read the row back: %v", err)
				}
				return rr.Code, rr.Body.String(), p.ForwardPorts
			},
		},
	}

	ports := []struct {
		name string
		send []types.PortForward
		// want is nil for a value the validator must refuse.
		want []types.PortForward
	}{
		{"container port only", []types.PortForward{{ContainerPort: 3000}}, []types.PortForward{{ContainerPort: 3000}}},
		{"explicit wildcard stored as empty", []types.PortForward{{ContainerPort: 3000, HostIP: "0.0.0.0"}}, []types.PortForward{{ContainerPort: 3000}}},
		{"IPv6 wildcard stored as empty", []types.PortForward{{ContainerPort: 3000, HostIP: "::"}}, []types.PortForward{{ContainerPort: 3000}}},
		{"LAN address kept", []types.PortForward{{ContainerPort: 80, HostIP: "192.168.1.100"}}, []types.PortForward{{ContainerPort: 80, HostIP: "192.168.1.100"}}},
		{"loopback is the user's choice", []types.PortForward{{ContainerPort: 3000, HostIP: "127.0.0.1"}}, []types.PortForward{{ContainerPort: 3000, HostIP: "127.0.0.1"}}},
		{"a second port asking for every interface", []types.PortForward{{ContainerPort: 8080, HostIP: "192.168.1.100"}, {ContainerPort: 9000, HostIP: "0.0.0.0"}}, []types.PortForward{{ContainerPort: 8080, HostIP: "192.168.1.100"}, {ContainerPort: 9000}}},
		{"a hostname is not a bind address", []types.PortForward{{ContainerPort: 3000, HostIP: "saturn.lan"}}, nil},
		{"a port inside the address", []types.PortForward{{ContainerPort: 3000, HostIP: "192.168.1.100:8080"}}, nil},
		{"no container port", []types.PortForward{{ContainerPort: 0}}, nil},
		{"a port above the range", []types.PortForward{{ContainerPort: 70000}}, nil},
	}
	for _, tc := range cases {
		for _, p := range ports {
			t.Run(tc.name+"/"+p.name, func(t *testing.T) {
				code, msg, stored := tc.write(t, p.send)
				shown := func(ports []types.PortForward) string { return fmt.Sprintf("%+v", ports) }
				if p.want == nil {
					if code != http.StatusBadRequest {
						t.Fatalf("%s: status = %d (%s), want 400", p.name, code, msg)
					}
					if shown(stored) != shown(tc.seeded) {
						t.Errorf("a refused %s wrote %s, want the row left as %s", p.name, shown(stored), shown(tc.seeded))
					}
					return
				}
				if code != tc.wantOK {
					t.Fatalf("%s: status = %d (%s), want %d", p.name, code, msg, tc.wantOK)
				}
				if shown(stored) != shown(p.want) {
					t.Errorf("stored ports = %s, want %s", shown(stored), shown(p.want))
				}
			})
		}
	}
}

// pricingMessage is one assistant message the stats aggregate over.
type pricingMessage struct {
	taskID, model, provider                    string
	promptTokens, predictedTokens, cacheTokens int
	promptMs, predictedMs                      int
}

// pricingModelWant is one model row's expected answer.
type pricingModelWant struct {
	cost     float64
	powerKwh float64
}

// TestApplyPricingPricesEachModelAgainstItsProvider is the pricing table: a message is priced by the
// provider row its message names, that provider's model row overrides it field by field, an agent
// provider prices nothing, and the project totals are the sum of the per-model rows rather than one
// model's answer read twice. The wire keys are asserted alongside the numbers because the stats panel
// reads the cost and the kWh out of them.
func TestApplyPricingPricesEachModelAgainstItsProvider(t *testing.T) {
	// kwh converts the cumulative generation time a message records into the energy the provider's
	// wattage drew while it ran.
	kwh := func(watts float64, ms int) float64 { return watts * float64(ms) / 3_600_000_000 }

	// msg is the one message shape every case uses: 900 uncached input, 100 cache-read and 500
	// generated tokens over 5s of generation.
	msg := func(taskID, model, provider string) pricingMessage {
		return pricingMessage{
			taskID: taskID, model: model, provider: provider,
			promptTokens: 1000, predictedTokens: 500, cacheTokens: 100,
			promptMs: 1000, predictedMs: 5000,
		}
	}

	apiPrices := func(id int) types.Provider {
		return types.Provider{
			ID: id, Name: "prov-x", DisplayName: "Provider X",
			URL:        "http://localhost:8080/v1",
			InputPrice: 3.0, OutputPrice: 5.0, CacheReadPrice: 1.0,
		}
	}
	withPower := func(p types.Provider, watts, perKwh float64) types.Provider {
		p.PowerConsumptionW = watts
		p.ElectricityCostPerKwh = perKwh
		return p
	}

	for _, tt := range []struct {
		name      string
		messages  []pricingMessage
		providers []types.Provider
		// wantModels is the per-model answer; wantCost/wantKwh the project totals.
		wantModels    map[string]pricingModelWant
		wantTotalCost float64
		wantTotalKwh  float64
	}{
		{
			name:      "a provider prices each token family it quotes",
			messages:  []pricingMessage{msg("task-1", "model-a", "1")},
			providers: []types.Provider{apiPrices(1)},
			// (900 * 3 + 100 * 1 + 500 * 5) / 1M
			wantModels:    map[string]pricingModelWant{"model-a": {cost: 0.0053}},
			wantTotalCost: 0.0053,
		},
		{
			name:          "generation time at a wattage costs electricity",
			messages:      []pricingMessage{msg("task-1", "model-a", "1")},
			providers:     []types.Provider{withPower(apiPrices(1), 500.0, 0.12)},
			wantModels:    map[string]pricingModelWant{"model-a": {cost: 0.0053 + kwh(500.0, 5000)*0.12, powerKwh: kwh(500.0, 5000)}},
			wantTotalCost: 0.0053 + kwh(500.0, 5000)*0.12,
			wantTotalKwh:  kwh(500.0, 5000),
		},
		{
			name: "a provider with power but no electricity price records kWh and adds nothing",
			messages: []pricingMessage{
				msg("task-1", "model-a", "1"),
				// A second shape, so a total that read one model twice cannot pass.
				{taskID: "task-2", model: "model-b", provider: "2",
					promptTokens: 2000, predictedTokens: 300, cacheTokens: 200,
					promptMs: 2000, predictedMs: 3000},
			},
			providers: []types.Provider{
				withPower(apiPrices(1), 500.0, 0),
				{ID: 2, Name: "prov-y", DisplayName: "Provider Y", URL: "http://localhost:9090/v1",
					InputPrice: 1.0, OutputPrice: 2.0},
			},
			wantModels: map[string]pricingModelWant{
				"model-a": {cost: 0.0053, powerKwh: kwh(500.0, 5000)},
				// prov-y quotes no cache read, so the cached tokens are free.
				// prov-y quotes no cache read: (1800 * 1 + 200 * 0 + 300 * 2) / 1M
				"model-b": {cost: 0.0024},
			},
			wantTotalCost: 0.0053 + 0.0024,
			wantTotalKwh:  kwh(500.0, 5000),
		},
		{
			name: "an agent provider prices nothing it is asked to price",
			// The ID matters: a provider is matched by its id, and one without an id is skipped
			// before the agent kind is ever read.
			messages: []pricingMessage{msg("task-1", "agent-model", "1")},
			providers: []types.Provider{withPower(types.Provider{
				ID: 1, Name: "my-agent", DisplayName: "My Agent", Kind: "agent",
				URL: "agent://my-agent",
			}, 500.0, 0.12)},
			wantModels:    map[string]pricingModelWant{"agent-model": {}},
			wantTotalCost: 0,
			wantTotalKwh:  0,
		},
		{
			name:     "a model row overrides its provider's prices",
			messages: []pricingMessage{msg("task-1", "model-a", "1")},
			providers: func() []types.Provider {
				p := withPower(apiPrices(1), 500.0, 0)
				inputP, outputP, cacheReadP := 6.0, 9.0, 2.0
				p.Models = []types.ModelConfig{{Name: "model-a", InputPrice: &inputP, OutputPrice: &outputP, CacheReadPrice: &cacheReadP}}
				return []types.Provider{p}
			}(),
			// (900 * 6 + 100 * 2 + 500 * 9) / 1M, and the provider's power still applies.
			wantModels:    map[string]pricingModelWant{"model-a": {cost: 0.0101, powerKwh: kwh(500.0, 5000)}},
			wantTotalCost: 0.0101,
			wantTotalKwh:  kwh(500.0, 5000),
		},
		{
			name:     "a model row overrides its provider's wattage",
			messages: []pricingMessage{msg("task-1", "model-a", "1")},
			providers: func() []types.Provider {
				p := withPower(apiPrices(1), 500.0, 0.12)
				modelPower := 1000.0
				p.Models = []types.ModelConfig{{Name: "model-a", PowerConsumptionW: &modelPower}}
				return []types.Provider{p}
			}(),
			wantModels:    map[string]pricingModelWant{"model-a": {cost: 0.0053 + kwh(1000.0, 5000)*0.12, powerKwh: kwh(1000.0, 5000)}},
			wantTotalCost: 0.0053 + kwh(1000.0, 5000)*0.12,
			wantTotalKwh:  kwh(1000.0, 5000),
		},
		{
			name:     "a model row that sets only cache_write_price leaves the rest to the provider",
			messages: []pricingMessage{msg("task-1", "model-a", "1")},
			providers: func() []types.Provider {
				p := withPower(apiPrices(1), 500.0, 0)
				cacheWriteP := 2.0
				p.Models = []types.ModelConfig{{Name: "model-a", CacheWritePrice: &cacheWriteP}}
				return []types.Provider{p}
			}(),
			// cache write replaces the input rate: (900 * 2 + 100 * 1 + 500 * 5) / 1M
			wantModels:    map[string]pricingModelWant{"model-a": {cost: 0.0044, powerKwh: kwh(500.0, 5000)}},
			wantTotalCost: 0.0044,
			wantTotalKwh:  kwh(500.0, 5000),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, projID, s := testProjectHandlerWithStore(t)
			for _, m := range tt.messages {
				createTestTaskWithMessages(t, s, projID, m.taskID, m.model, m.provider,
					m.promptTokens, m.predictedTokens, m.cacheTokens, m.promptMs, m.predictedMs)
			}
			cfg, err := h.configStore.GetConfig()
			if err != nil {
				t.Fatalf("read config: %v", err)
			}
			cfg.Providers = tt.providers
			h.configStore.UpdateConfig(cfg)

			stats, err := h.store.GetProjectStats(projID)
			if err != nil {
				t.Fatalf("get stats: %v", err)
			}
			h.applyPricing(stats)

			if len(stats.ByModel) != len(tt.wantModels) {
				t.Fatalf("%d model rows, want %d: %+v", len(stats.ByModel), len(tt.wantModels), stats.ByModel)
			}
			for _, m := range stats.ByModel {
				want, ok := tt.wantModels[m.ModelName]
				if !ok {
					t.Fatalf("stats answered a row for %q, which this case did not write", m.ModelName)
				}
				floatApprox(t, m.Cost, want.cost, 0.0001)
				floatApprox(t, m.PowerKwh, want.powerKwh, 0.000001)
			}
			floatApprox(t, stats.TotalCost, tt.wantTotalCost, 0.0001)
			floatApprox(t, stats.TotalPowerKwh, tt.wantTotalKwh, 0.000001)

			// The stats panel reads these two keys out of the answer.
			data, err := json.Marshal(stats)
			if err != nil {
				t.Fatalf("marshal stats: %v", err)
			}
			for _, key := range []string{`"power_kwh"`, `"total_power_kwh"`, `"cost"`} {
				if !strings.Contains(string(data), key) {
					t.Errorf("stats JSON carries no %s: %s", key, data)
				}
			}
			var round types.ProjectStats
			if err := json.Unmarshal(data, &round); err != nil {
				t.Fatalf("unmarshal stats: %v", err)
			}
			floatApprox(t, round.TotalCost, tt.wantTotalCost, 0.0001)
			floatApprox(t, round.TotalPowerKwh, tt.wantTotalKwh, 0.000001)
		})
	}
}

// TestHandleCreateCloneRejectsHelperTransport verifies the end-to-end behaviour:
// an `ext::` URL is refused with 400 and the payload never executes (no canary
// file), even though git itself would honour it on a host that enables
// protocol.ext.allow.
func TestHandleCreateCloneRejectsHelperTransport(t *testing.T) {
	h, _ := testProjectHandlerGranted(t)
	tmp := t.TempDir()
	canary := filepath.Join(tmp, "pwned")
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	body, _ := json.Marshal(types.CreateProjectRequest{
		Path:   workspace,
		GitURL: "ext::sh -c 'touch " + canary + "'",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	h.HandleCreate(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(canary); err == nil {
		t.Fatal("ext:: payload executed — the clone source was not validated")
	}
}

// putProject sends a partial-update body to the project route and answers what the handler did. The
// body is raw JSON so a test can name a field the request type has not been given yet.
func putProject(t *testing.T, h *ProjectHandler, projID, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/projects/{projectID}", h.HandleByID)
	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+projID, strings.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// TestUpdateProjectCommitTrailer: commit_assisted_by is written both ways, and a request that does not
// mention it leaves the stored answer alone.
func TestUpdateProjectCommitTrailer(t *testing.T) {
	h, projID := testProjectHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/projects/{projectID}", h.HandleByID)

	put := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/api/projects/"+projID, strings.NewReader(body))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr
	}
	stored := func() *bool {
		t.Helper()
		p, err := h.store.GetProject(projID)
		if err != nil {
			t.Fatalf("get project: %v", err)
		}
		return p.CommitAssistedBy
	}

	for _, want := range []bool{false, true} {
		rr := put(fmt.Sprintf(`{"commit_assisted_by":%t}`, want))
		if rr.Code != http.StatusOK {
			t.Fatalf("PUT %t: status = %d, want 200 (%s)", want, rr.Code, rr.Body.String())
		}
		var answered types.Project
		if err := json.Unmarshal(rr.Body.Bytes(), &answered); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if answered.CommitAssistedBy == nil || *answered.CommitAssistedBy != want {
			t.Errorf("PUT %t: response carries %v", want, answered.CommitAssistedBy)
		}
		if got := stored(); got == nil || *got != want {
			t.Errorf("PUT %t: stored %v", want, got)
		}
	}

	if rr := put(`{"name":"Renamed"}`); rr.Code != http.StatusOK {
		t.Fatalf("PUT name: status = %d (%s)", rr.Code, rr.Body.String())
	}
	if got := stored(); got == nil || !*got {
		t.Errorf("an update that never mentioned the trailer changed it to %v", got)
	}
}

// doSandboxWatchRequest asks for the build this project already has running.
func doSandboxWatchRequest(t *testing.T, mux *http.ServeMux, projectID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/sandbox/build", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func sandboxWatchMux(h *ProjectHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects/{projectID}/sandbox/build", h.HandleSandboxBuildWatch)
	mux.HandleFunc("/api/projects/{projectID}/sandbox/build/cancel", h.HandleSandboxBuildCancel)
	return mux
}

// TestHandleSandboxBuildWatchAnswersThePreparationTheProjectHolds: the sandbox dialog asks this before it
// draws anything, so the route answers whichever preparation the project holds — an idle project gets its
// own message rather than a fault the screen would show, and a running build gets its statuses in the
// order the preparation produced them. The screen reads its state from the phase and its log from the lines.
func TestHandleSandboxBuildWatchAnswersThePreparationTheProjectHolds(t *testing.T) {
	statuses := make(chan types.SandboxImageStatus, 3)
	statuses <- types.SandboxImageStatus{Phase: types.SandboxImagePhaseBuilding, Image: "localhost/sandbox-p1"}
	statuses <- types.SandboxImageStatus{Phase: types.SandboxImagePhaseBuilding, Line: "step 1/3"}
	statuses <- types.SandboxImageStatus{Phase: types.SandboxImagePhaseDone, Image: "localhost/sandbox-p1"}
	close(statuses)

	cases := []struct {
		name    string
		sandbox *recordingSandbox
		want    int
		check   func(t *testing.T, rr *httptest.ResponseRecorder)
	}{
		{
			name:    "an idle project",
			sandbox: &recordingSandbox{},
			want:    http.StatusNotFound,
			check: func(t *testing.T, rr *httptest.ResponseRecorder) {
				if !strings.Contains(rr.Body.String(), "no sandbox image build is running") {
					t.Errorf("body = %q, want the idle answer", rr.Body.String())
				}
			},
		},
		{
			name:    "a build in flight",
			sandbox: &recordingSandbox{watching: true, watch: statuses},
			want:    http.StatusOK,
			check: func(t *testing.T, rr *httptest.ResponseRecorder) {
				events := readSSEEvents(t, rr.Body)
				if len(events) != 3 {
					t.Fatalf("frames = %v, want the phase, the line and the ending", events)
				}
				if events[0]["phase"] != types.SandboxImagePhaseBuilding || events[0]["image"] != "localhost/sandbox-p1" {
					t.Errorf("first frame = %v, want the phase and the image it is for", events[0])
				}
				if events[1]["line"] != "step 1/3" {
					t.Errorf("second frame = %v, want the build's own line", events[1])
				}
				if events[2]["phase"] != types.SandboxImagePhaseDone {
					t.Errorf("last frame = %v, want %q", events[2], types.SandboxImagePhaseDone)
				}
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			h, projID := testProjectHandler(t)
			h.sandboxMgr = tt.sandbox

			rr := doSandboxWatchRequest(t, sandboxWatchMux(h), projID)
			if rr.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tt.want, rr.Body.String())
			}
			tt.check(t, rr)
		})
	}
}

// TestHandleSandboxBuildCancelAnswersWhetherItStoppedSomething: a cancel that found no build running asked for
// something that was already true, so it answers instead of failing, and a cancel that stopped a build says so
// once per click.
func TestHandleSandboxBuildCancelAnswersWhetherItStoppedSomething(t *testing.T) {
	for _, tt := range []struct {
		name  string
		stops bool
		want  bool
	}{{"a build in flight", true, true}, {"an idle project", false, false}} {
		t.Run(tt.name, func(t *testing.T) {
			h, projID := testProjectHandler(t)
			fake := &recordingSandbox{cancelled: tt.stops}
			h.sandboxMgr = fake

			req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projID+"/sandbox/build/cancel", nil)
			rr := httptest.NewRecorder()
			sandboxWatchMux(h).ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			var frame buildCancelFrame
			if err := json.Unmarshal(rr.Body.Bytes(), &frame); err != nil {
				t.Fatalf("body = %q, want the cancelled frame: %v", rr.Body.String(), err)
			}
			if frame.Cancelled != tt.want {
				t.Errorf("cancelled = %v, want %v", frame.Cancelled, tt.want)
			}
			if fake.cancelCalls != 1 {
				t.Errorf("the sandbox was asked to cancel %d times, want 1", fake.cancelCalls)
			}
		})
	}
}
