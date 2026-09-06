package extensions

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestHandleProxyCapsResponse bounds what an upstream may send back. The proxy is the
// one way an extension reaches the network, and its upstream is named by a third-party
// manifest, so without a cap any API on the allowlist could make the server buffer a
// response of any size.
func TestHandleProxyCapsResponse(t *testing.T) {
	// A real body just past the cap: the handler buffers whatever it is handed.
	const oversized = maxProxyResponseBytes + 4096
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
	if w.Body.Len() > int(maxProxyResponseBytes) {
		t.Errorf("the refusal itself carried %d bytes", w.Body.Len())
	}
}

// TestHandleProxyPassesNormalResponse is the other side: the cap is a bound, not a
// break. A response well inside it still reaches the extension unchanged.
func TestHandleProxyPassesNormalResponse(t *testing.T) {
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
