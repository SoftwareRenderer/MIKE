package api

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/sandbox"
	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// A project added from a folder the person already had must answer that deleting it deletes nothing
// outside the board.

func TestAddedProjectIsNotStampedAsCloned(t *testing.T) {
	h, _, st := testProjectHandlerWithStore(t)
	h.dangerZone = sandbox.DangerZone(true)
	repo := testgit.Repo(t)

	body, _ := json.Marshal(types.CreateProjectRequest{Path: repo, Name: "added-proj"})
	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	h.HandleCreate(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `"cloned"`) {
		t.Errorf("a folder this server never wrote answered as its own: %s", rr.Body.String())
	}
	var created types.Project
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	stored, err := st.GetProject(created.ID)
	if err != nil {
		t.Fatalf("read the row back: %v", err)
	}
	if stored.Cloned {
		t.Error("a project added from an existing folder was stored as cloned")
	}
}

func TestHandleCloneStampsTheProjectFolderAsItsOwn(t *testing.T) {
	h, _, st := testProjectHandlerWithStore(t)
	h.dangerZone = sandbox.DangerZone(true)
	tmp := t.TempDir()
	url := makeCommittedRepo(t, filepath.Join(tmp, "upstream"), 1)
	parent := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	body, _ := json.Marshal(types.CreateProjectRequest{Path: parent, GitURL: url})
	req := httptest.NewRequest(http.MethodPost, "/api/projects/clone", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.HandleClone(rr, req)

	var done map[string]any
	for _, payload := range sseDataFrames(rr.Body.String()) {
		var frame map[string]any
		if err := json.Unmarshal([]byte(payload), &frame); err != nil {
			continue
		}
		if frame["type"] == string(types.SSETypeDone) {
			done = frame
		}
	}
	if done == nil {
		t.Fatalf("the stream never reported a project: %s", rr.Body.String())
	}
	project, ok := done["project"].(map[string]any)
	if !ok || project["cloned"] != true {
		t.Fatalf("the cloned project the stream answered with: %v", done["project"])
	}

	stored, err := st.GetProject(project["id"].(string))
	if err != nil {
		t.Fatalf("read the row back: %v", err)
	}
	if !stored.Cloned {
		t.Error("the checkout this server wrote was stored uncloned")
	}
}
