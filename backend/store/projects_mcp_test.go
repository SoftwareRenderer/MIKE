package store

import (
	"database/sql"
	"reflect"
	"testing"
)

// A project's opt-in list decides whether a server's tools reach a turn, so the list has to survive
// every read shape the board uses.

func TestProjectMCPServerIDsRoundTrip(t *testing.T) {
	s := testStore(t)

	p := newProject("proj-mcp", "MCP", "/tmp/mcp")
	p.MCPServerIDs = []string{"a1b2c3d4e5f60708", "1122334455667788"}
	if err := s.CreateProject(p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	got, err := s.GetProject("proj-mcp")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if !reflect.DeepEqual(got.MCPServerIDs, p.MCPServerIDs) {
		t.Errorf("create preserved %v, want %v", got.MCPServerIDs, p.MCPServerIDs)
	}

	listed, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(listed) != 1 || !reflect.DeepEqual(listed[0].MCPServerIDs, p.MCPServerIDs) {
		t.Errorf("ListProjects = %+v, want the enabled pair back", listed)
	}

	if err := s.UpdateProjectMCPServerIDs("proj-mcp", []string{"1122334455667788"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err = s.GetProject("proj-mcp")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if want := []string{"1122334455667788"}; !reflect.DeepEqual(got.MCPServerIDs, want) {
		t.Errorf("update wrote %v, want %v", got.MCPServerIDs, want)
	}

	// An empty list is a decision, not a no-op: a project that enabled everything then
	// unchecked them all must read back as enabling nothing.
	if err := s.UpdateProjectMCPServerIDs("proj-mcp", nil); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	got, err = s.GetProject("proj-mcp")
	if err != nil {
		t.Fatalf("get after clearing: %v", err)
	}
	if len(got.MCPServerIDs) != 0 {
		t.Errorf("clearing left %v, want nothing", got.MCPServerIDs)
	}

	// A stored value the scanner cannot read is an empty list, not a failed project read:
	// the board must still render over one bad column.
	if err := s.updateProjectColumn("proj-mcp", "mcp_server_ids", "not json"); err != nil {
		t.Fatalf("write unreadable value: %v", err)
	}
	if got, err = s.GetProject("proj-mcp"); err != nil {
		t.Fatalf("project read failed over an unreadable list: %v", err)
	}
	if len(got.MCPServerIDs) != 0 {
		t.Errorf("unreadable value read as %v, want nothing", got.MCPServerIDs)
	}

	var stored sql.NullString
	if err := s.db.QueryRow(`SELECT mcp_server_ids FROM projects WHERE id = ?`, "proj-mcp").Scan(&stored); err != nil {
		t.Fatalf("read stored column: %v", err)
	}
	if stored.String != "not json" {
		t.Errorf("stored column = %q, want the value left alone", stored.String)
	}
}
