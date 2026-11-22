package api

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"mike/backend/extensions"
)

// The ReviewBar's Try Extension decides from two fields on the extension listing: a row whose
// install_source is "dev" and whose project_id is the project the task belongs to. A create that writes
// the row but loses either field on the wire leaves every task of that authoring project with no way to
// load what the agent wrote.
func TestScaffoldRowIsListedForTheProjectThatOwnsIt(t *testing.T) {
	h, _, extStore, _, _ := scaffoldHandler(t)
	rec := postScaffold(t, h, `{"name":"My Ext","description":"count the words on a card"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}

	// Decoded as the browser reads it: the field names are the contract, not the Go struct.
	var out struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
		Extension map[string]any `json:"extension"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Project.ID == "" {
		t.Fatal("the create answered no project")
	}
	if got := out.Extension["install_source"]; got != "dev" {
		t.Errorf("the listing's install_source = %v, want dev", got)
	}
	if got := out.Extension["project_id"]; got != out.Project.ID {
		t.Errorf("the listing's project_id = %v, want the created project %v", got, out.Project.ID)
	}

	// The browser reads the listing, not this response, so the row has to be in it too.
	rows, err := extStore.List()
	if err != nil {
		t.Fatal(err)
	}
	var row *extensions.Extension
	for i := range rows {
		if rows[i].ProjectID == out.Project.ID {
			row = &rows[i]
		}
	}
	if row == nil {
		t.Fatalf("no row of the listing names the created project %s", out.Project.ID)
	}
	if row.InstallSource != "dev" {
		t.Errorf("the stored row's install_source = %q, want dev", row.InstallSource)
	}
}
