package server

import (
	"net/http"
	"strings"
	"time"
)

// Session management API — lets the signed-in user inspect and revoke their own
// sessions. Every route here is session-authenticated and every non-GET one is
// CSRF-guarded; no /api/sessions path is ever added to publicPaths.

// sessionInfoResponse is one row of GET /api/sessions. It deliberately has no
// field for a token, token hash or CSRF token, so a credential cannot leak by
// accident.
type sessionInfoResponse struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"` // RFC3339, UTC
	LastSeen  string `json:"last_seen"`  // RFC3339, UTC; "" when never used since login
	ExpiresAt string `json:"expires_at"` // RFC3339, UTC
	IP        string `json:"ip"`         // client IP recorded at login ("" for pre-migration rows)
	UserAgent string `json:"user_agent"` // User-Agent recorded at login ("" for pre-migration rows)
	Current   bool   `json:"current"`    // true for the session making this request
}

// formatAuthTime renders a session timestamp for the API. A zero time — never
// seen, or a row predating the columns — is "".
func formatAuthTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// currentSessionID returns the session id injected by SessionMiddleware, or ""
// when the request carries no authenticated session.
func currentSessionID(r *http.Request) string {
	id, _ := r.Context().Value(sessionKey).(string)
	return id
}

// handleSessionsList handles GET /api/sessions.
// Returns every live session, newest first, with `current` marking the caller's.
func (s *Server) handleSessionsList(w http.ResponseWriter, r *http.Request) {
	current := currentSessionID(r)
	if current == "" {
		JSONError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	sessions, err := s.store.ListSessions(r.Context())
	if err != nil {
		Fault(w, "list sessions", err)
		return
	}

	out := make([]sessionInfoResponse, 0, len(sessions))
	for _, se := range sessions {
		out = append(out, sessionInfoResponse{
			ID:        se.ID,
			CreatedAt: formatAuthTime(se.CreatedAt),
			LastSeen:  formatAuthTime(se.LastSeen),
			ExpiresAt: formatAuthTime(se.ExpiresAt),
			IP:        se.IP,
			UserAgent: se.UserAgent,
			Current:   se.ID == current,
		})
	}
	JSONResponse(w, http.StatusOK, out)
}

// handleSessionRevoke handles DELETE /api/sessions/{id}. Revoking the caller's own
// session is allowed — their next request 401s and the SPA falls back to the login
// modal.
func (s *Server) handleSessionRevoke(w http.ResponseWriter, r *http.Request) {
	if currentSessionID(r) == "" {
		JSONError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		JSONError(w, http.StatusBadRequest, "session id required")
		return
	}

	found, err := s.store.DeleteSessionByID(r.Context(), id)
	if err != nil {
		Fault(w, "revoke session", err)
		return
	}
	if !found {
		JSONError(w, http.StatusNotFound, "session not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSessionsRevokeOthers handles POST /api/sessions/revoke-others: deletes
// every session except the caller's and reports {"revoked": n}.
func (s *Server) handleSessionsRevokeOthers(w http.ResponseWriter, r *http.Request) {
	current := currentSessionID(r)
	if current == "" {
		JSONError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	n, err := s.store.DeleteOtherSessions(r.Context(), current)
	if err != nil {
		Fault(w, "revoke other sessions", err)
		return
	}
	JSONResponse(w, http.StatusOK, map[string]int{"revoked": n})
}
