package api

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/git"
	"mike/backend/store"
	"mike/backend/testutil/testgit"
	"mike/backend/testutil/teststore"
	"mike/backend/types"
)

// --- Test helpers (shared) ---

// mustRun executes a command in dir, failing the test on error.
func mustRun(t *testing.T, dir, cmd string, args ...string) {
	t.Helper()
	c := exec.Command(cmd, args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", cmd, args, err, out)
	}
}

// initGitRepo creates a git repo at path with one committed file.
// Used by tests that need git history (log, diff, etc.).
func initGitRepo(t *testing.T, path string) {
	t.Helper()
	testgit.RepoAt(t, path)
}

// initGitRepoTemp creates a temp directory, initializes a bare git repo there
// (no commits, no user config), and returns the repo path. Used by tests that
// only need a git working tree for staging.
func initGitRepoTemp(t *testing.T) string {
	t.Helper()
	return testgit.EmptyRepo(t)
}

// createFile creates a file with the given name and content inside repoPath.
func createFile(t *testing.T, repoPath, filename, content string) {
	t.Helper()
	fullPath := filepath.Join(repoPath, filename)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", filename, err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write file %s: %v", filename, err)
	}
}

// stagedFiles returns the names of files staged in the index.
func stagedFiles(t *testing.T, repoPath string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoPath, "diff", "--cached", "--name-only").Output()
	if err != nil {
		t.Fatalf("git diff --cached: %v: %s", err, out)
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}

// createWorktreeAt creates a git worktree at worktreePath with the given branch.
func createWorktreeAt(t *testing.T, projectPath, worktreePath, branch string) {
	t.Helper()
	mustRun(t, projectPath, "git", "worktree", "add", "-B", branch, worktreePath)
}

// gitTestStore creates a temp store and registers cleanup.
func gitTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s := teststore.StoreIn(t, dbPath)
	return s
}

// newTestGitHandler creates a GitHandler backed by a temporary store, config, and
// worktree manager with config values set so ConfigStore.GetConfig() succeeds.
func newTestGitHandler(t *testing.T) (*GitHandler, *store.Store, *git.WorktreeManager, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	worktreesDir := filepath.Join(dir, "worktrees")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	s := teststore.StoreIn(t, dbPath)

	s.SetConfigValue("providers", "[]")
	s.SetConfigValue("worker_provider", "")
	s.SetConfigValue("worker_model", "")
	s.SetConfigValue("utility_provider", "")
	s.SetConfigValue("utility_model", "")

	cfgStore := teststore.ConfigStore(t, s, dbPath)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)
	wm := git.NewWorktreeManager(s, cfgStore)
	h := NewGitHandler(s, wm)

	return h, s, wm, worktreesDir
}

// newTestGitMux creates a ServeMux wired with the four git handler routes so
// that {projectID} and {taskID} path values are populated automatically.
func newTestGitMux(h *GitHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects/{projectID}/git/log", h.HandleLog)
	mux.HandleFunc("GET /api/projects/{projectID}/git/diff", h.HandleDiff)
	mux.HandleFunc("GET /api/projects/{projectID}/git/diff/file", h.HandleProjectDiffFile)
	mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/git/log", h.HandleTaskLog)
	mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/diff", h.HandleTaskDiff)
	return mux
}

// doRequest sends a request through the mux and returns the recorded response.
func doRequest(t *testing.T, mux *http.ServeMux, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// newStageRequest builds one stage request naming every path given, as the panel does.
func newStageRequest(t *testing.T, target string, stage bool, paths ...string) *http.Request {
	t.Helper()
	stageStr := "true"
	if !stage {
		stageStr = "false"
	}
	quoted := make([]string, 0, len(paths))
	for _, path := range paths {
		quoted = append(quoted, `"`+path+`"`)
	}
	body := strings.NewReader(`{"paths": [` + strings.Join(quoted, ", ") + `], "stage": ` + stageStr + `}`)
	req := httptest.NewRequest("POST", target, body)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// decodeBatchAnswer reads what a batch git write answered.
func decodeBatchAnswer(t *testing.T, rec *httptest.ResponseRecorder) gitBatchAnswer {
	t.Helper()
	var answer gitBatchAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode answer: %v (body %s)", err, rec.Body.String())
	}
	return answer
}

// TestGitRefValidation is the ref-injection matrix for every git route that takes a ref:
// project log/diff and task log/diff. A ref lands on a git command line, so two families
// of value must be rejected before that happens — option-looking refs ("-foo",
// "--format=json"), which git would parse as flags, and the rev-syntax characters git
// itself would reinterpret ("~", "^", ":", ".."). Each target carries its own fixture
// because the task routes resolve the ref inside a worktree that has to exist, and each carries the
// spellings it must answer 200 for, which is what proves the 400s come from validation and not from a
// broken route.

// --- The ref-injection matrix, and the accepted side that proves it ---

// acceptedRef is one ref spelling a route must answer 200 for, with what that answer must hold.
// `query` is the route's whole query string, because each route names its refs differently.
type acceptedRef struct {
	query string
	check func(*testing.T, *httptest.ResponseRecorder)
}

// decodedGitLog reads a ?full=1 log answer.
func decodedGitLog(t *testing.T, rr *httptest.ResponseRecorder) types.GitLogResponse {
	t.Helper()
	var log types.GitLogResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &log); err != nil {
		t.Fatalf("decode log answer: %v (%s)", err, rr.Body.String())
	}
	return log
}

// requireSubjects fails unless the log holds exactly these commit subjects, in order — which is what
// a validated ref that was then silently dropped cannot produce.
func requireSubjects(t *testing.T, commits []types.GitCommit, want ...string) {
	t.Helper()
	var got []string
	for _, c := range commits {
		got = append(got, c.Subject)
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("log subjects = %q, want %q", strings.Join(got, "|"), strings.Join(want, "|"))
	}
}

// requireParsedFilesIn reads a task diff answer's parsed_files and fails unless it holds exactly
// these paths — the key the panel renders and never re-parses.
func requireParsedFilesIn(t *testing.T, rr *httptest.ResponseRecorder, want ...string) {
	t.Helper()
	var body struct {
		ParsedFiles []git.TrackedDiffFile `json:"parsed_files"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not a task diff answer: %v (%s)", err, rr.Body.String())
	}
	var got []string
	for _, f := range body.ParsedFiles {
		got = append(got, f.Path)
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("parsed_files = %q, want %q (%s)", strings.Join(got, "|"), strings.Join(want, "|"), rr.Body.String())
	}
}

// requireParsedDiffFiles reads /git/diff's answer and fails unless it holds exactly these paths.
func requireParsedDiffFiles(t *testing.T, rr *httptest.ResponseRecorder, want ...string) {
	t.Helper()
	var files []git.TrackedDiffFile
	if err := json.Unmarshal(rr.Body.Bytes(), &files); err != nil {
		t.Fatalf("body is not []TrackedDiffFile: %v (%s)", err, rr.Body.String())
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Path)
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("diff files = %q, want %q", strings.Join(got, "|"), strings.Join(want, "|"))
	}
}

// refMatrixRepo is the repository every target of the matrix reads: the one commit initGitRepo
// writes, a second commit adding refdiff.txt on top of it, and base-branch pinned to the first
// commit — so an accepted comparison names two trees that differ by a file nobody can mistake for
// another, and an accepted log can tell its two histories apart by subject.
func refMatrixRepo(t *testing.T) string {
	t.Helper()
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	testgit.Git(t, projPath, "branch", "base-branch", "HEAD")
	createFile(t, projPath, "refdiff.txt", "the second commit's line\n")
	testgit.Git(t, projPath, "add", "refdiff.txt")
	testgit.Git(t, projPath, "commit", "-m", "add refdiff")
	return projPath
}

func TestGitRefValidation(t *testing.T) {
	// A log that answers the matrix repository's full history: the second commit is on top, so a
	// route that ignored the ref it was given and read HEAD would still answer this.
	fullHistory := func(t *testing.T, rr *httptest.ResponseRecorder) {
		requireSubjects(t, decodedGitLog(t, rr).Commits, "add refdiff", "Initial commit")
	}
	// The task-log fixture moves the task branch ahead of what its worktree has checked out, so
	// these two subjects are what only the task's own branch can answer with.
	taskBranchHistory := func(t *testing.T, rr *httptest.ResponseRecorder) {
		requireSubjects(t, decodedGitLog(t, rr).Commits, "task branch work", "Initial commit")
	}

	targets := []struct {
		name string
		// setup builds the fixture and returns the mux plus a URL builder for one ref
		// position (each route exposes its refs under different parameter names). The empty
		// parameter name asks for an accepted row's raw query string.
		setup func(t *testing.T) (mux *http.ServeMux, url func(param, value string) string)
		// params are the ref positions the matrix poisons; each takes the others' defaults.
		params []string
		// ref builds the value under test from a bare invalid character. The diff routes
		// take full revs, so the character is embedded in a rev-looking string there.
		ref func(ch string) string
		// accepted is the other half of the contract: the spellings the route must answer for,
		// and what the answer must hold. A row naming no ref exercises the route's own default —
		// HEAD for a project, the task's branch for a task.
		accepted []acceptedRef
	}{
		{
			name: "project log",
			setup: func(t *testing.T) (*http.ServeMux, func(string, string) string) {
				h, s, _, _ := newTestGitHandler(t)
				projPath := refMatrixRepo(t)
				if err := s.CreateProject(&types.Project{ID: "log-char-proj", Name: "log-char-proj", Path: projPath}); err != nil {
					t.Fatal(err)
				}
				return newTestGitMux(h), func(param, value string) string {
					if param == "" {
						return "/api/projects/log-char-proj/git/log?" + value
					}
					return "/api/projects/log-char-proj/git/log?branch=" + value + "&full=1"
				}
			},
			params: []string{"branch"},
			ref:    func(ch string) string { return ch },
			accepted: []acceptedRef{
				{query: "branch=HEAD&full=1", check: fullHistory},
				{query: "full=1", check: fullHistory}, // no branch at all: the default is HEAD
				{query: "branch=base-branch&full=1", check: func(t *testing.T, rr *httptest.ResponseRecorder) {
					log := decodedGitLog(t, rr)
					requireSubjects(t, log.Commits, "Initial commit")
					if log.CurrentBranch == "" {
						t.Error("current_branch was not resolved for the project checkout")
					}
				}},
			},
		},
		{
			name: "project diff",
			setup: func(t *testing.T) (*http.ServeMux, func(string, string) string) {
				h, s, _, _ := newTestGitHandler(t)
				projPath := refMatrixRepo(t)
				if err := s.CreateProject(&types.Project{ID: "refval-diff-proj", Name: "refval-diff-proj", Path: projPath}); err != nil {
					t.Fatal(err)
				}
				return newTestGitMux(h), func(param, value string) string {
					if param == "" {
						return "/api/projects/refval-diff-proj/git/diff?" + value
					}
					if param == "base" {
						return "/api/projects/refval-diff-proj/git/diff?base=" + value + "&head=HEAD"
					}
					return "/api/projects/refval-diff-proj/git/diff?base=HEAD&head=" + value
				}
			},
			params: []string{"base", "head"},
			ref:    func(ch string) string { return "feat" + ch + "branch" },
			accepted: []acceptedRef{
				{query: "base=base-branch&head=HEAD", check: func(t *testing.T, rr *httptest.ResponseRecorder) {
					requireParsedDiffFiles(t, rr, "refdiff.txt")
				}},
			},
		},
		{
			name: "task log",
			setup: func(t *testing.T) (*http.ServeMux, func(string, string) string) {
				h, s, _, worktreesDir := newTestGitHandler(t)
				projPath := t.TempDir()
				initGitRepo(t, projPath)
				if err := s.CreateProject(&types.Project{ID: "refval-tasklog-proj", Name: "refval-tasklog-proj", Path: projPath}); err != nil {
					t.Fatal(err)
				}
				task := &types.Task{ID: "refval-tasklog-task", ProjectID: "refval-tasklog-proj",
					Title: "log task", Status: types.StatusInProgress, Branch: "main"}
				if err := s.CreateTask(task); err != nil {
					t.Fatal(err)
				}
				wtPath := filepath.Join(worktreesDir, "refval-tasklog-proj", task.ID)
				createWorktreeAt(t, projPath, wtPath, "main")
				// The task branch moves ahead of what the worktree has checked out, so the
				// route's default rev is distinguishable from the worktree's HEAD.
				createFile(t, wtPath, "on-task-branch.txt", "work on the task branch\n")
				testgit.Git(t, wtPath, "add", "on-task-branch.txt")
				testgit.Git(t, wtPath, "commit", "-m", "task branch work")
				testgit.Git(t, wtPath, "checkout", "-q", "-B", "head-is-behind", "HEAD~1")
				return newTestGitMux(h), func(param, value string) string {
					if param == "" {
						return "/api/projects/refval-tasklog-proj/tasks/refval-tasklog-task/git/log?" + value
					}
					return "/api/projects/refval-tasklog-proj/tasks/refval-tasklog-task/git/log?branch=" + value + "&full=1"
				}
			},
			params: []string{"branch"},
			ref:    func(ch string) string { return ch },
			accepted: []acceptedRef{
				{query: "branch=main&full=1", check: taskBranchHistory},
				// The absent branch is the task's own, not the worktree's checked-out one.
				{query: "full=1", check: taskBranchHistory},
				{query: "branch=head-is-behind&full=1", check: func(t *testing.T, rr *httptest.ResponseRecorder) {
					requireSubjects(t, decodedGitLog(t, rr).Commits, "Initial commit")
				}},
			},
		},
		{
			name: "task diff",
			setup: func(t *testing.T) (*http.ServeMux, func(string, string) string) {
				h, s, _, worktreesDir := newTestGitHandler(t)
				projPath := refMatrixRepo(t)
				if err := s.CreateProject(&types.Project{ID: "refval-taskdiff-proj", Name: "refval-taskdiff-proj", Path: projPath}); err != nil {
					t.Fatal(err)
				}
				task := &types.Task{ID: "refval-taskdiff-task", ProjectID: "refval-taskdiff-proj",
					Title: "diff task", Status: types.StatusInProgress, Branch: "main"}
				if err := s.CreateTask(task); err != nil {
					t.Fatal(err)
				}
				createWorktreeAt(t, projPath, filepath.Join(worktreesDir, "refval-taskdiff-proj", task.ID), "main")
				return newTestGitMux(h), func(param, value string) string {
					if param == "" {
						return "/api/projects/refval-taskdiff-proj/tasks/refval-taskdiff-task/diff?" + value
					}
					return "/api/projects/refval-taskdiff-proj/tasks/refval-taskdiff-task/diff?" + param + "=" + value
				}
			},
			params: []string{"base", "head"},
			ref:    func(ch string) string { return "feat" + ch + "branch" },
			accepted: []acceptedRef{
				{query: "base=base-branch&head=HEAD&parsed=1", check: func(t *testing.T, rr *httptest.ResponseRecorder) {
					requireParsedFilesIn(t, rr, "refdiff.txt")
				}},
			},
		},
	}

	// The space is URL-encoded because that is how it arrives from a browser.
	encoded := map[string]string{" ": "feat%20branch"}

	for _, tgt := range targets {
		t.Run(tgt.name, func(t *testing.T) {
			mux, url := tgt.setup(t)

			for _, param := range tgt.params {
				// Dash-prefixed refs are sent verbatim — that is exactly the shape git would
				// misread as an option.
				for _, option := range []string{"-foo", "--format=json"} {
					t.Run(param+"/"+option, func(t *testing.T) {
						rr := doRequest(t, mux, "GET", url(param, option))
						if rr.Code != http.StatusBadRequest {
							t.Fatalf("%s=%q: status = %d, want 400; body: %s",
								param, option, rr.Code, rr.Body.String())
						}
					})
				}

				for _, ch := range invalidBranchChars {
					value, ok := encoded[ch]
					if !ok {
						value = tgt.ref(ch)
					}
					t.Run(param+"/"+strings.TrimSpace(ch), func(t *testing.T) {
						rr := doRequest(t, mux, "GET", url(param, value))
						if rr.Code != http.StatusBadRequest {
							t.Fatalf("%s=%q: status = %d, want 400; body: %s",
								param, value, rr.Code, rr.Body.String())
						}
					})
				}
			}

			for _, acc := range tgt.accepted {
				t.Run("accepted/"+acc.query, func(t *testing.T) {
					rr := doRequest(t, mux, "GET", url("", acc.query))
					if rr.Code != http.StatusOK {
						t.Fatalf("query %q: status = %d, want 200; body: %s", acc.query, rr.Code, rr.Body.String())
					}
					acc.check(t, rr)
				})
			}
		})
	}
}

// --- HandleDiff: one grammar, with and without the flag ---

// TestHandleProjectDiffAnswersTheParsedGrammar is the wire contract for the single diff grammar on
// GET /projects/{id}/git/diff. The panel renders parsed files and never re-parses patch text, so
// this route answers git.ParseUnifiedDiff's shape unconditionally: `?parsed=1` is a contract every
// diff path honours, and here there is no raw mode for it to switch on, which is why both spellings
// must produce the same body. `git diff` exits 1 when there are changes, and that exit code must not
// be read as a failure. An unchanged tree is [] and never null, because the client reads
// files.length off it and an unchanged working tree is the ordinary case rather than an error.
func TestHandleProjectDiffAnswersTheParsedGrammar(t *testing.T) {
	for _, query := range []string{"", "parsed=1"} {
		t.Run("working tree changes/"+strings.TrimSuffix(query, "=1"), func(t *testing.T) {
			h, s, _, _ := newTestGitHandler(t)

			projPath := t.TempDir()
			initGitRepo(t, projPath)
			createFile(t, projPath, "tracked.txt", "one\ntwo\nthree\n")
			testgit.Git(t, projPath, "add", "tracked.txt")
			testgit.Git(t, projPath, "commit", "-m", "base")
			// Modify without committing: the default `git diff` (working tree vs index) now has content.
			createFile(t, projPath, "tracked.txt", "one\nTWO\nthree\nfour\n")

			if err := s.CreateProject(&types.Project{ID: "diff-parsed-proj", Name: "diff-parsed-proj", Path: projPath}); err != nil {
				t.Fatal(err)
			}
			mux := newTestGitMux(h)

			rr := doRequest(t, mux, "GET", "/api/projects/diff-parsed-proj/git/diff?"+query)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (git diff exits 1 when there are changes): %s", rr.Code, rr.Body.String())
			}
			if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			var files []git.TrackedDiffFile
			if err := json.Unmarshal(rr.Body.Bytes(), &files); err != nil {
				t.Fatalf("body is not []TrackedDiffFile: %v\n%s", err, rr.Body.String())
			}
			if len(files) != 1 {
				t.Fatalf("got %d files, want 1: %s", len(files), rr.Body.String())
			}
			f := files[0]
			if f.Path != "tracked.txt" {
				t.Errorf("path = %q, want tracked.txt", f.Path)
			}
			if f.Added != 2 || f.Removed != 1 {
				t.Errorf("added/removed = %d/%d, want 2/1", f.Added, f.Removed)
			}
			if len(f.Rows) == 0 {
				t.Error("rows is empty; the client would render a file with no content")
			}
			// The JSON field names are the frontend's contract (frontend/src/lib/types/diff.ts DiffFile).
			if !strings.Contains(rr.Body.String(), `"lineNumber"`) || !strings.Contains(rr.Body.String(), `"variant"`) {
				t.Errorf("response does not use the DiffFile/DiffRow field names the client binds to: %s", rr.Body.String())
			}
		})
	}

	t.Run("an unchanged tree is an empty array, not null", func(t *testing.T) {
		h, s, _, _ := newTestGitHandler(t)
		projPath := t.TempDir()
		initGitRepo(t, projPath)
		if err := s.CreateProject(&types.Project{ID: "diff-parsed-empty", Name: "diff-parsed-empty", Path: projPath}); err != nil {
			t.Fatal(err)
		}

		rr := doRequest(t, newTestGitMux(h), "GET", "/api/projects/diff-parsed-empty/git/diff?parsed=1")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
		}
		if body := strings.TrimSpace(rr.Body.String()); body != "[]" {
			t.Errorf("body = %q, want []", body)
		}
	})
}

// TestHandleTaskLog_FullSharesProjectResponse locks the ?full=1 shape that
// HandleLog and HandleTaskLog now produce from one code path
// (writeFullGitLog): every commit field, the body between the BODY markers,
// hasMore paging and the current branch. The task endpoint had no full=1
// coverage before, so this is what proves the shared implementation still
// serves a worktree exactly like a project checkout.
func TestHandleTaskLog_FullSharesProjectResponse(t *testing.T) {
	h, s, _, worktreesDir := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	// A second commit with a multi-line body exercises the %b extraction.
	createFile(t, projPath, "second.go", "package main\n")
	mustRun(t, projPath, "git", "add", "second.go")
	mustRun(t, projPath, "git", "commit", "-m", "Add second file", "-m", "Body line one\nBody line two")

	proj := &types.Project{ID: "tasklogfull-proj", Name: "tasklogfull-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	task := &types.Task{
		ID:        "tasklogfull-task",
		ProjectID: proj.ID,
		Title:     "log full",
		Status:    types.StatusInProgress,
		Branch:    "main",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	createWorktreeAt(t, projPath, filepath.Join(worktreesDir, proj.ID, task.ID), "main")

	mux := newTestGitMux(h)

	rr := doRequest(t, mux, "GET", "/api/projects/tasklogfull-proj/tasks/tasklogfull-task/git/log?full=1&limit=1")
	if rr.Code != http.StatusOK {
		t.Fatalf("task full log: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var taskLog types.GitLogResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &taskLog); err != nil {
		t.Fatalf("decode task full log: %v (%s)", err, rr.Body.String())
	}
	if len(taskLog.Commits) != 1 {
		t.Fatalf("expected 1 commit with limit=1, got %d", len(taskLog.Commits))
	}
	if !taskLog.HasMore {
		t.Error("expected has_more=true when more commits exist than the limit")
	}
	top := taskLog.Commits[0]
	if top.Hash == "" || top.Short == "" || top.Author == "" || top.Date == "" {
		t.Errorf("expected hash/short/author/date populated, got %+v", top)
	}
	if top.Subject != "Add second file" {
		t.Errorf("subject = %q, want %q", top.Subject, "Add second file")
	}
	if !strings.Contains(top.Body, "Body line one") || !strings.Contains(top.Body, "Body line two") {
		t.Errorf("body = %q, want both body lines", top.Body)
	}
	if len(top.Parents) != 1 {
		t.Errorf("expected 1 parent on the second commit, got %v", top.Parents)
	}
	if taskLog.CurrentBranch == "" {
		t.Error("expected current_branch to be resolved in the worktree")
	}

	// The project endpoint reads the same repo and must agree exactly.
	rr2 := doRequest(t, mux, "GET", "/api/projects/tasklogfull-proj/git/log?full=1&limit=1")
	if rr2.Code != http.StatusOK {
		t.Fatalf("project full log: expected 200, got %d: %s", rr2.Code, rr2.Body.String())
	}
	var projLog types.GitLogResponse
	if err := json.Unmarshal(rr2.Body.Bytes(), &projLog); err != nil {
		t.Fatalf("decode project full log: %v (%s)", err, rr2.Body.String())
	}
	if len(projLog.Commits) != 1 || projLog.Commits[0].Hash != top.Hash {
		t.Errorf("project log top commit = %+v, want same hash as task log (%s)", projLog.Commits, top.Hash)
	}
	if projLog.HasMore != taskLog.HasMore {
		t.Errorf("project has_more=%v, task has_more=%v — must agree", projLog.HasMore, taskLog.HasMore)
	}
}

// --- Character validation tests ---
// These tests verify that characters not in the allowed set (letters, digits,
// -, _, ., /, +) are rejected with 400 Bad Request.

var invalidBranchChars = []string{"~", "^", ":", "?", "*", "[", "\\", " ", "feat..branch"}

// --- A path starting with '-' is a filename, not a switch ---

// TestHandleProjectStagePaths_DashPrefixedPathIsAFileNotASwitch: every path rides behind `--`, so
// git reads it as a name. Both directions are asserted — a dash-named file really reaches the
// index, and a path spelled like a switch is refused as a missing file rather than obeyed as one.
// The ignored file is the witness for the second half: only a force-add could have staged it.
//
// The task route stages through the same stagePaths and needs no copy of these two rows;
// TestHandleStagePaths_StagesEveryPathInOneRequest (git_stage_test.go) is what pins the directory
// that batch runs in.
func TestHandleProjectStagePaths_DashPrefixedPathIsAFileNotASwitch(t *testing.T) {
	for _, tt := range []struct {
		name string
		// plant writes what the repository holds before the request names it.
		plant func(t *testing.T, repoPath string)
		path  string
		// wantApplied/wantFailed are the answer's two lists.
		wantApplied []string
		wantFailed  []string
		// index asserts what the index holds afterwards.
		index func(*testing.T, []string)
	}{
		{
			name:        "a dash-named file is staged",
			plant:       func(t *testing.T, repoPath string) { createFile(t, repoPath, "-dash-file.txt", "content\n") },
			path:        "-dash-file.txt",
			wantApplied: []string{"-dash-file.txt"},
			index: func(t *testing.T, staged []string) {
				t.Helper()
				for _, f := range staged {
					if f == "-dash-file.txt" {
						return
					}
				}
				t.Errorf("expected %q to be staged, got: %v", "-dash-file.txt", staged)
			},
		},
		{
			name: "a path spelled like a switch is refused as a path",
			plant: func(t *testing.T, repoPath string) {
				createFile(t, repoPath, ".gitignore", "ignored.txt\n")
				createFile(t, repoPath, "ignored.txt", "only a force-add would stage this\n")
			},
			path:       "-f",
			wantFailed: []string{"-f"},
			index: func(t *testing.T, staged []string) {
				t.Helper()
				if len(staged) != 0 {
					t.Errorf("-f reached git as a flag: the index holds %v", staged)
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repoPath := initGitRepoTemp(t)
			s := gitTestStore(t)
			if err := s.CreateProject(&types.Project{ID: "test-proj", Name: "test", Path: repoPath}); err != nil {
				t.Fatalf("create project: %v", err)
			}
			tt.plant(t, repoPath)

			h := &GitHandler{store: s}
			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/projects/{projectID}/git/stage", h.HandleProjectStagePaths)

			req := newStageRequest(t, "/api/projects/test-proj/git/stage", true, tt.path)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			answer := decodeBatchAnswer(t, rec)
			if strings.Join(answer.Applied, "|") != strings.Join(tt.wantApplied, "|") {
				t.Errorf("applied = %v, want %v", answer.Applied, tt.wantApplied)
			}
			if len(answer.Failed) != len(tt.wantFailed) {
				t.Fatalf("failed = %v, want %v", answer.Failed, tt.wantFailed)
			}
			for i, f := range answer.Failed {
				if f.Path != tt.wantFailed[i] {
					t.Errorf("failed[%d] = %q, want %q", i, f.Path, tt.wantFailed[i])
				}
			}
			tt.index(t, stagedFiles(t, repoPath))
		})
	}
}

// --- HandleCheckout tests ---

func TestHandleCheckout_DashPrefix(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "checkout-proj", Name: "checkout-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/git/checkout", h.HandleCheckout)

	// Dash-prefixed branch → 400
	body := strings.NewReader(`{"branch": "-foo"}`)
	req := httptest.NewRequest("POST", "/api/projects/checkout-proj/git/checkout", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for dash-prefixed checkout branch, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleBranchesNamesTheWorktreeHoldingEachRef is the branch listing's holder contract: the
// screen decides what it can check out from the holder the route reports, never from the ref's name.
// One checkout, one worktree per held ref, and the task rows behind some of them: a branch the
// project itself has checked out is not a holder, a free branch names none, a branch another
// worktree holds names that worktree, a held task branch names the task whose row owns it (a row
// with nothing to open is a dead row), and a held branch no task row owns names no task.
func TestHandleBranchesNamesTheWorktreeHoldingEachRef(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	testgit.Git(t, projPath, "branch", "free-branch")
	head := strings.TrimSpace(testgit.GitOutput(t, projPath, "rev-parse", "--abbrev-ref", "HEAD"))
	testgit.Git(t, projPath, "worktree", "add", "-q", "-b", "task/held-there", filepath.Join(t.TempDir(), "task-worktree"), "HEAD")

	if err := s.CreateProject(&types.Project{ID: "branches-holder-proj", Name: "branches-holder-proj", Path: projPath}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(&types.Task{
		ID: "branches-task-row", ProjectID: "branches-holder-proj", Title: "keep", Status: types.StatusBacklog,
		Branch: "task/keep-this-one",
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	testgit.Git(t, projPath, "worktree", "add", "-q", "-b", "task/keep-this-one", filepath.Join(t.TempDir(), "held"), "HEAD")
	// This ref is held by a worktree with no task row behind it.
	testgit.Git(t, projPath, "worktree", "add", "-q", "-b", "unclaimed", filepath.Join(t.TempDir(), "mine"), "HEAD")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects/{projectID}/git/branches", h.HandleBranches)
	rr := doRequest(t, mux, http.MethodGet, "/api/projects/branches-holder-proj/git/branches")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 listing branches, got %d: %s", rr.Code, rr.Body.String())
	}

	var refs []struct {
		Name     string `json:"name"`
		Worktree string `json:"worktree"`
		TaskID   string `json:"task_id"`
	}
	if err := json.UnmarshalRead(rr.Body, &refs); err != nil {
		t.Fatalf("decode branch rows: %v: %s", err, rr.Body.String())
	}
	holder := map[string]string{}
	taskOf := map[string]string{}
	for _, ref := range refs {
		holder[ref.Name] = ref.Worktree
		taskOf[ref.Name] = ref.TaskID
	}

	if _, ok := holder[head]; !ok {
		t.Fatalf("the checked-out branch is missing from the listing: %+v", holder)
	}
	if holder[head] != "" {
		t.Errorf("the checkout itself reported a holder %q; the screen must still be able to show its own branch", holder[head])
	}
	if got, ok := holder["free-branch"]; !ok || got != "" {
		t.Errorf("free-branch: holder %q (present %v), want no holder", got, ok)
	}
	if holder["task/held-there"] == "" {
		t.Errorf("a branch a second worktree holds reported no holder: %+v", holder)
	}
	if taskOf["task/keep-this-one"] != "branches-task-row" {
		t.Errorf("a held task branch naming no task is a row with nothing to open; got %q", taskOf["task/keep-this-one"])
	}
	if taskOf["unclaimed"] != "" {
		t.Errorf("a branch no task row owns must not name a task, got %q", taskOf["unclaimed"])
	}
}

// --- HandlePull / HandlePush ---

func addBareRemote(t *testing.T, projPath string) string {
	t.Helper()
	bare := t.TempDir()
	mustRun(t, "", "git", "init", "--bare", bare)
	testgit.Git(t, projPath, "remote", "add", "origin", bare)
	testgit.Git(t, projPath, "push", "-u", "origin", "HEAD")
	return bare
}

// decodeGitError parses a JSON error response body and returns the message.
func decodeGitError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.UnmarshalRead(rec.Body, &body); err != nil {
		t.Fatalf("decode error response: %v: %s", err, rec.Body.String())
	}
	return body.Error
}

// newPullPushMux wires the pull/push routes exactly like the server does.
func newPullPushMux(h *GitHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/git/pull", h.HandlePull)
	mux.HandleFunc("POST /api/projects/{projectID}/git/push", h.HandlePush)
	return mux
}

// TestHandlePullPush_RoundTripsWithTheRemote: both verbs run through the one handleGitRemote path,
// and the answer means the repository moved — a push leaves the bare remote at the local commit, a
// pull leaves the remote's commit in the working tree.
func TestHandlePullPush_RoundTripsWithTheRemote(t *testing.T) {
	for _, tt := range []struct {
		name string
		op   string
		// advance moves the side the operation pulls from before the click.
		advance func(t *testing.T, projPath, bare string)
		// verify reads the result back off disk.
		verify func(t *testing.T, projPath string)
	}{
		{
			name: "push carries the local commit to the remote",
			op:   "push",
			advance: func(t *testing.T, projPath, _ string) {
				createFile(t, projPath, "pushed-file.txt", "pushed\n")
				testgit.Git(t, projPath, "add", "pushed-file.txt")
				testgit.Git(t, projPath, "commit", "-m", "commit to push")
			},
			verify: func(t *testing.T, projPath string) {
				bare := strings.TrimSpace(testgit.GitOutput(t, projPath, "remote", "get-url", "origin"))
				local := testgit.GitOutput(t, projPath, "rev-parse", "HEAD")
				if remote := testgit.GitOutput(t, bare, "rev-parse", "HEAD"); remote != local {
					t.Fatalf("remote HEAD %q != local HEAD %q", remote, local)
				}
			},
		},
		{
			name: "pull brings the remote's commit into the working tree",
			op:   "pull",
			advance: func(t *testing.T, _, bare string) {
				// Advance the remote via a second clone: the local repository must be behind.
				advanceRemoteWith(t, bare)
			},
			verify: func(t *testing.T, projPath string) {
				if _, err := os.Stat(filepath.Join(projPath, "remote-file.txt")); err != nil {
					t.Fatalf("expected remote-file.txt after pull: %v", err)
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, s, _, _ := newTestGitHandler(t)
			projPath := t.TempDir()
			initGitRepo(t, projPath)
			bare := addBareRemote(t, projPath)
			if err := s.CreateProject(&types.Project{ID: "pullpush-proj", Name: "pullpush-proj", Path: projPath}); err != nil {
				t.Fatal(err)
			}
			tt.advance(t, projPath, bare)

			req := httptest.NewRequest("POST", "/api/projects/pullpush-proj/git/"+tt.op, nil)
			rec := httptest.NewRecorder()
			newPullPushMux(h).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 for %s, got %d: %s", tt.op, rec.Code, rec.Body.String())
			}
			tt.verify(t, projPath)
		})
	}
}

// A plain directory (not a git repo) gives a deterministic git error whose wording is stable across
// git versions, and both verbs reach it through the same call.
func TestHandlePullPush_NonGitRepoAnswersWithTheGitOutput(t *testing.T) {
	for _, op := range []string{"pull", "push"} {
		t.Run(op, func(t *testing.T) {
			h, s, _, _ := newTestGitHandler(t)
			projPath := t.TempDir()
			if err := s.CreateProject(&types.Project{ID: "git-error-proj", Name: "git-error-proj", Path: projPath}); err != nil {
				t.Fatal(err)
			}

			rec := httptest.NewRecorder()
			newPullPushMux(h).ServeHTTP(rec, httptest.NewRequest("POST", "/api/projects/git-error-proj/git/"+op, nil))

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("expected 500 for %s on non-git dir, got %d: %s", op, rec.Code, rec.Body.String())
			}
			if msg := decodeGitError(t, rec); !strings.Contains(msg, "not a git repository") {
				t.Fatalf("error message should include git output, got %q", msg)
			}
		})
	}
}

// --- git/status ahead/behind ---

// statusAheadBehind reads GET git/status over the route the server registers. The status code and the
// body shape are checked here so a handler that stopped answering cannot pass on two zero values.
func statusAheadBehind(t *testing.T, h *GitHandler, id string) (ahead, behind int) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects/{projectID}/git/status", h.HandleStatus)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/projects/"+id+"/git/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from git/status, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Ahead  *int `json:"ahead"`
		Behind *int `json:"behind"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status body: %v: %s", err, rec.Body.String())
	}
	if body.Ahead == nil || body.Behind == nil {
		t.Fatalf("status body carries no ahead/behind pair: %s", rec.Body.String())
	}
	return *body.Ahead, *body.Behind
}

// storeGitProject stores the project row the route helpers above resolve.
func storeGitProject(t *testing.T, s *store.Store, id, projPath string) {
	t.Helper()
	if err := s.CreateProject(&types.Project{ID: id, Name: id, Path: projPath}); err != nil {
		t.Fatal(err)
	}
}

// postGitFetch runs git/fetch over the route the server registers and returns its answer: whether the
// pair means anything now, and the pair.
func postGitFetch(t *testing.T, h *GitHandler, id string) (ok bool, ahead, behind int) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/git/fetch", h.HandleFetch)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/projects/"+id+"/git/fetch", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from git/fetch, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		OK     *bool `json:"ok"`
		Ahead  *int  `json:"ahead"`
		Behind *int  `json:"behind"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode git/fetch body: %v: %s", err, rec.Body.String())
	}
	if body.OK == nil || body.Ahead == nil || body.Behind == nil {
		t.Fatalf("git/fetch body carries no ok/ahead/behind: %s", rec.Body.String())
	}
	return *body.OK, *body.Ahead, *body.Behind
}

// advanceRemoteWith commits on a second clone and pushes, so the remote holds a commit this
// repository has not fetched yet.
func advanceRemoteWith(t *testing.T, bare string) {
	t.Helper()
	clone := t.TempDir()
	testgit.Git(t, "", "clone", bare, clone)
	createFile(t, clone, "remote-file.txt", "from remote\n")
	testgit.Git(t, clone, "add", "remote-file.txt")
	testgit.Git(t, clone, "commit", "-m", "remote commit")
	testgit.Git(t, clone, "push")
}

// TestHandleStatus_CountsAgainstTheUpstream: the pair is counted against refs/remotes/…, which is
// what the three shapes a person can have are read from — unpushed work, work fetched from the
// remote, and no upstream at all, which answers 0/0 rather than an unresolvable-ref error.
func TestHandleStatus_CountsAgainstTheUpstream(t *testing.T) {
	for _, tt := range []struct {
		name       string
		withRemote bool
		advance    func(t *testing.T, projPath, bare string)
		ahead      int
		behind     int
	}{
		{
			name:       "one unpushed commit",
			withRemote: true,
			advance: func(t *testing.T, projPath, _ string) {
				createFile(t, projPath, "local.txt", "local\n")
				testgit.Git(t, projPath, "add", "local.txt")
				testgit.Git(t, projPath, "commit", "-m", "local commit")
			},
			ahead: 1,
		},
		{
			name:       "one commit fetched from the remote",
			withRemote: true,
			advance: func(t *testing.T, projPath, bare string) {
				// The fetch is what moves refs/remotes/…, which is where the count is read from.
				advanceRemoteWith(t, bare)
				testgit.Git(t, projPath, "fetch", "origin")
			},
			behind: 1,
		},
		{
			name: "a local-only repository has nothing to be ahead or behind",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, s, _, _ := newTestGitHandler(t)
			projPath := t.TempDir()
			initGitRepo(t, projPath)
			bare := ""
			if tt.withRemote {
				bare = addBareRemote(t, projPath)
			}
			if tt.advance != nil {
				tt.advance(t, projPath, bare)
			}

			storeGitProject(t, s, "status-proj", projPath)
			if ahead, behind := statusAheadBehind(t, h, "status-proj"); ahead != tt.ahead || behind != tt.behind {
				t.Fatalf("got ahead=%d behind=%d, want ahead=%d behind=%d", ahead, behind, tt.ahead, tt.behind)
			}
		})
	}
}

// A remote that moved since the last fetch reads 0 until git/fetch runs.
func TestHandleFetch_MovesTheBehindCountToWhatTheRemoteHolds(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	bare := addBareRemote(t, projPath)

	clone := t.TempDir()
	testgit.Git(t, "", "clone", bare, clone)
	createFile(t, clone, "remote-file.txt", "from remote\n")
	testgit.Git(t, clone, "add", "remote-file.txt")
	testgit.Git(t, clone, "commit", "-m", "remote commit")
	testgit.Git(t, clone, "push")

	storeGitProject(t, s, "fetch-proj", projPath)
	if ahead, behind := statusAheadBehind(t, h, "fetch-proj"); ahead != 0 || behind != 0 {
		t.Fatalf("before any fetch: got ahead=%d behind=%d, want 0/0", ahead, behind)
	}
	ok, ahead, behind := postGitFetch(t, h, "fetch-proj")
	if !ok || ahead != 0 || behind != 1 {
		t.Fatalf("git/fetch answered ok=%v ahead=%d behind=%d, want ok=true ahead=0 behind=1", ok, ahead, behind)
	}
	if a, b := statusAheadBehind(t, h, "fetch-proj"); a != ahead || b != behind {
		t.Fatalf("git/status says ahead=%d behind=%d after the fetch answered %d/%d", a, b, ahead, behind)
	}
}

func TestHandleFetch_WithoutUpstreamDeclinesWithoutAFetch(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	storeGitProject(t, s, "fetch-noupstream-proj", projPath)

	// Nothing to count against, so the answer comes before any remote is contacted: FETCH_HEAD is what a
	// fetch leaves behind.
	if ok, _, _ := postGitFetch(t, h, "fetch-noupstream-proj"); ok {
		t.Fatal("git/fetch reported a fresh count for a local-only repository")
	}
	if _, err := os.Stat(filepath.Join(projPath, ".git", "FETCH_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("git/fetch contacted a remote that does not exist (FETCH_HEAD err=%v)", err)
	}
}
