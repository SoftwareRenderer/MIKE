package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mike/backend/startup"
	"mike/backend/types"
	"mike/pkg/logging"
)

// The Test route reports a verdict, not a fault: a server that is not there is a 200 with a kind. Only a
// form that cannot be dialled at all is a 4xx naming the field.

func postMCPTest(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	return postMCPTestOn(t, testServer(t), body)
}

func postMCPTestOn(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	var answer map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("answer is not JSON (%v): %s", err, rec.Body.String())
	}
	return rec.Code, answer
}

func TestMCPTestRouteReportsAVerdictNotAFault(t *testing.T) {
	code, answer := postMCPTest(t, `{"name":"Dead","url":"http://127.0.0.1:1/mcp","timeout_seconds":1}`)
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200: %v", code, answer)
	}
	if ok, _ := answer["ok"].(bool); ok {
		t.Fatal("nothing is listening on that address")
	}
	if kind, _ := answer["kind"].(string); kind != "unreachable" {
		t.Errorf("kind = %q, want the one that says check the address", kind)
	}
	if message, _ := answer["message"].(string); !strings.Contains(message, "check the address") {
		t.Errorf("message %q does not say what to check", message)
	}
}

func TestMCPTestRouteRefusesAFormThatCannotBeDialled(t *testing.T) {
	for _, body := range []string{
		`{"name":"No URL"}`,
		`{"name":"Wrong transport","url":"stdio://nothing"}`,
		`{"name":"Basic auth","url":"https://u:p@mcp.example.com/mcp"}`,
	} {
		code, answer := postMCPTest(t, body)
		if code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, code)
		}
		if message, _ := answer["error"].(string); message == "" {
			t.Errorf("%s: the 400 carries no sentence: %v", body, answer)
		}
	}
}

// A POST to a route registered with no verb would answer this handler to a GET and skip the CSRF
// middleware, so the verb is the thing being asserted here.
func TestMCPTestRouteIsPostOnly(t *testing.T) {
	s := testServer(t)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/test", nil))
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Errorf("a GET reached the test handler: status %d", rec.Code)
	}
}

// A probe dials the address the browser typed, so under MIKE_RESTRICT_MCP it is the one way to reach an
// address nobody registered. It refuses before dialling.
func TestMCPTestRefusesWhileTheRegistryIsRestricted(t *testing.T) {
	var dialed int
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dialed++
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(fake.Close)

	s := testServerWith(t, func(c *startup.Config) { c.RestrictMCP = true })
	code, answer := postMCPTestOn(t, s, `{"name":"Anything","url":"`+fake.URL+`/mcp"}`)
	if code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %v", code, answer)
	}
	message, _ := answer["error"].(string)
	if !strings.Contains(message, "MIKE_RESTRICT_MCP") {
		t.Errorf("refusal %q does not name the variable", message)
	}
	if dialed != 0 {
		t.Errorf("the probe dialled %d times; it refused after dialling", dialed)
	}
}

// The lock freezes the registry; it does not hide it or stop it being offered, and the only config write
// it refuses is one that changes the registry.
func TestRestrictedRegistryIsListedAndWritableAroundTheFreeze(t *testing.T) {
	s := testServerWith(t, func(c *startup.Config) { c.RestrictMCP = true })
	row, err := json.Marshal([]types.MCPServer{{ID: "srv1", Name: "Search", URL: "https://mcp.example.com/mcp", Enabled: true}})
	if err != nil {
		t.Fatalf("marshal the seeded registry: %v", err)
	}
	if err := s.store.SetConfigValue("mcp_servers", string(row)); err != nil {
		t.Fatalf("seed the registry: %v", err)
	}

	body := mustJSON[map[string]any](t, testResponseAuth(t, s, testRequest(http.MethodGet, "/api/config", nil)).Body)
	if restricted, _ := body["mcp_restricted"].(bool); !restricted {
		t.Error("the config view does not say the registry is restricted")
	}
	servers, _ := body["mcp_servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("a restricted registry listed %d servers, want the stored one still offered", len(servers))
	}

	// The form sends the registry it was shown, unchanged.
	unchanged := map[string]any{"mcp_servers": []any{
		map[string]any{"id": "srv1", "name": "Search", "url": "https://mcp.example.com/mcp", "enabled": true},
	}}
	rec := testResponseAuth(t, s, testRequest(http.MethodPut, "/api/config", unchanged))
	if rec.Code != http.StatusOK {
		t.Fatalf("an unchanged registry was refused with %d: %s", rec.Code, rec.Body)
	}

	// A save from any other tab carries no registry at all.
	rec = testResponseAuth(t, s, testRequest(http.MethodPut, "/api/config", map[string]any{"worker_model": "qwen-7b"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("an unrelated preference was refused with %d: %s", rec.Code, rec.Body)
	}

	rec = testResponseAuth(t, s, testRequest(http.MethodPut, "/api/config", map[string]any{
		"mcp_servers": []any{map[string]any{"name": "Added", "url": "https://added.example.com/mcp"}},
	}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("an added server was refused with %d, want 403: %s", rec.Code, rec.Body)
	}
	if message := mustJSON[map[string]any](t, rec.Body)["error"]; !strings.Contains(fmt.Sprint(message), "MIKE_RESTRICT_MCP") {
		t.Errorf("refusal %v does not name the variable", message)
	}
	stored, err := s.store.GetConfigValue("mcp_servers")
	if err != nil {
		t.Fatalf("read the registry row: %v", err)
	}
	if stored != string(row) {
		t.Errorf("a refused write changed the registry row to %s", stored)
	}

	// A project's own opt-in is the MCP write that stays open.
	if err := s.store.CreateProject(&types.Project{ID: "mcp-proj", Name: "mcp", Path: t.TempDir()}); err != nil {
		t.Fatalf("create the project: %v", err)
	}
	rec = testResponseAuth(t, s, testRequest(http.MethodPut, "/api/projects/mcp-proj", map[string]any{
		"mcp_server_ids": []string{"srv1"},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("a project opt-in was refused with %d: %s", rec.Code, rec.Body)
	}
	project, err := s.store.GetProject("mcp-proj")
	if err != nil {
		t.Fatalf("read the project: %v", err)
	}
	if len(project.MCPServerIDs) != 1 || project.MCPServerIDs[0] != "srv1" {
		t.Errorf("the opt-in did not land: %v", project.MCPServerIDs)
	}
}

// The boot says which posture it took, in the log the operator reads.
func TestTheBootAnnouncesARestrictedRegistry(t *testing.T) {
	buf, restore := logging.TestSink()
	t.Cleanup(restore)
	testServerWith(t, func(c *startup.Config) { c.RestrictMCP = true })
	if !strings.Contains(buf.String(), "MIKE_RESTRICT_MCP") {
		t.Error("the boot did not name the variable that froze the registry")
	}
}
