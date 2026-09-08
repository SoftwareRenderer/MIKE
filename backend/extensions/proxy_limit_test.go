package extensions

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"kanban/backend/httputil"
)

// TestHandleProxyCapsResponse: the proxied response is buffered in full, so without a cap any
// API on an extension's allowlist could make the server buffer a response of any size.
func TestHandleProxyCapsResponse(t *testing.T) {
	allowLoopbackProxy(t)
	// A real body just past the cap: the handler buffers whatever it is handed.
	const oversized = int(httputil.MaxUpstreamBodyBytes) + 4096
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		chunk := strings.Repeat("y", 1<<20)
		for written := 0; written < oversized; written += len(chunk) {
			w.Write([]byte(chunk))
		}
	}))
	defer big.Close()

	h := testHandler(t)
	u, _ := url.Parse(big.URL)
	makeProxyExt(t, h, []string{u.Hostname()})

	body := `{"url":"` + big.URL + `","method":"GET"}`
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/proxy-ext/proxy", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.HandleProxy(w, req, "proxy-ext")

	if w.Code == http.StatusOK {
		t.Fatalf("an unbounded upstream response was proxied: %d bytes handed back", w.Body.Len())
	}
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
	// The refusal must be the cap, not any other read failure — both answer 502.
	if !strings.Contains(w.Body.String(), "too large") {
		t.Errorf("the refusal should name the cap, got: %s", w.Body.String())
	}
	if w.Body.Len() > int(httputil.MaxUpstreamBodyBytes) {
		t.Errorf("the refusal itself carried %d bytes", w.Body.Len())
	}
}

// TestHandleProxyPassesNormalResponse is the other side: the cap is a bound, not a
// break. A response well inside it still reaches the extension unchanged.
func TestHandleProxyPassesNormalResponse(t *testing.T) {
	allowLoopbackProxy(t)
	small := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer small.Close()

	h := testHandler(t)
	u, _ := url.Parse(small.URL)
	makeProxyExt(t, h, []string{u.Hostname()})

	body := `{"url":"` + small.URL + `","method":"GET"}`
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/proxy-ext/proxy", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.HandleProxy(w, req, "proxy-ext")

	if w.Code != http.StatusOK {
		t.Fatalf("a normal response was refused: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Errorf("body = %q, want the upstream's", w.Body.String())
	}
}

// TestProxyContentType: JSON keeps the shape the client branches on; everything else becomes a
// type that cannot execute.
func TestProxyContentType(t *testing.T) {
	for _, tc := range []struct{ upstream, want string }{
		{"application/json", "application/json"},
		{"application/json; charset=utf-8", "application/json"},
		{"application/geo+json", "application/json"},
		{"text/html", "text/plain; charset=utf-8"},
		{"text/html; charset=utf-8", "text/plain; charset=utf-8"},
		{"image/svg+xml", "text/plain; charset=utf-8"},
		{"application/x-shockwave-flash", "text/plain; charset=utf-8"},
		{"", "text/plain; charset=utf-8"},
		{"not a media type at all", "text/plain; charset=utf-8"},
	} {
		if got := proxyContentType(tc.upstream); got != tc.want {
			t.Errorf("proxyContentType(%q) = %q, want %q", tc.upstream, got, tc.want)
		}
	}
}

// The response is served from MIKE's own origin, so an upstream's bytes must never arrive
// labelled as something the browser would run.
func TestHandleProxyDoesNotEchoUpstreamContentType(t *testing.T) {
	allowLoopbackProxy(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<script>alert(1)</script>"))
	}))
	defer upstream.Close()

	h := testHandler(t)
	u, _ := url.Parse(upstream.URL)
	makeProxyExt(t, h, []string{u.Hostname()})

	body := `{"url":"` + upstream.URL + `","method":"GET"}`
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/proxy-ext/proxy", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.HandleProxy(w, req, "proxy-ext")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q: an upstream chose the type of a MIKE-origin response", got)
	}
	// Only the label changed; the bytes still reach the extension.
	if !strings.Contains(w.Body.String(), "alert(1)") {
		t.Errorf("body = %q, want the upstream's bytes passed through", w.Body.String())
	}
}
