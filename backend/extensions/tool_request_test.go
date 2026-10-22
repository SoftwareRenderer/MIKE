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

	for _, tc := range []struct{ arg, wantURI string }{
		{"cats", "/search/cats"},
		{"two cats", "/search/two%20cats"},
		{"../../admin/users", "/search/..%2F..%2Fadmin%2Fusers"},
		{"hi?admin=true", "/search/hi%3Fadmin=true"},
		{"hi#frag", "/search/hi%23frag"},
		{"100%safe", "/search/100%25safe"},
	} {
		filled, err := tool.FillRequest(map[string]any{"query": tc.arg}, searchConfig)
		if err != nil {
			t.Fatalf("FillRequest(%q): %v", tc.arg, err)
		}
		uri := filled.URL[len("https://searx.example:443"):]
		if uri != tc.wantURI {
			t.Errorf("argument %q was sent as %q, want %q", tc.arg, uri, tc.wantURI)
		}
	}
}

func TestFillRequestEscapeCannotLeaveTheConfiguredAddress(t *testing.T) {
	tool := searchTool(nil)
	tool.Request.URL = "{{config.base}}/search/{{args.query}}"

	for _, arg := range []string{"../../etc/passwd", "?x=1", "#x", "@evil.test/", "//evil.test"} {
		filled, err := tool.FillRequest(map[string]any{"query": arg}, searchConfig)
		if err != nil {
			t.Fatalf("FillRequest(%q): %v", arg, err)
		}
		if host := hostOf(t, filled.URL); host != "searx.example" {
			t.Errorf("argument %q reached host %q", arg, host)
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
