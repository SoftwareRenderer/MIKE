package extensions

import "testing"

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
