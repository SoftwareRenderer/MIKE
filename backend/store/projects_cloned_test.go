package store

import "testing"

// The cloned flag is the grant a deletion has to remove the folder at Path, so it must survive every read
// shape, and a row from a file that predates the column must read back as no grant.

func TestProjectClonedRoundTrips(t *testing.T) {
	s := testStore(t)

	written := newProject("proj-cloned", "Cloned", "/tmp/cloned")
	written.Cloned = true
	if err := s.CreateProject(written); err != nil {
		t.Fatalf("create the cloned project: %v", err)
	}
	if err := s.CreateProject(newProject("proj-added", "Added", "/tmp/added")); err != nil {
		t.Fatalf("create the added project: %v", err)
	}

	got, err := s.GetProject("proj-cloned")
	if err != nil {
		t.Fatalf("get the cloned project: %v", err)
	}
	if !got.Cloned {
		t.Error("a row stored cloned read back uncloned")
	}
	if got, err = s.GetProject("proj-added"); err != nil {
		t.Fatalf("get the added project: %v", err)
	}
	if got.Cloned {
		t.Error("a row stored without the flag read back cloned")
	}

	listed, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("ListProjects = %d rows, want 2", len(listed))
	}
	for _, p := range listed {
		want := p.ID == "proj-cloned"
		if p.Cloned != want {
			t.Errorf("%s: cloned = %v, want %v", p.ID, p.Cloned, want)
		}
	}
}
