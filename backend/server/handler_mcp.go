package server

import (
	"net/http"

	"mike/backend/config"
	"mike/backend/httputil"
	"mike/backend/mcp"
)

// handleMCPTest serves POST /api/mcp/test: Settings → MCP asking one server whether it is there. A
// verdict is a 200 carrying a kind and a sentence a person can act on; only a body that cannot be dialled
// at all is a 4xx naming the field, and nothing is written.
func (s *Server) handleMCPTest(w http.ResponseWriter, r *http.Request) {
	// A probe dials what the browser typed, so under the lock it is the one way to reach an address no
	// person registered. It refuses before the dial rather than after.
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
