package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"mike/backend/store"
	"mike/backend/types"
)

// An MCP token lives only in the registry row, so what matters is that it is stored sealed, never leaves
// through a read, and survives a save that never saw it.

// mcpHeaderOf is one header's value as the test reads it. Production opens the whole list and never
// asks for one header by name, so the accessor it would have used has no caller to justify it.
func mcpHeaderOf(server types.MCPServer, name string) string {
	for _, h := range server.Headers {
		if h.Name == name && h.Value != nil {
			return *h.Value
		}
	}
	return ""
}

func mcpConfig(servers ...types.MCPServer) *types.Config {
	return &types.Config{Providers: []types.Provider{
		{ID: 1, Name: "llm", Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1,
			Models: []types.ModelConfig{{Name: "llama-3"}}},
	}, MCPServers: servers}
}

func header(t *testing.T, name, value string) types.MCPHeader {
	t.Helper()
	v := value
	return types.MCPHeader{Name: name, Value: &v}
}

// storedMCPRow reads the registry row straight from the database, past every accessor, so an assertion
// is about what is stored. No row and an empty row are both "nothing registered".
func storedMCPRow(t *testing.T, cs *ConfigStore) string {
	t.Helper()
	rows, err := cs.db.GetConfigValues(mcpServersRow)
	if err != nil {
		t.Fatalf("read the %s row: %v", mcpServersRow, err)
	}
	return rows[mcpServersRow]
}

func storedMCPServers(t *testing.T, cs *ConfigStore) []types.MCPServer {
	t.Helper()
	raw := storedMCPRow(t, cs)
	var servers []types.MCPServer
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &servers); err != nil {
			t.Fatalf("unmarshal the stored registry: %v (stored %q)", err, raw)
		}
	}
	return servers
}

func TestMCPServerSaveSealsAndReadsBack(t *testing.T) {
	cs, _ := testConfigStoreWithStore(t)
	token := "Bearer real-token-value"
	if err := cs.UpdateConfig(mcpConfig(types.MCPServer{
		Name: "Search", URL: "https://mcp.example.com/mcp", Enabled: true,
		Headers: []types.MCPHeader{header(t, "Authorization", token)},
	})); err != nil {
		t.Fatalf("update config: %v", err)
	}

	stored := storedMCPServers(t, cs)
	if len(stored) != 1 {
		t.Fatalf("stored %d servers, want 1", len(stored))
	}
	got := stored[0]
	if got.ID == "" {
		t.Error("a new server was stored without an ID, so no project could ever enable it")
	}
	if len(got.Headers) != 1 {
		t.Fatalf("stored headers = %+v, want the one", got.Headers)
	}
	sealed := got.Headers[0].Value
	if sealed == nil || *sealed == token {
		t.Fatalf("stored header = %v, want the token sealed", sealed)
	}
	if !sharedSecretBox().Opens(mcpKeyPurpose, mcpRecord(got.ID, "Authorization"), *sealed) {
		t.Errorf("stored header is not a record this box opens for that server and header")
	}

	opened, err := cs.OpenMCPServers()
	if err != nil {
		t.Fatalf("open the registry: %v", err)
	}
	if len(opened) != 1 || mcpHeaderOf(opened[0], "Authorization") != token {
		t.Errorf("OpenMCPServers = %+v, want the token back", opened)
	}
}

func TestGetConfigAndResponseHandOutNoMCPHeader(t *testing.T) {
	cs, _ := testConfigStoreWithStore(t)
	token := "Bearer real-token-value"
	cfg := mcpConfig(types.MCPServer{
		Name: "Search", URL: "https://mcp.example.com/mcp", Enabled: true,
		Headers: []types.MCPHeader{header(t, "Authorization", token)},
	})
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if len(cfg.MCPServers) != 1 {
		t.Fatalf("GetConfig returned %d servers, want 1", len(cfg.MCPServers))
	}
	if v := mcpHeaderOf(cfg.MCPServers[0], "Authorization"); v != maskedAPIKey {
		t.Errorf("GetConfig handed out %q, want the masked placeholder", v)
	}

	// The shape that reaches a browser: serialise the response and look for the token anywhere in it.
	resp := cs.buildConfigResponse(cfg)
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal the response: %v", err)
	}
	if strings.Contains(string(body), token) {
		t.Error("the config response carries the token")
	}
	if !strings.Contains(string(body), "mcp_servers") {
		t.Error("the config response names no registry, so Settings cannot list it")
	}
}

// The edit form is never shown a stored token, so what it sends back carries the placeholder (or no
// value at all) and the stored one must survive.
func TestMCPSaveKeepsAHeaderTheFormNeverSaw(t *testing.T) {
	cs, _ := testConfigStoreWithStore(t)
	token := "Bearer real-token-value"
	if err := cs.UpdateConfig(mcpConfig(types.MCPServer{
		Name: "Search", URL: "https://mcp.example.com/mcp", Enabled: true,
		Headers: []types.MCPHeader{header(t, "Authorization", token)},
	})); err != nil {
		t.Fatalf("update config: %v", err)
	}
	id := storedMCPServers(t, cs)[0].ID

	masked := maskedAPIKey
	saved := types.MCPServer{ID: id, Name: "Search renamed", URL: "https://mcp.example.com/mcp", Enabled: true,
		Headers: []types.MCPHeader{{Name: "Authorization", Value: &masked}}}
	if err := cs.UpdateConfig(mcpConfig(saved)); err != nil {
		t.Fatalf("save the renamed server: %v", err)
	}
	opened, err := cs.OpenMCPServers()
	if err != nil {
		t.Fatalf("open the registry: %v", err)
	}
	if len(opened) != 1 || opened[0].Name != "Search renamed" {
		t.Fatalf("registry = %+v, want the renamed server", opened)
	}
	if got := mcpHeaderOf(opened[0], "Authorization"); got != token {
		t.Errorf("the masked placeholder overwrote the token: got %q", got)
	}
	// A placeholder for a header that was never stored reads as no header, not as the placeholder.
	if got := mcpHeaderOf(opened[0], "X-Never-Stored"); got != "" {
		t.Errorf("unknown header = %q, want empty", got)
	}

	// An empty value is the click that clears it.
	if err := cs.UpdateConfig(mcpConfig(types.MCPServer{
		ID: id, Name: "Search renamed", URL: "https://mcp.example.com/mcp", Enabled: true,
		Headers: []types.MCPHeader{header(t, "Authorization", "")},
	})); err != nil {
		t.Fatalf("clear the header: %v", err)
	}
	opened, err = cs.OpenMCPServers()
	if err != nil {
		t.Fatalf("open the registry: %v", err)
	}
	if len(opened[0].Headers) != 0 {
		t.Errorf("clearing left %+v, want no headers", opened[0].Headers)
	}
}

// A save is refused by naming what a person has to correct, and nothing is written before every
// server validates.
func TestMCPSaveRefusesByNamingTheField(t *testing.T) {
	cs, _ := testConfigStoreWithStore(t)
	ok := types.MCPServer{Name: "Good", URL: "https://mcp.example.com/mcp"}

	for _, tc := range []struct {
		name    string
		servers []types.MCPServer
		wants   string
	}{
		{"no url", []types.MCPServer{{Name: "anon"}}, "no URL"},
		{"other transport", []types.MCPServer{{Name: "local", URL: "stdio://nothing"}}, "http or https"},
		{"credentials in the url", []types.MCPServer{{Name: "basic", URL: "https://user:pw@mcp.example.com/mcp"}}, "send them as a header"},
		{"no host", []types.MCPServer{{Name: "hostless", URL: "https://"}}, "no host"},
		{"nameless", []types.MCPServer{{Name: "  ", URL: "https://mcp.example.com"}}, "needs a name"},
		{"two of a name", []types.MCPServer{{Name: "same", URL: "https://a.example.com"}, {Name: "Same", URL: "https://b.example.com"}}, "named"},
		{"two names one slug", []types.MCPServer{{Name: "my server", URL: "https://a.example.com"}, {Name: "My-Server", URL: "https://b.example.com"}}, "would both name their tools"},
		{"timeout past the ceiling", []types.MCPServer{{Name: "slow", URL: "https://a.example.com", TimeoutSeconds: types.MaxMCPTimeoutSeconds + 1}}, "timeout"},
		{"header name with a space", []types.MCPServer{{Name: "smuggled", URL: "https://a.example.com", Headers: []types.MCPHeader{header(t, "X Bad", "v")}}}, "not a header field name"},
		{"header name with a break", []types.MCPServer{{Name: "smuggled", URL: "https://a.example.com", Headers: []types.MCPHeader{header(t, "X-Bad\r\nX-Other", "v")}}}, "not a header field name"},
		{"same header twice", []types.MCPServer{{Name: "twice", URL: "https://a.example.com", Headers: []types.MCPHeader{header(t, "Authorization", "a"), header(t, "authorization", "b")}}}, "declares the header"},
		{"too many servers", func() []types.MCPServer {
			many := make([]types.MCPServer, types.MaxMCPServers+1)
			for i := range many {
				many[i] = types.MCPServer{Name: string(rune('a'+i%26)) + string(rune('a'+i/26)) + string(rune('0'+i%10)), URL: "https://a.example.com"}
			}
			return many
		}(), "too many MCP servers"},
	} {
		err := cs.UpdateConfig(mcpConfig(append([]types.MCPServer{ok}, tc.servers...)...))
		if err == nil {
			t.Errorf("%s: accepted, want a refusal naming %q", tc.name, tc.wants)
			continue
		}
		if !strings.Contains(err.Error(), tc.wants) {
			t.Errorf("%s: refused with %q, want it to name %q", tc.name, err, tc.wants)
		}
	}

	if servers := storedMCPServers(t, cs); len(servers) != 0 {
		t.Errorf("a refused save wrote %d servers; nothing is stored before everything validates", len(servers))
	}
	if row := storedMCPRow(t, cs); row != "" && row != "[]" {
		t.Errorf("a refused save left a registry row behind: %q", row)
	}
}

func TestMCPRegistrySurvivesARestart(t *testing.T) {
	cs, db := testConfigStoreWithStore(t)
	token := "Bearer real-token-value"
	if err := cs.UpdateConfig(mcpConfig(types.MCPServer{
		Name: "Search", URL: "https://mcp.example.com/mcp", Enabled: true, TimeoutSeconds: 5,
		Headers: []types.MCPHeader{header(t, "Authorization", token)},
	})); err != nil {
		t.Fatalf("update config: %v", err)
	}

	// A new store over the same database is the restart; the same box opens what was sealed.
	restarted := NewConfigStore(db, cs.dbPath, "test", sharedSecretBox(), false)
	opened, err := restarted.OpenMCPServers()
	if err != nil {
		t.Fatalf("open after restart: %v", err)
	}
	if len(opened) != 1 || mcpHeaderOf(opened[0], "Authorization") != token {
		t.Errorf("after restart = %+v, want the server and its token", opened)
	}
	if opened[0].TimeoutSecondsOrDefault() != 5 {
		t.Errorf("timeout = %d, want the stored 5", opened[0].TimeoutSecondsOrDefault())
	}
}

// A header sealed for another server, or for another header of the same server, does not open: the
// record binds both.
func TestMCPHeaderOpensOnlyItsOwnRecord(t *testing.T) {
	cs, _ := testConfigStoreWithStore(t)
	if err := cs.UpdateConfig(mcpConfig(
		types.MCPServer{Name: "One", URL: "https://a.example.com", Headers: []types.MCPHeader{header(t, "Authorization", "secret-one")}},
		types.MCPServer{Name: "Two", URL: "https://b.example.com", Headers: []types.MCPHeader{header(t, "X-Key", "secret-two")}},
	)); err != nil {
		t.Fatalf("update config: %v", err)
	}
	servers := storedMCPServers(t, cs)
	if len(servers) != 2 {
		t.Fatalf("stored %d servers, want 2", len(servers))
	}
	sealedOne := *servers[0].Headers[0].Value
	box := sharedSecretBox()

	for _, want := range []struct{ why, purpose, record string }{
		{"another server's record", mcpKeyPurpose, mcpRecord(servers[1].ID, "X-Key")},
		{"another header's record", mcpKeyPurpose, mcpRecord(servers[0].ID, "X-Key")},
		{"another secret kind", providerKeyPurpose, mcpRecord(servers[0].ID, "authorization")},
	} {
		got, err := box.Decrypt(want.purpose, want.record, sealedOne)
		if err == nil || got != "" {
			t.Errorf("opened under %s as %q (%v), want nothing", want.why, got, err)
		}
	}

	// And it does open under its own record, so the loop above is about the binding, not about a
	// value that was never sealed.
	if got, err := box.Decrypt(mcpKeyPurpose, mcpRecord(servers[0].ID, "Authorization"), sealedOne); err != nil || got != "secret-one" {
		t.Errorf("own record opened as %q (%v), want the secret", got, err)
	}
}

// An unreadable row costs the MCP tools and nothing else: the config read still answers.
func TestUnreadableMCPRowReadsAsNoServers(t *testing.T) {
	cs, db := testConfigStoreWithStore(t)
	if err := cs.UpdateConfig(mcpConfig(types.MCPServer{Name: "One", URL: "https://a.example.com"})); err != nil {
		t.Fatalf("update config: %v", err)
	}
	if err := db.SetConfigValue(mcpServersRow, "{not an array"); err != nil {
		t.Fatalf("write an unreadable row: %v", err)
	}
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("an unreadable registry row failed the config read: %v", err)
	}
	if len(cfg.MCPServers) != 0 {
		t.Errorf("unreadable row read as %d servers, want none", len(cfg.MCPServers))
	}
	if row := storedMCPRow(t, cs); row != "{not an array" {
		t.Errorf("the unreadable row became %q, want it left alone for a human", row)
	}
}

// Every other Settings tab saves over this same endpoint, so the config view — masked headers and all —
// has to survive being written back.
func TestConfigViewSurvivesASaveRoundTrip(t *testing.T) {
	cs, _ := testConfigStoreWithStore(t)
	if err := cs.UpdateConfig(mcpConfig(types.MCPServer{Name: "One", URL: "https://a.example.com"})); err != nil {
		t.Fatalf("update config: %v", err)
	}
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if len(cfg.MCPServers) != 1 {
		t.Fatalf("GetConfig returned %d servers, want 1", len(cfg.MCPServers))
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("save the config view back: %v", err)
	}
	opened, err := cs.OpenMCPServers()
	if err != nil {
		t.Fatalf("open the registry: %v", err)
	}
	if len(opened) != 1 || opened[0].Name != "One" || opened[0].URL != "https://a.example.com" {
		t.Errorf("registry after a round trip = %+v, want the server untouched", opened)
	}
}

// restrictedOver is a store over the same database with the operator's lock resolved on: the reboot that
// freezes what the previous run registered. The decision is a value, so a test opts in rather than
// setting a variable.
func restrictedOver(t *testing.T, db *store.Store, dbPath string) *ConfigStore {
	t.Helper()
	return NewConfigStore(db, dbPath, "test", sharedSecretBox(), true)
}

func registerMCPServer(t *testing.T, cs *ConfigStore) string {
	t.Helper()
	if err := cs.UpdateConfig(mcpConfig(types.MCPServer{
		Name: "Search", URL: "https://mcp.example.com/mcp", Enabled: true, TimeoutSeconds: 5,
		Headers: []types.MCPHeader{header(t, "Authorization", "Bearer real-token-value")},
	})); err != nil {
		t.Fatalf("register the server: %v", err)
	}
	servers := storedMCPServers(t, cs)
	if len(servers) != 1 {
		t.Fatalf("stored %d servers, want 1", len(servers))
	}
	return servers[0].ID
}

// The lock freezes the registry, not the config: what an untouched Settings form sends back is the same
// bytes the row holds, and every other tab keeps saving.
func TestRestrictedRegistryRoundTripsAndOtherPrefsStillSave(t *testing.T) {
	cs, db := testConfigStoreWithStore(t)
	registerMCPServer(t, cs)
	locked := restrictedOver(t, db, cs.dbPath)

	cfg, err := locked.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if err := locked.UpdateConfig(cfg); err != nil {
		t.Fatalf("a save that changed nothing was refused: %v", err)
	}

	cfg, err = locked.GetConfig()
	if err != nil {
		t.Fatalf("get config again: %v", err)
	}
	cfg.WorkerModel = "qwen-7b"
	parallel := true
	cfg.RunSiblingsParallel = &parallel
	if err := locked.UpdateConfig(cfg); err != nil {
		t.Fatalf("an unrelated preference was refused: %v", err)
	}
	read, err := locked.GetConfig()
	if err != nil {
		t.Fatalf("get config after the preference save: %v", err)
	}
	if read.WorkerModel != "qwen-7b" || read.RunSiblingsParallel == nil || !*read.RunSiblingsParallel {
		t.Errorf("the unrelated preference did not land: model=%q siblings=%v", read.WorkerModel, read.RunSiblingsParallel)
	}
	// The tab reads this to show itself read-only; its arrival over the wire is the server test's claim.
	if !locked.buildConfigResponse(read).MCPRestricted {
		t.Error("the config response does not say the registry is restricted")
	}
}

// Every way the registry itself can be changed is refused, by name, and the row is left alone.
func TestRestrictedRegistryRefusesEveryEdit(t *testing.T) {
	cs, db := testConfigStoreWithStore(t)
	id := registerMCPServer(t, cs)
	locked := restrictedOver(t, db, cs.dbPath)
	before := storedMCPRow(t, locked)

	masked := maskedAPIKey
	for _, tc := range []struct {
		why     string
		servers []types.MCPServer
	}{
		{"an added server", []types.MCPServer{
			{ID: id, Name: "Search", URL: "https://mcp.example.com/mcp", Enabled: true, TimeoutSeconds: 5,
				Headers: []types.MCPHeader{{Name: "Authorization", Value: &masked}}},
			{Name: "Added", URL: "https://added.example.com/mcp"},
		}},
		{"a re-pointed url", []types.MCPServer{
			{ID: id, Name: "Search", URL: "https://evil.example.com/mcp", Enabled: true, TimeoutSeconds: 5,
				Headers: []types.MCPHeader{{Name: "Authorization", Value: &masked}}},
		}},
		{"a removed server", nil},
		{"a retyped header", []types.MCPServer{
			{ID: id, Name: "Search", URL: "https://mcp.example.com/mcp", Enabled: true, TimeoutSeconds: 5,
				Headers: []types.MCPHeader{header(t, "Authorization", "Bearer someone-elses-token")}},
		}},
		{"a dropped header", []types.MCPServer{
			{ID: id, Name: "Search", URL: "https://mcp.example.com/mcp", Enabled: true, TimeoutSeconds: 5},
		}},
		{"a flipped enabled flag", []types.MCPServer{
			{ID: id, Name: "Search", URL: "https://mcp.example.com/mcp", Enabled: false, TimeoutSeconds: 5,
				Headers: []types.MCPHeader{{Name: "Authorization", Value: &masked}}},
		}},
		{"a renamed server", []types.MCPServer{
			{ID: id, Name: "Searchr", URL: "https://mcp.example.com/mcp", Enabled: true, TimeoutSeconds: 5,
				Headers: []types.MCPHeader{{Name: "Authorization", Value: &masked}}},
		}},
		{"a changed timeout", []types.MCPServer{
			{ID: id, Name: "Search", URL: "https://mcp.example.com/mcp", Enabled: true, TimeoutSeconds: 30,
				Headers: []types.MCPHeader{{Name: "Authorization", Value: &masked}}},
		}},
	} {
		err := locked.UpdateConfig(mcpConfig(tc.servers...))
		if err == nil {
			t.Errorf("%s: accepted, want a refusal naming %s", tc.why, EnvRestrictMCP)
			continue
		}
		if !errors.Is(err, ErrMCPLocked) {
			t.Errorf("%s: refused with %v, want ErrMCPLocked", tc.why, err)
		}
		if !strings.Contains(err.Error(), EnvRestrictMCP) {
			t.Errorf("%s: refusal %q does not name %s", tc.why, err, EnvRestrictMCP)
		}
		if !strings.Contains(err.Error(), "Project Defaults") {
			t.Errorf("%s: refusal %q does not say what is still writable", tc.why, err)
		}
		if row := storedMCPRow(t, locked); row != before {
			t.Errorf("%s: a refused save changed the registry row to %q", tc.why, row)
		}
	}
}

// With nothing registered there is nothing to freeze: an empty list is the unchanged list, so a Settings
// save on a fresh install still works.
func TestRestrictedRegistryWithNothingRegisteredStillSaves(t *testing.T) {
	cs, db := testConfigStoreWithStore(t)
	locked := restrictedOver(t, db, cs.dbPath)
	for _, servers := range [][]types.MCPServer{nil, {}} {
		if err := locked.UpdateConfig(mcpConfig(servers...)); err != nil {
			t.Errorf("an empty registry (%v) was refused: %v", servers, err)
		}
	}
}
