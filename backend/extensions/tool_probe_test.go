package extensions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mike/backend/types"
)

// What is under test: the probe sends the extension's own request to the typed address, reports which
// failure it was, and stores nothing.

// addProbeExtension stores the reference search extension pointed at storedURL, and returns the handler.
func addProbeExtension(t *testing.T, storedURL string, headers map[string]string) *ExtensionHandler {
	t.Helper()
	h := testHandler(t)
	ext := addToolExtension(t, h.store, "search-ext", "searxng", storedURL, nil)
	ext.Manifest.Capabilities.Network.Headers = headers
	if err := h.store.Update(ext.ID, ext); err != nil {
		t.Fatalf("store headers: %v", err)
	}
	return h
}

// searchUpstream answers like a working instance, and records what it was asked.
func searchUpstream(t *testing.T, seen *http.Request, body string, status int, contentType string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = *r
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
}

func probeBody(key, address string, config map[string]any) string {
	payload := map[string]any{"key": key, "url": address}
	if config != nil {
		payload["config"] = config
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

func postProbe(t *testing.T, h *ExtensionHandler, payload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/search-ext/test-url", strings.NewReader(payload))
	w := httptest.NewRecorder()
	h.HandleTestURL(w, req, "search-ext")
	return w
}

func answerProbe(t *testing.T, h *ExtensionHandler, payload string) (int, testURLResponse) {
	t.Helper()
	w := postProbe(t, h, payload)
	var answer testURLResponse
	_ = json.Unmarshal(w.Body.Bytes(), &answer)
	return w.Code, answer
}

// The probe reaches the typed address: that is the only way a pre-save test can say anything about an
// unsaved value. It uses the production client, which admits loopback only for a granted hostname.
func TestProbeDialsTheTypedAddress(t *testing.T) {
	var seen http.Request
	upstream := searchUpstream(t, &seen, `{"number_of_results":412,"results":[{"title":"A","url":"https://a","content":"txt"}]}`,
		http.StatusOK, "application/json")
	defer upstream.Close()

	h := addProbeExtension(t, "https://somewhere.else", nil)
	_, answer := answerProbe(t, h, probeBody("base_url", upstream.URL, nil))

	if answer.Class != "reachable" || !answer.OK {
		t.Fatalf("answer = %+v, want reachable", answer)
	}
	if seen.URL.Path != "/search" {
		t.Errorf("upstream path = %q, want the declaration's /search", seen.URL.Path)
	}
	query := seen.URL.Query()
	if query.Get("format") != "json" {
		t.Errorf("format = %q — the declaration's own parameters must be sent", query.Get("format"))
	}
	if query.Get("q") != probeArgument {
		t.Errorf("q = %q, want the probe argument", query.Get("q"))
	}
}

// Otherwise the person saves an address, tests it later, and gets a 401 they could have seen first.
func TestProbeSendsTheUnsavedToken(t *testing.T) {
	var seen http.Request
	upstream := searchUpstream(t, &seen, `{"results":[{"title":"A"}]}`, http.StatusOK, "application/json")
	defer upstream.Close()

	h := addProbeExtension(t, upstream.URL, map[string]string{"Authorization": "Bearer {{auth_token}}"})
	_, answer := answerProbe(t, h, probeBody("base_url", upstream.URL, map[string]any{"auth_token": "typed-token"}))

	if answer.Class != "reachable" {
		t.Fatalf("answer = %+v", answer)
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer typed-token" {
		t.Errorf("Authorization = %q, want the unsaved token the form holds", got)
	}
}

// Ten bullets in a sensitive field stands for the stored value, which the browser was never shown.
func TestProbeSendsTheStoredTokenForAMaskedField(t *testing.T) {
	var seen http.Request
	upstream := searchUpstream(t, &seen, `{"results":[{"title":"A"}]}`, http.StatusOK, "application/json")
	defer upstream.Close()

	h := addProbeExtension(t, upstream.URL, map[string]string{"Authorization": "Bearer {{auth_token}}"})
	stored, err := h.store.GetByID("search-ext")
	if err != nil {
		t.Fatalf("read the row: %v", err)
	}
	stored.Config["auth_token"] = "vault-held-token"
	if err := h.store.Update(stored.ID, stored); err != nil {
		t.Fatalf("store the token: %v", err)
	}

	_, answer := answerProbe(t, h, probeBody("base_url", upstream.URL, map[string]any{"auth_token": types.MaskedSecret}))
	if !answer.OK {
		t.Fatalf("answer = %+v, want the saved token used", answer)
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer vault-held-token" {
		t.Errorf("Authorization = %q, want the stored token behind the mask", got)
	}
}

func TestProbeSendsNoCredentialForAnUnbackedMask(t *testing.T) {
	var seen http.Request
	upstream := searchUpstream(t, &seen, `{"results":[{"title":"A"}]}`, http.StatusOK, "application/json")
	defer upstream.Close()

	h := addProbeExtension(t, upstream.URL, map[string]string{"Authorization": "Bearer {{auth_token}}"})
	if _, answer := answerProbe(t, h, probeBody("base_url", upstream.URL, map[string]any{"auth_token": types.MaskedSecret})); !answer.OK {
		t.Fatalf("answer = %+v", answer)
	}
	if got := seen.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want no credential sent for a mask that stores nothing", got)
	}
}

// The answer has to say the probe ran without the token: a server that wants auth refuses it either way.
func TestProbeWithholdsTheStoredTokenFromAnotherHost(t *testing.T) {
	var seen http.Request
	upstream := searchUpstream(t, &seen, `{"results":[{"title":"A"}]}`, http.StatusOK, "application/json")
	defer upstream.Close()

	h := addProbeExtension(t, "https://search.example", map[string]string{"Authorization": "Bearer {{auth_token}}"})
	stored, err := h.store.GetByID("search-ext")
	if err != nil {
		t.Fatalf("read the row: %v", err)
	}
	stored.Config["auth_token"] = "vault-held-token"
	stored.Config["categories"] = "it"
	if err := h.store.Update(stored.ID, stored); err != nil {
		t.Fatalf("store the token: %v", err)
	}

	_, answer := answerProbe(t, h, probeBody("base_url", upstream.URL, nil))
	if got := seen.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q — the stored token reached an address the row never named", got)
	}
	if !strings.Contains(answer.Message, "different host") {
		t.Errorf("message %q does not say the saved token was withheld", answer.Message)
	}

	// Only credentials are withheld, so a stored non-secret still rides along: the reference
	// declaration routes `categories` into the `language` parameter, which the upstream sees.
	if got := seen.URL.Query().Get("language"); got != "it" {
		t.Errorf("language = %q, want the stored non-secret value to survive", got)
	}
}

// The dialog always posts the mask, so the host rule has to hold behind it too.
func TestProbeWithholdsTheStoredTokenBehindAMaskOnAnotherHost(t *testing.T) {
	var seen http.Request
	upstream := searchUpstream(t, &seen, `{"results":[{"title":"A"}]}`, http.StatusOK, "application/json")
	defer upstream.Close()

	h := addProbeExtension(t, "https://search.example", map[string]string{"Authorization": "Bearer {{auth_token}}"})
	stored, err := h.store.GetByID("search-ext")
	if err != nil {
		t.Fatalf("read the row: %v", err)
	}
	stored.Config["auth_token"] = "vault-held-token"
	if err := h.store.Update(stored.ID, stored); err != nil {
		t.Fatalf("store the token: %v", err)
	}

	_, answer := answerProbe(t, h, probeBody("base_url", upstream.URL, map[string]any{"auth_token": types.MaskedSecret}))
	if got := seen.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q — the mask reached another host carrying the stored token", got)
	}
	if !strings.Contains(answer.Message, "different host") {
		t.Errorf("message %q does not say the saved token was withheld", answer.Message)
	}
}

func TestProbeSendsTheStoredTokenToAnotherLoopbackSpelling(t *testing.T) {
	var seen http.Request
	upstream := searchUpstream(t, &seen, `{"results":[{"title":"A"}]}`, http.StatusOK, "application/json")
	defer upstream.Close()

	storedAddress := "http://localhost:" + strings.TrimPrefix(upstream.URL, "http://127.0.0.1:")
	h := addProbeExtension(t, storedAddress, map[string]string{"Authorization": "Bearer {{auth_token}}"})
	stored, err := h.store.GetByID("search-ext")
	if err != nil {
		t.Fatalf("read the row: %v", err)
	}
	stored.Config["auth_token"] = "vault-held-token"
	if err := h.store.Update(stored.ID, stored); err != nil {
		t.Fatalf("store the token: %v", err)
	}

	_, answer := answerProbe(t, h, probeBody("base_url", upstream.URL, map[string]any{"auth_token": types.MaskedSecret}))
	if got := seen.Header.Get("Authorization"); got != "Bearer vault-held-token" {
		t.Errorf("Authorization = %q, want the stored token: %s", got, answer.Message)
	}
}

func TestProbeSendsTheAddressItTestsEvenWhenThatPropertyIsSensitive(t *testing.T) {
	var seen http.Request
	upstream := searchUpstream(t, &seen, `{"results":[{"title":"A"}]}`, http.StatusOK, "application/json")
	defer upstream.Close()

	h := addProbeExtension(t, "https://search.example", map[string]string{"Authorization": "Bearer {{auth_token}}"})
	stored, err := h.store.GetByID("search-ext")
	if err != nil {
		t.Fatalf("read the row: %v", err)
	}
	for i := range stored.Manifest.ConfigSchema.Properties {
		if stored.Manifest.ConfigSchema.Properties[i].Key == "base_url" {
			stored.Manifest.ConfigSchema.Properties[i].Sensitive = true
		}
	}
	stored.Config["auth_token"] = "vault-held-token"
	if err := h.store.Update(stored.ID, stored); err != nil {
		t.Fatalf("store the row: %v", err)
	}

	_, answer := answerProbe(t, h, probeBody("base_url", upstream.URL, nil))
	wantHost := strings.TrimPrefix(upstream.URL, "http://")
	if seen.Host != wantHost {
		t.Fatalf("the probe reached %q, want the address under test %q (answer %q)", seen.Host, wantHost, answer.Message)
	}
	if got := seen.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q — the other host still got the stored token", got)
	}
}

// A test button must not change the stored row.
func TestProbeStoresNothing(t *testing.T) {
	var seen http.Request
	upstream := searchUpstream(t, &seen, `{"results":[{"title":"A"}]}`, http.StatusOK, "application/json")
	defer upstream.Close()

	h := addProbeExtension(t, "https://stored.example", map[string]string{"Authorization": "Bearer {{auth_token}}"})
	answerProbe(t, h, probeBody("base_url", upstream.URL, map[string]any{"auth_token": "secret", "categories": "it"}))

	stored, err := h.store.GetByID("search-ext")
	if err != nil {
		t.Fatalf("read the row back: %v", err)
	}
	if stored.Config["base_url"] != "https://stored.example" {
		t.Errorf("base_url = %v — the typed address was written", stored.Config["base_url"])
	}
	if _, ok := stored.Config["auth_token"]; ok {
		t.Error("auth_token was written by a probe")
	}
	if _, ok := stored.Config["categories"]; ok {
		t.Error("a form value was written by a probe")
	}
	if want := strings.TrimPrefix(upstream.URL, "http://"); seen.Host != want {
		t.Errorf("the probe reached %q, not the typed address %q", seen.Host, want)
	}
}

// Each class sends the person to a different fix.
func TestProbeNamesEachFailure(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantClass   string
		wantIn      string
	}{
		{"works", http.StatusOK, "application/json", `{"results":[{"title":"A"},{"title":"B"}]}`, "reachable", "2 results"},
		{"answers html", http.StatusOK, "text/html", `<!doctype html><html>`, "not_json", "rather than JSON"},
		{"refuses the format", http.StatusForbidden, "text/html", `<html>error</html>`, "not_json", "HTTP 403"},
		{"limiter", http.StatusTooManyRequests, "application/json", `{}`, "rate_limited", "rate limiter"},
		{"wants a token", http.StatusUnauthorized, "application/json", `{}`, "refused", "auth token"},
		{"json of another shape", http.StatusOK, "application/json", `{"page":1}`, "unexpected_shape", "results"},
		{"answers nothing", http.StatusOK, "application/json", `{"results":[]}`, "empty", "no results"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var seen http.Request
			upstream := searchUpstream(t, &seen, tc.body, tc.status, tc.contentType)
			defer upstream.Close()

			h := addProbeExtension(t, upstream.URL, nil)
			code, answer := answerProbe(t, h, probeBody("base_url", upstream.URL, nil))
			if code != http.StatusOK {
				t.Fatalf("status = %d, want 200 with the answer in the body: %s", code, answer.Message)
			}
			if answer.Class != tc.wantClass {
				t.Errorf("class = %q, want %q (message %q)", answer.Class, tc.wantClass, answer.Message)
			}
			if !strings.Contains(answer.Message, tc.wantIn) {
				t.Errorf("message %q does not say %q", answer.Message, tc.wantIn)
			}
			if answer.OK != (tc.wantClass == "reachable") {
				t.Errorf("ok = %v for class %q", answer.OK, answer.Class)
			}
		})
	}
}

// Nothing listening and no route are one class: the next step is the same.
func TestProbeReportsAnAddressThatNeverAnswers(t *testing.T) {
	h := addProbeExtension(t, "https://stored.example", nil)
	_, answer := answerProbe(t, h, probeBody("base_url", "http://127.0.0.1:1", nil))

	if answer.Class != "unreachable" {
		t.Fatalf("class = %q, want unreachable (message %q)", answer.Class, answer.Message)
	}
	if !strings.Contains(answer.Message, "Nothing answered at 127.0.0.1") {
		t.Errorf("message %q does not name the host that did not answer", answer.Message)
	}
}

// The probe may dial only an address one of this extension's own tools would send to.
func TestProbeRefusesAKeyNothingSendsTo(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantIn  string
	}{
		{"no key", `{"url":"https://searx.example"}`, "key is required"},
		{"unknown key", `{"key":"nope","url":"https://searx.example"}`, "not a property"},
		{"a key that is not an address", `{"key":"categories","url":"https://searx.example"}`, `not an address`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := addProbeExtension(t, "https://stored.example", nil)
			w := postProbe(t, h, tc.payload)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for %s", w.Code, tc.payload)
			}
			if !strings.Contains(w.Body.String(), tc.wantIn) {
				t.Errorf("the refusal does not say %q", tc.wantIn)
			}
		})
	}
}

// A broken address is answered in the body, where the dialog already shows answers.
func TestProbeAnswersAboutABadAddress(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"a name with no scheme", "searx.example"},
		{"credentials in the address", "https://user:pw@searx.example"},
		{"nothing typed", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := addProbeExtension(t, "https://stored.example", nil)
			code, answer := answerProbe(t, h, probeBody("base_url", tc.url, nil))
			if code != http.StatusOK {
				t.Fatalf("status = %d, want 200 with the answer in the body", code)
			}
			if answer.Class != "unusable" || answer.OK {
				t.Errorf("answer = %+v, want an unusable-address answer", answer)
			}
			if answer.Message == "" {
				t.Error("the answer says nothing about what is wrong with the address")
			}
		})
	}
}

// TestProbeNeedsNetworkCapability: the network capability is what says this extension talks at all.
func TestProbeNeedsNetworkCapability(t *testing.T) {
	h := testHandler(t)
	ext := addToolExtension(t, h.store, "silent-ext", "quiet", "https://searx.example", nil)
	ext.Manifest.Capabilities.Network = nil
	if err := h.store.Update(ext.ID, ext); err != nil {
		t.Fatalf("update: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/extensions/silent-ext/test-url",
		strings.NewReader(probeBody("base_url", "https://searx.example", nil)))
	w := httptest.NewRecorder()
	h.HandleTestURL(w, req, "silent-ext")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

// A missing row answers like every other extension route.
func TestProbeMissingExtension(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/nope/test-url",
		strings.NewReader(probeBody("base_url", "https://searx.example", nil)))
	w := httptest.NewRecorder()
	h.HandleTestURL(w, req, "nope")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// The probe's view is a copy: a shared map would put an unsaved token into the stored row.
func TestWithOverlayConfigLeavesTheRowAlone(t *testing.T) {
	row := makeTestExt("row-ext", "row", ExtensionTypeTool)
	row.Manifest.ConfigSchema = searxngLikeSchema()
	row.Config = map[string]any{"base_url": "https://stored.example", "categories": "general"}

	probe := row.withOverlayConfig(map[string]any{"auth_token": "typed", "not_a_property": "x",
		"categories": "it", "base_url": "https://typed.example"})

	if row.Config["base_url"] != "https://stored.example" || len(row.Config) != 2 {
		t.Errorf("the row itself changed: %v", row.Config)
	}
	if probe.Config["base_url"] != "https://typed.example" || probe.Config["auth_token"] != "typed" {
		t.Errorf("the probe did not see the form's values: %v", probe.Config)
	}
	if probe.Config["categories"] != "it" {
		t.Errorf("categories = %v, want the form's value", probe.Config["categories"])
	}
	if _, ok := probe.Config["not_a_property"]; ok {
		t.Error("a value no property declares reached the extension's configuration")
	}
}
