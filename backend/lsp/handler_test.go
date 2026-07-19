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

// subscribeClient connects and subscribes to a workspace.
func subscribeClient(t *testing.T, url string, workspace, file string) *testClient {
	t.Helper()
	client := connectClient(t, url)
	client.send(t, map[string]any{
		"method": "workspace/subscribe",
		"params": map[string]any{
			"workspace": workspace,
			"file":      file,
		},
	})
	time.Sleep(50 * time.Millisecond)
	return client
}

func TestIDRemapping(t *testing.T) {
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	// Connect client and subscribe
	client := subscribeClient(t, url, "/tmp/test-project", "main.go")

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

	// Connect 3 clients to the same workspace
	clients := make([]*testClient, 3)
	for i := range clients {
		clients[i] = subscribeClient(t, url, "/tmp/test-project", "main.go")
	}

	// Simulate a server notification (no id, has method)
	notification := map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/publishDiagnostics",
		"params": map[string]any{
			"uri":         "file:///main.go",
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
	client1 := subscribeClient(t, url, "/tmp/test-project", "main.go")

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
	client2 := subscribeClient(t, url, "/tmp/test-project", "main.go")

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
	client := subscribeClient(t, url, "/tmp/test-project", "main.go")

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

	client := subscribeClient(t, url, "/tmp/test-project", "main.go")

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

func TestNotificationDoesNotRemapIDs(t *testing.T) {
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	client := subscribeClient(t, url, "/tmp/test-project", "main.go")

	// Server sends a notification — it should arrive unchanged
	notification := map[string]any{
		"jsonrpc": "2.0",
		"method":  "$/progress",
		"params": map[string]any{
			"token": "progressToken1",
			"value": map[string]any{
				"kind":    "report",
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

	client := subscribeClient(t, url, "/tmp/test-project", "main.go")
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
	client1 := subscribeClient(t, url, "/tmp/project-a", "main.go")

	// Client 2 subscribes to workspace B
	client2 := subscribeClient(t, url, "/tmp/project-b", "main.go")

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

func TestTwoClientsSameWorkspace(t *testing.T) {
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	// Both clients subscribe to the same workspace
	client1 := subscribeClient(t, url, "/tmp/test-project", "main.go")
	client2 := subscribeClient(t, url, "/tmp/test-project", "main.go")

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

	// Client 2 should NOT receive the response (it didn't make the request)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, err := client2.conn.Read(ctx)
	if err == nil {
		t.Error("client2 should not have received the response")
	}
}

func TestNotificationBroadcastsToAllSameWorkspace(t *testing.T) {
	mgr := newMockProcessManager()
	reg := NewRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	// Both clients subscribe to the same workspace
	client1 := subscribeClient(t, url, "/tmp/test-project", "main.go")
	client2 := subscribeClient(t, url, "/tmp/test-project", "main.go")

	// Server sends a notification
	notification := map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/publishDiagnostics",
		"params":  map[string]any{"uri": "file:///main.go"},
	}
	notifData, _ := json.Marshal(notification)
	mgr.Deliver(notifData)

	// Both clients should receive it
	for i, c := range []*testClient{client1, client2} {
		resp := c.receiveJSON(t)
		if resp.Method != "textDocument/publishDiagnostics" {
			t.Errorf("client %d: expected publishDiagnostics, got %s", i+1, resp.Method)
		}
	}
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
