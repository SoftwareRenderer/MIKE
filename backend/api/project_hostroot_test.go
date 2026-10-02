package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/config"
	"mike/backend/fsutil"
	"mike/backend/store"
	"mike/backend/types"
)

// Adding a project is a host-wide file operation, so it obeys the picker's rule — inside $HOME
// plus the declared directories, or MIKE_ALLOW_ROOT_FS=1 — and a refused clone leaves nothing.

// jailedProjectHandler builds a handler whose jail is a home directory the test owns, plus a
// second directory outside every root. HOME is pinned here so neither a developer's shell nor the
// suite's environment decides the outcome, and the jail is built jailed: the widening is a
// constructor decision in production, so a test that wants the other side says so with widenHostRoot.
func jailedProjectHandler(t *testing.T) (h *ProjectHandler, s *store.Store, home, outside string) {
	t.Helper()
	home = t.TempDir()
	outside = t.TempDir()
	t.Setenv("HOME", home)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	h = &ProjectHandler{
		store:       st,
		configStore: config.NewConfigStore(st, dbPath, "test", nil, false, false),
		// Home only: no declared directory, so "inside home" is the whole jail.
		hostRoot: fsutil.NewHostRoot(false),
	}
	return h, st, home, outside
}

// widenHostRoot swaps a handler's jail to the widened one. It exists because production decides the
// widening once, at construction: a test that pins both directions needs the second construction, and
// this file is the only place that reaches the field — production cannot swap a jail under a handler.
func widenHostRoot(h *ProjectHandler) { h.hostRoot = fsutil.NewHostRoot(true) }

// repoProjectRequest is the body a create request carries for an existing repository.
func repoProjectRequest(t *testing.T, path string) []byte {
	t.Helper()
	body, err := json.Marshal(types.CreateProjectRequest{
		Path:                    path,
		SandboxEnabled:          true,
		SandboxNetworkIsolation: ptr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func ptr[T any](v T) *T { return &v }

func TestCreateProjectIsJailedToTheHostRoots(t *testing.T) {
	h, s, home, outside := jailedProjectHandler(t)

	// A repository inside home: created.
	inside := filepath.Join(home, "repo")
	createBareRepo(t, inside) // a bare repo is still a git repository
	rr := httptest.NewRecorder()
	h.HandleCreate(rr, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(string(repoProjectRequest(t, inside)))))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create inside home = %d, want 201: %s", rr.Code, rr.Body.String())
	}

	// A repository outside every root: 403 naming the variable, and no project row.
	elsewhere := filepath.Join(outside, "repo")
	createBareRepo(t, elsewhere)
	rr = httptest.NewRecorder()
	h.HandleCreate(rr, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(string(repoProjectRequest(t, elsewhere)))))
	if rr.Code != http.StatusForbidden {
		t.Errorf("create outside the roots = %d, want 403: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), fsutil.AllowRootFSEnv) {
		t.Errorf("refusal must name %s, got %s", fsutil.AllowRootFSEnv, rr.Body.String())
	}
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		if p.Path == elsewhere {
			t.Errorf("a refused create still stored a project row for %s", elsewhere)
		}
	}

	// Widened: the same request goes through, which is how the suites run.
	widenHostRoot(h)
	rr = httptest.NewRecorder()
	h.HandleCreate(rr, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(string(repoProjectRequest(t, elsewhere)))))
	if rr.Code != http.StatusCreated {
		t.Errorf("widened create outside = %d, want 201: %s", rr.Code, rr.Body.String())
	}
}

func TestCloneTargetIsJailedBeforeAnyBytesMove(t *testing.T) {
	h, s, home, outside := jailedProjectHandler(t)

	// Source is a local repository; only the destination is what the jail decides.
	src := makeCommittedRepo(t, filepath.Join(outside, "source-repo"), 1)

	clone := func(parent string) *httptest.ResponseRecorder {
		body, err := json.Marshal(types.CreateProjectRequest{
			Path:                    parent,
			GitURL:                  src,
			SandboxEnabled:          true,
			SandboxNetworkIsolation: ptr(false),
		})
		if err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		h.HandleClone(rr, httptest.NewRequest(http.MethodPost, "/api/projects/clone", strings.NewReader(string(body))))
		return rr
	}

	// Into a directory outside every root: refused, with nothing left on disk.
	jailedParent := filepath.Join(outside, "workspace")
	if err := os.MkdirAll(jailedParent, 0o755); err != nil {
		t.Fatal(err)
	}
	rr := clone(jailedParent)
	if events := readSSEEvents(t, rr.Body); len(events) == 0 || events[0]["type"] != "error" {
		t.Fatalf("clone into a refused directory: %v", events)
	} else if msg, _ := events[0]["message"].(string); !strings.Contains(msg, fsutil.AllowRootFSEnv) {
		t.Errorf("refusal must name %s, got %q", fsutil.AllowRootFSEnv, msg)
	}
	if _, err := os.Stat(filepath.Join(jailedParent, "source-repo")); !os.IsNotExist(err) {
		t.Error("a refused clone left a partial checkout behind")
	}
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Errorf("a refused clone stored %d project rows", len(projects))
	}

	// Inside home: cloned and created.
	homeParent := filepath.Join(home, "workspace")
	if err := os.MkdirAll(homeParent, 0o755); err != nil {
		t.Fatal(err)
	}
	rr = clone(homeParent)
	events := readSSEEvents(t, rr.Body)
	if len(events) == 0 || events[len(events)-1]["type"] != "done" {
		t.Fatalf("clone inside home: %v", events)
	}
	if _, err := os.Stat(filepath.Join(homeParent, "source-repo", ".git")); err != nil {
		t.Errorf("clone inside home did not land: %v", err)
	}

	// Widened: the outside parent is reachable again.
	widenHostRoot(h)
	if events := readSSEEvents(t, clone(jailedParent).Body); len(events) == 0 || events[len(events)-1]["type"] != "done" {
		t.Errorf("widened clone outside = %v, want it to finish", events)
	}
}

// A project already registered outside the jail stays usable: the jail gates ADDING
// one, which is why widening it is not a prerequisite for ordinary work.
func TestExistingProjectOutsideTheJailIsUnaffected(t *testing.T) {
	h, s, _, outside := jailedProjectHandler(t)
	path := filepath.Join(outside, "registered")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProject(&types.Project{ID: "outside-proj", Name: "outside", Path: path}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	h.HandleList(rr, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "outside-proj") {
		t.Errorf("listing projects = %d: %s", rr.Code, rr.Body.String())
	}
	// And deleting it is not gated either — refusing that would strand the row.
	rr = httptest.NewRecorder()
	h.HandleByID(rr, httptest.NewRequest(http.MethodDelete, "/api/projects/outside-proj", nil))
	if rr.Code == http.StatusForbidden {
		t.Errorf("deleting a project outside the jail was refused by the host jail: %s", rr.Body.String())
	}
}
