package mcp

import (
	"context"
	"strings"
	"testing"

	"mike/backend/types"
	"mike/pkg/logging"
)

// The cache is the part of the offer that can be wrong without anyone noticing, so every test here
// counts dials: what a turn costs, and what makes it pay again.

// fakeSource is the registered registry.
type fakeSource struct {
	servers []types.MCPServer
	err     error
}

func (f fakeSource) OpenMCPServers() ([]types.MCPServer, error) { return f.servers, f.err }

// fakeProjects is the project table, holding only what an opt-in list needs.
type fakeProjects struct {
	byID map[string][]string
}

func (f fakeProjects) GetProject(id string) (*types.Project, error) {
	return &types.Project{ID: id, Name: id, MCPServerIDs: f.byID[id]}, nil
}

// registryFixture is one running server, one project that enabled it and one that did not.
func registryFixture(t *testing.T, servers ...types.MCPServer) (*Registry, *fakeServer, *fakeProjects) {
	t.Helper()
	fake := fakeMCPServer(t, fakeOptions{Stateless: true})
	if len(servers) == 0 {
		servers = []types.MCPServer{registered(fake.URL, 5)}
	}
	projects := fakeProjects{byID: map[string][]string{
		"p-on":  {servers[0].ID},
		"p-off": nil,
	}}
	return NewRegistry(fakeSource{servers: servers}, projects, testID), fake, &projects
}

func specNames(specs []types.ToolSpec) []string {
	var out []string
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

// A project that enabled nothing costs no dial and no tool.
func TestOfferForAProjectThatEnabledNothing(t *testing.T) {
	reg, fake, _ := registryFixture(t)
	ctx := context.Background()

	if specs := reg.SpecsForProject(ctx, "p-off"); specs != nil {
		t.Errorf("a project that enabled nothing was offered %v", specNames(specs))
	}
	if specs := reg.SpecsForProject(ctx, ""); specs != nil {
		t.Errorf("an empty project id was offered %v", specNames(specs))
	}
	if fake.Requests() != 0 {
		t.Errorf("the offer dialled %d times for a project that enabled nothing", fake.Requests())
	}
}

// The offer names a tool after both its owners, so two servers with the same tool cannot collide, and
// carries the server's own schema and label.
func TestOfferedNameAndSpec(t *testing.T) {
	reg, _, _ := registryFixture(t)
	specs := reg.SpecsForProject(context.Background(), "p-on")
	if len(specs) != 2 {
		t.Fatalf("offered %v, want the fake's two tools", specNames(specs))
	}
	want := types.MCPToolNamePrefix + "fake_" + "echo"
	var echo *types.ToolSpec
	for i := range specs {
		if specs[i].Name == want {
			echo = &specs[i]
		}
	}
	if echo == nil {
		t.Fatalf("offered %v, want one named %q", specNames(specs), want)
	}
	if echo.DisplayName != "Fake · Echo tool" {
		t.Errorf("display name = %q, want the server and the tool's title", echo.DisplayName)
	}
	if echo.Description == "" || echo.Parameters == nil {
		t.Errorf("the offer lost what the server declared: %+v", echo)
	}
}

// The lookup is the offer: a tool of a server this project did not enable is not resolvable, which is
// what makes dispatch's refusal real rather than a courtesy.
func TestLookupFollowsTheOffer(t *testing.T) {
	reg, _, _ := registryFixture(t)
	ctx := context.Background()
	name := types.MCPToolNamePrefix + "fake_echo"

	if _, ok := reg.Lookup(ctx, "p-off", name); ok {
		t.Error("a project that enabled nothing resolved the tool")
	}
	offered, ok := reg.Lookup(ctx, "p-on", name)
	if !ok {
		t.Fatal("the enabling project could not resolve its own tool")
	}
	if offered.Tool != "echo" {
		t.Errorf("call name = %q, want the server's own name for the tool", offered.Tool)
	}
	if offered.Server.ID != registered("", 0).ID {
		t.Errorf("lookup carried server %+v, want the registered row", offered.Server)
	}
}

// A listing is reused until it goes stale or the row changes: a turn must not pay for a dial the last
// turn already made.
func TestListingIsReusedUntilTheRowChanges(t *testing.T) {
	reg, fake, _ := registryFixture(t)
	ctx := context.Background()

	first := len(reg.SpecsForProject(ctx, "p-on"))
	dials := fake.Requests()
	if dials == 0 {
		t.Fatal("the first offer dialled nothing")
	}
	for range 3 {
		if got := len(reg.SpecsForProject(ctx, "p-on")); got != first {
			t.Fatalf("the offer changed: %d tools then %d", first, got)
		}
	}
	if fake.Requests() != dials {
		t.Errorf("the offer dialled %d more times within the interval", fake.Requests()-dials)
	}

	// The same id under a different URL is a different server as far as the cache is concerned: the
	// offer dials again instead of answering from the listing taken for the old row.
	moved := registered(fake.URL+"/moved", 5)
	changed := NewRegistry(fakeSource{servers: []types.MCPServer{moved}},
		fakeProjects{byID: map[string][]string{"p-on": {moved.ID}}}, testID)
	changed.entries = reg.entries
	if _, ok := changed.Lookup(ctx, "p-on", types.MCPToolNamePrefix+"fake_echo"); !ok {
		t.Fatal("the moved row offered nothing")
	}
	if fake.Requests() == dials {
		t.Error("the changed row answered from the old listing — the cache is keyed on the id alone")
	}
}

// A server that cannot be listed offers nothing, says why in the log, and is not dialled again on the
// next turn.
func TestUnlistableServerOffersNothingAndSaysWhy(t *testing.T) {
	logged, restore := logging.TestSink()
	defer restore()
	dead := registered("http://127.0.0.1:1/mcp", 1)
	reg, _, _ := registryFixture(t, dead)
	ctx := context.Background()

	if specs := reg.SpecsForProject(ctx, "p-on"); specs != nil {
		t.Errorf("an unreachable server was offered %v", specNames(specs))
	}
	// The log is where the one report of a failed listing lives: the offer withholds the tools and the
	// Test action answers afresh, so nothing else carries the sentence.
	if !hasLine(logged.Lines(), "server=Fake listing failed", "level=WARN") {
		t.Errorf("no warning about the server that could not be listed, got %q", logged.Lines())
	}

	if specs := reg.SpecsForProject(ctx, "p-on"); specs != nil {
		t.Errorf("the second offer was %v, want nothing", specNames(specs))
	}
}

// hasLine reports whether one captured line holds all the fragments, which is how a level and its
// sentence are checked together rather than trusting a substring of the whole buffer.
func hasLine(lines []string, fragments ...string) bool {
	for _, line := range lines {
		found := true
		for _, f := range fragments {
			if !strings.Contains(line, f) {
				found = false
				break
			}
		}
		if found {
			return true
		}
	}
	return false
}

// Probe is the click that says "look again now": it lists over the cache and the offer sees the result.
func TestProbeOverwritesAFailure(t *testing.T) {
	fake := fakeMCPServer(t, fakeOptions{Stateless: true})
	dead := registered("http://127.0.0.1:1/mcp", 1)
	reg, _, _ := registryFixture(t, dead)
	ctx := context.Background()

	if got := reg.SpecsForProject(ctx, "p-on"); got != nil {
		t.Fatalf("the dead server offered %v", specNames(got))
	}

	live := registered(fake.URL, 5)
	live.ID = dead.ID
	tools, dropped, err := reg.Probe(ctx, live)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(tools) == 0 || dropped != 0 {
		t.Fatalf("probe found %d tools (%d dropped), want the fake's list", len(tools), dropped)
	}
	// The registry now answers for the live row without dialling again.
	projects := fakeProjects{byID: map[string][]string{"p-on": {live.ID}}}
	relaunched := NewRegistry(fakeSource{servers: []types.MCPServer{live}}, projects, testID)
	relaunched.entries = reg.entries
	if specs := relaunched.SpecsForProject(ctx, "p-on"); len(specs) == 0 {
		t.Error("the offer did not see what the probe found")
	}
}

// A disabled server is not offered and not dialled, whatever a project's list says.
func TestDisabledServerIsNotOffered(t *testing.T) {
	off := registered("http://127.0.0.1:1/mcp", 1)
	off.Enabled = false
	reg, fake, _ := registryFixture(t, off)

	if specs := reg.SpecsForProject(context.Background(), "p-on"); specs != nil {
		t.Errorf("a disabled server was offered %v", specNames(specs))
	}
	if fake.Requests() != 0 {
		t.Errorf("a disabled server was dialled %d times", fake.Requests())
	}
}
