package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// L1: /extensions/ is an UNAUTHENTICATED static surface (sandboxed
// extension iframes have an opaque origin and cannot send session cookies —
// see isPublicPath), so it must serve FILES ONLY. A raw http.FileServer
// renders a directory index for any directory lacking index.html, which
// would disclose the installed-extension tree; such requests must 404.
func TestExtensionsStatic_NoDirectoryListings(t *testing.T) {
	s := testServer(t)

	// The extensions root is CWD-relative (http.Dir("extensions")) and
	// resolves per request — drive the route from a throwaway tree instead
	// of touching the repo's real extensions/ directory.
	root := t.TempDir()
	extRoot := filepath.Join(root, "extensions")
	must := func(rel, content string) {
		t.Helper()
		full := filepath.Join(extRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("README.md", "top-level file")
	must("acme/alpha/app.js", "console.log(1)")
	must("beta/plain/nested.txt", "nested file, no index anywhere")
	must("gamma/widget/index.html", "<html>WIDGET</html>")
	secret := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secret, []byte("do-not-leak"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	// Public path: no session, exactly like the sandboxed iframe.
	get := func(target string) *httptest.ResponseRecorder {
		return testResponse(t, s, testRequest(http.MethodGet, target, nil))
	}
	getFollowing := func(target string) *httptest.ResponseRecorder {
		t.Helper()
		cur, rr := target, get(target)
		for i := 0; (rr.Code == http.StatusMovedPermanently || rr.Code == http.StatusFound) && i < 3; i++ {
			// FileServer redirects relative under StripPrefix — resolve like
			// the browser does, against the current path.
			loc := rr.Header().Get("Location")
			if loc == "" {
				break
			}
			cur = (&url.URL{Path: cur}).ResolveReference(&url.URL{Path: loc}).String()
			rr = get(cur)
		}
		return rr
	}

	// Directories that would render a listing 404 — the tree stays hidden.
	for _, dir := range []string{
		"/extensions/",
		"/extensions/acme",
		"/extensions/acme/",
		"/extensions/beta",
		"/extensions/beta/plain/",
	} {
		rr := getFollowing(dir)
		if rr.Code != http.StatusNotFound {
			t.Errorf("GET %s: expected 404, got %d\n%s", dir, rr.Code, rr.Body.String())
		}
		if body := rr.Body.String(); strings.Contains(body, "alpha") ||
			strings.Contains(body, "nested.txt") ||
			strings.Contains(body, "widget") {
			t.Errorf("GET %s leaked directory entries: %s", dir, body)
		}
	}

	// Real files serve unauthenticated — including nested paths with no
	// index.html anywhere in sight.
	for _, f := range []struct{ path, body string }{
		{"/extensions/README.md", "top-level file"},
		{"/extensions/acme/alpha/app.js", "console.log(1)"},
		{"/extensions/beta/plain/nested.txt", "nested file, no index anywhere"},
	} {
		rr := get(f.path)
		if rr.Code != http.StatusOK || rr.Body.String() != f.body {
			t.Errorf("GET %s: %d %q, want 200 %q", f.path, rr.Code, rr.Body.String(), f.body)
		}
	}

	// Extension entry contract preserved: a directory serves its OWN
	// index.html (that is how a sandboxed iframe loads /extensions/<author>/
	// <name>/), and the index.html URL 301s to the directory as before.
	if rr := get("/extensions/gamma/widget/"); rr.Code != http.StatusOK || rr.Body.String() != "<html>WIDGET</html>" {
		t.Errorf("entry dir GET: %d %q, want 200 the index page", rr.Code, rr.Body.String())
	}
	if rr := getFollowing("/extensions/gamma/widget"); rr.Code != http.StatusOK || rr.Body.String() != "<html>WIDGET</html>" {
		t.Errorf("entry dir (no slash) GET: %d %q, want the index page after the redirect", rr.Code, rr.Body.String())
	}
	if rr := get("/extensions/gamma/widget/index.html"); rr.Code != http.StatusMovedPermanently && rr.Code != http.StatusFound {
		t.Errorf("index.html GET: expected the 301-to-directory, got %d", rr.Code)
	}

	// Traversal stays blocked — whatever the redirect chain, the file
	// outside the root never answers.
	rr := getFollowing("/extensions/%2e%2e/secret.txt")
	if strings.Contains(rr.Body.String(), "do-not-leak") {
		t.Fatalf("traversal leaked %s", rr.Body.String())
	}
	if rr.Code == http.StatusOK {
		t.Fatalf("traversal returned 200: %s", rr.Body.String())
	}
}
