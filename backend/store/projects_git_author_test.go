package store

import (
	"database/sql"
	"testing"
)

// The two halves decide who every commit in the project names, so each one travels on its own: a row
// that names only the email must not lose it, and clearing one half must leave the other standing.

func TestProjectGitAuthorRoundTrip(t *testing.T) {
	s := testStore(t)

	p := newProject("proj-author", "Author", "/tmp/author")
	p.GitAuthorName = "Ada Lovelace"
	p.GitAuthorEmail = "ada@example.com"
	if err := s.CreateProject(p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	got, err := s.GetProject("proj-author")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if got.GitAuthorName != p.GitAuthorName || got.GitAuthorEmail != p.GitAuthorEmail {
		t.Errorf("create preserved %q <%q>, want %q <%q>", got.GitAuthorName, got.GitAuthorEmail, p.GitAuthorName, p.GitAuthorEmail)
	}

	listed, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(listed) != 1 || listed[0].GitAuthorName != p.GitAuthorName || listed[0].GitAuthorEmail != p.GitAuthorEmail {
		t.Errorf("ListProjects = %+v, want the stored pair back", listed)
	}

	// A project nobody gave an identity to reads back as naming nothing, which is what leaves the
	// half to this server's stamp and then to the repository's own config.
	if err := s.CreateProject(newProject("proj-unset", "Unset", "/tmp/unset")); err != nil {
		t.Fatalf("create the untouched project: %v", err)
	}
	unset, err := s.GetProject("proj-unset")
	if err != nil {
		t.Fatalf("get the untouched project: %v", err)
	}
	if unset.GitAuthorName != "" || unset.GitAuthorEmail != "" {
		t.Errorf("an identity nobody set read as %q <%q>", unset.GitAuthorName, unset.GitAuthorEmail)
	}
}

func TestUpdateProjectGitAuthorWritesOneHalfAtATime(t *testing.T) {
	s := testStore(t)

	p := newProject("proj-author", "Author", "/tmp/author")
	p.GitAuthorEmail = "ada@example.com"
	if err := s.CreateProject(p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	if err := s.UpdateProjectGitAuthorName("proj-author", "Ada Lovelace"); err != nil {
		t.Fatalf("set the name: %v", err)
	}
	got, err := s.GetProject("proj-author")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if got.GitAuthorName != "Ada Lovelace" || got.GitAuthorEmail != "ada@example.com" {
		t.Errorf("after the name write: %q <%q>, want %q <%q>", got.GitAuthorName, got.GitAuthorEmail, "Ada Lovelace", "ada@example.com")
	}

	if err := s.UpdateProjectGitAuthorEmail("proj-author", ""); err != nil {
		t.Fatalf("clear the email: %v", err)
	}
	got, err = s.GetProject("proj-author")
	if err != nil {
		t.Fatalf("get project after clearing: %v", err)
	}
	if got.GitAuthorEmail != "" {
		t.Errorf("clearing the email left %q", got.GitAuthorEmail)
	}
	if got.GitAuthorName != "Ada Lovelace" {
		t.Errorf("clearing the email also changed the name to %q", got.GitAuthorName)
	}

	// An empty half is stored NULL, not the empty string: a row written by a build without the
	// columns and a row whose person cleared the field have to read the same way.
	var stored sql.NullString
	if err := s.db.QueryRow(`SELECT git_author_email FROM projects WHERE id = ?`, "proj-author").Scan(&stored); err != nil {
		t.Fatalf("read stored column: %v", err)
	}
	if stored.Valid {
		t.Errorf("a cleared email stored %q, want NULL", stored.String)
	}
}
