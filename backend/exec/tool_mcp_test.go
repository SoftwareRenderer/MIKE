package exec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"mike/backend/mcp"
	"mike/backend/store"
	"mike/backend/types"
)

// The offer and the dispatch of a tool that lives on somebody else's server. The fake is the SDK's own
// server, so what MIKE sends and reads is the real protocol.

// fakeMCPServer is a server MIKE can dial, with one working tool and one that reports its own failure.
func fakeMCPServer(t *testing.T, name string) *httptest.Server {
	t.Helper()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: name, Version: "1"}, nil)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "echo", Description: "Echo text", Title: "Echo"},
		func(ctx context.Context, req *mcpsdk.CallToolRequest, in struct {
			Text string `json:"text"`
		}) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echoed: " + in.Text}}}, nil, nil
		})
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "always_fails", Description: "Fails"},
		func(ctx context.Context, req *mcpsdk.CallToolRequest, in struct{}) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{
				IsError: true,
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "the server said no"}},
			}, nil, nil
		})
	ts := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv },
		&mcpsdk.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(ts.Close)
	return ts
}

// staticServers answers the interface *config.ConfigStore does, so the registry cannot tell the two
// apart. It is deliberately not a mock of the whole config: only the registry row is under test here.
type staticServers struct{ servers []types.MCPServer }

func (s staticServers) OpenMCPServers() ([]types.MCPServer, error) { return s.servers, nil }

// mcpFixture is a store with two projects — one that enabled the registered server and one that did
// not — and an executor offering it.
type mcpFixture struct {
	store    *store.Store
	server   types.MCPServer
	enabled  string
	disabled string
	exec     *TaskExecutor
}

func newMCPFixture(t *testing.T, serverEnabled bool) *mcpFixture {
	t.Helper()
	ts := fakeMCPServer(t, "fixture")
	st := testStore(t)
	cs := newTestConfigStore(t, st)

	server := types.MCPServer{
		ID: "a1b2c3d4e5f60708", Name: "Search Corp", URL: ts.URL,
		Enabled: serverEnabled, TimeoutSeconds: 10,
	}
	f := &mcpFixture{
		store:    st,
		server:   server,
		enabled:  mcpProject(t, st, "proj-on"),
		disabled: mcpProject(t, st, "proj-off"),
	}
	if err := st.UpdateProjectMCPServerIDs(f.enabled, []string{server.ID}); err != nil {
		t.Fatalf("enable the server for the project: %v", err)
	}
	reg := mcp.NewRegistry(staticServers{[]types.MCPServer{server}}, st,
		mcp.Identity{Name: "mike-test", Version: "0"})
	f.exec = newTestExecutorWith(t, Deps{Store: st, ConfigStore: cs, MCP: reg})
	return f
}

// mcpProject writes a project and returns its id.
func mcpProject(t *testing.T, st *store.Store, id string) string {
	t.Helper()
	now := time.Now().UTC()
	if err := st.CreateProject(&types.Project{ID: id, Name: id, Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create project %s: %v", id, err)
	}
	return id
}

func mcpTask(projectID string, mode types.OperatingMode) *types.Task {
	return &types.Task{ID: "task-" + projectID + "-" + string(mode), ProjectID: projectID, Title: "t", Prompt: "p", OperatingMode: mode}
}

// offeredNames is what a task would be offered, as a set.
func offeredNames(tools []types.ChatTool) map[string]bool {
	out := map[string]bool{}
	for _, tool := range tools {
		if tool.Function != nil {
			out[tool.Function.Name] = true
		}
	}
	return out
}

// mcpName is the offered name of one of the fixture server's tools, spelled the way the offer spells it.
func mcpName(tool string) string {
	return types.MCPToolNamePrefix + types.Slugify("Search Corp", 24) + "_" + types.Slugify(tool, 48)
}

// A project that enabled nothing sees nothing and a project that enabled a server sees its tools, in
// every operating mode — which is the choice contributed tools already make.
func TestMCPOfferIsPerProjectAndPerMode(t *testing.T) {
	f := newMCPFixture(t, true)
	ctx := context.Background()

	for _, mode := range []types.OperatingMode{types.ModeAct, types.ModePlan, types.ModeExplore} {
		names := offeredNames(f.exec.toolDefinitionsFor(ctx, mcpTask(f.enabled, mode)))
		if !names[mcpName("echo")] {
			t.Errorf("%s: the enabling project was not offered the server's tool (offered %d)", mode, len(names))
		}

		names = offeredNames(f.exec.toolDefinitionsFor(ctx, mcpTask(f.disabled, mode)))
		for name := range names {
			if strings.HasPrefix(name, types.MCPToolNamePrefix) {
				t.Errorf("%s: a project that enabled nothing was offered %s", mode, name)
			}
		}
	}
}

// A disabled server offers nothing, so its name is an unknown tool rather than a call that could still
// have worked.
func TestDisabledServerOffersNothing(t *testing.T) {
	f := newMCPFixture(t, false)
	task := mcpTask(f.enabled, types.ModeAct)

	if names := offeredNames(f.exec.toolDefinitionsFor(context.Background(), task)); names[mcpName("echo")] {
		t.Error("a disabled server still offered its tool")
	}
	answer, ok := f.exec.tools.run(context.Background(), mcpName("echo"),
		map[string]any{"text": "hi"}, t.TempDir(), task, &executionState{}, nil)
	if ok || !strings.Contains(answer, "Unknown tool") {
		t.Errorf("a disabled server's tool answered %q ok=%v, want the unknown-tool refusal", answer, ok)
	}
}

// A name belonging to a server nobody registered is the same refusal: dispatch never dials on a hunch.
func TestUnknownMCPNameIsRefused(t *testing.T) {
	f := newMCPFixture(t, true)
	task := mcpTask(f.enabled, types.ModeAct)

	answer, ok := f.exec.tools.run(context.Background(), types.MCPToolNamePrefix+"noserver_notatool",
		nil, t.TempDir(), task, &executionState{}, nil)
	if ok || !strings.Contains(answer, "Unknown tool") {
		t.Errorf("answer %q ok=%v, want the unknown-tool refusal", answer, ok)
	}
}

// The call runs end to end: dispatch resolves the name, the registry dials, the answer arrives.
func TestMCPCallRunsThroughDispatch(t *testing.T) {
	f := newMCPFixture(t, true)
	task := mcpTask(f.enabled, types.ModeAct)

	answer, ok := f.exec.tools.run(context.Background(), mcpName("echo"),
		map[string]any{"text": "hello"}, t.TempDir(), task, &executionState{}, nil)
	if !ok {
		t.Fatalf("the call failed: %q", answer)
	}
	if answer != "echoed: hello" {
		t.Errorf("answer = %q, want the server's own text", answer)
	}
}

// A tool that reports its own failure is an ordinary failed tool call: the server's sentence reaches
// the model, because it is the answer.
func TestMCPToolFailureReachesTheModel(t *testing.T) {
	f := newMCPFixture(t, true)
	task := mcpTask(f.enabled, types.ModeAct)

	answer, ok := f.exec.tools.run(context.Background(), mcpName("always_fails"),
		nil, t.TempDir(), task, &executionState{}, nil)
	if ok {
		t.Errorf("a failing tool reported success: %q", answer)
	}
	if !strings.Contains(answer, "the server said no") {
		t.Errorf("the server's own words are missing: %q", answer)
	}
}

// The card label comes from the executor-level resolver rather than the static core table, and it names
// both the server and the tool.
func TestMCPCardLabelNamesTheServerAndTool(t *testing.T) {
	f := newMCPFixture(t, true)
	task := mcpTask(f.enabled, types.ModeAct)

	// The label resolver reads the listing the offer populates, so the offer runs first.
	if names := offeredNames(f.exec.toolDefinitionsFor(context.Background(), task)); !names[mcpName("echo")] {
		t.Fatal("the fixture offered nothing, so the label question cannot be asked")
	}
	if label := f.exec.toolDisplayName(mcpName("echo")); label != "Search Corp · Echo" {
		t.Errorf("label = %q, want the server and the tool's title", label)
	}
	if got := f.exec.toolDisplayName("read_file"); got == "" {
		t.Error("a core tool lost its label to the MCP lookup")
	}
}
