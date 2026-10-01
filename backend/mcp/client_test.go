package mcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mike/backend/toolresult"
	"mike/backend/types"
)

// The fake is the thing that makes every claim in this package testable without a socket on the
// internet: the SDK's own server, pinned to the era MIKE speaks, answering over Streamable HTTP.

type fakeOptions struct {
	// JSON makes the server answer with application/json instead of an event stream, which is the
	// other shape a conforming server may choose.
	JSON bool
	// Tools is what the fake lists; a handler that fails is one of them.
	Stateless bool
	// Slow delays tools/list past any sane deadline.
	Slow time.Duration
	// BigText is the size of the answer the "big" tool returns.
	BigText int
	// EchoText makes the echo tool answer with its "text" argument.
	EchoText bool
	// RequireToken refuses every request that does not carry it as a bearer token, which is how the
	// Test action's stored-token path is reachable without a live server.
	RequireToken string
	// Elicit names a field the "elicit_me" tool asks the client for mid-call, which the pinned era
	// refuses: the tool reports that refusal instead of waiting for a person.
	Elicit string
}

// fakeServer is a running MCP server plus the URL a registered server would hold.
type fakeServer struct {
	URL string
	// requests counts the POSTs that reached the handler, which is how a test knows MIKE's egress client
	// is the one dialling. It is atomic because the offer dials servers concurrently.
	requests atomic.Int32
}

func (f *fakeServer) Requests() int { return int(f.requests.Load()) }

func fakeMCPServer(t *testing.T, opts fakeOptions) *fakeServer {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "echo",
		Description: "Echo the text back",
		Annotations: &mcp.ToolAnnotations{Title: "Echo tool"},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Text string `json:"text" jsonschema:"the text to echo"`
	}) (*mcp.CallToolResult, any, error) {
		if !opts.EchoText {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + in.Text}}}, nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{Name: "always_fails", Description: "Reports its own failure"},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "upstream said no"}},
			}, nil, nil
		})

	if opts.BigText > 0 {
		size := opts.BigText
		mcp.AddTool(srv, &mcp.Tool{Name: "big"},
			func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", size)}},
				}, nil, nil
			})
	}

	if opts.Elicit != "" {
		field := opts.Elicit
		mcp.AddTool(srv, &mcp.Tool{Name: "elicit_me", Description: "Asks the client for a field mid-call"},
			func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
				_, err := req.Session.Elicit(ctx, &mcp.ElicitParams{
					Message:         "which target?",
					RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{field: map[string]any{"type": "string"}}},
				})
				text := fmt.Sprintf("elicitation answered: %v", err)
				return &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{&mcp.TextContent{Text: text}},
				}, nil, nil
			})
	}

	fake := &fakeServer{}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: opts.Stateless, JSONResponse: opts.JSON})
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.requests.Add(1)
		if opts.RequireToken != "" && r.Header.Get("Authorization") != "Bearer "+opts.RequireToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && opts.Slow > 0 {
			select {
			case <-time.After(opts.Slow):
			case <-r.Context().Done():
				return
			}
		}
		handler.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(wrapped)
	t.Cleanup(ts.Close)
	fake.URL = ts.URL
	return fake
}

func registered(url string, timeoutSeconds int) types.MCPServer {
	return types.MCPServer{
		ID: "a1b2c3d4e5f60708", Name: "Fake", URL: url, Enabled: true,
		TimeoutSeconds: timeoutSeconds,
	}
}

var testID = Identity{Name: "mike-test", Version: "0"}

// connect opens and closes a session inside the test, so a failure reports the connect error.
func dial(t *testing.T, server types.MCPServer) *session {
	t.Helper()
	sess, err := connect(context.Background(), server, testID)
	if err != nil {
		t.Fatalf("connect to %s: %v", server.URL, err)
	}
	t.Cleanup(func() { _ = sess.close() })
	return sess
}

func findTool(tools []Tool, name string) *Tool {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}

// kindOf reads the Failure out of an error, failing the test when it is not one of ours.
func kindOf(t *testing.T, err error) Failure {
	t.Helper()
	var failure *Error
	if !asError(err, &failure) || failure == nil {
		t.Fatalf("error is not an *mcp.Error: %T %v", err, err)
	}
	return failure.Kind
}

func asError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}

// TestConnectSpeaksThePinnedEra is the whole of D2 on the client side: the session must come back
// having negotiated Era, and a server that answers a different version must be refused.
func TestConnectNegotiatesThePinnedEra(t *testing.T) {
	for _, name := range []string{"event-stream", "json"} {
		t.Run(name, func(t *testing.T) {
			fake := fakeMCPServer(t, fakeOptions{
				JSON:      name == "json",
				Stateless: true,
				EchoText:  false,
				BigText:   4096,
			})
			sess := dial(t, registered(fake.URL, 5))

			tools, dropped, err := sess.listTools(context.Background())
			if err != nil {
				t.Fatalf("list tools: %v", err)
			}
			if dropped != 0 {
				t.Errorf("dropped %d tools, want none", dropped)
			}
			if len(tools) != 3 {
				t.Fatalf("listed %d tools, want 3: %+v", len(tools), tools)
			}
			echo := findTool(tools, "echo")
			if echo == nil {
				t.Fatalf("no echo tool in %+v", tools)
			}
			if echo.DisplayName != "Echo tool" {
				t.Errorf("display name = %q, want the tool's own title", echo.DisplayName)
			}
			// The schema must survive as a map, because that is what the provider is handed.
			if props, ok := echo.Parameters["properties"].(map[string]any); !ok {
				t.Errorf("input schema = %v, want an object with properties", echo.Parameters)
			} else if _, ok := props["text"]; !ok {
				t.Errorf("input schema lost its argument: %v", echo.Parameters)
			}

			answer, err := sess.call(context.Background(), "echo", map[string]any{"text": "hello"})
			if err != nil {
				t.Fatalf("call echo: %v", err)
			}
			if answer != "echo: hello" {
				t.Errorf("answer = %q, want %q", answer, "echo: hello")
			}
			if fake.Requests() == 0 {
				t.Error("the fake saw no request, so MIKE did not dial it")
			}
		})
	}
}

// A tool that reports failure is the model's problem to read, not a transport fault: the kind says
// tool_error and the text carries what the server said.
func TestCallToolReportedFailure(t *testing.T) {
	fake := fakeMCPServer(t, fakeOptions{Stateless: true})
	sess := dial(t, registered(fake.URL, 5))

	answer, err := sess.call(context.Background(), "always_fails", nil)
	if err == nil {
		t.Fatalf("a failing tool answered %q with no error", answer)
	}
	if kind := kindOf(t, err); kind != FailureToolError {
		t.Errorf("kind = %q, want %q", kind, FailureToolError)
	}
	if !strings.Contains(err.Error(), "upstream said no") {
		t.Errorf("the tool's own words are missing from %q", err)
	}
}

// The era forbids a server eliciting a human while serving a tools/call, so the turn is never parked.
// The asserted fragment is the SDK's rule, not MIKE's: if an upgrade makes this red, the handler question
// (docs/mcp.md) reopens.
func TestCallElicitationCannotInterruptAToolCall(t *testing.T) {
	fake := fakeMCPServer(t, fakeOptions{Stateless: true, Elicit: "target"})
	sess := dial(t, registered(fake.URL, 5))

	_, err := sess.call(context.Background(), "elicit_me", nil)
	if err == nil {
		t.Fatal("the server elicited mid-call; the pinned era forbids it")
	}
	if kind := kindOf(t, err); kind != FailureToolError {
		t.Errorf("kind = %q, want the server's own tool failure: the refusal is the server's to report", kind)
	}
	if !strings.Contains(err.Error(), "cannot be sent while serving a request") {
		t.Errorf("the era's own refusal is missing from %q", err)
	}
}

// The answer is bounded where it is captured, not where it is shown.
func TestCallAnswerIsCapped(t *testing.T) {
	fake := fakeMCPServer(t, fakeOptions{Stateless: true, BigText: 4 << 20})
	sess := dial(t, registered(fake.URL, 10))

	answer, err := sess.call(context.Background(), "big", nil)
	if err != nil {
		t.Fatalf("call big: %v", err)
	}
	if len(answer) > toolresult.MaxCaptureBytes+4096 {
		t.Errorf("answer is %d bytes, want it capped near %d", len(answer), toolresult.MaxCaptureBytes)
	}
	if !strings.Contains(answer, "truncated") {
		t.Errorf("a capped answer must say so: %q…", answer[:min(len(answer), 80)])
	}
}

// A server that never answers costs the deadline it was given, and nothing longer.
func TestSlowServerHitsItsOwnDeadline(t *testing.T) {
	fake := fakeMCPServer(t, fakeOptions{Stateless: true, Slow: 3 * time.Second})
	start := time.Now()

	_, err := connect(context.Background(), registered(fake.URL, 1), testID)
	if err == nil {
		t.Fatal("a server that never answered connected")
	}
	if kind := kindOf(t, err); kind != FailureTimeout {
		t.Errorf("kind = %q, want %q (%v)", kind, FailureTimeout, err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("waited %v for a 1s deadline", took)
	}
}

// Something on the address that is not an MCP server must be reported as that, not as unreachable:
// the two have different fixes and a person cannot tell them apart from the outside.
func TestNotAnMCPServer(t *testing.T) {
	pages := map[string]http.Handler{
		"404 page":       http.NotFoundHandler(),
		"html landing":   http.HandlerFunc(pageHandler("<html><body>docs</body></html>")),
		"empty 200 page": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }),
		// A body that is not json-rpc at all fails on the spot, which is why it is here and not with
		// the server that answers correctly to nobody's request.
		"body that is not json-rpc": http.HandlerFunc(jsonHandler(`{"error":"nope"}`)),
	}
	for name, h := range pages {
		t.Run(name, func(t *testing.T) {
			ts := httptest.NewServer(h)
			t.Cleanup(ts.Close)
			_, err := connect(context.Background(), registered(ts.URL, 3), testID)
			if err == nil {
				t.Fatal("a non-MCP endpoint connected")
			}
			kind := kindOf(t, err)
			if kind != FailureNotMCP && kind != FailureUnreachable {
				t.Errorf("kind = %q, want not_mcp or unreachable (%v)", kind, err)
			}
			if strings.Contains(err.Error(), "<html>") {
				t.Errorf("the upstream body leaked into the message: %q", err)
			}
		})
	}
}

// A server answering with some other request's id leaves the client waiting for a response that will never
// arrive. Only the deadline ends it: an http.Client timeout stops a request, not a wait.
func TestServerThatAnswersTheWrongIdIsWaitedOut(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`)
	}))
	t.Cleanup(ts.Close)

	start := time.Now()
	_, err := connect(context.Background(), registered(ts.URL, 1), testID)
	took := time.Since(start)
	if err == nil {
		t.Fatal("a server that answers nothing we asked connected")
	}
	if kind := kindOf(t, err); kind != FailureTimeout {
		t.Errorf("kind = %q, want %q (%v)", kind, FailureTimeout, err)
	}
	if took > 4*time.Second {
		t.Errorf("waited %v past a 1s deadline — the deadline is the only bound on this wait", took)
	}
}

func pageHandler(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, body)
	}
}

func jsonHandler(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
}

// A server that answers 401 means the token is wrong or absent, which is the one refusal a person can
// fix in Settings.
func TestUnauthorizedIsRefused(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(ts.Close)

	_, err := connect(context.Background(), registered(ts.URL, 3), testID)
	if err == nil {
		t.Fatal("a 401 endpoint connected")
	}
	if kind := kindOf(t, err); kind != FailureRefused {
		t.Errorf("kind = %q, want %q (%v)", kind, FailureRefused, err)
	}
}

// Nothing listening on the port is the unreachable case, and it must be reached before the generic
// one: "check the address" and "this is not an MCP endpoint" are different sentences.
func TestNothingListeningIsUnreachable(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()

	_, err := connect(context.Background(), registered(url, 2), testID)
	if err == nil {
		t.Fatal("an address with nothing on it connected")
	}
	if kind := kindOf(t, err); kind != FailureUnreachable {
		t.Errorf("kind = %q, want %q (%v)", kind, FailureUnreachable, err)
	}
}

// The URL a person typed is what MIKE dials; anything else the server points at is refused, so a
// registered server cannot be used as a proxy into the local network.
func TestRedirectFromTheRegisteredURLIsNotFollowed(t *testing.T) {
	private := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(private.Close)

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.URL, http.StatusFound)
	}))
	t.Cleanup(proxy.Close)

	_, err := connect(context.Background(), registered(proxy.URL, 3), testID)
	if err == nil {
		t.Fatal("a redirect was followed, so one registered URL can reach any address")
	}
}

// A registered server whose address is not a URL at all is a configuration fault, reported without a
// dial.
func TestUnparseableAddressNeverDials(t *testing.T) {
	for _, url := range []string{"mcp.example.com", "not a url", "https://"} {
		_, err := connect(context.Background(), registered(url, 1), testID)
		if err == nil {
			t.Errorf("%q connected", url)
			continue
		}
		if kind := kindOf(t, err); kind != FailureNotMCP {
			t.Errorf("%q: kind = %q, want %q", url, kind, FailureNotMCP)
		}
	}
}

// The tool list is bounded, because every offered definition rides on every provider request.
func TestToolListIsBounded(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "many", Version: "1"}, nil)
	for i := range types.MaxToolsPerServer + 5 {
		name := fmt.Sprintf("tool_%03d", i)
		mcp.AddTool(srv, &mcp.Tool{Name: name},
			func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
				return nil, nil, nil
			})
	}
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(ts.Close)

	sess := dial(t, registered(ts.URL, 5))
	tools, dropped, err := sess.listTools(context.Background())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != types.MaxToolsPerServer {
		t.Errorf("offered %d tools, want the bound of %d", len(tools), types.MaxToolsPerServer)
	}
	if dropped != 5 {
		t.Errorf("dropped %d, want the 5 over the bound reported", dropped)
	}
}
