package api

// Creating an extension makes a directory, a repository, a project, a row and a task at once, so these
// tests are about what is left behind: a refusal leaves nothing, and a success leaves the four objects
// agreeing about the name.

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"mike/backend/extensions"
	"mike/backend/fsutil"
	"mike/backend/git"
	"mike/backend/store"
	"mike/backend/testutil/testgit"
	"mike/backend/testutil/teststore"
	"mike/backend/types"
)

func scaffoldBundle() fstest.MapFS {
	return fstest.MapFS{
		"README.md": {Data: []byte("# {{NAME}}\n")},
		"AGENTS.md": {Data: []byte("rules for {{NAME}}\n")},
		".agents/skills/extension-authoring/SKILL.md": {Data: []byte("---\nname: extension-authoring\n---\n")},
	}
}

// scaffoldHandler wires the create path over a store of its own. The task handler is left unwired:
// starting a turn is the executor's decision, and these tests are about what the create writes.
func scaffoldHandler(t *testing.T) (*ExtensionScaffoldHandler, *store.Store, *extensions.ExtensionStore, string) {
	t.Helper()
	// The commit is part of the create, so the test supplies the identity the server would have been
	// stamped with at boot rather than inheriting whoever runs the suite.
	name, email := git.AuthorIdentity()
	t.Cleanup(func() { git.SetAuthorIdentity(name, email) })
	git.SetAuthorIdentity("MIKE Operator", "operator@example.invalid")

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s := teststore.StoreIn(t, dbPath)
	cfgStore := teststore.ConfigStore(t, s, dbPath)
	projects := &ProjectHandler{
		store:       s,
		configStore: cfgStore,
		hostRoot:    fsutil.NewHostRoot(true, os.TempDir()),
	}
	extRoot := filepath.Join(dir, "extensions")
	extStore := extensions.NewExtensionStore(s, nil)
	if err := extStore.InitSchema(); err != nil {
		t.Fatal(err)
	}
	extH := extensions.NewExtensionHandler(extStore, nil, nil, &extensions.ExtensionConfig{ExtensionsDir: extRoot})
	extH.SetAuthoringBundle(scaffoldBundle())
	h := NewExtensionScaffoldHandler(ExtensionScaffoldDeps{
		Store:       s,
		Projects:    projects,
		Extensions:  extH,
		ConfigStore: cfgStore,
	})
	return h, s, extStore, extRoot
}

func postScaffold(t *testing.T, h *ExtensionScaffoldHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/scaffold", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.HandleScaffold(rec, req)
	return rec
}

func TestScaffoldRefusesAMissingNameOrDescription(t *testing.T) {
	h, s, _, extRoot := scaffoldHandler(t)
	for _, body := range []string{`{}`, `{"name":"  "}`, `{"name":"My Ext"}`, `{"name":"My Ext","description":"  "}`} {
		rec := postScaffold(t, h, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST %s = %d, want 400: %s", body, rec.Code, rec.Body.String())
		}
	}
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Errorf("a refused create made projects: %+v", projects)
	}
	if entries, err := os.ReadDir(extRoot); err == nil && len(entries) != 0 {
		t.Errorf("a refused create left %d directories in the extensions tree", len(entries))
	}
}

func TestScaffoldMakesRepositoryProjectRowAndTask(t *testing.T) {
	h, s, extStore, extRoot := scaffoldHandler(t)
	rec := postScaffold(t, h, `{"name":"My Ext","description":"count the words on a card"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Project    types.Project        `json:"project"`
		Extension  extensions.Extension `json:"extension"`
		Task       types.Task           `json:"task"`
		Repository string               `json:"repository"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(extRoot, extensions.DevNamespace, "my-ext")
	if out.Repository != repo {
		t.Errorf("repository = %s, want %s", out.Repository, repo)
	}
	for _, name := range []string{"README.md", "AGENTS.md", ".agents/skills/extension-authoring/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(repo, name)); err != nil {
			t.Errorf("%s never reached the scaffolded repository: %v", name, err)
		}
	}
	// One commit before the project row exists: a worktree is cut from HEAD, and an empty
	// repository leaves the first task nothing to branch from.
	if got := testgit.GitOutput(t, repo, "rev-list", "--count", "HEAD"); got != "1" {
		t.Errorf("the repository holds %s commits, want one", got)
	}
	// The task's worktree is the ordinary one — a development task is a task, and the served root
	// must never hold one.
	worktreeRoot := filepath.Join(filepath.Dir(extRoot), ".worktrees")
	if strings.HasPrefix(worktreeRoot, repo) {
		t.Error("the worktrees root sits inside the served extensions tree")
	}

	project, err := s.GetProject(out.Project.ID)
	if err != nil {
		t.Fatalf("the project row is missing: %v", err)
	}
	if project.Path != repo || !project.SandboxEnabled {
		t.Errorf("the project is not the sandboxed project beside that repository: %+v", project)
	}
	if !project.SandboxNetworkIsolation {
		t.Error("an authoring project was given a network a turn does not need")
	}

	row, err := extStore.GetByID(out.Extension.ID)
	if err != nil {
		t.Fatalf("the development row is missing: %v", err)
	}
	if !row.IsDev() || row.ProjectID != project.ID || row.IsLoaded() || row.Enabled {
		t.Errorf("the row is not an unloaded row beside that project: %+v", row)
	}

	task, err := s.GetTask(project.ID, out.Task.ID)
	if err != nil {
		t.Fatalf("the task row is missing: %v", err)
	}
	if !strings.Contains(task.Prompt, "count the words on a card") {
		t.Errorf("the person's own words never reached the agent: %q", task.Prompt)
	}
	if !strings.Contains(task.Prompt, "extension-authoring") {
		t.Error("the prompt lost the instruction that tells the agent to choose the type")
	}
	if strings.Contains(task.Prompt, "count the words on a card") && strings.Count(task.Prompt, "count the words on a card") != 1 {
		t.Error("the person's words were pasted twice")
	}
	// The task's own turn was written as its first message, the same as a card's task carries.
	messages, err := s.GetTaskMessages(task.ID, task.CurrentThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Errorf("the task holds %d messages; the agent starts with the person's request in its thread", len(messages))
	}
}

// Without the worker pair on this card the start at the end of the create has nothing to run.
func TestScaffoldTaskCarriesTheConfiguredDefaults(t *testing.T) {
	h, s, _, _ := scaffoldHandler(t)
	mode := types.ModePlan
	turns := 5
	yes := true
	if err := h.configStore.UpdateConfig(&types.Config{
		Providers: []types.Provider{{ID: 3, Name: "worker", Kind: types.ProviderKindLLM,
			URL: workerURL, Models: []types.ModelConfig{{Name: workerModel}}}},
		WorkerProvider:        "3",
		WorkerModel:           workerModel,
		DefaultOperatingMode:  &mode,
		DefaultMaxFailedTurns: &turns,
		DefaultAutoCommit:     &yes,
	}); err != nil {
		t.Fatalf("configure the worker pair: %v", err)
	}

	rec := postScaffold(t, h, `{"name":"Counted","description":"count the words on a card"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Task types.Task `json:"task"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	task, err := s.GetTask(out.Task.ProjectID, out.Task.ID)
	if err != nil {
		t.Fatalf("the task row is missing: %v", err)
	}
	if task.LlamaServerURL != workerURL || task.ModelName != workerModel {
		t.Errorf("the first task names (%q, %q); the start path needs the configured worker pair (%q, %q)",
			task.LlamaServerURL, task.ModelName, workerURL, workerModel)
	}
	if task.OperatingMode != types.ModePlan {
		t.Errorf("operating_mode = %q, want the configured default %q", task.OperatingMode, types.ModePlan)
	}
	if task.MaxConsecutiveFailedTurns != turns {
		t.Errorf("max_consecutive_failed_turns = %d, want the configured default %d", task.MaxConsecutiveFailedTurns, turns)
	}
	if !task.AutoCommit {
		t.Error("the configured auto-commit default never reached the card")
	}
}

func TestScaffoldRefusesANameAlreadyTaken(t *testing.T) {
	h, s, _, _ := scaffoldHandler(t)
	if rec := postScaffold(t, h, `{"name":"My Ext","description":"first"}`); rec.Code != http.StatusCreated {
		t.Fatalf("first create = %d: %s", rec.Code, rec.Body.String())
	}
	rec := postScaffold(t, h, `{"name":"My Ext!","description":"second"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("second create = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already exists") {
		t.Errorf("the refusal does not say the name is taken: %s", rec.Body.String())
	}
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Errorf("the refused create made a second project: %+v", projects)
	}
}

func TestScaffoldWithoutAProjectPathLeavesNoRepository(t *testing.T) {
	// The project path is the last thing that can refuse a create after the directory exists, so a
	// create whose project never happened has to take the directory back with it.
	h, s, extStore, extRoot := scaffoldHandler(t)
	h.projects = nil // the wiring a build can be missing; the create must refuse rather than crash
	rec := postScaffold(t, h, `{"name":"My Ext","description":"x"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("create with no project path = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(extRoot, extensions.DevNamespace, "my-ext")); !os.IsNotExist(err) {
		t.Error("a create that made no project left its repository claiming the name")
	}
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Errorf("a refused create left a project row: %+v", projects)
	}
	rows, err := extStore.GetByType("")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("a refused create left an extension row: %+v", rows)
	}
}
