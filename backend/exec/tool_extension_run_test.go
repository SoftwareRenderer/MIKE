package exec

import (
	"context"
	"encoding/json/v2"
	"net/url"
	"strings"
	"testing"
	"time"

	"mike/backend/extensions"
	"mike/backend/types"
)

// The orchestration: what request the declaration produces, what the answer becomes, what a failure says.
// The address rule is tested in backend/extensions, where it lives.

// searxResults is an upstream answer in the shape the test manifest declares.
func searxResults(count int, total int) []byte {
	type result struct {
		Title string `json:"title"`
		URL   string `json:"url"`
		Text  string `json:"content"`
	}
	body := map[string]any{"number_of_results": total, "query": "gVisor"}
	items := make([]result, 0, count)
	for i := 0; i < count; i++ {
		items = append(items, result{
			Title: "Result " + string(rune('A'+i)),
			URL:   "https://example.test/" + string(rune('a'+i)),
			Text:  "snippet " + string(rune('a'+i)),
		})
	}
	body["results"] = items
	data, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return data
}

type recordedEgress struct {
	calls     int
	url       string
	method    string
	form      url.Values
	granted   []string
	deadline  time.Time
	hadDeadln bool
	result    *extensions.EgressResult
	err       error
}

func (e *recordedEgress) func_(ctx context.Context, ext *extensions.Extension, req extensions.EgressRequest) (*extensions.EgressResult, error) {
	e.calls++
	e.url = req.URL
	e.method = req.Method
	e.form = req.Form
	e.granted = req.GrantedHosts
	e.deadline, e.hadDeadln = ctx.Deadline()
	if e.err != nil {
		return nil, e.err
	}
	return e.result, nil
}

func jsonResult(body []byte) *extensions.EgressResult {
	return &extensions.EgressResult{StatusCode: 200, ContentType: "application/json", Body: body}
}

// runSearch dispatches one call against an executor whose extension is configured at address.
func runSearch(t *testing.T, address string, args map[string]any, fake *recordedEgress) (string, bool) {
	t.Helper()
	te := newTestExecutorWith(t, Deps{ExtensionTools: toolRegistryWith(t, searchToolExtension(address))})
	te.tools.egress = fake.func_
	return te.executeTool(context.Background(), "web_search", args, "/tmp/worktree",
		&types.Task{ID: "task-1", OperatingMode: types.ModeAct}, &executionState{}, nil)
}

// The manifest's template becomes the URL egress was called with, and the answer arrives as text.
func TestContributedToolRunsTheDeclaredRequest(t *testing.T) {
	t.Parallel()
	fake := &recordedEgress{result: jsonResult(searxResults(3, 41))}
	result, ok := runSearch(t, "https://searx.example", map[string]any{"query": "gVisor", "category": "it"}, fake)
	if !ok {
		t.Fatalf("the call failed: %s", result)
	}
	if fake.calls != 1 {
		t.Fatalf("egress calls = %d, want 1", fake.calls)
	}
	u, err := url.Parse(fake.url)
	if err != nil {
		t.Fatalf("the request URL does not parse: %v", err)
	}
	if u.Scheme+"://"+u.Host != "https://searx.example:443" || u.Path != "/search" {
		t.Errorf("requested %s%s, want the configured origin plus /search", u.Scheme, u.Path)
	}
	if got := u.Query().Get("q"); got != "gVisor" {
		t.Errorf("q = %q, want the argument the model passed", got)
	}
	if got := u.Query().Get("categories"); got != "it" {
		t.Errorf("categories = %q, want the declared argument", got)
	}
	if got := u.Query().Get("format"); got != "json" {
		t.Errorf("format = %q, want the manifest's constant", got)
	}
	if len(fake.granted) != 1 || fake.granted[0] != "searx.example" {
		t.Errorf("granted hosts = %v, want the host a person configured", fake.granted)
	}
	if !fake.hadDeadln {
		t.Error("the upstream call has no deadline; a silent instance would hold the turn")
	}
	if !strings.Contains(result, "3 results") || !strings.Contains(result, "41 the upstream says match") {
		t.Errorf("answer header is wrong:\n%s", result)
	}
	for _, want := range []string{"1. title: Result A", "url: https://example.test/a", "snippet: snippet a"} {
		if !strings.Contains(result, want) {
			t.Errorf("answer is missing %q:\n%s", want, result)
		}
	}
}

// The declaration's own request is what goes out, for either verb: the manifest templates the URL and
// the query, the model fills the arguments, and a parameter that resolves to nothing takes the
// declared default. The grant — the host a person typed into the `url` property — travels with the
// call, which is what widens the address rule for loopback and nothing else.
func TestContributedToolSendsTheDeclaredRequest(t *testing.T) {
	t.Parallel()
	postManifest := strings.Replace(searxngManifest, `"method": "GET"`, `"method": "POST"`, 1)
	postExt := searchToolExtension("http://127.0.0.1:8888")
	if err := json.Unmarshal([]byte(postManifest), &postExt.Manifest); err != nil {
		t.Fatalf("manifest: %v", err)
	}

	cases := []struct {
		name    string
		address string
		ext     *extensions.Extension
		args    map[string]any
		method  string
		url     string // scheme://host[:port] + path
		query   map[string]string
		form    map[string]string
		granted string
	}{
		{
			name: "GET fills the declared query", address: "https://searx.example",
			args:   map[string]any{"query": "x", "category": "it"},
			method: "GET", url: "https://searx.example:443/search",
			query:   map[string]string{"q": "x", "categories": "it", "format": "json"},
			granted: "searx.example",
		},
		{
			name: "an omitted argument takes its declared default", address: "https://searx.example",
			args:   map[string]any{"query": "x"},
			method: "GET", url: "https://searx.example:443/search",
			query:   map[string]string{"q": "x", "categories": "general", "format": "json"},
			granted: "searx.example",
		},
		{
			name: "POST sends the arguments as a form", ext: postExt,
			args:   map[string]any{"query": "x"},
			method: "POST", url: "http://127.0.0.1:8888/search",
			form:    map[string]string{"q": "x", "categories": "general", "format": "json"},
			granted: "127.0.0.1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &recordedEgress{result: jsonResult(searxResults(1, 1))}
			var result string
			var ok bool
			if tc.ext != nil {
				te := newTestExecutorWith(t, Deps{ExtensionTools: toolRegistryWith(t, tc.ext)})
				te.tools.egress = fake.func_
				result, ok = te.executeTool(context.Background(), "web_search", tc.args, "/tmp/wt",
					&types.Task{ID: "t"}, &executionState{}, nil)
			} else {
				result, ok = runSearch(t, tc.address, tc.args, fake)
			}
			if !ok {
				t.Fatalf("the call failed: %s", result)
			}
			if fake.calls != 1 {
				t.Fatalf("egress calls = %d, want 1", fake.calls)
			}
			if fake.method != tc.method {
				t.Errorf("method = %q, want %q", fake.method, tc.method)
			}
			u, err := url.Parse(fake.url)
			if err != nil {
				t.Fatalf("the request URL does not parse: %v", err)
			}
			if got := u.Scheme + "://" + u.Host + u.Path; got != tc.url {
				t.Errorf("requested %q, want the configured origin plus its declared path", got)
			}
			for key, want := range tc.query {
				if got := u.Query().Get(key); got != want {
					t.Errorf("query %s = %q, want %q", key, got, want)
				}
			}
			if tc.method == "POST" {
				if strings.Contains(fake.url, "?") {
					t.Errorf("a POST put its parameters in the URL: %s", fake.url)
				}
				for key, want := range tc.form {
					if got := fake.form.Get(key); got != want {
						t.Errorf("form %s = %q, want %q", key, got, want)
					}
				}
			}
			if len(fake.granted) != 1 || fake.granted[0] != tc.granted {
				t.Errorf("granted hosts = %v, want the one host a person configured (%s)", fake.granted, tc.granted)
			}
			if !fake.hadDeadln {
				t.Error("the upstream call has no deadline; a silent instance would hold the turn")
			}
		})
	}
}

// What an upstream answer becomes in the conversation: `max_results` bounds how much of it is
// rendered and the answer says how much was left out, a declared snippet length clamps one field
// rather than letting it pull the whole document in, and no results is an answer, not a failure.
func TestContributedToolAnswerShape(t *testing.T) {
	t.Parallel()
	wide, err := json.Marshal(map[string]any{
		"number_of_results": 1,
		"results":           []any{map[string]any{"title": "T", "url": "u", "content": strings.Repeat("word ", 400)}},
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		result *extensions.EgressResult
		wants  []string
		bans   []string
	}{
		{
			name:   "max_results caps the rendered items and says what was left out",
			result: jsonResult(searxResults(12, 12)),
			wants:  []string{"5 results of 12", "7 more results"},
			bans:   []string{"Result F"},
		},
		{
			name:   "a long field is clamped and says so",
			result: jsonResult(wide),
			wants:  []string{"…"},
			bans:   []string{strings.Repeat("word ", 400)},
		},
		{
			name:   "no results is an answer",
			result: jsonResult([]byte(`{"number_of_results":0,"results":[]}`)),
			wants:  []string{"No results"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &recordedEgress{result: tc.result}
			result, ok := runSearch(t, "https://searx.example", map[string]any{"query": "x"}, fake)
			if !ok {
				t.Fatalf("the call failed: %s", result)
			}
			for _, want := range tc.wants {
				if !strings.Contains(result, want) {
					t.Errorf("the answer is missing %q:\n%s", want, firstLine(result))
				}
			}
			for _, ban := range tc.bans {
				if strings.Contains(result, ban) {
					t.Errorf("the answer carries %q, which the declaration capped", firstChars(ban, 40))
				}
			}
		})
	}
}

// Each upstream failure names the misconfiguration a person can correct.
func TestContributedToolFailuresSayWhatToFix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		result  *extensions.EgressResult
		wantSub string
	}{
		{"html answer", &extensions.EgressResult{StatusCode: 200, ContentType: "text/html", Body: []byte("<html>")},
			"HTML page"},
		{"refused", &extensions.EgressResult{StatusCode: 403, ContentType: "application/json", Body: []byte("{}")},
			"refused the request"},
		{"rate limited", &extensions.EgressResult{StatusCode: 429, ContentType: "application/json", Body: []byte("{}")},
			"too many requests"},
		{"json without the declared field", jsonResult([]byte(`{"suggestions":[]}`)), `"results" field`},
		{"json but not a list", jsonResult([]byte(`{"results":{"a":1}}`)), "not a list of results"},
		{"unparsable json", jsonResult([]byte("{")), "does not parse"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &recordedEgress{result: tc.result}
			result, ok := runSearch(t, "https://searx.example", map[string]any{"query": "x"}, fake)
			if ok {
				t.Fatalf("a broken answer was reported as success: %s", result)
			}
			if !strings.Contains(result, tc.wantSub) {
				t.Errorf("answer %q does not say %q", result, tc.wantSub)
			}
		})
	}
}

// A call that arrives with no address configured is refused, not guessed at.
func TestContributedToolUnconfiguredRefusesTheCall(t *testing.T) {
	t.Parallel()
	fake := &recordedEgress{result: jsonResult(searxResults(1, 1))}
	result, ok := runSearch(t, "", map[string]any{"query": "x"}, fake)
	if ok {
		t.Fatalf("an unconfigured extension ran a call: %s", result)
	}
	if !strings.Contains(result, "Unknown tool: web_search") {
		t.Errorf("answer = %q, want the unknown-tool vocabulary", result)
	}
	if fake.calls != 0 {
		t.Errorf("egress was called %d times for a tool that is not offered", fake.calls)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
