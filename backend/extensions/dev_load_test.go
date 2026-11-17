package extensions

// Loading is the whole test loop: the person presses Try Extension on a task and the app must run
// *that task's* bytes, in the browser and in every server-side reader alike.

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/types"
)

// fakeDevSources is the project table and the worktree layout, as the load path sees them.
type fakeDevSources struct {
	projects  map[string]*types.Project
	tasks     map[string]*types.Task
	worktrees map[string]string
}

func (f fakeDevSources) GetProject(id string) (*types.Project, error) {
	if p, ok := f.projects[id]; ok {
		return p, nil
	}
	return nil, &missingRow{id: id}
}
func (f fakeDevSources) ListProjects() ([]types.Project, error) {
	var out []types.Project
	for _, p := range f.projects {
		out = append(out, *p)
	}
	return out, nil
}
func (f fakeDevSources) GetTask(id string) (*types.Task, error) {
	if t, ok := f.tasks[id]; ok {
		return t, nil
	}
	return nil, &missingRow{id: id}
}
func (f fakeDevSources) WorktreePath(taskID string) (string, error) {
	if p, ok := f.worktrees[taskID]; ok {
		return p, nil
	}
	return "", &missingRow{id: taskID}
}

type missingRow struct{ id string }

func (m *missingRow) Error() string { return "no row for " + m.id }

// devFixture lays out a development project the way the scaffold does: a repository inside the
// namespace, a worktree of the same project outside the served root, and the row between them. The two
// trees carry different content under the same names, so a reader aimed at the wrong one is caught by
// what it reads.
type devFixture struct {
	root     string // the extensions directory the handler serves
	repo     string // <root>/_dev/my-ext
	worktree string // outside the served root
	sources  fakeDevSources
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func newDevFixture(t *testing.T, extRoot string, files map[string]string) *devFixture {
	t.Helper()
	repo := DevRepoDir(extRoot, "my-ext")
	worktree := filepath.Join(t.TempDir(), "my-project", "task1")
	writeTree(t, repo, files)
	writeTree(t, worktree, files)
	return &devFixture{
		root:     extRoot,
		repo:     repo,
		worktree: worktree,
		sources: fakeDevSources{
			projects:  map[string]*types.Project{"my-project": {ID: "my-project", Name: "my-project", Path: repo}},
			tasks:     map[string]*types.Task{"task1": {ID: "task1", ProjectID: "my-project"}},
			worktrees: map[string]string{"task1": worktree},
		},
	}
}

func (f *devFixture) writeWorktree(t *testing.T, name, body string) {
	t.Helper()
	writeTree(t, f.worktree, map[string]string{name: body})
}

func uiManifest(displayName string) string {
	return `{"display_name":"` + displayName + `","description":"d","author":"op","version":"0.1.0",` +
		`"capabilities":{"ui":{"injection_points":["taskCard"],"permissions":["ui.toast"]}}}`
}

func loadDev(t *testing.T, h *ExtensionHandler, id, taskID string) (*Extension, error) {
	t.Helper()
	return h.LoadDev(id, taskID)
}

// TestLoadingATaskServesThatTaskEverywhere: one press points the row at the task's bytes, and the
// browser's URL, the prompt overlay and the LLM mappings all resolve there.
func TestLoadingATaskServesThatTaskEverywhere(t *testing.T) {
	pack := `{"display_name":"My Pack","description":"d","author":"op","version":"0.2.0",` +
		`"capabilities":{"i18n":{"locale":"fr","messages_url":"messages.json","prompts_dir":"prompts"}}}`
	mapper := `{"display_name":"My Mapper","description":"d","author":"op","version":"0.2.0",` +
		`"capabilities":{"llm":{"mappings_file":"mappings.json"}}}`

	h, store, extRoot := devHandler(t)
	packRepo := DevRepoDir(extRoot, "my-pack")
	mapperRepo := DevRepoDir(extRoot, "my-mapper")
	worktree := filepath.Join(t.TempDir(), "my-project", "task1")
	writeTree(t, packRepo, map[string]string{
		"manifest.json":     pack,
		"messages.json":     `{"app.hello":"bonjour"}`,
		"prompts/base.tmpl": "repo {{ .ParentTitle }}",
	})
	writeTree(t, mapperRepo, map[string]string{
		"manifest.json": mapper,
		"mappings.json": `[{"match":"repo","path":"/v1/chat/completions"}]`,
	})
	writeTree(t, worktree, map[string]string{
		"manifest.json":     pack,
		"messages.json":     `{"app.hello":"bonjour"}`,
		"prompts/base.tmpl": "task {{ .ParentTitle }}",
	})
	// The mapper's worktree is the same directory shape under its own name, so both rows load the
	// task's bytes and neither can be reading its repository by accident.
	mapperWorktree := filepath.Join(t.TempDir(), "my-project", "task1")
	writeTree(t, mapperWorktree, map[string]string{
		"manifest.json": mapper,
		"mappings.json": `[{"match":"task","path":"/v1/chat/completions"}]`,
	})
	sources := fakeDevSources{
		projects: map[string]*types.Project{
			"my-project":     {ID: "my-project", Name: "my-pack", Path: packRepo},
			"mapper-project": {ID: "mapper-project", Name: "my-mapper", Path: mapperRepo},
		},
		tasks:     map[string]*types.Task{"task1": {ID: "task1", ProjectID: "my-project"}},
		worktrees: map[string]string{"task1": worktree},
	}
	h.SetDevSources(sources)
	createDevRow(t, store, "dev-pack", "My Pack", "", "")
	// The mapper lives in a repository of its own, so its row names that project and loads through a
	// task of it — a row may never read a stranger's worktree.
	mapperRow := &Extension{ID: "dev-mapper", Name: "My Mapper", Author: "operator", InstallSource: DevInstallSource, ProjectID: "mapper-project"}
	mapperRow.Manifest = ExtensionManifest{DisplayName: "My Mapper", Author: "operator"}
	if err := store.Create(mapperRow); err != nil {
		t.Fatal(err)
	}
	sources.tasks["task2"] = &types.Task{ID: "task2", ProjectID: "mapper-project"}
	sources.worktrees["task2"] = mapperWorktree

	ext, err := loadDev(t, h, "dev-pack", "task1")
	if err != nil {
		t.Fatalf("LoadDev: %v", err)
	}
	if !ext.IsLoaded() || !strings.HasPrefix(ext.LoadedDir, DevNamespace+"/"+devStagingNamespace+"/my-pack/task1") {
		t.Fatalf("the row loads %q, want the task's staging copy", ext.LoadedDir)
	}
	if ext.Version != "0.2.0" || ext.Type != ExtensionTypeI18N {
		t.Errorf("the row kept %s/%s, want the loaded manifest's 0.2.0/%s", ext.Version, ext.Type, ExtensionTypeI18N)
	}

	// The browser's half.
	stored, err := store.GetByID("dev-pack")
	if err != nil {
		t.Fatal(err)
	}
	resp := h.toResponse(stored, nil)
	if resp.ServedURL != "/extensions/"+DevNamespace+"/"+devStagingNamespace+"/my-pack/task1" {
		t.Errorf("served_url = %q, want it under the task's staging directory", resp.ServedURL)
	}

	// The server's halves: the prompt overlay and the LLM mappings must read the same copy the
	// browser was handed, not the repository the row also names.
	all, err := store.GetByType("")
	if err != nil {
		t.Fatal(err)
	}
	overlay := PromptOverlayFor(all, extRoot)
	if !strings.HasSuffix(overlay.Dir, filepath.Join(DevNamespace, devStagingNamespace, "my-pack", "task1", "prompts")) {
		t.Errorf("the prompt overlay reads %q, want the task's staging copy", overlay.Dir)
	}
	body, err := os.ReadFile(filepath.Join(overlay.Dir, "base.tmpl"))
	if err != nil {
		t.Fatalf("the overlay directory holds no template: %v", err)
	}
	if !strings.Contains(string(body), "task") {
		t.Errorf("the overlay reads the repository's template: %s", body)
	}

	if _, err := loadDev(t, h, "dev-mapper", "task2"); err != nil {
		t.Fatalf("loading the mapper's task: %v", err)
	}
	mappings, err := CollectLLMMappings(store, extRoot)
	if err != nil {
		t.Fatalf("CollectLLMMappings: %v", err)
	}
	if len(mappings) == 0 {
		t.Fatal("the loaded extension contributed no mappings, so its files were read from elsewhere")
	}
	mapperLoaded, err := store.GetByID("dev-mapper")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := os.ReadFile(filepath.Join(extRoot, filepath.FromSlash(mapperLoaded.LoadedDir), "mappings.json"))
	if err != nil {
		t.Fatalf("the loaded copy holds no mappings file: %v", err)
	}
	if !strings.Contains(string(loaded), "task") {
		t.Errorf("the loaded copy is the repository's file: %s", loaded)
	}
	if _, err := os.Stat(filepath.Join(extRoot, filepath.FromSlash(stored.LoadedDir), ".git")); !os.IsNotExist(err) {
		t.Error("the loaded copy took the repository's .git directory")
	}
}

func TestLoadingIsIdempotentAndReloadReturnsToTheRepository(t *testing.T) {
	h, store, root := devHandler(t)
	f := newDevFixture(t, root, map[string]string{"manifest.json": uiManifest("My Ext"), "index.html": "<title>ext</title>", "MyExt.js": "export default ()=>()=>{}"})
	h.SetDevSources(f.sources)
	createDevRow(t, store, "dev1", "My Ext", "", "")

	first, err := loadDev(t, h, "dev1", "task1")
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	again, err := loadDev(t, h, "dev1", "task1")
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if again.LoadedDir != first.LoadedDir || again.Version != first.Version || again.Type != first.Type {
		t.Errorf("pressing the button twice changed the row: %q -> %q", first.LoadedDir, again.LoadedDir)
	}
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(again.LoadedDir)))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("the second load left nothing behind")
	}
	// The staging copy is replaced, not appended to: a file the source drops stops being served even
	// though nothing else about the load changed.
	f.writeWorktree(t, "stale.js", "// here for one load")
	if _, err := loadDev(t, h, "dev1", "task1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(again.LoadedDir), "stale.js")); err != nil {
		t.Fatalf("the load did not take the file the source has: %v", err)
	}
	if err := os.Remove(filepath.Join(f.worktree, "stale.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDev(t, h, "dev1", "task1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(again.LoadedDir), "stale.js")); !os.IsNotExist(err) {
		t.Error("a file the source no longer has is still being served")
	}

	// Reload — the same call with no task — puts the row back on the repository.
	repo, err := loadDev(t, h, "dev1", "")
	if err != nil {
		t.Fatalf("loading the repository: %v", err)
	}
	if repo.LoadedDir != DevNamespace+"/my-ext" {
		t.Errorf("reload loaded %q, want the repository", repo.LoadedDir)
	}
}

func TestLoadRefusesATaskThatIsNotTheRowsOrHasNoWorktree(t *testing.T) {
	h, store, root := devHandler(t)
	f := newDevFixture(t, root, map[string]string{"manifest.json": uiManifest("My Ext")})
	h.SetDevSources(f.sources)
	createDevRow(t, store, "dev1", "My Ext", "", "")

	f.sources.tasks["other-task"] = &types.Task{ID: "other-task", ProjectID: "another-project"}
	if _, err := loadDev(t, h, "dev1", "other-task"); err == nil || !strings.Contains(err.Error(), "another project") {
		t.Fatalf("loading another project's task = %v, want a refusal naming the project", err)
	}
	f.sources.tasks["task-no-worktree"] = &types.Task{ID: "task-no-worktree", ProjectID: "my-project"}
	if _, err := loadDev(t, h, "dev1", "task-no-worktree"); err == nil || !strings.Contains(err.Error(), "no worktree") {
		t.Fatalf("loading a task with no worktree = %v, want a refusal that says so", err)
	}
	stored, err := store.GetByID("dev1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.IsLoaded() {
		t.Error("a refused load still pointed the row at bytes it never read")
	}
}

func TestABadManifestLeavesTheLoadedVersionRunning(t *testing.T) {
	h, store, root := devHandler(t)
	f := newDevFixture(t, root, map[string]string{"manifest.json": uiManifest("My Ext"), "index.html": "<title>ext</title>"})
	h.SetDevSources(f.sources)
	createDevRow(t, store, "dev1", "My Ext", "", "")

	if _, err := loadDev(t, h, "dev1", "task1"); err != nil {
		t.Fatalf("first load: %v", err)
	}
	// The agent breaks the manifest: a stylesheet with no url is unusable for a css pack, and a ui
	// injection point that is not a known one is caught by the same list an install runs.
	f.writeWorktree(t, "manifest.json", `{"display_name":"My Ext","description":"d","author":"op","version":"0.3.0","capabilities":{"css":{}}}`)
	if _, err := loadDev(t, h, "dev1", "task1"); err == nil {
		t.Fatal("a manifest with no stylesheet url loaded; install would refuse it")
	}
	stored, err := store.GetByID("dev1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != "0.1.0" || !stored.IsLoaded() {
		t.Errorf("the refusal replaced the working version: %s loaded=%v", stored.Version, stored.IsLoaded())
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(stored.LoadedDir), "index.html")); err != nil {
		t.Errorf("the bytes that were serving are gone: %v", err)
	}
}

func TestLoadedCopyTakesNoDotFileAndNoLink(t *testing.T) {
	h, store, root := devHandler(t)
	f := newDevFixture(t, root, map[string]string{"manifest.json": uiManifest("My Ext"), "index.html": "<title>x</title>"})
	f.writeWorktree(t, ".env", "TOKEN=sekrit")
	if err := os.Symlink(f.worktree, filepath.Join(f.worktree, "loop")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	h.SetDevSources(f.sources)
	createDevRow(t, store, "dev1", "My Ext", "", "")

	if _, err := loadDev(t, h, "dev1", "task1"); err != nil {
		t.Fatalf("load: %v", err)
	}
	dst := filepath.Join(root, filepath.FromSlash(mustLoaded(t, store, "dev1")))
	if _, err := os.Stat(filepath.Join(dst, ".env")); !os.IsNotExist(err) {
		t.Error("a dot file from the worktree was copied into the served tree")
	}
	if _, err := os.Stat(filepath.Join(dst, "loop")); !os.IsNotExist(err) {
		t.Error("a link from the worktree was copied into the served tree")
	}
}

func mustLoaded(t *testing.T, store *ExtensionStore, id string) string {
	t.Helper()
	ext, err := store.GetByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return ext.LoadedDir
}

func TestReloadRouteAnswersTheLoadAndItsFailure(t *testing.T) {
	h, store, root := devHandler(t)
	f := newDevFixture(t, root, map[string]string{"manifest.json": uiManifest("My Ext"), "index.html": "<title>x</title>"})
	h.SetDevSources(f.sources)
	createDevRow(t, store, "dev1", "My Ext", "", "")

	reload := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/extensions/dev1/reload", strings.NewReader(body))
		req.SetPathValue("id", "dev1")
		rec := httptest.NewRecorder()
		h.HandleReloadDev(rec, req, "dev1")
		return rec
	}
	rec := reload(`{"task_id":"task1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reload = %d: %s", rec.Code, rec.Body.String())
	}
	var resp ExtensionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Loaded || resp.ProjectID != "my-project" || resp.TaskID != "task1" {
		t.Errorf("the response hides where the loaded bytes came from: %+v", resp)
	}

	f.writeWorktree(t, "manifest.json", `{"display_name":"My Ext","description":"d","author":"op","version":"0.4.0","capabilities":{"css":{}}}`)
	rec = reload(`{"task_id":"task1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a refused load = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "stylesheet") {
		t.Errorf("the failure the person relays to the agent is not the manifest's problem: %s", rec.Body.String())
	}
}

// TestPruningADeletedTaskTakesItsLoadedCopy: the row is the server's, the repository is the person's,
// and a task that is gone leaves nothing being served in its name.
func TestPruningADeletedTaskTakesItsLoadedCopy(t *testing.T) {
	h, store, root := devHandler(t)
	f := newDevFixture(t, root, map[string]string{"manifest.json": uiManifest("My Ext"), "index.html": "<title>x</title>"})
	h.SetDevSources(f.sources)
	createDevRow(t, store, "dev1", "My Ext", "", "")
	if _, err := loadDev(t, h, "dev1", "task1"); err != nil {
		t.Fatalf("load: %v", err)
	}
	staging := filepath.Join(root, filepath.FromSlash(mustLoaded(t, store, "dev1")))
	if !exists(staging) {
		t.Fatal("the load wrote nothing, so the prune would prove nothing")
	}

	h.PruneDevStaging("task1")

	if exists(staging) {
		t.Error("a deleted task's bytes are still being served")
	}
	stored, err := store.GetByID("dev1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.IsLoaded() || stored.Enabled || stored.TaskID != "" {
		t.Errorf("the row still claims bytes nothing owns: loaded=%q enabled=%v task=%q", stored.LoadedDir, stored.Enabled, stored.TaskID)
	}
	if !exists(filepath.Join(f.repo, "manifest.json")) {
		t.Error("pruning a task's copy deleted the author's repository")
	}
	// Pruning again, or pruning a task no row names, changes nothing and says nothing.
	h.PruneDevStaging("task1")
	h.PruneDevStaging("task-no-row")
}

// TestDevListingShowsARepositoryWhoseRowIsGone: the repository survives its row on purpose, so the
// listing must name it and the attach route must give it a row again.
func TestDevListingShowsARepositoryWhoseRowIsGone(t *testing.T) {
	h, store, root := devHandler(t)
	writeTree(t, DevRepoDir(root, "my-ext"), map[string]string{"manifest.json": uiManifest("My Ext")})
	writeTree(t, DevRepoDir(root, "orphan"), map[string]string{"manifest.json": uiManifest("Orphan")})
	h.SetDevSources(fakeDevSources{
		projects: map[string]*types.Project{
			"my-project":     {ID: "my-project", Name: "my-project", Path: DevRepoDir(root, "my-ext")},
			"orphan-project": {ID: "orphan-project", Name: "orphan", Path: DevRepoDir(root, "orphan")},
		},
		tasks: map[string]*types.Task{},
	})
	createDevRow(t, store, "dev1", "My Ext", "", "")

	list := func() []DevExtension {
		req := httptest.NewRequest(http.MethodGet, "/api/extensions/dev", nil)
		rec := httptest.NewRecorder()
		h.HandleDevExtensions(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("listing = %d: %s", rec.Code, rec.Body.String())
		}
		var out []DevExtension
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	seen := map[string]DevExtension{}
	for _, e := range list() {
		seen[e.Slug] = e
	}
	if got, ok := seen["orphan"]; !ok {
		t.Fatalf("the unrowed repository is not listed, so it has no way back: %+v", seen)
	} else if got.ID != "" || got.Loaded {
		t.Errorf("an unrowed repository claims a row it does not have: %+v", got)
	}
	if got, ok := seen["my-ext"]; !ok || got.ID != "dev1" {
		t.Errorf("the repository with a row is not reported as itself: %+v", seen)
	}
	if _, ok := seen[devStagingNamespace]; ok {
		t.Error("the staging tree was listed as an authoring repository")
	}

	attach := func(slug string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/extensions/dev-attach", strings.NewReader(`{"slug":"`+slug+`"}`))
		rec := httptest.NewRecorder()
		h.HandleDevAttach(rec, req)
		return rec
	}
	rec := attach("orphan")
	if rec.Code != http.StatusCreated {
		t.Fatalf("attaching the orphaned repository = %d: %s", rec.Code, rec.Body.String())
	}
	var attached ExtensionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &attached); err != nil {
		t.Fatal(err)
	}
	if attached.ProjectID != "orphan-project" || attached.InstallSource != DevInstallSource || attached.Loaded {
		t.Errorf("the attached row is not an unloaded row beside that project: %+v", attached)
	}
	for _, e := range list() {
		if e.Slug == "orphan" && e.ID == "" {
			t.Error("the attached repository is still listed as unrowed")
		}
	}
	if rec := attach("orphan"); rec.Code != http.StatusConflict {
		t.Errorf("attaching twice = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if rec := attach("nowhere"); rec.Code != http.StatusNotFound {
		t.Errorf("attaching a repository that does not exist = %d, want 404", rec.Code)
	}
	if rec := attach("../.."); rec.Code != http.StatusBadRequest {
		t.Errorf("attaching a name that is not a slug = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
