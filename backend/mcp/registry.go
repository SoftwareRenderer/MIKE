package mcp

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"mike/backend/types"
	"mike/pkg/logging"
)

// The offer is read per turn, so saving a server or enabling one for a project takes effect on the next
// turn. A server's tools live on the server, so the answer is a cache and its staleness is a rule
// (docs/mcp.md).

// listBudget is how long a turn waits for one listing, however long the server's own deadline is. A
// slow listing is not a reason to hold a turn.
const listBudget = 10 * time.Second

// refreshInterval is how long a listing is reused. A server may change its list at any moment, so this
// bounds staleness rather than promising freshness.
const refreshInterval = 5 * time.Minute

// toolSlugLen bounds the tool half of an offered name. The server half is types.MCPServerSlugLen, the
// cap the save's duplicate-name refusal uses.
const toolSlugLen = 48

// ServerSource is the registry of registered servers with their headers opened.
// *config.ConfigStore answers it; the interface keeps this package out of the one that does.
type ServerSource interface {
	OpenMCPServers() ([]types.MCPServer, error)
}

// ProjectSource reads a project, which is where its MCP opt-in list lives. *store.Store answers it.
type ProjectSource interface {
	GetProject(id string) (*types.Project, error)
}

// Registry answers what a project may offer and which server runs a named tool, from one cache so the
// two questions cannot disagree.
type Registry struct {
	servers  ServerSource
	projects ProjectSource
	id       Identity

	mu      sync.Mutex
	entries map[string]*entry
}

// entry is one server's last listing. A failed listing keeps nothing and reports nothing: the Test
// action answers afresh, and a verdict five minutes old beside that button is a second answer.
type entry struct {
	// shape is the registered row as of this listing; another shape means the entry is about
	// something else and is refreshed.
	shape string
	tools []Tool
	at    time.Time
}

// NewRegistry wires the two sources and the identity MIKE reports to a server.
func NewRegistry(servers ServerSource, projects ProjectSource, id Identity) *Registry {
	return &Registry{servers: servers, projects: projects, id: id, entries: map[string]*entry{}}
}

// OfferedTool is a tool this registry offers, with the server that runs it.
type OfferedTool struct {
	Spec types.ToolSpec
	// Server and Tool are the pair a call needs: the row to dial and the name that server uses.
	Server types.MCPServer
	Tool   string
}

// SpecsForProject is the offer: every tool of every server this project enabled. A server whose listing
// failed offers nothing.
func (r *Registry) SpecsForProject(ctx context.Context, projectID string) []types.ToolSpec {
	offered := r.toolsForProject(ctx, projectID)
	if len(offered) == 0 {
		return nil
	}
	specs := make([]types.ToolSpec, 0, len(offered))
	for _, tool := range offered {
		specs = append(specs, tool.Spec)
	}
	return specs
}

// Lookup resolves an offered name for a project. It answers only for what the offer offered, so naming
// a tool of a server this project did not enable is an unknown tool, not a call.
func (r *Registry) Lookup(ctx context.Context, projectID, name string) (OfferedTool, bool) {
	for _, tool := range r.toolsForProject(ctx, projectID) {
		if tool.Spec.Name == name {
			return tool, true
		}
	}
	return OfferedTool{}, false
}

// DisplayName is the card label for an offered name, read across every registered server: the label a
// tool ran under must not change because a different project is asking.
func (r *Registry) DisplayName(name string) (string, bool) {
	servers, err := r.registered()
	if err != nil {
		return "", false
	}
	for _, server := range servers {
		for _, tool := range r.cached(server) {
			if offeredName(server.Name, tool.Name) == name {
				return displayName(server, tool), true
			}
		}
	}
	return "", false
}

// Probe lists one server now, ignoring the cache, and stores what it found so the offer sees the same
// answer. The row it dials may hold unsaved values, which is the point of the Test action.
func (r *Registry) Probe(ctx context.Context, server types.MCPServer) ([]Tool, int, error) {
	tools, dropped, err := r.list(ctx, server)
	r.store(server, tools)
	return tools, dropped, err
}

// Call runs one offered tool for a project and returns its answer, already capped. It sits on the
// registry so the identity a server sees is the one MIKE was built with.
func (r *Registry) Call(ctx context.Context, tool OfferedTool, args map[string]any) (string, error) {
	session, err := connect(ctx, tool.Server, r.id)
	if err != nil {
		return "", err
	}
	defer session.close()
	return session.call(ctx, tool.Tool, args)
}

// toolsForProject is the one path both the offer and the lookup take, so the two cannot disagree
// about what is offered.
func (r *Registry) toolsForProject(ctx context.Context, projectID string) []OfferedTool {
	if r.projects == nil || projectID == "" {
		return nil
	}
	project, err := r.projects.GetProject(projectID)
	if err != nil || len(project.MCPServerIDs) == 0 {
		return nil
	}
	servers, err := r.registered()
	if err != nil {
		return nil
	}
	enabled := make(map[string]bool, len(project.MCPServerIDs))
	for _, id := range project.MCPServerIDs {
		enabled[id] = true
	}

	var waiting []types.MCPServer
	for _, server := range servers {
		if server.Enabled && enabled[server.ID] && !r.fresh(server) {
			waiting = append(waiting, server)
		}
	}
	r.refreshAll(ctx, waiting)

	var (
		out   []OfferedTool
		taken = map[string]bool{}
	)
	for _, server := range servers {
		if !server.Enabled || !enabled[server.ID] {
			continue
		}
		for _, tool := range r.cached(server) {
			name := offeredName(server.Name, tool.Name)
			switch {
			case types.IsCoreToolName(name):
				report(server, tool.Name, "the name belongs to a core tool")
				continue
			case taken[name]:
				report(server, tool.Name, "another enabled server already offers this name")
				continue
			}
			taken[name] = true
			out = append(out, OfferedTool{
				Spec: types.ToolSpec{
					Name:        name,
					DisplayName: displayName(server, tool),
					Description: tool.Description,
					Parameters:  tool.Parameters,
				},
				Server: server,
				Tool:   tool.Name,
			})
		}
	}
	return out
}

// registered reads the global registry and drops any listing no registered row is behind.
func (r *Registry) registered() ([]types.MCPServer, error) {
	if r.servers == nil {
		return nil, nil
	}
	servers, err := r.servers.OpenMCPServers()
	if err != nil {
		logging.For(logging.MCP).Warnf("read the MCP registry: %v", err)
		return nil, err
	}
	known := make(map[string]bool, len(servers))
	for _, server := range servers {
		known[server.ID] = true
	}
	r.mu.Lock()
	for id := range r.entries {
		if !known[id] {
			delete(r.entries, id)
		}
	}
	r.mu.Unlock()
	return servers, nil
}

// fresh reports whether a cached listing still stands for this row.
func (r *Registry) fresh(server types.MCPServer) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entries[server.ID]
	return e != nil && e.shape == shapeOf(server) && time.Since(e.at) < refreshInterval
}

// cached is the last listing for a server, or nothing when there is none or it is about another row.
func (r *Registry) cached(server types.MCPServer) []Tool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entries[server.ID]
	if e == nil || e.shape != shapeOf(server) {
		return nil
	}
	return e.tools
}

// refreshAll re-lists every waiting server at once. The wait a turn pays is therefore the slowest
// single listing, not their sum, and each is bounded by listBudget.
func (r *Registry) refreshAll(ctx context.Context, servers []types.MCPServer) {
	var wg sync.WaitGroup
	for _, server := range servers {
		wg.Add(1)
		go func(server types.MCPServer) {
			defer wg.Done()
			listCtx, cancel := context.WithTimeout(ctx, listBudget)
			defer cancel()
			tools, dropped, err := r.list(listCtx, server)
			if err != nil {
				// The only place a listing failure is reported: the offer withholds the server's tools
				// and the Test action answers afresh. Warn, not Debug — the run continued.
				logging.For(logging.MCP).Warnf("server=%s listing failed: %v", server.Name, err)
			}
			if dropped > 0 {
				logging.For(logging.MCP).Warnf("server=%s lists %d tools past the bound of %d; they are not offered",
					server.Name, dropped, types.MaxToolsPerServer)
			}
			r.store(server, tools)
		}(server)
	}
	wg.Wait()
}

// list dials one server for its tools. A failure keeps no tools; the caller decides who hears about it.
func (r *Registry) list(ctx context.Context, server types.MCPServer) (tools []Tool, dropped int, err error) {
	session, err := connect(ctx, server, r.id)
	if err != nil {
		return nil, 0, err
	}
	defer session.close()
	tools, dropped, err = session.listTools(ctx)
	if err != nil {
		return nil, 0, err
	}
	return tools, dropped, nil
}

// store records a listing. A row with no id is a draft being tested, and no offer is keyed on it.
func (r *Registry) store(server types.MCPServer, tools []Tool) {
	if server.ID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[server.ID] = &entry{shape: shapeOf(server), tools: tools, at: time.Now()}
}

func report(server types.MCPServer, toolName, reason string) {
	logging.For(logging.MCP).Debugf("server=%s tool=%s not offered: %s", server.Name, toolName, reason)
}

// offeredName is the name the model is given and dispatch resolves. Both halves are slugged: a server
// names its tools whatever it likes, and the server half keeps two servers from colliding.
func offeredName(serverName, toolName string) string {
	return types.MCPToolNamePrefix + types.Slugify(serverName, types.MCPServerSlugLen) + "_" + types.Slugify(toolName, toolSlugLen)
}

// displayName is what the tool card shows: the server's name and the tool's own label.
func displayName(server types.MCPServer, tool Tool) string {
	if tool.DisplayName == "" || tool.DisplayName == tool.Name {
		return server.Name
	}
	return server.Name + " · " + tool.DisplayName
}

// shapeOf is what a cached listing is keyed against: a row that changed in any way a listing depends
// on forces a fresh one. A header value is left out because it cannot change what a server lists.
func shapeOf(server types.MCPServer) string {
	var b strings.Builder
	b.WriteString(server.Name)
	b.WriteByte(0)
	b.WriteString(server.URL)
	b.WriteByte(0)
	b.WriteString(strconv.FormatBool(server.Enabled))
	b.WriteByte(0)
	b.WriteString(strconv.Itoa(server.TimeoutSecondsOrDefault()))
	return b.String()
}
