package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/config"
	"kanban/backend/fsutil"
	"kanban/backend/git"
	"kanban/backend/store"
	"kanban/backend/types"

	"kanban/backend/sandbox"
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

// readSSEEvents parses the "data: <json>" frames of an SSE response body.
func readSSEEvents(t *testing.T, body io.Reader) []map[string]any {
	t.Helper()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var events []map[string]any
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line[len("data: "):]), &ev); err != nil {
			t.Fatalf("bad SSE frame %q: %v", line, err)
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read SSE: %v", err)
	}
	return events
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
		case "status", "done":
		case "error":
			t.Fatalf("unexpected error event: %v", ev)
		default:
			t.Fatalf("unknown event type: %v", ev)
		}
	}
	if !sawProgress {
		t.Fatal("no progress events")
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

// TestHandleCloneTargetExists verifies the failure path: a pre-existing
// target dir yields a terminal error event and no project.
func TestHandleCloneTargetExists(t *testing.T) {
	h, _ := testProjectHandlerGranted(t)
	tmp := t.TempDir()
	src := makeCommittedRepo(t, filepath.Join(tmp, "my-remote-repo"), 3)
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "my-remote-repo"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	body, _ := json.Marshal(types.CreateProjectRequest{Path: workspace, GitURL: src})
	req := httptest.NewRequest(http.MethodPost, "/api/projects/clone", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	h.HandleClone(rr, req)

	events := readSSEEvents(t, rr.Body)
	if len(events) != 1 {
		t.Fatalf("expected a single error event, got %d: %v", len(events), events)
	}
	if events[0]["type"] != "error" {
		t.Fatalf("event = %v, want error", events[0])
	}
	if !strings.Contains(events[0]["message"].(string), "already exists") {
		t.Fatalf("message = %v, want 'already exists'", events[0]["message"])
	}
	projects, _ := h.store.ListProjects()
	for _, p := range projects {
		if p.Name == "my-remote-repo" {
			t.Fatalf("project created despite failed clone: %v", p)
		}
	}
}

// TestHandleCloneBadURL verifies that an invalid git URL fails fast with an
// error event (no clone attempted).
func TestHandleCloneBadURL(t *testing.T) {
	h, _ := testProjectHandlerGranted(t)
	tmp := t.TempDir()

	body, _ := json.Marshal(types.CreateProjectRequest{Path: tmp, GitURL: "-evil-flag"})
	req := httptest.NewRequest(http.MethodPost, "/api/projects/clone", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	h.HandleClone(rr, req)

	events := readSSEEvents(t, rr.Body)
	if len(events) != 1 || events[0]["type"] != "error" {
		t.Fatalf("events = %v, want single error", events)
	}
	if !strings.Contains(events[0]["message"].(string), "dash") {
		t.Fatalf("message = %v", events[0]["message"])
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

	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	cfgStore := config.NewConfigStore(s, dbPath, "test")

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

// TestHandleSandboxBuildRequiresSandboxManager verifies that once a Dockerfile
// path is configured, the build proceeds to require a sandbox manager.
func TestHandleSandboxBuildRequiresSandboxManager(t *testing.T) {
	h, projID := testProjectHandler(t)
	if err := h.store.UpdateProjectSandboxDockerfilePath(projID, "/tmp/Dockerfile"); err != nil {
		t.Fatalf("set dockerfile path: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects/{projectID}/sandbox/build", h.HandleSandboxBuild)

	rr := doSandboxBuildRequest(t, mux, projID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(rr.Body.String(), "sandbox manager not available") {
		t.Fatalf("body = %q, want error message", rr.Body.String())
	}
}

// TestBuildWantsNoCache checks that only an explicit truthy ?no_cache value
// disables the build layer cache; anything else keeps the default cached build.
func TestBuildWantsNoCache(t *testing.T) {
	tests := []struct {
		query string
		want  bool
	}{
		{"", false},
		{"no_cache=", false},
		{"no_cache=1", true},
		{"no_cache=true", true},
		{"no_cache=TRUE", true},
		{"no_cache=0", false},
		{"no_cache=false", false},
		{"no_cache=maybe", false},
		{"other=1", false},
	}
	for _, tc := range tests {
		t.Run("query="+tc.query, func(t *testing.T) {
			u := "/api/projects/p1/sandbox/build"
			if tc.query != "" {
				u += "?" + tc.query
			}
			req := httptest.NewRequest(http.MethodPost, u, nil)
			if got := buildWantsNoCache(req); got != tc.want {
				t.Fatalf("buildWantsNoCache(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

// A sandbox image that names no registry ("ubuntu:24.04") breaks every later sandbox start, so it
// must be refused where it is entered.
func TestCreateProjectRejectsUnqualifiedSandboxImage(t *testing.T) {
	h, _ := testProjectHandler(t)
	repo := t.TempDir()
	createBareRepo(t, repo)

	isolation := false
	proj, code, msg := h.createProjectFromRequest(types.CreateProjectRequest{
		Path:                    repo,
		Name:                    "img-reject-proj",
		SandboxEnabled:          true,
		SandboxNetworkIsolation: &isolation,
		SandboxImage:            "ubuntu:24.04",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want %d", code, msg, http.StatusBadRequest)
	}
	if !strings.Contains(msg, "fully qualified") {
		t.Fatalf("message = %q, want it to say the image must be fully qualified", msg)
	}
	if proj != nil {
		t.Fatal("project must not be created when its image ref is unusable")
	}

	okProj, okCode, okMsg := h.createProjectFromRequest(types.CreateProjectRequest{
		Path:                    repo,
		Name:                    "img-accept-proj",
		SandboxEnabled:          true,
		SandboxNetworkIsolation: &isolation,
		SandboxImage:            "docker.io/library/ubuntu:24.04",
	})
	if okCode != http.StatusCreated {
		t.Fatalf("status = %d (%s), want %d", okCode, okMsg, http.StatusCreated)
	}
	if okProj.SandboxImage != "docker.io/library/ubuntu:24.04" {
		t.Fatalf("stored image = %q, want the configured ref", okProj.SandboxImage)
	}
}

// The update path validates before writing, so a rejected image can't half-update the project.
func TestUpdateProjectRejectsUnqualifiedSandboxImage(t *testing.T) {
	h, projID := testProjectHandler(t)
	const seeded = "docker.io/library/ubuntu:24.04"
	if err := h.store.UpdateProjectSandboxImage(projID, seeded); err != nil {
		t.Fatalf("seed image: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects/{projectID}", h.HandleByID)

	for _, bad := range []string{"alpine/git", "ubuntu:24.04", "sandbox-getmike.dev"} {
		req := httptest.NewRequest(http.MethodPut, "/api/projects/"+projID,
			strings.NewReader(`{"sandbox_image":"`+bad+`"}`))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("PUT sandbox_image=%q: status = %d, want %d (%s)", bad, rr.Code, http.StatusBadRequest, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "fully qualified") {
			t.Fatalf("PUT sandbox_image=%q: body = %q, want the fully-qualified-name error", bad, rr.Body.String())
		}
		if p, err := h.store.GetProject(projID); err != nil || p.SandboxImage != seeded {
			t.Fatalf("rejected value %q was written: stored=%q err=%v", bad, p.SandboxImage, err)
		}
	}

	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+projID,
		strings.NewReader(`{"sandbox_image":"quay.io/o/r:1"}`))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (%s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	if p, _ := h.store.GetProject(projID); p.SandboxImage != "quay.io/o/r:1" {
		t.Fatalf("stored image = %q, want quay.io/o/r:1", p.SandboxImage)
	}
}

// A forwarded port's bind address is checked only for shape and stored canonicalized:
// 0.0.0.0 is stored as "", because that is what "every interface" means everywhere else.
func TestCreateProjectNormalizesForwardPorts(t *testing.T) {
	h, _ := testProjectHandler(t)
	isolation := false

	tests := []struct {
		name  string
		ports []types.PortForward
		want  []types.PortForward
	}{
		{"container port only", []types.PortForward{{ContainerPort: 3000}}, []types.PortForward{{ContainerPort: 3000}}},
		{"explicit wildcard stored as empty", []types.PortForward{{ContainerPort: 3000, HostIP: "0.0.0.0"}}, []types.PortForward{{ContainerPort: 3000}}},
		{"IPv6 wildcard stored as empty", []types.PortForward{{ContainerPort: 3000, HostIP: "::"}}, []types.PortForward{{ContainerPort: 3000}}},
		{"LAN address kept", []types.PortForward{{ContainerPort: 80, HostIP: "192.168.1.100"}}, []types.PortForward{{ContainerPort: 80, HostIP: "192.168.1.100"}}},
		{"loopback is the user's choice", []types.PortForward{{ContainerPort: 3000, HostIP: "127.0.0.1"}}, []types.PortForward{{ContainerPort: 3000, HostIP: "127.0.0.1"}}},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			createBareRepo(t, repo)
			proj, code, msg := h.createProjectFromRequest(types.CreateProjectRequest{
				Path:                    repo,
				Name:                    fmt.Sprintf("fwd-%d", i),
				SandboxEnabled:          true,
				SandboxNetworkIsolation: &isolation,
				SandboxForwardPorts:     tc.ports,
			})
			if code != http.StatusCreated {
				t.Fatalf("status = %d (%s), want %d", code, msg, http.StatusCreated)
			}
			if got := fmt.Sprintf("%+v", proj.ForwardPorts); got != fmt.Sprintf("%+v", tc.want) {
				t.Fatalf("stored ports = %s, want %s", got, fmt.Sprintf("%+v", tc.want))
			}
		})
	}
}

// A bind address that is not an IP address, or a container port out of range, is refused
// before anything is written - a rejected PUT must not half-update the settings.
func TestUpdateProjectValidatesForwardPorts(t *testing.T) {
	h, projID := testProjectHandler(t)
	seeded := []types.PortForward{{ContainerPort: 3000}}
	if err := h.store.UpdateProjectSandboxForwardPorts(projID, seeded); err != nil {
		t.Fatalf("seed forward ports: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects/{projectID}", h.HandleByID)
	stored := func() string {
		t.Helper()
		p, err := h.store.GetProject(projID)
		if err != nil {
			t.Fatalf("get project: %v", err)
		}
		return fmt.Sprintf("%+v", p.ForwardPorts)
	}

	for _, body := range []string{
		`{"sandbox_forward_ports":[{"container_port":3000,"host_ip":"saturn.lan"}]}`,
		`{"sandbox_forward_ports":[{"container_port":3000,"host_ip":"192.168.1.100:8080"}]}`,
		`{"sandbox_forward_ports":[{"container_port":0}]}`,
		`{"sandbox_forward_ports":[{"container_port":70000}]}`,
	} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/api/projects/"+projID, strings.NewReader(body)))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("PUT %s: status = %d, want %d (%s)", body, rr.Code, http.StatusBadRequest, rr.Body.String())
		}
		if got := stored(); got != fmt.Sprintf("%+v", seeded) {
			t.Fatalf("rejected %s wrote %s", body, got)
		}
	}

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/api/projects/"+projID,
		strings.NewReader(`{"sandbox_forward_ports":[{"container_port":8080,"host_ip":"192.168.1.100"},{"container_port":9000,"host_ip":"0.0.0.0"}]}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (%s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	want := []types.PortForward{{ContainerPort: 8080, HostIP: "192.168.1.100"}, {ContainerPort: 9000}}
	if got := stored(); got != fmt.Sprintf("%+v", want) {
		t.Fatalf("stored ports = %s, want %s", got, fmt.Sprintf("%+v", want))
	}
}

func TestApplyPricingBasic(t *testing.T) {
	h, projID, s := testProjectHandlerWithStore(t)

	// Create a task with messages
	createTestTaskWithMessages(t, s, projID, "task-1", "model-a", "1",
		1000, 500, 100, 1000, 5000)

	// Set up provider with pricing (no power settings)
	cfg, _ := h.configStore.GetConfig()
	cfg.Providers = []types.Provider{
		{
			ID:             1,
			Name:           "prov-x",
			DisplayName:    "Provider X",
			URL:            "http://localhost:8080/v1",
			InputPrice:     3.0, // $3 per 1M input tokens
			OutputPrice:    5.0, // $5 per 1M output tokens
			CacheReadPrice: 1.0, // $1 per 1M cache-read tokens
		},
	}
	h.configStore.UpdateConfig(cfg)

	// Get stats via the handler
	stats, err := h.store.GetProjectStats(projID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	h.applyPricing(stats)

	// API cost: (900 * 3 + 100 * 1 + 500 * 5) / 1M = (2700 + 100 + 2500) / 1M = 0.0053
	if len(stats.ByModel) != 1 {
		t.Fatalf("expected 1 model, got %d", len(stats.ByModel))
	}
	m := stats.ByModel[0]
	floatApprox(t, m.Cost, 0.0053, 0.0001)
	floatApprox(t, stats.TotalCost, 0.0053, 0.0001)
	// No power settings, so power should be 0
	floatApprox(t, m.PowerKwh, 0, 0.0001)
}

func TestApplyPricingWithPower(t *testing.T) {
	h, projID := testProjectHandler(t)

	// Create a task with messages (5000ms predicted time)
	createTestTaskWithMessages(t, h.store, projID, "task-1", "model-a", "1",
		1000, 500, 100, 1000, 5000)

	// Set up provider with pricing AND power settings
	cfg, _ := h.configStore.GetConfig()
	cfg.Providers = []types.Provider{
		{
			ID:                    1,
			Name:                  "prov-x",
			DisplayName:           "Provider X",
			URL:                   "http://localhost:8080/v1",
			InputPrice:            3.0,
			OutputPrice:           5.0,
			CacheReadPrice:        1.0,
			PowerConsumptionW:     500.0, // 500W GPU
			ElectricityCostPerKwh: 0.12,  // $0.12 per kWh
		},
	}
	h.configStore.UpdateConfig(cfg)

	stats, err := h.store.GetProjectStats(projID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	h.applyPricing(stats)

	if len(stats.ByModel) != 1 {
		t.Fatalf("expected 1 model, got %d", len(stats.ByModel))
	}
	m := stats.ByModel[0]

	// Power: 500W * 5000ms / 3,600,000,000 = 0.00069444 kWh
	expectedKwh := 500.0 * 5000.0 / 3_600_000_000
	floatApprox(t, m.PowerKwh, expectedKwh, 0.000001)
	floatApprox(t, stats.TotalPowerKwh, expectedKwh, 0.000001)

	// Electricity cost: 0.00069444 * 0.12 = 0.00008333
	expectedElecCost := expectedKwh * 0.12
	// Total cost = API cost + electricity cost
	expectedTotalCost := 0.0053 + expectedElecCost
	floatApprox(t, m.Cost, expectedTotalCost, 0.0001)
	floatApprox(t, stats.TotalCost, expectedTotalCost, 0.0001)
}

func TestApplyPricingPowerOnlyNoElectricity(t *testing.T) {
	h, projID := testProjectHandler(t)

	createTestTaskWithMessages(t, h.store, projID, "task-1", "model-a", "1",
		1000, 500, 100, 1000, 5000)

	// Set up provider with power but NO electricity cost
	cfg, _ := h.configStore.GetConfig()
	cfg.Providers = []types.Provider{
		{
			ID:                1,
			Name:              "prov-x",
			DisplayName:       "Provider X",
			URL:               "http://localhost:8080/v1",
			InputPrice:        3.0,
			OutputPrice:       5.0,
			CacheReadPrice:    1.0,
			PowerConsumptionW: 500.0,
			// ElectricityCostPerKwh not set (0)
		},
	}
	h.configStore.UpdateConfig(cfg)

	stats, err := h.store.GetProjectStats(projID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	h.applyPricing(stats)

	if len(stats.ByModel) != 1 {
		t.Fatalf("expected 1 model, got %d", len(stats.ByModel))
	}
	m := stats.ByModel[0]

	// Power should be computed
	expectedKwh := 500.0 * 5000.0 / 3_600_000_000
	floatApprox(t, m.PowerKwh, expectedKwh, 0.000001)
	floatApprox(t, stats.TotalPowerKwh, expectedKwh, 0.000001)

	// Cost should be API only (no electricity added)
	floatApprox(t, m.Cost, 0.0053, 0.0001)
	floatApprox(t, stats.TotalCost, 0.0053, 0.0001)
}

func TestApplyPricingAgentProviderSkipped(t *testing.T) {
	h, projID := testProjectHandler(t)

	createTestTaskWithMessages(t, h.store, projID, "task-1", "agent-model", "my-agent",
		1000, 500, 100, 1000, 5000)

	// Set up agent provider with power settings (should be ignored)
	cfg, _ := h.configStore.GetConfig()
	cfg.Providers = []types.Provider{
		{
			Name:                  "my-agent",
			DisplayName:           "My Agent",
			Kind:                  "agent",
			URL:                   "agent://my-agent",
			InputPrice:            0,
			OutputPrice:           0,
			PowerConsumptionW:     500.0,
			ElectricityCostPerKwh: 0.12,
		},
	}
	h.configStore.UpdateConfig(cfg)

	stats, err := h.store.GetProjectStats(projID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	h.applyPricing(stats)

	if len(stats.ByModel) != 1 {
		t.Fatalf("expected 1 model, got %d", len(stats.ByModel))
	}
	m := stats.ByModel[0]

	// Agent provider: power/electricity should NOT be computed
	floatApprox(t, m.PowerKwh, 0, 0.000001)
	floatApprox(t, stats.TotalPowerKwh, 0, 0.000001)
	// Cost should be 0 (no pricing set for agent)
	floatApprox(t, m.Cost, 0, 0.0001)
}

func TestApplyPricingMultipleProviders(t *testing.T) {
	h, projID := testProjectHandler(t)

	// Task 1: model-a from prov-x (with power)
	createTestTaskWithMessages(t, h.store, projID, "task-1", "model-a", "1",
		1000, 500, 100, 1000, 5000)

	// Task 2: model-b from prov-y (no power)
	createTestTaskWithMessages(t, h.store, projID, "task-2", "model-b", "2",
		2000, 300, 200, 2000, 3000)

	cfg, _ := h.configStore.GetConfig()
	cfg.Providers = []types.Provider{
		{
			ID:                1,
			Name:              "prov-x",
			DisplayName:       "Provider X",
			URL:               "http://localhost:8080/v1",
			InputPrice:        3.0,
			OutputPrice:       5.0,
			CacheReadPrice:    1.0,
			PowerConsumptionW: 500.0,
		},
		{
			ID:          2,
			Name:        "prov-y",
			DisplayName: "Provider Y",
			URL:         "http://localhost:9090/v1",
			InputPrice:  1.0,
			OutputPrice: 2.0,
			// No power settings
		},
	}
	h.configStore.UpdateConfig(cfg)

	stats, err := h.store.GetProjectStats(projID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	h.applyPricing(stats)

	if len(stats.ByModel) != 2 {
		t.Fatalf("expected 2 models, got %d", len(stats.ByModel))
	}

	// Find each model
	var modelA, modelB *types.ModelStats
	for i := range stats.ByModel {
		if stats.ByModel[i].ModelName == "model-a" {
			modelA = &stats.ByModel[i]
		} else if stats.ByModel[i].ModelName == "model-b" {
			modelB = &stats.ByModel[i]
		}
	}
	if modelA == nil || modelB == nil {
		t.Fatal("missing model")
	}

	// model-a: has power, no electricity cost
	expectedKwhA := 500.0 * 5000.0 / 3_600_000_000
	floatApprox(t, modelA.PowerKwh, expectedKwhA, 0.000001)
	// API cost for model-a: (900*3 + 100*1 + 500*5) / 1M = 0.0053
	floatApprox(t, modelA.Cost, 0.0053, 0.0001)

	// model-b: no power
	floatApprox(t, modelB.PowerKwh, 0, 0.000001)
	// API cost for model-b: (1800*1 + 200*0 + 300*2) / 1M = (1800 + 600) / 1M = 0.0024
	// Note: prov-y has no cache_read_price set, so cache cost is 0
	floatApprox(t, modelB.Cost, 0.0024, 0.0001)

	// Totals
	floatApprox(t, stats.TotalPowerKwh, expectedKwhA, 0.000001)
	floatApprox(t, stats.TotalCost, 0.0053+0.0024, 0.0001)
}

// Test JSON serialization of new fields
// TestApplyPricingModelPricingOverride verifies per-model pricing overrides
// provider-level defaults when computing API cost.
func TestApplyPricingModelPricingOverride(t *testing.T) {
	h, projID := testProjectHandler(t)

	createTestTaskWithMessages(t, h.store, projID, "task-1", "model-a", "1",
		1000, 500, 100, 1000, 5000)

	inputP := 6.0
	outputP := 9.0
	cacheReadP := 2.0
	cfg, _ := h.configStore.GetConfig()
	cfg.Providers = []types.Provider{
		{
			ID:                1,
			Name:              "prov-x",
			DisplayName:       "Provider X",
			URL:               "http://localhost:8080/v1",
			InputPrice:        3.0, // provider defaults — must be overridden
			OutputPrice:       5.0,
			CacheReadPrice:    1.0,
			PowerConsumptionW: 500.0,
			Models: []types.ModelConfig{
				{Name: "model-a", InputPrice: &inputP, OutputPrice: &outputP, CacheReadPrice: &cacheReadP},
			},
		},
	}
	h.configStore.UpdateConfig(cfg)

	stats, err := h.store.GetProjectStats(projID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	h.applyPricing(stats)

	if len(stats.ByModel) != 1 {
		t.Fatalf("expected 1 model, got %d", len(stats.ByModel))
	}
	m := stats.ByModel[0]
	// API cost: (900 * 6 + 100 * 2 + 500 * 9) / 1M = (5400 + 200 + 4500) / 1M = 0.0101
	floatApprox(t, m.Cost, 0.0101, 0.0001)
	floatApprox(t, stats.TotalCost, 0.0101, 0.0001)
	// Provider power is still the fallback (no model power override) — computed
	floatApprox(t, m.PowerKwh, 500.0*5000.0/3_600_000_000, 0.000001)
}

// TestApplyPricingModelPowerOverride verifies a per-model power override
// replaces the provider-level power when computing kWh and electricity cost.
func TestApplyPricingModelPowerOverride(t *testing.T) {
	h, projID := testProjectHandler(t)

	createTestTaskWithMessages(t, h.store, projID, "task-1", "model-a", "1",
		1000, 500, 100, 1000, 5000)

	modelPower := 1000.0 // override: provider says 500W, model says 1000W
	cfg, _ := h.configStore.GetConfig()
	cfg.Providers = []types.Provider{
		{
			ID:                    1,
			Name:                  "prov-x",
			DisplayName:           "Provider X",
			URL:                   "http://localhost:8080/v1",
			InputPrice:            3.0,
			OutputPrice:           5.0,
			CacheReadPrice:        1.0,
			PowerConsumptionW:     500.0,
			ElectricityCostPerKwh: 0.12,
			Models: []types.ModelConfig{
				{Name: "model-a", PowerConsumptionW: &modelPower},
			},
		},
	}
	h.configStore.UpdateConfig(cfg)

	stats, err := h.store.GetProjectStats(projID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	h.applyPricing(stats)

	if len(stats.ByModel) != 1 {
		t.Fatalf("expected 1 model, got %d", len(stats.ByModel))
	}
	m := stats.ByModel[0]

	// Power uses the model override: 1000W * 5000ms / 3.6e9 = 0.00138889 kWh
	expectedKwh := 1000.0 * 5000.0 / 3_600_000_000
	floatApprox(t, m.PowerKwh, expectedKwh, 0.000001)
	floatApprox(t, stats.TotalPowerKwh, expectedKwh, 0.000001)

	// Electricity cost uses the provider rate: kWh * 0.12
	expectedTotalCost := 0.0053 + expectedKwh*0.12
	floatApprox(t, m.Cost, expectedTotalCost, 0.0001)
	floatApprox(t, stats.TotalCost, expectedTotalCost, 0.0001)
}

// TestApplyPricingModelPartialFallback verifies that when only some per-model
// fields are set, the remaining fields inherit provider-level defaults, and that
// a model-level cache_write_price overrides the input rate.
func TestApplyPricingModelPartialFallback(t *testing.T) {
	h, projID := testProjectHandler(t)

	createTestTaskWithMessages(t, h.store, projID, "task-1", "model-a", "1",
		1000, 500, 100, 1000, 5000)

	cacheWriteP := 2.0 // model only sets cache write
	cfg, _ := h.configStore.GetConfig()
	cfg.Providers = []types.Provider{
		{
			ID:                1,
			Name:              "prov-x",
			DisplayName:       "Provider X",
			URL:               "http://localhost:8080/v1",
			InputPrice:        3.0,
			OutputPrice:       5.0,
			CacheReadPrice:    1.0,
			PowerConsumptionW: 500.0,
			Models: []types.ModelConfig{
				{Name: "model-a", CacheWritePrice: &cacheWriteP},
			},
		},
	}
	h.configStore.UpdateConfig(cfg)

	stats, err := h.store.GetProjectStats(projID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	h.applyPricing(stats)

	if len(stats.ByModel) != 1 {
		t.Fatalf("expected 1 model, got %d", len(stats.ByModel))
	}
	m := stats.ByModel[0]
	// inputRate = model cache write (2.0); cache read + output inherit provider (1.0, 5.0)
	// (900 * 2 + 100 * 1 + 500 * 5) / 1M = (1800 + 100 + 2500) / 1M = 0.0044
	floatApprox(t, m.Cost, 0.0044, 0.0001)
	floatApprox(t, stats.TotalCost, 0.0044, 0.0001)
	// Power inherits provider default (500W)
	floatApprox(t, m.PowerKwh, 500.0*5000.0/3_600_000_000, 0.000001)
}

func TestApplyPricingJSONSerialization(t *testing.T) {
	h, projID := testProjectHandler(t)

	createTestTaskWithMessages(t, h.store, projID, "task-1", "model-a", "1",
		1000, 500, 100, 1000, 5000)

	cfg, _ := h.configStore.GetConfig()
	cfg.Providers = []types.Provider{
		{
			ID:                    1,
			Name:                  "prov-x",
			DisplayName:           "Provider X",
			URL:                   "http://localhost:8080/v1",
			InputPrice:            3.0,
			OutputPrice:           5.0,
			CacheReadPrice:        1.0,
			PowerConsumptionW:     500.0,
			ElectricityCostPerKwh: 0.12,
		},
	}
	h.configStore.UpdateConfig(cfg)

	stats, err := h.store.GetProjectStats(projID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	h.applyPricing(stats)

	// Marshal to JSON and verify fields are present
	data, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("marshal stats: %v", err)
	}
	jsonStr := string(data)

	// Verify power_kwh field is in JSON
	if !strings.Contains(jsonStr, `"power_kwh"`) {
		t.Fatalf("expected power_kwh in JSON, got: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"total_power_kwh"`) {
		t.Fatalf("expected total_power_kwh in JSON, got: %s", jsonStr)
	}

	// Unmarshal and verify values
	var unmarshaled types.ProjectStats
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("unmarshal stats: %v", err)
	}
	if len(unmarshaled.ByModel) != 1 {
		t.Fatalf("expected 1 model in JSON, got %d", len(unmarshaled.ByModel))
	}
	m := unmarshaled.ByModel[0]

	if m.PowerKwh <= 0 {
		t.Fatalf("expected power_kwh > 0, got %f", m.PowerKwh)
	}
	if unmarshaled.TotalPowerKwh <= 0 {
		t.Fatalf("expected total_power_kwh > 0, got %f", unmarshaled.TotalPowerKwh)
	}
	expectedKwh := 500.0 * 5000.0 / 3_600_000_000
	expectedTotalCost := 0.0053 + expectedKwh*0.12
	floatApprox(t, m.Cost, expectedTotalCost, 0.0001)
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
