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
//
// It is also the rule for why its callers stay serial: egressCall is one package-level variable, so
// two tests that swap it at once each end up running the other one's dial policy.
func allowLoopbackProxy(t *testing.T) {
	t.Helper()
	prev := egressCall
	egressCall = func(ctx context.Context, req httputil.OutboundRequest) (*httputil.OutboundAnswer, error) {
		return httputil.DoOutboundWith(ctx, httputil.NewSingleAttemptClient(req.Timeout), req)
	}
	t.Cleanup(func() { egressCall = prev })
}

// TestExtensionProxyRefusesLoopbackTarget (security): a name on a manifest allowlist can
// resolve to this host, and the proxy dials from the host.
func TestExtensionProxyRefusesLoopbackTarget(t *testing.T) {
	t.Parallel()
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

// TestExtensionProxyAllowsLoopbackWhenPolicyIsLifted is the control: the refusal is the dial
// policy, not a broken test.
// serial for the reason at allowLoopbackProxy.
func TestExtensionProxyAllowsLoopbackWhenPolicyIsLifted(t *testing.T) {
	allowLoopbackProxy(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
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
		t.Fatalf("control case did not reach the upstream: %d %s", w.Code, w.Body.String())
	}
}
