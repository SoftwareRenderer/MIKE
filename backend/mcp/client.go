// Package mcp is MIKE's client side of the Model Context Protocol: it dials a registered server, reads
// the tools it lists, and runs one of them on a task's behalf.
//
// It speaks one protocol era (Era) over Streamable HTTP, holds nothing between calls, and declares no
// capability beyond tools. docs/mcp.md.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mike/backend/httputil"
	"mike/backend/toolresult"
	"mike/backend/types"
	"mike/pkg/logging"
)

// Era is the only protocol version this package speaks; a server answering another is refused by name,
// never negotiated down to. The handshake-era shape assumes a session that outlives a request.
const Era = "2026-07-28"

// maxEventBytes bounds one server-sent event, set beside httputil.MaxUpstreamBodyBytes so the two
// bounds on somebody else's bytes agree. Past it an answer is refused, not buffered.
const maxEventBytes = 8 << 20

// Identity is what MIKE reports about itself to a server. It is passed in rather than read here, so
// the version a server sees is the one this binary was built with.
type Identity struct {
	Name    string
	Version string
}

// Failure is the kind of a failed MCP interaction; each kind has a different fix in a different
// program. The sentence a person reads comes from the kind and the cause stays in the log.
type Failure string

const (
	// FailureUnreachable: nothing answered at the address a person typed.
	FailureUnreachable Failure = "unreachable"
	// FailureNotMCP: something answered, and it is not an MCP server.
	FailureNotMCP Failure = "not_mcp"
	// FailureWrongEra: an MCP server that speaks a protocol MIKE does not.
	FailureWrongEra Failure = "wrong_era"
	// FailureRefused: the server reached us and said no — an unset or wrong token lives here.
	FailureRefused Failure = "refused"
	// FailureTimeout: the server did not answer inside its own deadline.
	FailureTimeout Failure = "timeout"
	// FailureTooBig: the server answered, with more bytes than this process will hold.
	FailureTooBig Failure = "too_big"
	// FailureToolError: the tool ran and reported its own failure.
	FailureToolError Failure = "tool_error"
)

// Error is one failed interaction. Error() is what a person or the model reads and holds MIKE's own
// sentence; a transport fault's text stays in Cause.
type Error struct {
	Server string
	Kind   Failure
	Cause  error
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Server, e.detail()) }

// Unwrap keeps the cause reachable for errors.Is and for the log, never for a message.
func (e *Error) Unwrap() error { return e.Cause }

func (e *Error) detail() string {
	switch e.Kind {
	case FailureUnreachable:
		return "is not answering at its URL — check the address and that the server is running"
	case FailureNotMCP:
		return "did not answer as a Streamable-HTTP MCP server — the URL must be the MCP endpoint, not the site it serves"
	case FailureWrongEra:
		return fmt.Sprintf("does not speak protocol %s, which is the only version supported; it is not negotiated down", Era)
	case FailureRefused:
		return "refused the request — its headers or token are set under Settings → MCP"
	case FailureTimeout:
		return "did not answer in time; its timeout is set under Settings → MCP"
	case FailureTooBig:
		return fmt.Sprintf("sent more than %d bytes in one answer", maxEventBytes)
	case FailureToolError:
		// The tool's own sentence is the answer the model can act on.
		if e.Cause != nil && e.Cause.Error() != "" {
			return "the tool reported a failure: " + e.Cause.Error()
		}
		return "the tool reported a failure"
	default:
		return "failed"
	}
}

// Tool is one tool as its server listed it, before any name MIKE gives it. The offer carries which
// server listed it; the row is what a call dials.
type Tool struct {
	// Name is the server's own tool name, which is what a call must carry.
	Name        string
	DisplayName string
	Description string
	// Parameters is the server's own input schema, handed to the provider verbatim.
	Parameters map[string]any
}

// session is one connection to one server, alive for one listing or call. The server's own deadline is
// on it when it set one; with no deadline the caller's context is the only bound. The SDK's wait ends
// when that context ends; an http.Client timeout does not bound it.
type session struct {
	server types.MCPServer
	sess   *mcp.ClientSession
	ctx    context.Context
	cancel context.CancelFunc
}

// connect reaches a server and confirms it speaks Era. The caller closes what it returns, and every
// failure path here releases the deadline it opened.
func connect(ctx context.Context, server types.MCPServer, id Identity) (*session, error) {
	u, err := url.Parse(server.URL)
	if err != nil || u.Host == "" {
		return nil, &Error{Server: server.Name, Kind: FailureNotMCP, Cause: err}
	}
	timeout := server.Timeout()
	// The deadline covers the whole session, which is the wait the person agreed to. No deadline is a
	// choice rather than an oversight: the caller's context — a stopped task, a shutting-down server —
	// is then the only thing that ends the wait.
	callCtx, cancel := context.WithCancel(ctx)
	if timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, timeout)
	}

	// The address a person typed is the only host granted at loopback, and the grant is per server.
	// The server's own headers ride the same client because the SDK's transport has no per-request hook.
	egress := httputil.WithHeaders(
		httputil.NewExtensionEgressClientGranting(timeout, []string{u.Hostname()}),
		requestHeaders(server))

	client := mcp.NewClient(&mcp.Implementation{Name: id.Name, Version: id.Version}, &mcp.ClientOptions{
		// Declaring nothing is the decline: with no capability on the wire a server cannot sample our
		// model, elicit from a human, or ask for roots it was never offered.
		Capabilities: &mcp.ClientCapabilities{},
	})

	sess, err := client.Connect(callCtx, &mcp.StreamableClientTransport{
		Endpoint:   server.URL,
		HTTPClient: egress,
		// One attempt: a retry would run the tool twice, and a tool is not idempotent.
		MaxRetries: -1,
		// The long-lived GET stream is how the older era pushed at a client, and a session that lives
		// for one call cannot answer a push.
		DisableStandaloneSSE: true,
		MaxEventSize:         maxEventBytes,
	}, &mcp.ClientSessionOptions{ProtocolVersion: Era})
	if err != nil {
		cancel()
		return nil, classify(server.Name, err)
	}

	// The SDK falls back to the initialize handshake whenever server/discover fails, so pinning the
	// version does not enforce the era. This check does.
	got := ""
	if res := sess.InitializeResult(); res != nil {
		got = res.ProtocolVersion
	}
	if got != Era {
		_ = sess.Close()
		cancel()
		return nil, &Error{Server: server.Name, Kind: FailureWrongEra, Cause: fmt.Errorf("negotiated %q", got)}
	}
	return &session{server: server, sess: sess, ctx: callCtx, cancel: cancel}, nil
}

// close ends the session and its deadline. Safe on a session whose connection already went away.
func (s *session) close() error {
	if s == nil {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.sess == nil {
		return nil
	}
	return s.sess.Close()
}

// listTools reads the server's own tool list, keeping at most types.MaxToolsPerServer and returning the
// rest as a dropped count for the caller to report. The context is the caller's cancellation; the
// server's own deadline is already on the session.
func (s *session) listTools(ctx context.Context) (tools []Tool, dropped int, err error) {
	if ctx.Err() != nil {
		return nil, 0, ctx.Err()
	}
	for tool, lerr := range s.sess.Tools(s.ctx, nil) {
		if lerr != nil {
			return nil, 0, classify(s.server.Name, lerr)
		}
		if len(tools) >= types.MaxToolsPerServer {
			dropped++
			continue
		}
		tools = append(tools, Tool{
			Name:        tool.Name,
			DisplayName: toolDisplayName(tool),
			Description: tool.Description,
			Parameters:  schemaMap(tool.InputSchema),
		})
	}
	return tools, dropped, nil
}

// requestHeaders are one server's stored headers, in the shape the egress client takes. A header with
// no value behind it sends nothing.
func requestHeaders(server types.MCPServer) []httputil.Header {
	var out []httputil.Header
	for _, h := range server.Headers {
		if h.Value == nil || strings.TrimSpace(h.Name) == "" {
			continue
		}
		out = append(out, httputil.Header{Name: h.Name, Value: *h.Value})
	}
	return out
}

// toolDisplayName follows the spec's precedence: the tool's own title, then its annotation's title,
// then its name.
func toolDisplayName(tool *mcp.Tool) string {
	if strings.TrimSpace(tool.Title) != "" {
		return tool.Title
	}
	if tool.Annotations != nil && strings.TrimSpace(tool.Annotations.Title) != "" {
		return tool.Annotations.Title
	}
	return tool.Name
}

// schemaMap is the server's schema as a plain map, the shape the provider is given. A schema that is
// not a JSON object comes back empty, and llama.ContributedToolDefinitions turns an empty map into the
// object schema a model is offered.
func schemaMap(schema any) map[string]any {
	if schema == nil {
		return map[string]any{}
	}
	if m, ok := schema.(map[string]any); ok {
		return m
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// call runs one tool and returns its answer as plain text, capped where it is captured. A tool that
// reports its own failure is a FailureToolError carrying that text: it is the answer, not a fault of
// ours.
func (s *session) call(ctx context.Context, name string, args map[string]any) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	res, err := s.sess.CallTool(s.ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", classify(s.server.Name, err)
	}
	text := renderContent(res)
	if res != nil && res.IsError {
		return "", &Error{Server: s.server.Name, Kind: FailureToolError, Cause: errors.New(firstLine(text))}
	}
	return toolresult.CapText(text), nil
}

// renderContent flattens the answer's content blocks to text. A non-text block is named and skipped
// rather than dropped in silence.
func renderContent(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range res.Content {
		switch block := c.(type) {
		case *mcp.TextContent:
			b.WriteString(block.Text)
			b.WriteString("\n")
		case *mcp.ImageContent:
			fmt.Fprintf(&b, "[image content (%s) omitted]\n", block.MIMEType)
		case *mcp.AudioContent:
			fmt.Fprintf(&b, "[audio content (%s) omitted]\n", block.MIMEType)
		default:
			fmt.Fprintf(&b, "[content of type %T omitted]\n", c)
		}
	}
	if res.StructuredContent != nil {
		if raw, err := json.Marshal(res.StructuredContent); err == nil {
			b.WriteString("\n" + string(raw))
		} else {
			b.WriteString("\n[structured content could not be read]")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// classify turns a transport or protocol error into the kind a person can act on. The upstream text
// never reaches the message; it is kept as Cause for the log.
func classify(server string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		// A cancelled caller is not a server fault: pass it through to the turn's cancellation path.
		return err
	}
	kind := FailureNotMCP
	switch {
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		kind = FailureTimeout
	case isTooBig(err):
		kind = FailureTooBig
	case isRefusedByServer(err):
		kind = FailureRefused
	case isUnreachable(err):
		kind = FailureUnreachable
	case isNotMCP(err):
		kind = FailureNotMCP
	}
	failure := &Error{Server: server, Kind: kind, Cause: err}
	logging.For(logging.MCP).Warnf("mcp server %s %s: %v", server, kind, err)
	return failure
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// isTooBig: the answer was bigger than this process will hold, which is a fix on the server side. Only
// the wordings that mean the server's own bytes are matched, so a context deadline stays a timeout.
func isTooBig(err error) bool {
	return containsAny(err, "sse event exceeded", "response too large", "body exceeds")
}

// isRefusedByServer recognises an answer that means "identify yourself first". The SDK reports an
// upstream HTTP status in its error text and exports no type carrying it, so this reads the text.
func isRefusedByServer(err error) bool {
	return containsAny(err, "status 401", "status 403", "unauthorized", "forbidden")
}

// isUnreachable is the dial failing: no route, no DNS answer, nothing listening.
func isUnreachable(err error) bool {
	var (
		urlErr *url.Error
		opErr  *net.OpError
	)
	if errors.As(err, &urlErr) || errors.As(err, &opErr) {
		return true
	}
	return containsAny(err, "connection refused", "no such host", "network is unreachable", "connection reset", "eof")
}

// isNotMCP: the other end answered in a shape that is not this protocol — a 404 page, an HTML landing
// page, a JSON-RPC endpoint that has never heard of these methods, or a version nothing shares.
func isNotMCP(err error) bool {
	return containsAny(err,
		"status 404", "status 405", "status 5", "content-type", "content type", "html",
		"method not found", "not found", "unsupported protocol version", "invalid json",
		"invalid character", "unexpected")
}

func containsAny(err error, wants ...string) bool {
	text := strings.ToLower(err.Error())
	for _, want := range wants {
		if strings.Contains(text, want) {
			return true
		}
	}
	return false
}

func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i]
	}
	return text
}
