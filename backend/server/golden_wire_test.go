package server

// Golden wire JSON: the byte-identity anchor for the responses the browser parses.
//
// These four payloads are what the frontend decodes field by field, and the simplification plan
// touches every path that produces one of them (service-locator teardown, context propagation, the
// wire-type split, the diff snapshot). A handler that drops a field, renames one, or answers a
// nested shape differently compiles, passes every existing test, and renders a blank panel — which
// is the failure mode this file exists to make loud.
//
// Volatile content is normalized rather than excluded: a temp-directory path and a wall-clock
// timestamp are the only two things that change between runs, and both are rewritten (see
// normalizeWire). That is verified, not assumed — TestGoldenWire_IsStableAcrossRuns runs the whole
// capture again under a *different* temp directory, so any unnormalized path or timestamp fails
// there. A golden that only passes in the run that generated it is not an anchor.
//
// Regenerate deliberately: go test ./backend/server/ -run TestGoldenWire -update
// and read the diff. A golden update that is not explained by the change that needed it is the
// regression this gate is for.

import (
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/git"
	"kanban/backend/testutil/testgit"
	"kanban/backend/types"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden wire files instead of comparing")

const (
	goldenProjectID = "proj-golden"
	goldenTaskID    = "task-golden"
)

// goldenDir is the testdata directory holding the captured payloads.
func goldenDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("testdata", "golden")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create golden dir: %v", err)
	}
	return dir
}

// volatileKeys name the fields that carry wall-clock time or a per-run token. Everything else in
// these four payloads is a product decision about shape, and shape is exactly what is being pinned.
var volatileKeys = map[string]bool{
	"created_at": true, "updated_at": true, "started_at": true, "completed_at": true,
	"last_used_at": true, "generated_at": true, "timestamp": true, "expires_at": true,
	"moved_at": true, "pinned_at": true, "last_seen": true,
	"csrf_token": true, "token": true, "preview_url": true,
}

// api_key is deliberately NOT volatile. maskAPIKeys answers the fixed string "••••••••••" for any
// stored key, so the masked value is deterministic and pinning it is the assertion that the endpoint
// masks: collapsing it to a marker would print the same golden for a masked key and a leaked one.

// goldenAPIKey is stored in the fixture config so the config golden pins the *masked* key shape. It
// is also checked against the raw body in checkGoldenConfig, so the assertion does not depend on the
// mask staying pinned.

// tmpPrefixes lists the temp roots a run creates: the server's (its database and worktrees live
// under it) and the fixture repo's. Both are absolute paths that appear in wire fields, and neither
// is part of the contract.
func tmpPrefixes(s *Server, fixtureRepo string) []string {
	prefixes := make([]string, 0, 2)
	// The server's own workspace dir, read back from its configuration: the resolved --worktrees-dir
	// decision is handed to NewServer, and no test writes it into this process's environment any more.
	if cfg, err := s.configStore.GetConfig(); err == nil && cfg.WorkspaceDir != "" {
		prefixes = append(prefixes, filepath.Dir(cfg.WorkspaceDir))
	}
	if fixtureRepo != "" {
		prefixes = append(prefixes, filepath.Dir(fixtureRepo))
	}
	return prefixes
}

// normalizeWire makes a response comparable across runs: volatile keys collapse to a marker, and
// every absolute path under a run temp root collapses to <TMP>.
func normalizeWire(v any, tmpRoots []string) any {
	switch node := v.(type) {
	case map[string]any:
		for k, child := range node {
			if volatileKeys[k] {
				node[k] = "<VOLATILE>"
				continue
			}
			node[k] = normalizeWire(child, tmpRoots)
		}
		return node
	case []any:
		for i, child := range node {
			node[i] = normalizeWire(child, tmpRoots)
		}
		return node
	case string:
		for _, root := range tmpRoots {
			if root != "" && strings.Contains(node, root) {
				node = strings.ReplaceAll(node, root, "<TMP>")
			}
		}
		return node
	default:
		return v
	}
}

// checkGolden compares a response body against its golden file, or rewrites it under -update.
// Comparison is on re-marshalled JSON, so key order and whitespace are not part of the contract;
// the presence, name and nesting of every field is.
func checkGolden(t *testing.T, name string, body []byte, tmpRoots []string) {
	t.Helper()

	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("%s: body is not JSON (%v): %s", name, err, truncate(string(body), 300))
	}
	normalized, err := json.MarshalIndent(normalizeWire(decoded, tmpRoots), "", "  ")
	if err != nil {
		t.Fatalf("%s: re-marshal: %v", name, err)
	}
	normalized = append(normalized, '\n')

	path := filepath.Join(goldenDir(t), name+".json")
	if *updateGolden {
		if err := os.WriteFile(path, normalized, 0o644); err != nil {
			t.Fatalf("%s: write golden: %v", name, err)
		}
		t.Logf("%s: golden rewritten", name)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: no golden file (%v) — generate with: go test ./backend/server/ -run TestGoldenWire -update", name, err)
	}
	if string(want) != string(normalized) {
		t.Errorf("%s: wire shape differs from golden %s\n--- golden ---\n%s\n--- now ---\n%s\n"+
			"If this change is intended, re-run with -update and read the diff.", name, path, want, normalized)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// getAuth performs an authenticated GET and returns the body, failing on any non-200.
func getAuth(t *testing.T, s *Server, path string) []byte {
	t.Helper()
	rr := testResponseAuth(t, s, testRequest(http.MethodGet, path, nil))
	if rr.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, rr.Code, truncate(rr.Body.String(), 400))
	}
	return rr.Body.Bytes()
}

// setupGoldenFixture builds the one fixture all four goldens share: a repo with a base commit, a
// done task with a worktree carrying a committed change, plus a second task in another column so
// the board payload has more than one shape to pin.
//
// Fixed IDs and fixed titles keep the payload deterministic; the temp root is what normalizeWire
// collapses. Returns the temp root (for that collapse) and the server.
func setupGoldenFixture(t *testing.T) (tmpRoots []string, s *Server) {
	t.Helper()
	// Set before the server is built: the config store derives its AES key at construction, and a
	// stored provider key is what makes the golden pin the masked-key response and
	// encryption_enabled: true. Self-contained rather than inherited from the shell, so the golden
	// means the same thing on a machine that exports nothing.
	t.Setenv("MIKE_ENCRYPTION_KEY", "golden-wire-test-key")
	s = testServer(t)

	repoDir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("create repo dir: %v", err)
	}
	testgit.Init(t, repoDir)
	testgit.WriteFile(t, repoDir, "README.md", "# base\n")
	testgit.Git(t, repoDir, "add", "README.md")
	testgit.Git(t, repoDir, "commit", "-m", "initial")

	if err := s.store.CreateProject(&types.Project{ID: goldenProjectID, Name: "Golden", Path: repoDir}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// A WorktreeManager of our own: NewServer keeps its worktreeMgr as a local (it is injected into
	// the handlers, not stored on Server), and the fixture only needs git-side effects on the repo.
	wm := git.NewWorktreeManager(s.store, s.configStore)

	// StatusDone from the start, so the diff endpoint takes the done-task snapshot path: that is the
	// branch whose stored body must stay the parsed body (?parsed=1), and the one GET that writes.
	done := &types.Task{
		ID: goldenTaskID, ProjectID: goldenProjectID, Title: "Golden done task",
		Prompt: "add a file", Status: types.StatusDone, Branch: "task/" + goldenTaskID,
	}
	if err := s.store.CreateTask(done); err != nil {
		t.Fatalf("create done task: %v", err)
	}
	if _, _, err := wm.CreateWorktree(goldenProjectID, goldenTaskID, done.Branch, ""); err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	wtPath, err := wm.WorktreePath(goldenTaskID)
	if err != nil {
		t.Fatalf("worktree path: %v", err)
	}
	testgit.WriteFile(t, wtPath, "feature.go", "package feature\n\n// Answer is the change the golden diff pins.\nconst Answer = 42\n")
	testgit.WriteFile(t, wtPath, "README.md", "# base\n\nchanged by the golden task\n")
	testgit.Git(t, wtPath, "add", "-A")
	testgit.Git(t, wtPath, "commit", "-m", "golden change")

	review := &types.Task{
		ID: "task-golden-2", ProjectID: goldenProjectID, Title: "Golden review task",
		Prompt: "second column", Status: types.StatusReview, Branch: "task/task-golden-2",
	}
	if err := s.store.CreateTask(review); err != nil {
		t.Fatalf("create review task: %v", err)
	}
	if err := s.store.CreateTaskMessage(&types.TaskMessage{
		ID: "msg-golden-1", TaskID: goldenTaskID, Role: "user", Content: "add a file",
		ModelName: "gpt-test",
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}

	// Seed configuration so the config golden pins the shapes a bare config omits: a provider with a
	// stored key (the response must carry the mask, never the key), the worker/utility pairs, the
	// *bool preferences at a non-default value, and a shortcut object. Left at defaults the endpoint
	// answers a dozen fields and the golden anchors almost nothing.
	yes, apiKey := true, goldenAPIKey
	plan := types.ModePlan
	disabled, ctxWindow := "", 128000
	cfg, err := s.configStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.Providers = []types.Provider{{
		ID: 1, DisplayName: "Golden Provider", URL: "https://llm.invalid/v1",
		APIKey: &apiKey, Concurrency: 2, InputPrice: 1.5,
		Models: []types.ModelConfig{{Name: "golden-model", ContextWindow: &ctxWindow}},
	}}
	cfg.WorkerProvider, cfg.WorkerModel = "Golden Provider", "golden-model"
	cfg.UtilityProvider, cfg.UtilityModel = "Golden Provider", "golden-model"
	cfg.DefaultAutoCommit, cfg.DefaultOperatingMode = &yes, &plan
	cfg.HideModelBadge, cfg.RunSiblingsParallel, cfg.SortOldestFirst = &yes, &yes, &yes
	cfg.KeyboardShortcuts = &types.KeyboardShortcuts{CreateTask: &disabled}
	if err := s.configStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	return tmpPrefixes(s, repoDir), s
}

// goldenAPIKey is declared here rather than inline so checkGoldenConfig can name it in its failure.
const goldenAPIKey = "sk-golden-plaintext-secret-do-not-echo"

// checkGoldenConfig is checkGolden plus the two assertions that keep the config golden honest: the
// provider list survived (an empty list would make "no key leaked" vacuously true), and the stored
// key never reaches the wire.
func checkGoldenConfig(t *testing.T, body []byte, tmpRoots []string) {
	t.Helper()
	if strings.Contains(string(body), goldenAPIKey) {
		t.Fatalf("GET /api/config echoed the stored provider API key in plaintext")
	}
	var parsed struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("config: decode: %v", err)
	}
	if len(parsed.Providers) == 0 {
		t.Fatalf("config golden has no providers — the no-key-leaked assertion would prove nothing")
	}
	checkGolden(t, "config", body, tmpRoots)
}

func TestGoldenWire(t *testing.T) {
	tmpRoots, s := setupGoldenFixture(t)

	t.Run("config", func(t *testing.T) {
		checkGoldenConfig(t, getAuth(t, s, "/api/config"), tmpRoots)
	})

	t.Run("board", func(t *testing.T) {
		checkGolden(t, "board", getAuth(t, s, "/api/projects/"+goldenProjectID+"/board"), tmpRoots)
	})

	t.Run("task", func(t *testing.T) {
		checkGolden(t, "task", getAuth(t, s, "/api/projects/"+goldenProjectID+"/tasks/"+goldenTaskID), tmpRoots)
	})

	t.Run("task_diff_parsed", func(t *testing.T) {
		path := "/api/projects/" + goldenProjectID + "/tasks/" + goldenTaskID + "/diff?parsed=1"
		checkGolden(t, "task_diff_parsed", getAuth(t, s, path), tmpRoots)
	})
}

// TestGoldenWire_IsStableAcrossRuns proves the normalization is real. A golden written by one run is
// only an anchor if a second run — different temp directory, later clock, new database — reproduces
// it byte for byte. Anything unnormalized (an absolute path, a timestamp, a random id) fails here
// rather than quietly making the goldens self-confirming.
func TestGoldenWire_IsStableAcrossRuns(t *testing.T) {
	if *updateGolden {
		t.Skip("regenerating goldens")
	}
	tmpRoots, s := setupGoldenFixture(t)
	for name, path := range map[string]string{
		"config":           "/api/config",
		"board":            "/api/projects/" + goldenProjectID + "/board",
		"task":             "/api/projects/" + goldenProjectID + "/tasks/" + goldenTaskID,
		"task_diff_parsed": "/api/projects/" + goldenProjectID + "/tasks/" + goldenTaskID + "/diff?parsed=1",
	} {
		t.Run(name, func(t *testing.T) {
			body := getAuth(t, s, path)
			if name == "config" {
				checkGoldenConfig(t, body, tmpRoots)
				return
			}
			checkGolden(t, name, body, tmpRoots)
		})
	}
}
