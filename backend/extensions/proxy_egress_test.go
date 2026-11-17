package extensions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"mike/backend/httputil"
)

// allowLoopbackProxy lets one test proxy to an httptest server, which listens on the loopback
// address production refuses. The egress rule must not be exercised through it.
func allowLoopbackProxy(t *testing.T) {
	t.Helper()
	prev := egressCall
	egressCall = func(ctx context.Context, req httputil.OutboundRequest) (*httputil.OutboundAnswer, error) {
		return httputil.DoOutboundWith(ctx, httputil.NewSingleAttemptClient(req.Timeout), req)
	}
	t.Cleanup(func() { egressCall = prev })
}

// TestExtensionProxyRefusesLoopbackTarget (security): a name on a manifest allowlist can
// resolve to this host, and the proxy dials from the host. The control that this is the dial
// policy and not a broken proxy is TestHandleProxyPassesNormalResponse, which reaches an
// upstream over the same route with the loopback rule lifted.
func TestExtensionProxyRefusesLoopbackTarget(t *testing.T) {
	var reached bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Write([]byte(`{"secret":"value"}`))
	}))
	defer upstream.Close()

	h := testHandler(t)
	u, _ := url.Parse(upstream.URL)
	makeProxyExt(t, h, []string{u.Hostname()})

	body := `{"url":"` + upstream.URL + `","method":"GET"}`
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/proxy-ext/proxy", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.HandleProxy(w, req, "proxy-ext")

	if reached {
		t.Fatal("the extension reached a loopback service through the host")
	}
	if w.Code == http.StatusOK {
		t.Fatalf("a loopback target was proxied: %s", w.Body.String())
	}
}
