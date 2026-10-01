package exec

import (
	"context"
	"fmt"

	"mike/backend/mcp"
	"mike/pkg/logging"
)

// A tool from a registered MCP server. Like a contributed tool it runs in this process, so a project's
// Network Isolation says nothing about it; tool_extension.go and the Sandbox Configuration own that
// sentence.
//
// The registry answers which server runs a name, and it is the answer the offer gave: a name the offer
// withheld is an unknown tool here too, never a call.

func (r *toolRunner) runMCPTool(ctx context.Context, name string, args map[string]any, env *toolEnv) (string, bool) {
	var reg *mcp.Registry
	if r.mcpTools != nil {
		reg = r.mcpTools()
	}
	if reg == nil {
		return unknownTool(name), false
	}
	offered, ok := reg.Lookup(ctx, env.task.ProjectID, name)
	if !ok {
		return unknownTool(name), false
	}

	answer, err := reg.Call(ctx, offered, args)
	if err != nil {
		if ctx.Err() != nil {
			// The turn was cancelled; the loop reports that in its own words.
			return failed(err)
		}
		logging.For(logging.Tools).With(logging.TaskID, env.task.ID).Warnf(
			"%s via mcp-server=%s failed: %v", name, offered.Server.Name, err)
		return failed(err)
	}
	logging.For(logging.Tools).With(logging.TaskID, env.task.ID).Infof(
		"%s via mcp-server=%s ok bytes=%d", name, offered.Server.Name, len(answer))
	return answer, true
}

func unknownTool(name string) string {
	// The same wording a core tool uses for an unknown name.
	return fmt.Sprintf("Unknown tool: %s", name)
}
