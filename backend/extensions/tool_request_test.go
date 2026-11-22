package extensions

import (
	"strings"
	"testing"
)

// What breaks if these slip: an argument the model wrote becomes part of the request's path, so the
// extension's credential pays a call at an endpoint its manifest never named.

func searchTool(query map[string]string) *ToolConfig {
	return &ToolConfig{
		Name: "web_search",
		Request: ToolRequest{
			Method: "GET",
			URL:    "{{config.base}}/search",
			Query:  query,
		},
		Response: ToolResponse{Results: "results"},
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"query": map[string]any{"type": "string"}},
		},
	}
}

var searchConfig = map[string]any{"base": "https://searx.example"}

func TestFillRequestKeepsAnArgumentInsideOnePathSegment(t *testing.T) {
	tool := searchTool(nil)
	tool.Request.URL = "{{config.base}}/search/{{args.query}}"

	// What the upstream must see for each argument the model can write. The '=' and '@' that a path
	// segment may legally hold stay; the four that would open a new segment, a query, a fragment or
	// an escape sequence do not.
	for _, tc := range []struct{ arg, wantURI string }{
		{"cats", "/search/cats"},
		{"two cats", "/search/two%20cats"},
		{"../../admin/users", "/search/..%2F..%2Fadmin%2Fusers"},
		{"hi?admin=true", "/search/hi%3Fadmin=true"},
		{"hi#frag", "/search/hi%23frag"},
		{"100%safe", "/search/100%25safe"},
		{"../../etc/passwd", "/search/..%2F..%2Fetc%2Fpasswd"},
		{"?x=1", "/search/%3Fx=1"},
		{"#x", "/search/%23x"},
		{"@evil.test/", "/search/@evil.test%2F"},
		{"//evil.test", "/search/%2F%2Fevil.test"},
	} {
		filled, err := tool.FillRequest(map[string]any{"query": tc.arg}, searchConfig)
		if err != nil {
			t.Fatalf("FillRequest(%q): %v", tc.arg, err)
		}
		const origin = "https://searx.example:443"
		uri := filled.URL[len(origin):]
		if uri != tc.wantURI {
			t.Errorf("argument %q was sent as %q, want %q", tc.arg, uri, tc.wantURI)
		}
		// The consequence that matters: no argument reaches a host other than the one a person saved.
		if host := hostOf(t, filled.URL); host != "searx.example" {
			t.Errorf("argument %q reached host %q", tc.arg, host)
		}
	}
}

// A second encode pass would reach the upstream as %2520 for a space.
func TestFillRequestEncodesAQueryValueExactlyOnce(t *testing.T) {
	tool := searchTool(map[string]string{"format": "json", "q": "{{args.query}}"})

	filled, err := tool.FillRequest(map[string]any{"query": "two cats/final"}, searchConfig)
	if err != nil {
		t.Fatalf("FillRequest: %v", err)
	}
	if got := filled.Query.Get("q"); got != "two cats/final" {
		t.Errorf("the argument arrived changed: %q", got)
	}
	if got := filled.URL; got != "https://searx.example:443/search?format=json&q=two+cats%2Ffinal" {
		t.Errorf("request URL = %q, want the argument encoded once", got)
	}
}

// Nothing reads the upstream at load, so a `results` path naming one value rather than a list shows
// only at the first call — the mistake a copied example carries into every manifest that copies it.
func TestRenderAnswerRefusesAResultsPathThatNamesOneValue(t *testing.T) {
	body := []byte(`{"page":{"name":"Example"},"status":{"indicator":"none","description":"All Systems Operational"},"components":[{"name":"API","status":"operational","description":"Request and response API"}]}`)

	list := ToolResponse{Results: "components", Fields: []ToolField{{Label: "name", Key: "name"}, {Label: "status", Key: "status"}}}
	answer, err := list.RenderAnswer(body, true)
	if err != nil {
		t.Fatalf("a list path must render: %v", err)
	}
	for _, want := range []string{"1.", "name: API", "status: operational"} {
		if !strings.Contains(answer, want) {
			t.Errorf("the answer is missing %q:\n%s", want, answer)
		}
	}

	for _, path := range []string{"status.description", "page"} {
		scalar := ToolResponse{Results: path, Fields: []ToolField{{Label: "status", Key: "description"}}}
		if _, err := scalar.RenderAnswer(body, true); err == nil {
			t.Errorf("results %q names one value and rendered anyway", path)
		} else if !strings.Contains(err.Error(), "not a list of results") {
			t.Errorf("results %q refused with the wrong sentence: %v", path, err)
		}
	}

	missing := ToolResponse{Results: "nothing.here", Fields: []ToolField{{Label: "name", Key: "name"}}}
	if _, err := missing.RenderAnswer(body, true); err == nil || !strings.Contains(err.Error(), "has no") {
		t.Errorf("an absent path should name itself: %v", err)
	}
}
