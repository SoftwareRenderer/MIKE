package server

import (
	"context"
	"net/http"
	"strings"

	"mike/pkg/logging"
)

// Bearer-token authentication. A token is issued in Settings → About and authenticates exactly the
// routes below, which is the whole difference between it and a login cookie: it is a credential a
// headless client holds, so it gets the task surface and nothing that reads as the person who
// issued it — not the configuration, not the session list, not the ability to mint another token.
//
// docs/api.md is the client-facing copy of this table.

// tokenScope is everything a bearer token may reach, keyed by the pattern exactly as it is
// registered in routes.go: s.mux.Handler(r) reports that same string for a request, so the grant is
// checked against the route that will actually run rather than against a second path matcher that
// can disagree with the mux. Editing a route without editing this map revokes the grant — the
// failure direction this table must have.
var tokenScope = map[string]string{
	"GET /api/projects":             "list projects",
	"GET /api/projects/{projectID}": "read one project",

	"GET /api/projects/{projectID}/tasks":                         "list a project's tasks",
	"POST /api/projects/{projectID}/tasks":                        "create a task",
	"GET /api/projects/{projectID}/tasks/{taskID}":                "read one task",
	"PUT /api/projects/{projectID}/tasks/{taskID}":                "update a task (status included, so no /move grant is needed)",
	"DELETE /api/projects/{projectID}/tasks/{taskID}":             "delete a task and its subtree",
	"POST /api/projects/{projectID}/tasks/{taskID}/start":         "start a task; creates its worktree if there is none",
	"POST /api/projects/{projectID}/tasks/{taskID}/stop":          "pause a task and every descendant that could still run",
	"POST /api/projects/{projectID}/tasks/{taskID}/start-subtask": "start a task whose parent is already in progress",
}

// tokenIDKeyType exists so the key below is a context slot of its own: values of an empty struct
// type compare equal, so every key declared as `ctxKey{}` reads and writes the SAME slot
// (nonceKey and sessionKey already share one). Reusing ctxKey here would hand a request's CSP nonce
// to any code asking "which token authenticated this?".
type tokenIDKeyType struct{}

// apiTokenKey is the context key for the id of the API token that authenticated a request.
var apiTokenKey = tokenIDKeyType{}

// bearerPrefix is the only Authorization scheme this server defines. The comparison is
// case-insensitive because the scheme is a token, not a literal (RFC 7235); a request carrying any
// other scheme is not a token attempt and stays on the cookie path.
const bearerPrefix = "Bearer "

// bearerToken reads the credential out of an Authorization header and reports whether the header
// claimed the bearer scheme at all. The secret is never logged, by this package or its callers.
func bearerToken(header string) (token string, claimed bool) {
	if len(header) <= len(bearerPrefix) || !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}
	return strings.TrimSpace(header[len(bearerPrefix):]), true
}

// apiTokenID returns the id of the token that authenticated the request, or "" when the request is
// on the session-cookie path.
func apiTokenID(r *http.Request) string {
	id, _ := r.Context().Value(apiTokenKey).(string)
	return id
}

// APITokenMiddleware authenticates a request that presents `Authorization: Bearer <token>`, before
// CSRFMiddleware and SessionMiddleware see it. A request without the header is untouched: the
// cookie path is exactly what it was.
//
// A header that does claim the scheme is authoritative — an unknown or revoked token answers 401 and
// never falls back to the session cookie, because a client whose token was revoked must find that
// out instead of quietly acting as the person whose cookie it also carries.
//
// Authentication is answered before authorization: a valid token on a route outside tokenScope gets
// 403, which is what tells an operator that someone is knocking with a live credential.
func (s *Server) APITokenMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, claimed := bearerToken(r.Header.Get("Authorization"))
		if !claimed || isPublicPath(r.URL.Path, r.Method) {
			// No header, or a path that answers without any credential (health, login, static
			// assets, preview documents): there the token is not the question, and refusing a live
			// token on /api/health would be a worse answer than ignoring it.
			next.ServeHTTP(w, r)
			return
		}

		token, err := s.apiTokens.Validate(r.Context(), presented)
		if err != nil {
			logTokenRejection(r, "", "rejected a bearer token this server does not know")
			tokenChallenge(w)
			return
		}

		// An empty pattern is the mux's "no route for this path and verb": nothing runs, and the mux
		// answers 404/405. Anything else must be on the list.
		if pattern, scoped := s.routeScope(r); pattern != "" && !scoped {
			logTokenRejection(r, token.ID, "bearer token used outside its route scope")
			JSONError(w, http.StatusForbidden, "this token cannot access this endpoint")
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), apiTokenKey, token.ID)))
	})
}

// routeScope reports the registered pattern a request will reach and whether a bearer token may
// reach it. The empty pattern belongs to no route, and so to no scope.
func (s *Server) routeScope(r *http.Request) (pattern string, scoped bool) {
	_, pattern = s.mux.Handler(r)
	_, scoped = tokenScope[pattern]
	return pattern, scoped
}

// tokenChallenge answers 401 with the scheme a client should retry with, so a generic HTTP client
// reports "no valid credential" rather than a bare 401.
func tokenChallenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="mike"`)
	JSONError(w, http.StatusUnauthorized, "invalid bearer token")
}

// logTokenRejection records a refused token request. Debug: a broken client retries, and a rejected
// attempt is per-event chatter rather than a fault that reaches a person. The label is logged, never
// the secret, and the id only when the token was recognised.
func logTokenRejection(r *http.Request, id, msg string) {
	l := logging.For(logging.Auth).With("route", r.Method+" "+r.URL.Path)
	if id != "" {
		l = l.With("token_id", id)
	}
	l.Debugf("%s", msg)
}
