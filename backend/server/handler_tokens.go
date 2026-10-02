package server

import (
	"errors"
	"net/http"
	"strings"

	"mike/backend/auth"
	"mike/backend/httputil"
	"mike/backend/store"
	"mike/pkg/logging"
)

// API token management — the signed-in user issuing credentials for their own headless clients.
// These routes are the reason no token can mint or revoke a token: they are reachable only with the
// session cookie (and CSRF for the writes), and they are deliberately absent from tokenScope, so a
// token answering here is answered 403 before the handler runs.

// apiTokenResponse is one row of GET /api/tokens. It has no field for the token or its hash, so a
// listing cannot leak one; the secret appears exactly once, in the create response.
type apiTokenResponse struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	CreatedAt string `json:"created_at"`   // RFC3339, UTC
	LastUsed  string `json:"last_used_at"` // RFC3339, UTC; "" when the token has never authenticated a request
}

// createAPITokenResponse is the row plus the plaintext secret, which the caller must write down now
// because nothing this server keeps can reproduce it.
type createAPITokenResponse struct {
	apiTokenResponse
	Token string `json:"token"`
}

// createAPITokenRequest is the body of POST /api/tokens. The pointer makes a wrong type a 400
// naming the field rather than a dropped value.
type createAPITokenRequest struct {
	Label *string `json:"label"`
}

// newAPITokenResponse renders a stored row. Only the fields that are safe to display survive.
func newAPITokenResponse(t store.APITokenInfo) apiTokenResponse {
	return apiTokenResponse{
		ID:        t.ID,
		Label:     t.Label,
		CreatedAt: formatAuthTime(t.CreatedAt),
		LastUsed:  formatAuthTime(t.LastUsed),
	}
}

// handleAPITokensList handles GET /api/tokens — every token, newest first.
func (s *Server) handleAPITokensList(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSession(w, r); !ok {
		return
	}

	tokens, err := s.store.ListAPITokens(r.Context())
	if err != nil {
		Fault(w, "list api tokens", err)
		return
	}
	out := make([]apiTokenResponse, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, newAPITokenResponse(t))
	}
	JSONResponse(w, http.StatusOK, out)
}

// handleAPITokenCreate handles POST /api/tokens: {"label": "..."} and the new secret, once.
func (s *Server) handleAPITokenCreate(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSession(w, r); !ok {
		return
	}

	var req createAPITokenRequest
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	// An absent label arrives as nil and reads as "", which is what auth refuses: one rule for the
	// shape of a label, written once.
	label := ""
	if req.Label != nil {
		label = *req.Label
	}
	info, secret, err := s.apiTokens.Issue(r.Context(), label)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidLabel) {
			JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		Fault(w, "create api token", err)
		return
	}
	// The label is the only part of a token that is safe to name in a log, and the id is what a
	// later revocation is traced by.
	logging.For(logging.Auth).With("token_id", info.ID).Infof("issued an API token")

	JSONResponse(w, http.StatusCreated, createAPITokenResponse{
		apiTokenResponse: newAPITokenResponse(info),
		Token:            secret,
	})
}

// handleAPITokenRevoke handles DELETE /api/tokens/{id}. The row is gone, so the next request that
// presents that secret authenticates as nothing.
func (s *Server) handleAPITokenRevoke(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSession(w, r); !ok {
		return
	}

	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		JSONError(w, http.StatusBadRequest, "token id required")
		return
	}

	found, err := s.store.DeleteAPITokenByID(r.Context(), id)
	if err != nil {
		Fault(w, "revoke api token", err)
		return
	}
	if !found {
		JSONError(w, http.StatusNotFound, "token not found")
		return
	}
	logging.For(logging.Auth).With("token_id", id).Infof("revoked an API token")
	w.WriteHeader(http.StatusNoContent)
}
