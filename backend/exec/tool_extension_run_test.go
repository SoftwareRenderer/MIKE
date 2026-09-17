package exec

import (
	"context"
	"encoding/json"
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

// The default is the manifest's; a parameter resolving to nothing is dropped, not sent empty.
func TestContributedToolDefaultsAnOmittedArgument(t *testing.T) {
	fake := &recordedEgress{result: jsonResult(searxResults(1, 1))}
	if _, ok := runSearch(t, "https://searx.example", map[string]any{"query": "x"}, fake); !ok {
		t.Fatal("the call failed")
	}
	u, _ := url.Parse(fake.url)
	if got := u.Query().Get("categories"); got != "general" {
		t.Errorf("categories = %q, want the declared default", got)
	}
}

// max_results limits how much of an answer reaches the model, and the answer says how much was left out.
func TestContributedToolCapsWhatItRenders(t *testing.T) {
	fake := &recordedEgress{result: jsonResult(searxResults(12, 12))}
	result, ok := runSearch(t, "https://searx.example", map[string]any{"query": "x"}, fake)
	if !ok {
		t.Fatalf("the call failed: %s", result)
	}
	if !strings.HasPrefix(result, "5 results of 12") {
		t.Errorf("answer header = %q, want five rendered of twelve", firstLine(result))
	}
	if !strings.Contains(result, "7 more results") {
		t.Errorf("the answer hides what was left out:\n%s", result)
	}
	if strings.Contains(result, "Result F") {
		t.Error("the cap was not applied")
	}
}

// A POST sends the declared parameters as a form: the model supplies arguments, never bytes.
func TestContributedToolSendsAFormOnPost(t *testing.T) {
	manifest := strings.Replace(searxngManifest, `"method": "GET"`, `"method": "POST"`, 1)
	ext := searchToolExtension("http://127.0.0.1:8888")
	var parsed extensions.ExtensionManifest
	if err := json.Unmarshal([]byte(manifest), &parsed); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	ext.Manifest = parsed

	te := newTestExecutorWith(t, Deps{ExtensionTools: toolRegistryWith(t, ext)})
	fake := &recordedEgress{result: jsonResult(searxResults(1, 1))}
	te.tools.egress = fake.func_
	if _, ok := te.executeTool(context.Background(), "web_search", map[string]any{"query": "x"}, "/tmp/wt",
		&types.Task{ID: "t"}, &executionState{}, nil); !ok {
		t.Fatal("the POST call failed")
	}
	if fake.method != "POST" {
		t.Errorf("method = %q, want POST", fake.method)
	}
	if strings.Contains(fake.url, "?") {
		t.Errorf("a POST put its parameters in the URL: %s", fake.url)
	}
	if got := fake.form.Get("q"); got != "x" {
		t.Errorf("form q = %q, want the argument", got)
	}
	// The grant travels with the call.
	if len(fake.granted) != 1 || fake.granted[0] != "127.0.0.1" {
		t.Errorf("granted = %v, want the configured loopback host", fake.granted)
	}
}

// Each upstream failure names the misconfiguration a person can correct.
func TestContributedToolFailuresSayWhatToFix(t *testing.T) {
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

// One field must not pull the whole upstream document into the context.
func TestContributedToolClampsASnippet(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"number_of_results": 1,
		"results":           []any{map[string]any{"title": "T", "url": "u", "content": strings.Repeat("word ", 400)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := &recordedEgress{result: jsonResult(body)}
	result, ok := runSearch(t, "https://searx.example", map[string]any{"query": "x"}, fake)
	if !ok {
		t.Fatalf("the call failed: %s", result)
	}
	if strings.Contains(result, strings.Repeat("word ", 400)) {
		t.Errorf("the snippet was not clamped (%d bytes)", len(result))
	}
	if !strings.Contains(result, "…") {
		t.Error("a clamped snippet does not say it was clamped")
	}
}

// No results is an answer, not an error.
func TestContributedToolEmptyAnswerIsNotAFailure(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"number_of_results": 0, "results": []any{}})
	fake := &recordedEgress{result: jsonResult(body)}
	result, ok := runSearch(t, "https://searx.example", map[string]any{"query": "x"}, fake)
	if !ok {
		t.Fatalf("an empty result set was reported as a failure: %s", result)
	}
	if !strings.Contains(result, "No results") {
		t.Errorf("answer = %q", result)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
