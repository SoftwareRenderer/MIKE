package server

import (
	"net/http"

	"mike/backend/config"
	"mike/backend/httputil"
	"mike/backend/mcp"
)

// handleMCPTest tests a single MCP server's availability. Returns a 200 with the
// server's response, or a 4xx if the server cannot be reached.
func (s *Server) handleMCPTest(w http.ResponseWriter, r *http.Request) {
	// probes the provided address; refuses if MCP is restricted before attempting the dial.
	if s.settings.RestrictMCP {
		httputil.JSONError(w, http.StatusForbidden, config.ErrMCPLocked.Error())
		return
	}
	var body mcp.TestRequest
	if !httputil.DecodeJSON(w, r, &body) {
		return
	}
	answer, err := s.mcpRegistry.ProbeForm(r.Context(), body)
	if err != nil {
		if r.Context().Err() != nil {
			// The browser gave up mid-dial; writing to a socket nobody is reading reports a fault nobody
			// sees.
			return
		}
		httputil.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	httputil.JSONResponse(w, http.StatusOK, answer)
}
