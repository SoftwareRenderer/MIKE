package extensions

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The decision under test: which host is permitted, by whom, and what the request carries once permitted.
// It applies to the browser-callable proxy route and to a tool call alike.

func egressExt(network *NetworkConfig, config map[string]any) *Extension {
	ext := makeTestExt("egress-ext", "egress", ExtensionTypeTool)
	ext.Manifest.Capabilities = &Capabilities{Network: network}
	ext.Config = config
	return ext
}

func hostOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.Hostname()
}

// The extension's configuration, not the manifest, says where it may go.
func TestEgressAllowsAConfiguredHostWithNoAllowlist(t *testing.T) {
	allowLoopbackProxy(t)
	var got string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer upstream.Close()

	ext := egressExt(&NetworkConfig{}, nil)
	result, err := Egress(context.Background(), ext, EgressRequest{
		URL:          upstream.URL + "/search?q=x",
		GrantedHosts: []string{hostOf(t, upstream.URL)},
	})
	if err != nil {
		t.Fatalf("the configured host was refused: %v", err)
	}
	if result.StatusCode != http.StatusOK || !result.IsJSON() {
		t.Errorf("result = %+v", result)
	}
	if !strings.Contains(got, "/search?q=x") {
		t.Errorf("upstream saw %q", got)
	}
}

// An empty allowed_domains declares no peer; it does not permit every host.
func TestEgressRefusesAHostNobodyAllowed(t *testing.T) {
	allowLoopbackProxy(t)
	reached := false
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer upstream.Close()

	ext := egressExt(&NetworkConfig{}, nil)
	_, err := Egress(context.Background(), ext, EgressRequest{URL: upstream.URL})
	if !errors.Is(err, ErrEgressForbidden) {
		t.Fatalf("err = %v, want %v", err, ErrEgressForbidden)
	}
	if reached {
		t.Error("the upstream was dialed despite the refusal")
	}

	// A grant for a different host does not cover this one.
	if _, err := Egress(context.Background(), ext, EgressRequest{URL: upstream.URL, GrantedHosts: []string{"elsewhere.test"}}); !errors.Is(err, ErrEgressForbidden) {
		t.Errorf("err = %v, want the same refusal for a host nobody granted", err)
	}
}

// Unchanged for an extension that declares its peers and configures none.
func TestEgressStillHonoursTheManifestAllowlist(t *testing.T) {
	allowLoopbackProxy(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	ext := egressExt(&NetworkConfig{AllowedDomains: []string{hostOf(t, upstream.URL)}}, nil)
	if _, err := Egress(context.Background(), ext, EgressRequest{URL: upstream.URL}); err != nil {
		t.Fatalf("an allowlisted host was refused: %v", err)
	}

	other := egressExt(&NetworkConfig{AllowedDomains: []string{"api.github.com"}}, nil)
	if _, err := Egress(context.Background(), other, EgressRequest{URL: upstream.URL}); !errors.Is(err, ErrEgressForbidden) {
		t.Errorf("err = %v, want the allowlist refusal", err)
	}
}

// Templates are filled from the extension's configuration; one with no property behind it is left off.
func TestEgressSendsTheDeclaredHeaders(t *testing.T) {
	allowLoopbackProxy(t)
	var auth, missing, contentType, accept string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		missing = r.Header.Get("X-Unresolved")
		contentType = r.Header.Get("Content-Type")
		accept = r.Header.Get("Accept")
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	ext := egressExt(&NetworkConfig{Headers: map[string]string{
		"Authorization": "Bearer {{auth_token}}",
		"X-Unresolved":  "{{nope}}",
	}}, map[string]any{"auth_token": "tok"})

	if _, err := Egress(context.Background(), ext, EgressRequest{
		URL: upstream.URL, GrantedHosts: []string{hostOf(t, upstream.URL)},
	}); err != nil {
		t.Fatalf("egress: %v", err)
	}
	if auth != "Bearer tok" {
		t.Errorf("Authorization = %q, want the configured token", auth)
	}
	if missing != "" {
		t.Errorf("an unresolved template was sent: %q", missing)
	}
	if contentType != "application/json" || !strings.Contains(accept, "application/json") {
		t.Errorf("contentType=%q accept=%q", contentType, accept)
	}
}

// A contributed POST sends the declared parameters as a form: the declaration names parameters, not bytes.
func TestEgressSendsAFormNotAJSONBody(t *testing.T) {
	allowLoopbackProxy(t)
	var contentType, body string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	form := url.Values{"q": {"gVisor"}, "format": {"json"}}
	ext := egressExt(&NetworkConfig{}, nil)
	if _, err := Egress(context.Background(), ext, EgressRequest{
		URL: upstream.URL, Method: "POST", Form: form, GrantedHosts: []string{hostOf(t, upstream.URL)},
	}); err != nil {
		t.Fatalf("egress: %v", err)
	}
	if contentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want a form", contentType)
	}
	if !strings.Contains(body, "q=gVisor") {
		t.Errorf("body = %q", body)
	}

	// A caller cannot ask for both shapes at once, because neither would reliably win.
	if _, err := Egress(context.Background(), ext, EgressRequest{
		URL: upstream.URL, Method: "POST", Form: form, Body: "raw", GrantedHosts: []string{hostOf(t, upstream.URL)},
	}); !errors.Is(err, ErrEgressUnusable) {
		t.Errorf("err = %v, want the both-shapes refusal", err)
	}
}

func TestEgressRefusesUpFront(t *testing.T) {
	ext := egressExt(&NetworkConfig{AllowedDomains: []string{"x.test"}}, nil)

	if _, err := Egress(context.Background(), ext, EgressRequest{URL: "gopher://x.test:9/x"}); !errors.Is(err, ErrEgressUnusable) {
		t.Errorf("a non-http scheme: err = %v", err)
	}
	if _, err := Egress(context.Background(), ext, EgressRequest{URL: "not a url at all"}); !errors.Is(err, ErrEgressUnusable) {
		t.Errorf("an unparsable url: err = %v", err)
	}
	noNetwork := makeTestExt("no-net", "no-net", ExtensionTypeTool)
	if _, err := Egress(context.Background(), noNetwork, EgressRequest{URL: "https://x.test"}); !errors.Is(err, ErrEgressUnusable) {
		t.Errorf("an extension with no network capability: err = %v", err)
	}
}

// A redirect to another host would carry the request, and its token, somewhere nothing approved.
func TestEgressDoesNotFollowRedirects(t *testing.T) {
	allowLoopbackProxy(t)
	var redirected bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{URL: &url.URL{}}, target.URL, http.StatusFound)
	}))
	defer source.Close()

	ext := egressExt(&NetworkConfig{AllowedDomains: []string{hostOf(t, source.URL), hostOf(t, target.URL)}}, nil)
	result, err := Egress(context.Background(), ext, EgressRequest{URL: source.URL})
	if err != nil {
		t.Fatalf("egress: %v", err)
	}
	if result.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want the 3xx handed back to the caller", result.StatusCode)
	}
	if redirected {
		t.Error("the redirect was followed to a host nothing validated for this call")
	}
}
