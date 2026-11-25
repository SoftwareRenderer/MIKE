package api

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mike/backend/fsutil"
	"mike/backend/store"
	"mike/backend/types"
)

// folderProject builds a row whose folder exists on disk with one file in it, and returns both.
func folderProject(t *testing.T, st *store.Store, id, name string, cloned bool) *types.Project {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir folder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	p := &types.Project{ID: id, Name: name, Path: dir, Cloned: cloned}
	if err := st.CreateProject(p); err != nil {
		t.Fatalf("create project row: %v", err)
	}
	return p
}

// deleteProjectWithBody is a DELETE carrying the optional body; deleteProject
// (project_delete_tasks_test.go) is the body-less form.
func deleteProjectWithBody(t *testing.T, h *ProjectHandler, id string, req types.DeleteProjectRequest) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	httpReq := httptest.NewRequest(http.MethodDelete, "/api/projects/"+id, strings.NewReader(string(raw)))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.SetPathValue("projectID", id)
	rr := httptest.NewRecorder()
	h.HandleByID(rr, httpReq)
	return rr
}

// deleteProjectGivingUpFolder sends what the dialog sends when the folder was ticked and its name typed.
func deleteProjectGivingUpFolder(t *testing.T, h *ProjectHandler, id, typedName string) *httptest.ResponseRecorder {
	t.Helper()
	return deleteProjectWithBody(t, h, id, types.DeleteProjectRequest{DeleteData: true, ConfirmName: typedName})
}

func TestProjectDeleteWithoutTheOptionLeavesTheFolder(t *testing.T) {
	h, _, st := testProjectHandlerWithStore(t)
	p := folderProject(t, st, "stay-proj", "stay", false)

	if rr := deleteProject(t, h, "stay-proj"); rr.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(p.Path, "main.go")); err != nil {
		t.Errorf("the folder did not survive a plain delete: %v", err)
	}
	if _, err := st.GetProject("stay-proj"); err == nil {
		t.Error("the row survived the delete")
	}
}

func TestProjectDeleteWithDataTakesTheFolderItCloned(t *testing.T) {
	h, _, st := testProjectHandlerWithStore(t)
	p := folderProject(t, st, "gone-proj", "gone", true)

	if rr := deleteProjectGivingUpFolder(t, h, "gone-proj", "gone"); rr.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(p.Path); !os.IsNotExist(err) {
		t.Errorf("the folder survived the delete it was named into (%v)", err)
	}
	if _, err := st.GetProject("gone-proj"); err == nil {
		t.Error("the row survived the delete")
	}
}

func TestProjectDeleteWithANameButNoAskLeavesTheFolder(t *testing.T) {
	h, _, st := testProjectHandlerWithStore(t)
	p := folderProject(t, st, "half-proj", "half", true)

	rr := deleteProjectWithBody(t, h, "half-proj", types.DeleteProjectRequest{ConfirmName: "half"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(p.Path, "main.go")); err != nil {
		t.Errorf("the folder did not survive a delete that never asked for it: %v", err)
	}
}

// A refusal has to precede the teardown: a person who typed the wrong name asked for nothing.
func TestProjectDeleteWithDataRefusesANameThatDoesNotMatch(t *testing.T) {
	h, _, st := testProjectHandlerWithStore(t)
	rec := &recordingSandbox{}
	h.sandboxMgr = rec
	p := folderProject(t, st, "wrong-name-proj", "keepme", true)

	rr := deleteProjectGivingUpFolder(t, h, "wrong-name-proj", "keep")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("delete with the wrong name = %d: %s, want 400", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(p.Path, "main.go")); err != nil {
		t.Errorf("the folder did not survive a refused delete: %v", err)
	}
	if _, err := st.GetProject("wrong-name-proj"); err != nil {
		t.Errorf("the row did not survive a refused delete: %v", err)
	}
	if len(rec.stoppedProjects) != 0 {
		t.Errorf("a refused delete still tore down the sandbox: %v", rec.stoppedProjects)
	}
}

func TestProjectDeleteWithDataRefusesAFolderItNeverCloned(t *testing.T) {
	h, _, st := testProjectHandlerWithStore(t)
	p := folderProject(t, st, "added-proj", "added", false)

	rr := deleteProjectGivingUpFolder(t, h, "added-proj", "added")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("delete of an added folder = %d: %s, want 400", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(p.Path, "main.go")); err != nil {
		t.Errorf("a folder MIKE never wrote did not survive: %v", err)
	}
	if _, err := st.GetProject("added-proj"); err != nil {
		t.Errorf("the row did not survive the refused delete: %v", err)
	}
}

func TestProjectDeleteWithDataOfNoRowAnswersNotFound(t *testing.T) {
	h, _, _ := testProjectHandlerWithStore(t)

	rr := deleteProjectGivingUpFolder(t, h, "no-such-proj", "no-such-proj")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("delete of a row that is not there = %d: %s, want 404", rr.Code, rr.Body.String())
	}
}

func TestProjectDeleteWithDataRefusesAFolderOutsideTheJail(t *testing.T) {
	h, _, st := testProjectHandlerWithStore(t)
	inside := t.TempDir()
	t.Setenv("HOME", inside)
	h.hostRoot = fsutil.NewHostRoot(false, inside)
	p := folderProject(t, st, "outside-proj", "outside", true)

	rr := deleteProjectGivingUpFolder(t, h, "outside-proj", "outside")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("delete of a folder outside the jail = %d: %s, want 403", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(p.Path, "main.go")); err != nil {
		t.Errorf("the folder outside the jail did not survive: %v", err)
	}
	if _, err := st.GetProject("outside-proj"); err != nil {
		t.Errorf("the row did not survive the refused delete: %v", err)
	}
}

func TestProjectDeleteWithDataRefusesALinkAtTheFolderName(t *testing.T) {
	h, _, st := testProjectHandlerWithStore(t)
	real := filepath.Join(t.TempDir(), "real-folder")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(t.TempDir(), "linkname")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := st.CreateProject(&types.Project{ID: "link-proj", Name: "link", Path: link, Cloned: true}); err != nil {
		t.Fatalf("create project row: %v", err)
	}

	rr := deleteProjectGivingUpFolder(t, h, "link-proj", "linkname")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("delete of a link = %d: %s, want 500", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(link); err != nil {
		t.Errorf("the link was taken anyway: %v", err)
	}
	if _, err := os.Stat(real); err != nil {
		t.Errorf("the tree behind the link was touched: %v", err)
	}
}

func TestProjectDeleteWithDataRefusesAFolderHoldingTheDatabase(t *testing.T) {
	h, _, st := testProjectHandlerWithStore(t)
	dbDir := h.configStore.DatabaseDir()
	// The fixture row stands on this directory too, so it goes first.
	if err := st.DeleteProject("test-proj"); err != nil {
		t.Fatalf("clear the fixture row: %v", err)
	}
	if err := st.CreateProject(&types.Project{ID: "self-proj", Name: "self", Path: dbDir, Cloned: true}); err != nil {
		t.Fatalf("create project row: %v", err)
	}

	rr := deleteProjectGivingUpFolder(t, h, "self-proj", filepath.Base(dbDir))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("delete of the server's own folder = %d: %s, want 500", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(dbDir); err != nil {
		t.Errorf("the server's own directory did not survive its project's deletion: %v", err)
	}
	if _, err := st.GetProject("self-proj"); err != nil {
		t.Errorf("the row did not survive the refused delete: %v", err)
	}
}

func TestProjectDeleteWithDataRetiresTheBuiltImage(t *testing.T) {
	h, _, st := testProjectHandlerWithStore(t)
	rec := &recordingSandbox{}
	h.sandboxMgr = rec
	p := folderProject(t, st, "img-proj", "img", true)
	if err := st.UpdateProjectSandboxDockerfilePath("img-proj", filepath.Join(p.Path, "Dockerfile")); err != nil {
		t.Fatalf("set the Dockerfile: %v", err)
	}

	if rr := deleteProjectGivingUpFolder(t, h, "img-proj", "img"); rr.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", rr.Code, rr.Body.String())
	}
	if !slices.Equal(rec.removedImages, []string{"img"}) {
		t.Errorf("retired images = %v, want the project's own name", rec.removedImages)
	}
}

func TestProjectDeleteWithoutTheOptionKeepsTheBuiltImage(t *testing.T) {
	h, _, st := testProjectHandlerWithStore(t)
	rec := &recordingSandbox{}
	h.sandboxMgr = rec
	p := folderProject(t, st, "keep-img-proj", "keepimg", true)
	if err := st.UpdateProjectSandboxDockerfilePath("keep-img-proj", filepath.Join(p.Path, "Dockerfile")); err != nil {
		t.Fatalf("set the Dockerfile: %v", err)
	}

	if rr := deleteProject(t, h, "keep-img-proj"); rr.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", rr.Code, rr.Body.String())
	}
	if len(rec.removedImages) != 0 {
		t.Errorf("a delete that kept the folder retired %v", rec.removedImages)
	}
	if _, err := os.Stat(filepath.Join(p.Path, "main.go")); err != nil {
		t.Errorf("the folder did not survive a plain delete: %v", err)
	}
}
