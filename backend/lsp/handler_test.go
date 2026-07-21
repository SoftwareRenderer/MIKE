package lsp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// mockProcessManager is a test double for ProcessManager that captures sent messages
// and can inject server responses.
type mockProcessManager struct {
	mu           sync.Mutex
	processes    map[string]*LSPProcess // key: "workspace\tlanguage"
	nextID       atomic.Int64
	// sentMessages captures messages sent to the server (in order)
	sentMessages []json.RawMessage
	// messageCh for delivering server responses
	messageCh chan json.RawMessage
}

func newMockProcessManager() *mockProcessManager {
	return &mockProcessManager{
		processes: make(map[string]*LSPProcess),
		messageCh: make(chan json.RawMessage, 1024),
	}
}

func (m *mockProcessManager) GetOrCreate(workspacePath, language string, cfg *ServerConfig) (*LSPProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := processKey(workspacePath, language)
	if p, ok := m.processes[key]; ok {
		atomic.AddInt32(&p.RefCount, 1)
		return p, nil
	}
	m.nextID.Add(1)
	p := &LSPProcess{
		Workspace: workspacePath,
		Language:  language,
		RefCount:  1,
	}
	m.processes[key] = p
	return p, nil
}

func (m *mockProcessManager) Release(workspacePath, language string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := processKey(workspacePath, language)
	if p, ok := m.processes[key]; ok {
		n := atomic.AddInt32(&p.RefCount, -1)
		if n <= 0 {
			delete(m.processes, key)
		}
	}
}

func (m *mockProcessManager) SendToServer(process *LSPProcess, msg json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentMessages = append(m.sentMessages, msg)
	return nil
}

func (m *mockProcessManager) ServerMessageCh() <-chan json.RawMessage {
	return m.messageCh
}

func (m *mockProcessManager) Subscribe(process *LSPProcess, clientID uint64) chan []byte {
	return make(chan []byte, 64)
}

func (m *mockProcessManager) Unsubscribe(process *LSPProcess, clientID uint64) {}

func (m *mockProcessManager) Deliver(msg json.RawMessage) {
	select {
	case m.messageCh <- msg:
	default:
	}
}

func (m *mockProcessManager) SentMessages() []json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]json.RawMessage, len(m.sentMessages))
	copy(copied, m.sentMessages)
	return copied
}

func (m *mockProcessManager) ResetSent() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentMessages = nil
}

func (m *mockProcessManager) ProcessCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.processes)
}

// testClient wraps a WebSocket connection for test convenience.
type testClient struct {
	conn      *websocket.Conn
	channelID int64
	workspace string
	language  string
}

// send sends a JSON message to the server (raw, no hints added).
func (c *testClient) send(t *testing.T, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write error: %v", err)
	}
}

// sendLSP sends a JSON-RPC message with _ch, _workspace, _lang, _file hints.
func (c *testClient) sendLSP(t *testing.T, v any) {
	t.Helper()
	msgMap, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("sendLSP expects map[string]any, got %T", v)
	}
	msgMap["_ch"] = c.channelID
	msgMap["_workspace"] = c.workspace
	msgMap["_lang"] = c.language
	c.send(t, msgMap)
}

// receive reads a JSON message from the server.
func (c *testClient) receive(t *testing.T) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, data, err := c.conn.Read(ctx)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	return data
}

// receiveJSON reads and unmarshals a JSON-RPC message.
func (c *testClient) receiveJSON(t *testing.T) JSONRPCMessage {
	t.Helper()
	data := c.receive(t)
	var msg JSONRPCMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal error: %v, data: %s", err, data)
	}
	return msg
}

// startTestServer creates a test HTTP server and returns the base URL.
func startTestServer(t *testing.T, handler *LSPHandler) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/lsp", handler.HandleLSP)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// connectClient connects a WebSocket client to the test server.
func connectClient(t *testing.T, url string) *testClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, url+"/api/lsp", nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	return &testClient{conn: conn}
}

// subscribeClient connects and subscribes to a workspace with a channel ID.
// Drains the $server/lspReady notification sent by the handler.
func subscribeClient(t *testing.T, url string, channelID int64, workspace, file, language string) *testClient {
	t.Helper()
	client := &testClient{
		conn:      nil,
		channelID: channelID,
		workspace: workspace,
		language:  language,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, url+"/api/lsp", nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	client.conn = conn

	// Send subscribe message with _ch hint
	client.send(t, map[string]any{
		"_ch": channelID,
		"method": "workspace/subscribe",
		"params": map[string]any{
			"workspace": workspace,
			"file":      file,
		},
	})
	time.Sleep(50 * time.Millisecond)

	// Drain the $server/lspReady notification sent by handleSubscription
	client.receiveJSON(t)

	return client
}

// subscribeClientDefault is a convenience wrapper with channelID=1 and language="go".
func subscribeClientDefault(t *testing.T, url, workspace, file string) *testClient {
	return subscribeClient(t, url, 1, workspace, file, "go")
}

func TestIDRemapping(t *testing.T) {
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	// Connect client and subscribe
	client := subscribeClientDefault(t, url, "/tmp/test-project", "main.go")

	// Send a request with id=5 (includes _ch, _workspace, _lang hints)
	client.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "textDocument/completion",
		"params":  map[string]any{"textDocument": map[string]any{"uri": "file:///main.go"}},
	})

	// Give the handler time to process
	time.Sleep(50 * time.Millisecond)

	// Check that the server received the remapped request
	sent := mgr.SentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sent))
	}

	var serverMsg map[string]any
	if err := json.Unmarshal(sent[0], &serverMsg); err != nil {
		t.Fatalf("unmarshal sent message: %v", err)
	}

	// The id should have been remapped to a backend ID (not 5)
	serverID := serverMsg["id"]
	if serverID == 5 || serverID == "5" {
		t.Errorf("expected remapped backend ID, got original id %v", serverID)
	}

	// Simulate server response with the backend ID
	serverResponse := map[string]any{
		"jsonrpc": "2.0",
		"id":      serverID,
		"result":  map[string]any{"isIncomplete": false, "items": []any{}},
	}
	respData, _ := json.Marshal(serverResponse)
	mgr.Deliver(respData)

	// Client should receive the response with the original id=5
	resp := client.receiveJSON(t)
	if resp.ID == nil {
		t.Fatal("expected response with id")
	}
	if resp.ID.String() != "5" {
		t.Errorf("expected client id 5, got %s", resp.ID.String())
	}
	if resp.Result == nil {
		t.Error("expected result in response")
	}
}

func TestInitializeCaching(t *testing.T) {
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	// Connect first client (channel 1)
	client1 := subscribeClientDefault(t, url, "/tmp/test-project", "main.go")

	// First client sends initialize with id=1
	client1.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{"processId": 12345, "rootUri": "file:///tmp/test-project"},
	})

	time.Sleep(50 * time.Millisecond)

	// Check that initialize was forwarded to server
	sent := mgr.SentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message (initialize), got %d", len(sent))
	}

	var initReq map[string]any
	if err := json.Unmarshal(sent[0], &initReq); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if initReq["method"] != "initialize" {
		t.Errorf("expected initialize method, got %v", initReq["method"])
	}

	backendID := initReq["id"]

	// Simulate server initialize response
	initResponse := map[string]any{
		"jsonrpc": "2.0",
		"id":      backendID,
		"result": map[string]any{
			"capabilities": map[string]any{
				"textDocumentSync": 1,
				"completionProvider": map[string]any{
					"resolveProvider": true,
				},
			},
			"serverInfo": map[string]any{
				"name":    "gopls",
				"version": "v1.0",
			},
		},
	}
	respData, _ := json.Marshal(initResponse)
	mgr.Deliver(respData)

	// Client 1 should receive the initialize response with id=1
	resp1 := client1.receiveJSON(t)
	if resp1.ID == nil || resp1.ID.String() != "1" {
		t.Errorf("client1: expected id 1, got %v", resp1.ID)
	}
	if resp1.Result == nil {
		t.Error("client1: expected result in initialize response")
	}

	// Connect second client to the same workspace (channel 2)
	client2 := subscribeClient(t, url, 2, "/tmp/test-project", "main.go", "go")

	// Second client sends initialize with id=2
	client2.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "initialize",
		"params":  map[string]any{"processId": 67890, "rootUri": "file:///tmp/test-project"},
	})

	time.Sleep(50 * time.Millisecond)

	// No new messages should have been sent to the server (initialize was cached)
	sentAfter := mgr.SentMessages()
	if len(sentAfter) != 1 {
		t.Errorf("expected 1 sent message total (initialize cached), got %d", len(sentAfter))
	}

	// Client 2 should receive the cached response with id=2
	resp2 := client2.receiveJSON(t)
	if resp2.ID == nil || resp2.ID.String() != "2" {
		t.Errorf("client2: expected id 2, got %v", resp2.ID)
	}
	if resp2.Result == nil {
		t.Error("client2: expected result in cached initialize response")
	}

	// Verify the cached response has the same capabilities
	var result map[string]any
	if err := json.Unmarshal(resp2.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatal("expected capabilities in result")
	}
	if caps["textDocumentSync"].(float64) != 1 {
		t.Errorf("expected textDocumentSync=1, got %v", caps["textDocumentSync"])
	}
}

func TestDisconnectCleanup(t *testing.T) {
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	// Connect client
	client := subscribeClientDefault(t, url, "/tmp/test-project", "main.go")

	// Verify the process exists
	if mgr.ProcessCount() != 1 {
		t.Errorf("expected 1 process, got %d", mgr.ProcessCount())
	}

	// Disconnect client
	client.conn.Close(websocket.StatusNormalClosure, "done")

	// Give time for cleanup
	time.Sleep(100 * time.Millisecond)

	// Verify the process was removed (0 clients)
	if mgr.ProcessCount() != 0 {
		t.Errorf("expected 0 processes after disconnect, got %d", mgr.ProcessCount())
	}
}

func TestMultipleRequestRemapping(t *testing.T) {
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	client := subscribeClientDefault(t, url, "/tmp/test-project", "main.go")

	// Send multiple requests with different IDs
	requests := []int64{10, 20, 30}
	for _, id := range requests {
		client.sendLSP(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  "textDocument/definition",
			"params":  map[string]any{"textDocument": map[string]any{"uri": "file:///main.go"}},
		})
	}

	time.Sleep(50 * time.Millisecond)

	// Check that 3 messages were sent with unique backend IDs
	sent := mgr.SentMessages()
	if len(sent) != 3 {
		t.Fatalf("expected 3 sent messages, got %d", len(sent))
	}

	backendIDs := make(map[int64]bool)
	for _, msg := range sent {
		var m map[string]any
		json.Unmarshal(msg, &m)
		id := int64(m["id"].(float64))
		if backendIDs[id] {
			t.Errorf("duplicate backend ID: %d", id)
		}
		backendIDs[id] = true
	}

	// Simulate responses in different order
	for _, msg := range sent {
		var m map[string]any
		json.Unmarshal(msg, &m)
		backendID := m["id"]

		response := map[string]any{
			"jsonrpc": "2.0",
			"id":      backendID,
			"result":  []any{},
		}
		respData, _ := json.Marshal(response)
		mgr.Deliver(respData)
	}

	// All 3 responses should be received
	for i := 0; i < 3; i++ {
		resp := client.receiveJSON(t)
		if resp.ID == nil {
			t.Errorf("response %d: expected id, got nil", i)
		}
		if resp.Result == nil {
			t.Errorf("response %d: expected result", i)
		}
	}
}

func TestInitializedNotificationForwarded(t *testing.T) {
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	client := subscribeClientDefault(t, url, "/tmp/test-project", "main.go")
	mgr.ResetSent()

	// Send "initialized" notification (no id, method="initialized")
	client.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  "initialized",
		"params":  map[string]any{},
	})

	time.Sleep(50 * time.Millisecond)

	// The initialized notification should be forwarded to the server
	sent := mgr.SentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message (initialized notification), got %d", len(sent))
	}

	var m map[string]any
	json.Unmarshal(sent[0], &m)
	if m["method"] != "initialized" {
		t.Errorf("expected initialized notification, got %v", m["method"])
	}
}

func TestTwoClientsDifferentWorkspaces(t *testing.T) {
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	// Client 1 subscribes to workspace A (channel 1)
	client1 := subscribeClient(t, url, 1, "/tmp/project-a", "main.go", "go")

	// Client 2 subscribes to workspace B (channel 2)
	client2 := subscribeClient(t, url, 2, "/tmp/project-b", "main.go", "go")

	// Client 1 sends a request
	client1.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      100,
		"method":  "textDocument/completion",
		"params":  map[string]any{},
	})

	time.Sleep(50 * time.Millisecond)

	sent := mgr.SentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sent))
	}

	// Simulate response
	var m map[string]any
	json.Unmarshal(sent[0], &m)
	response := map[string]any{
		"jsonrpc": "2.0",
		"id":      m["id"],
		"result":  map[string]any{"items": []any{}},
	}
	respData, _ := json.Marshal(response)
	mgr.Deliver(respData)

	// Client 1 should receive the response
	resp := client1.receiveJSON(t)
	if resp.ID == nil || resp.ID.String() != "100" {
		t.Errorf("client1: expected id 100, got %v", resp.ID)
	}

	// Client 2 should NOT receive anything (different workspace)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, err := client2.conn.Read(ctx)
	if err == nil {
		t.Error("client2 should not have received any message (different workspace)")
	}
}

func TestTwoClientsSameWorkspace(t *testing.T) {
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	// Both clients subscribe to the same workspace (different channels)
	client1 := subscribeClient(t, url, 1, "/tmp/test-project", "main.go", "go")
	client2 := subscribeClient(t, url, 2, "/tmp/test-project", "main.go", "go")

	// Client 1 sends a request
	client1.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      100,
		"method":  "textDocument/completion",
		"params":  map[string]any{},
	})

	time.Sleep(50 * time.Millisecond)

	sent := mgr.SentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sent))
	}

	// Simulate response
	var m map[string]any
	json.Unmarshal(sent[0], &m)
	response := map[string]any{
		"jsonrpc": "2.0",
		"id":      m["id"],
		"result":  map[string]any{"items": []any{}},
	}
	respData, _ := json.Marshal(response)
	mgr.Deliver(respData)

	// Client 1 should receive the response
	resp := client1.receiveJSON(t)
	if resp.ID == nil || resp.ID.String() != "100" {
		t.Errorf("client1: expected id 100, got %v", resp.ID)
	}

	// Client 2 should NOT receive the response (it didn't make the request)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, err := client2.conn.Read(ctx)
	if err == nil {
		t.Error("client2 should not have received the response")
	}
}

func TestMultiWorkspaceSingleConnection(t *testing.T) {
	// Two channels on the same WebSocket connection, each subscribed to a different workspace.
	// Verifies that requests are routed to the correct workspace and responses go to the correct channel.
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	// Single connection, two channels
	client := connectClient(t, url)

	// Subscribe channel 1 to workspace A
	client.channelID = 1
	client.workspace = "/tmp/project-a"
	client.language = "go"
	client.send(t, map[string]any{
		"_ch":    1,
		"method": "workspace/subscribe",
		"params": map[string]any{
			"workspace": "/tmp/project-a",
			"file":      "main.go",
		},
	})

	// Subscribe channel 2 to workspace B
	client.channelID = 2
	client.workspace = "/tmp/project-b"
	client.language = "go"
	client.send(t, map[string]any{
		"_ch":    2,
		"method": "workspace/subscribe",
		"params": map[string]any{
			"workspace": "/tmp/project-b",
			"file":      "main.go",
		},
	})

	time.Sleep(50 * time.Millisecond)

	// Drain $server/lspReady notifications from both subscriptions
	client.receiveJSON(t) // ch=1 ready
	client.receiveJSON(t) // ch=2 ready

	// Two processes should exist (one per workspace)
	if mgr.ProcessCount() != 2 {
		t.Errorf("expected 2 processes, got %d", mgr.ProcessCount())
	}

	// Channel 1 sends a request to workspace A
	client.channelID = 1
	client.workspace = "/tmp/project-a"
	client.language = "go"
	client.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      100,
		"method":  "textDocument/completion",
		"params":  map[string]any{},
	})

	time.Sleep(50 * time.Millisecond)

	sent := mgr.SentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sent))
	}

	// Simulate response
	var m map[string]any
	json.Unmarshal(sent[0], &m)
	response := map[string]any{
		"jsonrpc": "2.0",
		"id":      m["id"],
		"result":  map[string]any{"items": []any{"from-a"}},
	}
	respData, _ := json.Marshal(response)
	mgr.Deliver(respData)

	// Client should receive the response with _ch=1 hint
	resp := client.receiveJSON(t)
	if resp.ID == nil || resp.ID.String() != "100" {
		t.Errorf("expected id 100, got %v", resp.ID)
	}

	// Channel 2 sends a request to workspace B
	client.channelID = 2
	client.workspace = "/tmp/project-b"
	client.language = "go"
	client.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      200,
		"method":  "textDocument/completion",
		"params":  map[string]any{},
	})

	time.Sleep(50 * time.Millisecond)

	sent2 := mgr.SentMessages()
	if len(sent2) != 2 {
		t.Fatalf("expected 2 sent messages, got %d", len(sent2))
	}

	// Simulate response for second request
	var m2 map[string]any
	json.Unmarshal(sent2[1], &m2)
	response2 := map[string]any{
		"jsonrpc": "2.0",
		"id":      m2["id"],
		"result":  map[string]any{"items": []any{"from-b"}},
	}
	respData2, _ := json.Marshal(response2)
	mgr.Deliver(respData2)

	// Client should receive the second response
	resp2 := client.receiveJSON(t)
	if resp2.ID == nil || resp2.ID.String() != "200" {
		t.Errorf("expected id 200, got %v", resp2.ID)
	}
}

func TestRouteServerRequest(t *testing.T) {
	// Server requests (has both id AND method) should not be logged as "unexpected".
	// LSP servers can send requests like window/workDoneProgress that the client
	// must respond to. We currently don't implement server request handling,
	// but the message should be silently dropped rather than logged as an error.
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	// Deliver a server request (has both id and method)
	serverRequest := json.RawMessage(`{"jsonrpc":"2.0","id":99,"method":"window/workDoneProgress","params":{"token":"abc"}}`)
	mgr.Deliver(serverRequest)

	// Give the router time to process
	time.Sleep(50 * time.Millisecond)

	// Should not panic or block — the message is silently dropped
	// (We don't verify the absence of a log line here, just that it doesn't crash)
}

func TestRouteMalformedMessage(t *testing.T) {
	// Messages with neither id nor method can occur during process teardown
	// (partial reads dispatch truncated bodies). Should be silently dropped.
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	// Deliver a malformed message (no id, no method)
	malformed := json.RawMessage(`{"jsonrpc":"2.0","result":{}}`)
	mgr.Deliver(malformed)

	// Give the router time to process
	time.Sleep(50 * time.Millisecond)

	// Should not panic or block — the message is silently dropped
}

func TestRouteEmptyMessage(t *testing.T) {
	// An empty JSON object should be silently dropped.
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	empty := json.RawMessage(`{}`)
	mgr.Deliver(empty)

	time.Sleep(50 * time.Millisecond)
}

func TestUnknownExtensionRejected(t *testing.T) {
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	client := connectClient(t, url)
	client.send(t, map[string]any{
		"method": "workspace/subscribe",
		"params": map[string]any{
			"workspace": "/tmp/test-project",
			"file":      "file.unknown",
		},
	})

	// The connection should be closed with an error
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, err := client.conn.Read(ctx)
	if err == nil {
		t.Error("expected connection to be closed for unknown extension")
	}
}

func TestLSPReadyNotification(t *testing.T) {
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	// Connect client and subscribe (subscribeClient drains $server/lspReady)
	client := subscribeClientDefault(t, url, "/tmp/test-project", "main.go")

	// Send initialize request
	client.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"processId": 12345,
			"rootUri":   "file:///tmp/test-project",
			"capabilities": map[string]any{},
		},
	})

	// Give handler time to process
	time.Sleep(50 * time.Millisecond)

	// Simulate initialize response from server
	sent := mgr.SentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message (initialize), got %d", len(sent))
	}

	var initReq map[string]any
	json.Unmarshal(sent[0], &initReq)
	backendID := initReq["id"]

	initResponse := map[string]any{
		"jsonrpc": "2.0",
		"id":      backendID,
		"result": map[string]any{
			"capabilities": map[string]any{
				"completionProvider": map[string]any{"resolveProvider": true},
			},
			"serverInfo": map[string]any{"name": "gopls"},
		},
	}
	respData, _ := json.Marshal(initResponse)
	mgr.Deliver(respData)

	// Client should receive initialize response
	resp := client.receiveJSON(t)
	if resp.ID == nil || resp.ID.String() != "1" {
		t.Fatalf("expected initialize response with id=1, got id=%v", resp.ID)
	}
	if resp.Result == nil {
		t.Fatal("expected result in initialize response")
	}

	// Send initialized notification
	client.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  "initialized",
		"params":  map[string]any{},
	})

	// Client should receive $server/lspReady notification
	readyMsg := client.receiveJSON(t)
	if readyMsg.Method != "$/lspReady" {
		t.Fatalf("expected $server/lspReady notification, got method=%s", readyMsg.Method)
	}
}

func TestLSPReadyNotificationCachedResponse(t *testing.T) {
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	// First client: complete full initialize handshake (channel 1)
	client1 := subscribeClientDefault(t, url, "/tmp/test-project", "main.go")

	client1.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"processId":    12345,
			"rootUri":      "file:///tmp/test-project",
			"capabilities": map[string]any{},
		},
	})
	time.Sleep(50 * time.Millisecond)

	sent := mgr.SentMessages()
	var initReq map[string]any
	json.Unmarshal(sent[0], &initReq)
	backendID := initReq["id"]

	initResponse := map[string]any{
		"jsonrpc": "2.0",
		"id":      backendID,
		"result": map[string]any{
			"capabilities": map[string]any{},
			"serverInfo":  map[string]any{"name": "gopls"},
		},
	}
	mgr.Deliver(json.RawMessage(mustMarshal(t, initResponse)))

	// Client1 receives initialize response
	resp1 := client1.receiveJSON(t)
	if resp1.ID == nil || resp1.ID.String() != "1" {
		t.Fatalf("client1: expected id=1, got %v", resp1.ID)
	}

	// Client1 sends initialized
	client1.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  "initialized",
		"params":  map[string]any{},
	})
	// Client1 receives $server/lspReady
	ready1 := client1.receiveJSON(t)
	if ready1.Method != "$/lspReady" {
		t.Fatalf("client1: expected $server/lspReady, got %s", ready1.Method)
	}

	// Second client: connects to same workspace (channel 2), gets cached response
	client2 := subscribeClient(t, url, 2, "/tmp/test-project", "main.go", "go")

	client2.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "initialize",
		"params": map[string]any{
			"processId":    99999,
			"rootUri":      "file:///tmp/test-project",
			"capabilities": map[string]any{},
		},
	})

	// Client2 should receive cached initialize response
	resp2 := client2.receiveJSON(t)
	if resp2.ID == nil || resp2.ID.String() != "2" {
		t.Fatalf("client2: expected id=2, got %v", resp2.ID)
	}
	if resp2.Result == nil {
		t.Fatal("client2: expected cached result")
	}

	// Client2 should also receive $server/lspReady
	ready2 := client2.receiveJSON(t)
	if ready2.Method != "$/lspReady" {
		t.Fatalf("client2: expected $server/lspReady, got %s", ready2.Method)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	return data
}
