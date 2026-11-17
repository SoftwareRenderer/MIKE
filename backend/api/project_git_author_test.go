package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"mike/backend/types"
)

// storedGitAuthor reads the two identity columns back off the row, which is what a refusal
// must leave untouched.
func storedGitAuthor(t *testing.T, h *ProjectHandler, projID string) (string, string) {
	t.Helper()
	p, err := h.store.GetProject(projID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	return p.GitAuthorName, p.GitAuthorEmail
}

// TestUpdateProjectGitAuthor: each half is written on its own, the response carries what the row now
// holds, and a request that never mentions an identity leaves the stored one standing.
func TestUpdateProjectGitAuthor(t *testing.T) {
	h, projID := testProjectHandler(t)

	rr := putProject(t, h, projID, `{"git_author_name":"Ada Lovelace","git_author_email":"ada@example.com"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT the pair: status = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	var answered types.Project
	if err := json.Unmarshal(rr.Body.Bytes(), &answered); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if answered.GitAuthorName != "Ada Lovelace" || answered.GitAuthorEmail != "ada@example.com" {
		t.Errorf("response carries %q <%q>, want %q <%q>", answered.GitAuthorName, answered.GitAuthorEmail, "Ada Lovelace", "ada@example.com")
	}
	if name, email := storedGitAuthor(t, h, projID); name != "Ada Lovelace" || email != "ada@example.com" {
		t.Errorf("stored %q <%q>", name, email)
	}

	// One half per request: the other is what a previous save left.
	if rr := putProject(t, h, projID, `{"git_author_email":"ada@lovelace.dev"}`); rr.Code != http.StatusOK {
		t.Fatalf("PUT the email alone: status = %d (%s)", rr.Code, rr.Body.String())
	}
	if name, email := storedGitAuthor(t, h, projID); name != "Ada Lovelace" || email != "ada@lovelace.dev" {
		t.Errorf("after the email-only write: %q <%q>", name, email)
	}

	// The whitespace around a typed value belongs to the form.
	if rr := putProject(t, h, projID, `{"git_author_name":"  Ada L  "}`); rr.Code != http.StatusOK {
		t.Fatalf("PUT a padded name: status = %d (%s)", rr.Code, rr.Body.String())
	}
	if name, _ := storedGitAuthor(t, h, projID); name != "Ada L" {
		t.Errorf("a padded name stored %q, want it trimmed", name)
	}

	// Empty clears, and only the half that was named.
	if rr := putProject(t, h, projID, `{"git_author_email":""}`); rr.Code != http.StatusOK {
		t.Fatalf("PUT an empty email: status = %d (%s)", rr.Code, rr.Body.String())
	}
	if name, email := storedGitAuthor(t, h, projID); name != "Ada L" || email != "" {
		t.Errorf("after clearing the email: %q <%q>", name, email)
	}

	if rr := putProject(t, h, projID, `{"name":"Renamed"}`); rr.Code != http.StatusOK {
		t.Fatalf("PUT name: status = %d (%s)", rr.Code, rr.Body.String())
	}
	if name, email := storedGitAuthor(t, h, projID); name != "Ada L" || email != "" {
		t.Errorf("an update that never mentioned the identity changed it to %q <%q>", name, email)
	}
}

// TestUpdateProjectGitAuthorRefuses: an address net/mail will not parse, and control text in a name,
// are each refused with 400 naming the field — and a request refused partway writes neither half.
func TestUpdateProjectGitAuthorRefuses(t *testing.T) {
	h, projID := testProjectHandler(t)
	if rr := putProject(t, h, projID, `{"git_author_name":"Ada Lovelace","git_author_email":"ada@example.com"}`); rr.Code != http.StatusOK {
		t.Fatalf("seed the identity: status = %d (%s)", rr.Code, rr.Body.String())
	}

	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"git_author_email":"not an address"}`, "git_author_email"},
		{`{"git_author_email":"Ada Lovelace <ada@example.com>"}`, "git_author_email"},
		{`{"git_author_email":"a@b.c; d@e.f"}`, "git_author_email"},
		{`{"git_author_name":"Ada\nLovelace"}`, "git_author_name"},
		{`{"git_author_name":"` + strings.Repeat("a", 256) + `"}`, "git_author_name"},
	} {
		rr := putProject(t, h, projID, tc.body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("PUT %s: status = %d, want 400 (%s)", tc.body, rr.Code, rr.Body.String())
			continue
		}
		if !strings.Contains(rr.Body.String(), tc.want) {
			t.Errorf("PUT %s: refusal %q does not name the field", tc.body, rr.Body.String())
		}
		if name, email := storedGitAuthor(t, h, projID); name != "Ada Lovelace" || email != "ada@example.com" {
			t.Errorf("PUT %s left the row at %q <%q>", tc.body, name, email)
		}
	}

	// A request carrying one good half and one bad half writes neither.
	rr := putProject(t, h, projID, `{"git_author_name":"Grace Hopper","git_author_email":"nope"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PUT a mixed pair: status = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
	if name, email := storedGitAuthor(t, h, projID); name != "Ada Lovelace" || email != "ada@example.com" {
		t.Errorf("a refused request wrote the good half: row is %q <%q>", name, email)
	}
}
