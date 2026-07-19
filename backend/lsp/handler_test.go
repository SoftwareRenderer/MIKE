package lsp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// mockProcessManager is a test double for ProcessManager that captures sent messages
// and can inject server responses.
type mockProcessManager struct {
	mu        sync.Mutex
	processes map[string]*LSPProcess
	nextID    int
	// sentMessages captures messages sent to the server (in order)
	sentMessages []json.RawMessage
	// messageCh for delivering server responses
	messageCh chan json.RawMessage
}

func newMockProcessManager() *mockProcessManager {
	return &mockProcessManager{
		processes:  make(map[string]*LSPProcess),
		messageCh:  make(chan json.RawMessage, 1024),
	}
}

func (m *mockProcessManager) GetOrCreateProcess(workspace string, cfg *ServerConfig) *LSPProcess {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.processes[workspace]; ok {
		return p
	}
	m.nextID++
	p := &LSPProcess{
		ID:        "test-proc-" + string(rune(m.nextID)),
		Workspace: workspace,
		Config:    cfg,
	}
	m.processes[workspace] = p
	return p
}

func (m *mockProcessManager) RemoveProcess(workspace string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.processes, workspace)
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

// dialClient creates a test HTTP request and upgrades it to a WebSocket.
func dialClient(t *testing.T, handler *LSPHandler) *websocket.Conn {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/lsp", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Version", "13")

	rec := httptest.NewRecorder()
	handler.HandleLSP(rec, req)

	if rec.Code != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101 Switching Protocols, got %d", rec.Code)
	}

	// The coder/websocket test recorder doesn't give us a real conn,
	// so we need to use a different approach with a real listener.
	_ = req
	_ = rec
	return nil
}

// testClient wraps a WebSocket connection for test convenience.
type testClient struct {
	conn *websocket.Conn
}

// send sends a JSON message to the server.
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

func TestIDRemapping(t *testing.T) {
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	// Connect client
	client := connectClient(t, url)

	// Subscribe
	client.send(t, map[string]any{
		"method": "workspace/subscribe",
		"params": map[string]any{
			"workspace": "/tmp/test-project",
			"file":      "main.go",
		},
	})

	// Send a request with id=5
	client.send(t, map[string]any{
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

func TestNotificationBroadcast(t *testing.T) {
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	// Connect 3 clients
	clients := make([]*testClient, 3)
	for i := range clients {
		clients[i] = connectClient(t, url)
		clients[i].send(t, map[string]any{
			"method": "workspace/subscribe",
			"params": map[string]any{
				"workspace": "/tmp/test-project",
				"file":      "main.go",
			},
		})
	}

	// Give time for subscriptions to process
	time.Sleep(50 * time.Millisecond)

	// Simulate a server notification (no id, has method)
	notification := map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/publishDiagnostics",
		"params": map[string]any{
			"uri":      "file:///main.go",
			"diagnostics": []any{},
		},
	}
	notifData, _ := json.Marshal(notification)
	mgr.Deliver(notifData)

	// All 3 clients should receive the notification
	for i, c := range clients {
		resp := c.receiveJSON(t)
		if resp.Method != "textDocument/publishDiagnostics" {
			t.Errorf("client %d: expected publishDiagnostics, got %s", i, resp.Method)
		}
		if resp.ID != nil {
			t.Errorf("client %d: notification should have no id", i)
		}
	}
}

func TestInitializeCaching(t *testing.T) {
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	// Connect first client
	client1 := connectClient(t, url)
	client1.send(t, map[string]any{
		"method": "workspace/subscribe",
		"params": map[string]any{
			"workspace": "/tmp/test-project",
			"file":      "main.go",
		},
	})

	// First client sends initialize with id=1
	client1.send(t, map[string]any{
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

	// Connect second client to the same workspace
	client2 := connectClient(t, url)
	client2.send(t, map[string]any{
		"method": "workspace/subscribe",
		"params": map[string]any{
			"workspace": "/tmp/test-project",
			"file":      "main.go",
		},
	})

	// Second client sends initialize with id=2
	client2.send(t, map[string]any{
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
	client := connectClient(t, url)
	client.send(t, map[string]any{
		"method": "workspace/subscribe",
		"params": map[string]any{
			"workspace": "/tmp/test-project",
			"file":      "main.go",
		},
	})

	time.Sleep(50 * time.Millisecond)

	// Verify the process has 1 client
	mgr.mu.Lock()
	process := mgr.processes["/tmp/test-project"]
	mgr.mu.Unlock()
	if process == nil {
		t.Fatal("expected process to exist after subscription")
	}
	if process.ClientCount() != 1 {
		t.Errorf("expected 1 client, got %d", process.ClientCount())
	}

	// Disconnect client
	client.conn.Close(websocket.StatusNormalClosure, "done")

	// Give time for cleanup
	time.Sleep(100 * time.Millisecond)

	// Verify the process was removed (0 clients)
	mgr.mu.Lock()
	_, exists := mgr.processes["/tmp/test-project"]
	mgr.mu.Unlock()
	if exists {
		t.Error("expected process to be removed after client disconnect")
	}
}

func TestMultipleRequestRemapping(t *testing.T) {
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
			"file":      "main.go",
		},
	})

	// Send multiple requests with different IDs
	requests := []int64{10, 20, 30}
	for _, id := range requests {
		client.send(t, map[string]any{
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

	// All 3 clients should receive responses
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

func TestNotificationDoesNotRemapIDs(t *testing.T) {
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
			"file":      "main.go",
		},
	})

	time.Sleep(50 * time.Millisecond)

	// Server sends a notification — it should arrive unchanged
	notification := map[string]any{
		"jsonrpc": "2.0",
		"method":  "$/progress",
		"params": map[string]any{
			"token": "progressToken1",
			"value": map[string]any{
				"kind": "report",
				"message": "Indexing...",
			},
		},
	}
	notifData, _ := json.Marshal(notification)
	mgr.Deliver(notifData)

	resp := client.receiveJSON(t)
	if resp.Method != "$/progress" {
		t.Errorf("expected $/progress, got %s", resp.Method)
	}
	if resp.ID != nil {
		t.Error("notification should have no id")
	}
}

func TestInitializedNotificationForwarded(t *testing.T) {
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
			"file":      "main.go",
		},
	})

	time.Sleep(50 * time.Millisecond)
	mgr.ResetSent()

	// Send "initialized" notification (no id, method="initialized")
	client.send(t, map[string]any{
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

	// Client 1 subscribes to workspace A
	client1 := connectClient(t, url)
	client1.send(t, map[string]any{
		"method": "workspace/subscribe",
		"params": map[string]any{
			"workspace": "/tmp/project-a",
			"file":      "main.go",
		},
	})

	// Client 2 subscribes to workspace B
	client2 := connectClient(t, url)
	client2.send(t, map[string]any{
		"method": "workspace/subscribe",
		"params": map[string]any{
			"workspace": "/tmp/project-b",
			"file":      "main.go",
		},
	})

	time.Sleep(50 * time.Millisecond)

	// Client 1 sends a request
	client1.send(t, map[string]any{
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
