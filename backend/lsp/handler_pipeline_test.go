package lsp

// What the editor's messages wait behind: a request stranded behind someone else's slow answer,
// or an answer thrown away on the way home, leaves a browser waiting on its own timer.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"mike/backend/types"

	"github.com/coder/websocket"
)

// gateManager stands in for a language server that can be made to take its time with one method.
// A send is recorded when it finishes, so the finished order is the order the server's stdin
// took.
type gateManager struct {
	mu       sync.Mutex
	finished []string
	sent     []json.RawMessage
	arrived  map[string]chan struct{}
	released map[string]chan struct{}
	held     map[string]bool
	replies  chan json.RawMessage
}

func newGateManager() *gateManager {
	return &gateManager{
		arrived:  map[string]chan struct{}{},
		released: map[string]chan struct{}{},
		held:     map[string]bool{},
		replies:  make(chan json.RawMessage, 256),
	}
}

// hold makes every send of one method wait until release is called for it.
func (m *gateManager) hold(method string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.held[method] = true
	m.arrived[method] = make(chan struct{}, 16)
	m.released[method] = make(chan struct{})
}

func (m *gateManager) waitArrived(t *testing.T, method string) {
	t.Helper()
	m.mu.Lock()
	ch := m.arrived[method]
	m.mu.Unlock()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s ever reached the language server", method)
	}
}

func (m *gateManager) release(method string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch, ok := m.released[method]; ok {
		select {
		case <-ch: // already released
		default:
			close(ch)
		}
	}
}

// finishedOrder is the methods in the order their sends completed.
func (m *gateManager) finishedOrder() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.finished...)
}

func (m *gateManager) hasFinished(method string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, got := range m.finished {
		if got == method {
			return true
		}
	}
	return false
}

// countOf is how many times one method reached the language server.
func (m *gateManager) countOf(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, got := range m.finished {
		if got == method {
			n++
		}
	}
	return n
}

// lastIDOf is the backend id on the most recent send of one method.
func (m *gateManager) lastIDOf(t *testing.T, method string) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.sent) - 1; i >= 0; i-- {
		var msg struct {
			ID     *json.Number `json:"id"`
			Method string       `json:"method"`
		}
		if err := json.Unmarshal(m.sent[i], &msg); err != nil {
			continue
		}
		if msg.Method == method && msg.ID != nil {
			return msg.ID.String()
		}
	}
	t.Fatalf("no %s ever reached the language server", method)
	return ""
}

// lastRequestNamedBy is the request id inside the params of the most recent send of one method.
func (m *gateManager) lastRequestNamedBy(t *testing.T, method string) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.sent) - 1; i >= 0; i-- {
		var msg struct {
			Method string `json:"method"`
			Params struct {
				ID *json.Number `json:"id"`
			} `json:"params"`
		}
		if err := json.Unmarshal(m.sent[i], &msg); err != nil || msg.Method != method {
			continue
		}
		if msg.Params.ID == nil {
			t.Fatalf("the %s reaching the language server named no request", method)
		}
		return msg.Params.ID.String()
	}
	t.Fatalf("no %s ever reached the language server", method)
	return ""
}

func (m *gateManager) GetOrCreate(_ context.Context, workspacePath, language string) (*LSPProcess, error) {
	return &LSPProcess{Workspace: workspacePath, Language: language}, nil
}

func (m *gateManager) Release(string, string) {}

func (m *gateManager) SendToServer(ctx context.Context, _ *LSPProcess, msg json.RawMessage) error {
	method := methodOf(msg)
	m.mu.Lock()
	arrival, release, held := m.arrived[method], m.released[method], m.held[method]
	m.sent = append(m.sent, msg)
	m.mu.Unlock()

	if held {
		select {
		case arrival <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	m.mu.Lock()
	m.finished = append(m.finished, method)
	m.mu.Unlock()
	return nil
}

func (m *gateManager) ServerMessageCh() <-chan json.RawMessage { return m.replies }

func (m *gateManager) SandboxMemory(string, string) *types.SandboxMemoryStats {
	return &types.SandboxMemoryStats{}
}

// answer pushes one server response onto the stream the handler routes from.
func (m *gateManager) answer(serverID any, result string) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      serverID,
		"result":  map[string]any{"contents": result},
	})
	m.replies <- body
}

func methodOf(msg json.RawMessage) string {
	var envelope struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(msg, &envelope); err != nil {
		return ""
	}
	return envelope.Method
}

func sendHoverWithID(t *testing.T, c *testClient, id int) {
	t.Helper()
	c.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "textDocument/hover",
		"params":  map[string]any{"textDocument": map[string]any{"uri": "file:///main.go"}},
	})
}

func sendDidOpen(t *testing.T, c *testClient) {
	t.Helper()
	c.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": "file:///main.go", "languageId": "go", "version": 1, "text": "package main\n"},
		},
	})
}

// quiet reports whether anything arrives within 400ms.
func quiet(c *testClient) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	_, _, err := c.conn.Read(ctx)
	return err != nil
}

// TestASlowRequestDoesNotHoldTheSocketBehindIt: every editor tab shares one /api/lsp socket, so a
// slow request must not stop what is sent after it.
func TestASlowRequestDoesNotHoldTheSocketBehindIt(t *testing.T) {
	mgr := newGateManager()
	mgr.hold("textDocument/hover")
	handler := NewLSPHandler(newTestRegistry(), mgr)
	defer handler.Close()

	url := startTestServer(t, handler)
	client := subscribeClientDefault(t, url, "/tmp/slow-hover-project", "main.go")

	sendHoverWithID(t, client, 1)
	mgr.waitArrived(t, "textDocument/hover") // in flight, and staying there

	sendDidOpen(t, client)
	mgr.waitUntilFinished(t, "textDocument/didOpen", "the document open reached the server behind the slow hover")

	client.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "textDocument/completion",
		"params":  map[string]any{"textDocument": map[string]any{"uri": "file:///main.go"}},
	})
	mgr.waitUntilFinished(t, "textDocument/completion", "the completion request reached the server behind the slow hover")

	// The slow one is still owed its answer, under the id the browser asked with.
	backendID := mgr.lastIDOf(t, "textDocument/hover")
	mgr.answer(backendID, "answered at last")
	if got := client.receiveJSON(t); got.ID == nil || got.ID.String() != "1" || got.Error != nil {
		t.Fatalf("the slow hover was answered as %+v, want id=1 with a result", got)
	}
	mgr.release("textDocument/hover")
}

// TestADocumentChangeLandsBeforeTheRequestBehindIt: the writer's asymmetry — a notification
// reaches the server before the request queued behind it.
func TestADocumentChangeLandsBeforeTheRequestBehindIt(t *testing.T) {
	mgr := newGateManager()
	mgr.hold("textDocument/didChange")
	handler := NewLSPHandler(newTestRegistry(), mgr)
	defer handler.Close()

	url := startTestServer(t, handler)
	client := subscribeClientDefault(t, url, "/tmp/ordered-project", "main.go")

	client.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didChange",
		"params": map[string]any{
			"textDocument":   map[string]any{"uri": "file:///main.go", "version": 2},
			"contentChanges": []any{map[string]any{"text": "package main // edited\n"}},
		},
	})
	mgr.waitArrived(t, "textDocument/didChange")

	sendHoverWithID(t, client, 7)
	// Sending now would let the server answer about text it has not been told about yet.
	time.Sleep(200 * time.Millisecond)
	if mgr.hasFinished("textDocument/didChange") {
		t.Fatal("the request behind a notification was dispatched before that notification was written")
	}

	mgr.release("textDocument/didChange")
	mgr.waitUntilFinished(t, "textDocument/hover", "the request queued behind the change was written after it")
	order := mgr.finishedOrder()
	if order[0] != "textDocument/didChange" || order[1] != "textDocument/hover" {
		t.Fatalf("the server read %v, want the change then the request computed from it", order)
	}
}

// TestRequestsStillQueuedAreAnsweredWhenTheWorkspaceIsDropped: dropping an entry ends its writer,
// and what never got its turn is still answered.
func TestRequestsStillQueuedAreAnsweredWhenTheWorkspaceIsDropped(t *testing.T) {
	mgr := newGateManager()
	mgr.hold("textDocument/didOpen")
	handler := NewLSPHandler(newTestRegistry(), mgr)
	defer handler.Close()

	url := startTestServer(t, handler)
	const workspace = "/tmp/dropped-project"
	client := subscribeClientDefault(t, url, workspace, "main.go")

	sendDidOpen(t, client)
	mgr.waitArrived(t, "textDocument/didOpen")

	for _, id := range []int{1, 2, 3} {
		sendHoverWithID(t, client, id)
	}
	// Give the read loop a moment to hand all three to the queue.
	time.Sleep(200 * time.Millisecond)

	handler.dropWorkspace(processKey(workspace, "go"))
	mgr.release("textDocument/didOpen")

	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		msg := client.receiveJSON(t)
		if msg.Error == nil || msg.ID == nil {
			t.Fatalf("a request left in a dropped workspace was answered as %+v", msg)
		}
		seen[msg.ID.String()] = true
	}
	for _, id := range []string{"1", "2", "3"} {
		if !seen[id] {
			t.Fatalf("request id=%s is waiting on a workspace that no longer exists: answered %v", id, seen)
		}
	}
	// Exactly once. Last assertion because a read that runs out of its own deadline hangs up the
	// socket.
	if !quiet(client) {
		t.Fatal("a request in a dropped workspace was answered twice")
	}
}

// TestAFullOutboundQueueEndsItsSessionAlone: a browser that stopped reading has its socket ended,
// and delivery never waits for it.
func TestAFullOutboundQueueEndsItsSessionAlone(t *testing.T) {
	mgr := newGateManager()
	handler := NewLSPHandler(newTestRegistry(), mgr)
	defer handler.Close()

	url := startTestServer(t, handler)
	eager := subscribeClientDefault(t, url, "/tmp/eager-reader-project", "main.go")

	// A session whose browser never reads, with no writer draining its queue, so the question is
	// what delivery does when nobody takes frames.
	serverConn, peerConn := quietPair(t)
	defer peerConn.CloseNow()
	session := newClientSession(serverConn, 99, 2)

	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "window/logMessage", "params": map[string]any{"message": "x"}})
	started := time.Now()
	refused := -1
	for i := 0; i < 20; i++ {
		if !session.deliver(append(payload, byte('0'+i))) {
			refused = i
			break
		}
	}
	if refused != 2 {
		t.Fatalf("frame %d was refused, want the queue's own bound of 2 to be the bound (refused=%d)", refused, refused)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("delivery waited %v on a browser that is not reading", elapsed)
	}

	// The socket really ended, so the client resyncs instead of believing it is current.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := peerConn.Read(ctx); err == nil {
		t.Fatal("the session that stopped reading left its socket open")
	}

	// Nothing about it held the router: another session is still served.
	notification, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "window/logMessage",
		"params":  map[string]any{"message": "still here"},
	})
	handler.routeNotification(notification)
	if got := eager.receive(t); !strings.Contains(string(got), "still here") {
		t.Fatalf("a slow reader starved the others: %s", got)
	}
}

// quietPair upgrades a WebSocket and ignores the server side of it, standing in for a browser
// that stopped reading.
func quietPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/quiet", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	peer, _, err := websocket.Dial(ctx, "ws://"+strings.TrimPrefix(srv.URL, "http://")+"/quiet", nil)
	if err != nil {
		t.Fatalf("dial the quiet peer: %v", err)
	}
	select {
	case server := <-accepted:
		return server, peer
	case <-time.After(2 * time.Second):
		t.Fatal("the quiet peer never accepted")
		return nil, nil
	}
}

func (m *gateManager) waitUntilFinished(t *testing.T, method, what string) {
	t.Helper()
	waitUntil(t, func() bool { return m.hasFinished(method) }, what)
}

// TestACancelNamesTheRequestTheServerIsWorkingOn: the two id spaces are independent, so a
// forwarded cancel has to carry the id the server holds, not the browser's.
func TestACancelNamesTheRequestTheServerIsWorkingOn(t *testing.T) {
	mgr := newGateManager()
	mgr.hold("textDocument/hover")
	handler := NewLSPHandler(newTestRegistry(), mgr)
	defer handler.Close()

	url := startTestServer(t, handler)
	client := subscribeClientDefault(t, url, "/tmp/cancel-project", "main.go")

	sendHoverWithID(t, client, 999)
	mgr.waitArrived(t, "textDocument/hover")
	backendID := mgr.lastIDOf(t, "textDocument/hover")
	if backendID == "999" {
		t.Fatalf("the fixture cannot tell the two id spaces apart: the server was given %s", backendID)
	}

	client.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  "$/cancelRequest",
		"params":  map[string]any{"id": 999},
	})
	mgr.waitUntilFinished(t, "$/cancelRequest", "the cancel reached the language server")
	if got := mgr.lastRequestNamedBy(t, "$/cancelRequest"); got != backendID {
		t.Fatalf("the cancel named request %s, want the id the server holds for that hover (%s)", got, backendID)
	}

	// A cancel naming nothing outstanding is not forwarded. The document open is the barrier:
	// notifications are written in order, so once it lands the cancel ahead of it was judged.
	client.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  "$/cancelRequest",
		"params":  map[string]any{"id": 4242},
	})
	sendDidOpen(t, client)
	mgr.waitUntilFinished(t, "textDocument/didOpen", "the document open behind the second cancel reached the server")
	if got := mgr.countOf("$/cancelRequest"); got != 1 {
		t.Fatalf("%d cancels reached the language server, want only the one naming a live request", got)
	}

	// The hover is owed its answer whatever was cancelled on the way.
	mgr.release("textDocument/hover")
	mgr.answer(backendID, "answered after the cancel")
	if got := client.receiveJSON(t); got.ID == nil || got.ID.String() != "999" || got.Error != nil {
		t.Fatalf("the hover was answered as %+v, want id=999 with a result", got)
	}
}
