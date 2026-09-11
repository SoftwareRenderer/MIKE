package server

// Every /api route declares its method in the pattern (routes.go), so the mux answers 405 before a
// handler runs. That is only a simplification if the 405 is right, and it is only safe if nothing
// that talks to this server was relying on a route accepting anything.
//
// What changed for a caller: the status code is the same 405 as the deleted handler guards produced,
// the body is now "Method Not Allowed\n" instead of "method not allowed\n", and the response carries
// an `Allow` header naming the registered methods — it never did before. No frontend call sends a
// wrong method, and the container healthcheck reads /api/config with a plain GET, which a GET pattern
// still answers.

import (
	"net/http"
	"strings"
	"testing"
)

// methodContract is what each route accepts. The pairs are the ones a wrong verb would hurt on:
// writes that mutate repositories, and the two reads whose response shape the browser parses.
var methodContract = []struct {
	path  string
	allow []string // methods that must reach the handler
	deny  string   // a method that must be refused
	probe string   // body sent, so a refusal is not an accidental decode error
}{
	{"/api/projects", []string{"GET", "POST"}, "DELETE", ""},
	{"/api/projects/p1", []string{"GET", "PUT", "DELETE"}, "PATCH", ""},
	{"/api/projects/p1/board", []string{"GET"}, "POST", ""},
	{"/api/projects/p1/stats", []string{"GET", "DELETE"}, "POST", ""},
	{"/api/projects/p1/tasks", []string{"GET", "POST"}, "PUT", ""},
	{"/api/projects/p1/tasks/t1", []string{"GET", "PUT", "DELETE"}, "PATCH", ""},
	{"/api/projects/p1/tasks/done", []string{"DELETE"}, "POST", ""},
	{"/api/lsp/memory", []string{"GET"}, "POST", ""},
	{"/api/projects/p1/tasks/t1/start", []string{"POST"}, "GET", ""},
	{"/api/projects/p1/tasks/t1/commit", []string{"POST"}, "GET", ""},
	{"/api/projects/p1/tasks/t1/diff", []string{"GET"}, "POST", ""},
	{"/api/projects/p1/tasks/t1/uploads", []string{"POST"}, "GET", ""},
	{"/api/projects/p1/git/checkout", []string{"POST"}, "GET", `{"branch":"main"}`},
	{"/api/projects/p1/git/status", []string{"GET"}, "POST", ""},
	{"/api/projects/p1/git/pull", []string{"POST"}, "GET", ""},
	{"/api/projects/p1/tasks/t1/threads", []string{"GET", "DELETE"}, "POST", ""},
	{"/api/config", []string{"GET", "PUT"}, "DELETE", ""},
	{"/api/home", []string{"GET"}, "POST", ""},
	{"/api/fs", []string{"GET"}, "POST", ""},
	{"/api/fs/write", []string{"POST"}, "GET", ""},
	{"/api/health", []string{"GET"}, "POST", ""},
	{"/api/extensions", []string{"GET"}, "DELETE", ""},
	{"/api/sessions", []string{"GET"}, "PUT", ""},
}

// TestRoutesRefuseUnregisteredMethodsThroughTheMux is the whole point of the method patterns: the
// refusal happens in the mux, so a handler cannot be reached by a verb it was never meant to serve.
func TestRoutesRefuseUnregisteredMethodsThroughTheMux(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)

	for _, tc := range methodContract {
		t.Run(tc.deny+" "+tc.path, func(t *testing.T) {
			req := testRequest(tc.deny, tc.path, nil)
			if tc.probe != "" {
				req = testRequest(tc.deny, tc.path, map[string]any{"branch": "main"})
			}
			withAuthCSRF(req, cookie, csrf)
			rr := testResponse(t, s, req)

			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s: got %d, want 405 (body %q)", tc.deny, tc.path, rr.Code, rr.Body.String())
			}
			// Allow must name the registered methods. Before the patterns existed there was no Allow
			// header at all, because the handler — not the mux — produced the refusal.
			allow := rr.Header().Get("Allow")
			if allow == "" {
				t.Fatalf("%s %s: 405 without an Allow header; the mux did not make this decision", tc.deny, tc.path)
			}
			for _, want := range tc.allow {
				if !strings.Contains(allow, want) {
					t.Errorf("%s %s: Allow=%q does not list %s", tc.deny, tc.path, allow, want)
				}
			}
		})
	}
}

// TestGETCheckoutNoLongerReachesTheCheckoutHandler is the reason 2a is not cosmetic. The route used to
// be registered without a method, so a GET arrived at a handler that decodes a JSON body and performs
// a checkout — and CSRFMiddleware guards non-GET requests only, so a cross-site GET faced no CSRF
// check on a route that writes. (Exploiting it needed a JSON body, which a cross-site <form> cannot
// carry; the hole was in the story, not provably in the door.)
func TestGETCheckoutNoLongerReachesTheCheckoutHandler(t *testing.T) {
	s := testServer(t)
	cookie, _ := loginTestCSRF(t, s)

	// A GET with no CSRF token: the request that used to be dispatched to the handler at all.
	req := testRequest(http.MethodGet, "/api/projects/p1/git/checkout", nil)
	withAuth(req, cookie)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET on a write route must be refused by the mux, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Header().Get("Allow"), "POST") {
		t.Errorf("Allow should name POST, got %q", rr.Header().Get("Allow"))
	}
}

// TestStaticPathFallsThroughToTheParameterisedSibling records a consequence of method patterns that
// is easy to miss: net/http chooses the **most specific path** first and only then checks the method.
// With `DELETE /api/projects/{p}/tasks/done` registered and no GET on that path, a GET no longer
// reaches HandleClearDone (which used to answer 405) — it matches `GET .../tasks/{taskID}` with
// taskID="done" and answers 404 "task not found". Nothing in the product sends it; the shape is
// pinned because "a wrong verb always gets 405" is therefore not universally true, and a reader who
// assumes it will be surprised.
func TestStaticPathFallsThroughToTheParameterisedSibling(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)

	req := testRequest(http.MethodGet, "/api/projects/p1/tasks/done", nil)
	withAuthCSRF(req, cookie, csrf)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET /tasks/done: got %d (%s), want the by-ID handler's 404", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "task not found") {
		t.Errorf("expected the by-ID handler's answer, got %s", rr.Body.String())
	}
}

// TestGETPatternAlsoAnswersHEAD pins the mux half of HEAD: a GET pattern matches HEAD, so a handler
// that suppresses a HEAD body — the two preview handlers, which check `r.Method != http.MethodHead`
// and skip writing the content — still receives the request. It uses /api/health because handlers
// that switch on r.Method decide HEAD for themselves: /api/config refuses it, exactly as it did
// before method patterns existed.
func TestGETPatternAlsoAnswersHEAD(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)
	req := testRequest(http.MethodHead, "/api/health", nil)
	withAuthCSRF(req, cookie, csrf)
	rr := testResponse(t, s, req)
	if rr.Code == http.StatusMethodNotAllowed {
		t.Fatalf("HEAD refused by the mux (Allow=%q); a GET pattern must also match HEAD", rr.Header().Get("Allow"))
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("HEAD /api/health: got %d, want 200", rr.Code)
	}
}

// TestDeletedGuardsLeaveNoHandlerReachableByTheWrongVerb walks one handler that used to refuse the
// verb itself, to show the mux now decides. Called directly with a bad method a handler no longer
// refuses — that is the point, and it is why the per-handler 405 assertions moved here.
func TestCheckoutHandlerIsNotGuardedAnymoreBecauseTheMuxDecides(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)

	req := testRequest(http.MethodPost, "/api/projects/p1/git/checkout", map[string]any{"branch": "no-such-branch"})
	withAuthCSRF(req, cookie, csrf)
	rr := testResponse(t, s, req)
	// The project does not exist, so the failure is a real one from inside the handler: proof the
	// request got past the mux instead of being refused for its verb.
	if rr.Code == http.StatusMethodNotAllowed {
		t.Fatalf("POST was refused by the mux: %s", rr.Body.String())
	}
}
