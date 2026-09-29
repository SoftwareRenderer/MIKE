package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"mike/backend/types"
)

// mockRegistry is a test double for lspRegistry. It resolves .go files to a
// gopls ServerConfig (mirroring the default registry behavior the handler
// tests depend on) and returns nil for everything else.
type mockRegistry struct{}

// newTestRegistry returns an lspRegistry test double.
func newTestRegistry() lspRegistry {
	return mockRegistry{}
}

func (mockRegistry) Lookup(fileOrPath string) *ServerConfig {
	ext := strings.TrimPrefix(filepath.Ext(fileOrPath), ".")
	if ext == "go" {
		return &ServerConfig{
			Command:  "gopls",
			Language: "go",
		}
	}
	return nil
}

// mockProcessManager is a test double for ProcessManager that captures sent messages
// and can inject server responses.
type mockProcessManager struct {
	mu        sync.Mutex
	processes map[string]*mockProcess // key: "workspace\tlanguage"
	nextID    int
	// sentMessages captures messages sent to the server (in order)
	sentMessages []json.RawMessage
	// sendErr, when set, is what every send to the server answers with.
	sendErr   error
	messageCh chan json.RawMessage
}

// mockProcess wraps LSPProcess with a test-only reference count.
type mockProcess struct {
	*LSPProcess
	refCount int
}

func newMockProcessManager() *mockProcessManager {
	return &mockProcessManager{
		processes: make(map[string]*mockProcess),
		messageCh: make(chan json.RawMessage, 1024),
	}
}

func (m *mockProcessManager) GetOrCreate(_ context.Context, workspacePath, language string) (*LSPProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := processKey(workspacePath, language)
	if mp, ok := m.processes[key]; ok {
		mp.refCount++
		return mp.LSPProcess, nil
	}
	m.nextID++
	mp := &mockProcess{
		LSPProcess: &LSPProcess{
			Workspace: workspacePath,
			Language:  language,
		},
		refCount: 1,
	}
	m.processes[key] = mp
	return mp.LSPProcess, nil
}

func (m *mockProcessManager) Release(workspacePath, language string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := processKey(workspacePath, language)
	if mp, ok := m.processes[key]; ok {
		mp.refCount--
		if mp.refCount <= 0 {
			delete(m.processes, key)
		}
	}
}

func (m *mockProcessManager) SendToServer(_ context.Context, process *LSPProcess, msg json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentMessages = append(m.sentMessages, msg)
	return m.sendErr
}

func (m *mockProcessManager) ServerMessageCh() <-chan json.RawMessage {
	return m.messageCh
}

func (m *mockProcessManager) SandboxMemory(workspacePath, filePath string) *types.SandboxMemoryStats {
	return &types.SandboxMemoryStats{Running: true, CurrentMB: 42, LimitMB: 256, PeakMB: 64, OOMKills: 1}
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

	client.send(t, map[string]any{
		"_ch":    channelID,
		"method": "workspace/subscribe",
		"params": map[string]any{
			"workspace": workspace,
			"file":      file,
		},
	})
	// The $server/lspReady notification handleSubscription sends is the wait: the process is
	// registered before it goes out.
	client.receiveJSON(t)

	return client
}

// subscribeClientDefault is a convenience wrapper with channelID=1 and language="go".
func subscribeClientDefault(t *testing.T, url, workspace, file string) *testClient {
	return subscribeClient(t, url, 1, workspace, file, "go")
}

// TestRequestIsAnsweredWhenItsServerIsGone: the browser gets an answer even when its language
// server is gone, instead of waiting on a request nobody will answer. Logging the failure and
// dropping the request also left the id mappings that route answers behind.
func TestRequestIsAnsweredWhenItsServerIsGone(t *testing.T) {
	mgr := newMockProcessManager()
	// The real manager wraps the sentinel; the double has to, or it tests a shape nothing sends.
	mgr.sendErr = fmt.Errorf("sandbox client %w for workspace=/tmp/test-project lang=go", errStaleClient)
	reg := newTestRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)
	client := subscribeClientDefault(t, url, "/tmp/test-project", "main.go")

	client.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "textDocument/completion",
		"params":  map[string]any{"textDocument": map[string]any{"uri": "file:///main.go"}},
	})

	msg := client.receiveJSON(t)
	if msg.ID == nil || msg.ID.String() != "5" {
		t.Fatalf("the answer carried id=%v, want the client's own id 5", msg.ID)
	}
	if msg.Error == nil {
		t.Fatalf("a request whose server was gone was answered without an error: %+v", msg)
	}
	if !strings.Contains(msg.Error.Message, "stopped") {
		t.Errorf("error message = %q, want the sentence that says the server stopped", msg.Error.Message)
	}
}

func TestIDRemapping(t *testing.T) {
	mgr := newMockProcessManager()
	reg := newTestRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	client := subscribeClientDefault(t, url, "/tmp/test-project", "main.go")

	client.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "textDocument/completion",
		"params":  map[string]any{"textDocument": map[string]any{"uri": "file:///main.go"}},
	})

	sent := waitForSentMessages(t, mgr, 1)
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sent))
	}

	var serverMsg map[string]any
	if err := json.Unmarshal(sent[0], &serverMsg); err != nil {
		t.Fatalf("unmarshal sent message: %v", err)
	}

	serverID := serverMsg["id"]
	if serverID == 5 || serverID == "5" {
		t.Errorf("expected remapped backend ID, got original id %v", serverID)
	}

	serverResponse := map[string]any{
		"jsonrpc": "2.0",
		"id":      serverID,
		"result":  map[string]any{"isIncomplete": false, "items": []any{}},
	}
	respData, _ := json.Marshal(serverResponse)
	mgr.Deliver(respData)

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
	reg := newTestRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	client1 := subscribeClientDefault(t, url, "/tmp/test-project", "main.go")

	client1.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
	})

	sent := waitForSentMessages(t, mgr, 1)
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

	resp1 := client1.receiveJSON(t)
	if resp1.ID == nil || resp1.ID.String() != "1" {
		t.Errorf("client1: expected id 1, got %v", resp1.ID)
	}
	if resp1.Result == nil {
		t.Error("client1: expected result in initialize response")
	}

	client2 := subscribeClient(t, url, 2, "/tmp/test-project", "main.go", "go")

	client2.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "initialize",
	})

	resp2 := client2.receiveJSON(t)
	if resp2.ID == nil || resp2.ID.String() != "2" {
		t.Errorf("client2: expected id 2, got %v", resp2.ID)
	}
	if resp2.Result == nil {
		t.Error("client2: expected result in cached initialize response")
	}

	// Reading that cached response is what proves client2's initialize was served; only then does
	// "nothing more reached the server" mean it was served from the cache.
	if sentAfter := mgr.SentMessages(); len(sentAfter) != 1 {
		t.Errorf("expected 1 sent message total (initialize cached), got %d", len(sentAfter))
	}

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

// writeInit writes an initialize request and returns the failure instead of reporting it, so the
// caller can send it from a goroutine the test goroutine only joins later.
func writeInit(c *testClient, id int, workspace, file string) error {
	data, err := json.Marshal(map[string]any{
		"jsonrpc":    "2.0",
		"id":         id,
		"method":     "initialize",
		"_ch":        c.channelID,
		"_workspace": workspace,
		"_lang":      c.language,
		"_file":      file,
		"params":     map[string]any{"rootUri": "file://" + workspace, "capabilities": map[string]any{}},
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return c.conn.Write(ctx, websocket.MessageText, data)
}

// awaitSentMethodID waits for a request with the method to reach the language server and returns
// its remapped id, which is what the fake server has to answer for the response to route.
func awaitSentMethodID(t *testing.T, mgr *mockProcessManager, method string) any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, m := range mgr.SentMessages() {
			var req struct {
				Method string `json:"method"`
				ID     any    `json:"id"`
			}
			if err := json.Unmarshal(m, &req); err == nil && req.Method == method {
				return req.ID
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %q request reached the language server", method)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// receiveResponseFor reads until the client gets a response — $/lspReady may arrive on either side
// of it — and fails when the response is not the client's own id and channel, or carries no result.
func receiveResponseFor(t *testing.T, c *testClient, wantID string, wantCh int64) JSONRPCMessage {
	t.Helper()
	for i := 0; i < 4; i++ {
		data := c.receive(t)
		var msg JSONRPCMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal error: %v, data: %s", err, data)
		}
		if msg.ID == nil || msg.Method != "" {
			continue
		}
		if msg.ID.String() != wantID {
			t.Fatalf("client %d: response carried id=%s, want its own id %s (data: %s)", c.channelID, msg.ID, wantID, data)
		}
		var hint struct {
			Ch int64 `json:"_ch"`
		}
		if err := json.Unmarshal(data, &hint); err != nil || hint.Ch != wantCh {
			t.Fatalf("client %d: response for id=%s carried _ch=%d (data: %s)", c.channelID, wantID, hint.Ch, data)
		}
		if msg.Error != nil {
			t.Fatalf("client %d: response for id=%s was an error: %s", c.channelID, wantID, msg.Error.Message)
		}
		if msg.Result == nil {
			t.Fatalf("client %d: response for id=%s carried no result", c.channelID, wantID)
		}
		return msg
	}
	t.Fatalf("client %d: no response for id=%s among the first 4 messages", c.channelID, wantID)
	return JSONRPCMessage{}
}

func countSentMethods(sent []json.RawMessage, method string) int {
	count := 0
	for _, m := range sent {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(m, &req); err == nil && req.Method == method {
			count++
		}
	}
	return count
}

// subscribeRaceChannel connects and subscribes without the pause subscribeClient takes: the drain
// read already waits for $/lspReady, and this test repeats the handoff often enough to need cheap
// rounds.
func subscribeRaceChannel(t *testing.T, url string, channelID int64, workspace, file, language string) *testClient {
	t.Helper()
	c := connectClient(t, url)
	c.channelID, c.workspace, c.language = channelID, workspace, language
	c.send(t, map[string]any{
		"_ch":    channelID,
		"method": "workspace/subscribe",
		"params": map[string]any{"workspace": workspace, "file": file},
	})
	c.receiveJSON(t) // $/lspReady for this channel
	return c
}

// TestSecondInitializeDuringFirstResponseRouting: one client's initialize response is being routed
// while other clients' initialize requests are being handled on their own connections, against one
// workspace entry. Across that overlap exactly one initialize reaches the language server, and
// every client gets a response with its own id and channel back. Rounds because the overlap is a
// goroutine handoff wide.
func TestSecondInitializeDuringFirstResponseRouting(t *testing.T) {
	const (
		workspace = "/tmp/test-project"
		rounds    = 24
		racers    = 3 // sessions whose initialize arrives while the first response is in flight
	)
	initResponse := func(backendID any) []byte {
		return mustMarshal(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      backendID,
			"result": map[string]any{
				"capabilities": map[string]any{"textDocumentSync": 1},
				"serverInfo":   map[string]any{"name": "gopls", "version": "v1.0"},
			},
		})
	}

	for round := 1; round <= rounds; round++ {
		t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
			mgr := newMockProcessManager()
			handler := NewLSPHandler(newTestRegistry(), mgr)
			defer handler.Close()

			url := startTestServer(t, handler)
			first := subscribeRaceChannel(t, url, 1, workspace, "main.go", "go")
			racing := make([]*testClient, 0, racers)
			for ch := int64(2); ch <= racers+1; ch++ {
				racing = append(racing, subscribeRaceChannel(t, url, ch, workspace, "main.go", "go"))
			}

			// The first initialize is forwarded and left unanswered.
			sendInit(t, first, 1, workspace, "main.go")
			backendID := awaitSentMethodID(t, mgr, "initialize")
			response := initResponse(backendID)

			// Released together: the router caches and replays the response while the other
			// clients' initialize requests are handled on their connections' goroutines.
			start := make(chan struct{})
			var both sync.WaitGroup
			writeErr := make(chan error, len(racing))
			for i, c := range racing {
				both.Add(1)
				go func(c *testClient, id int) {
					defer both.Done()
					<-start
					writeErr <- writeInit(c, id, workspace, "main.go")
				}(c, i+2)
			}
			both.Add(1)
			go func() {
				defer both.Done()
				<-start
				mgr.Deliver(response)
			}()
			close(start)
			both.Wait()
			close(writeErr)
			for err := range writeErr {
				if err != nil {
					t.Fatalf("an initialize could not be sent: %v", err)
				}
			}

			receiveResponseFor(t, first, "1", 1)
			for i, c := range racing {
				receiveResponseFor(t, c, fmt.Sprint(i+2), int64(i+2))
			}

			// A client that decided it was first forwards its own initialize only after the
			// response cleared the pending marker, so this is the window in which a second
			// initialize would arrive. Cut it and the count is 1 for the wrong reason.
			time.Sleep(20 * time.Millisecond)
			if got := countSentMethods(mgr.SentMessages(), "initialize"); got != 1 {
				t.Errorf("%d initialize requests reached the language server, want exactly 1", got)
			}
		})
	}
}

func TestDisconnectCleanup(t *testing.T) {
	mgr := newMockProcessManager()
	reg := newTestRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	client := subscribeClientDefault(t, url, "/tmp/test-project", "main.go")

	if mgr.ProcessCount() != 1 {
		t.Errorf("expected 1 process, got %d", mgr.ProcessCount())
	}

	client.conn.Close(websocket.StatusNormalClosure, "done")

	awaitInt(t, 0, "the language server released when its last client disconnected", mgr.ProcessCount)
}

func TestMultipleRequestRemapping(t *testing.T) {
	mgr := newMockProcessManager()
	reg := newTestRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	client := subscribeClientDefault(t, url, "/tmp/test-project", "main.go")

	requests := []int64{10, 20, 30}
	for _, id := range requests {
		client.sendLSP(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  "textDocument/definition",
			"params":  map[string]any{"textDocument": map[string]any{"uri": "file:///main.go"}},
		})
	}

	sent := waitForSentMessages(t, mgr, 3)
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
	reg := newTestRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	client := subscribeClientDefault(t, url, "/tmp/test-project", "main.go")
	mgr.ResetSent()

	client.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  "initialized",
		"params":  map[string]any{},
	})

	sent := waitForSentMessages(t, mgr, 1)
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
	reg := newTestRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	client1 := subscribeClient(t, url, 1, "/tmp/project-a", "main.go", "go")

	client2 := subscribeClient(t, url, 2, "/tmp/project-b", "main.go", "go")

	client1.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      100,
		"method":  "textDocument/completion",
		"params":  map[string]any{},
	})

	sent := waitForSentMessages(t, mgr, 1)
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sent))
	}

	var m map[string]any
	json.Unmarshal(sent[0], &m)
	response := map[string]any{
		"jsonrpc": "2.0",
		"id":      m["id"],
		"result":  map[string]any{"items": []any{}},
	}
	respData, _ := json.Marshal(response)
	mgr.Deliver(respData)

	resp := client1.receiveJSON(t)
	if resp.ID == nil || resp.ID.String() != "100" {
		t.Errorf("client1: expected id 100, got %v", resp.ID)
	}

	// Nothing arrives, so this read times out.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, err := client2.conn.Read(ctx)
	if err == nil {
		t.Error("client2 should not have received any message (different workspace)")
	}
}

func TestTwoClientsSameWorkspace(t *testing.T) {
	mgr := newMockProcessManager()
	reg := newTestRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	client1 := subscribeClient(t, url, 1, "/tmp/test-project", "main.go", "go")
	client2 := subscribeClient(t, url, 2, "/tmp/test-project", "main.go", "go")

	client1.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      100,
		"method":  "textDocument/completion",
		"params":  map[string]any{},
	})

	sent := waitForSentMessages(t, mgr, 1)
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sent))
	}

	var m map[string]any
	json.Unmarshal(sent[0], &m)
	response := map[string]any{
		"jsonrpc": "2.0",
		"id":      m["id"],
		"result":  map[string]any{"items": []any{}},
	}
	respData, _ := json.Marshal(response)
	mgr.Deliver(respData)

	resp := client1.receiveJSON(t)
	if resp.ID == nil || resp.ID.String() != "100" {
		t.Errorf("client1: expected id 100, got %v", resp.ID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, err := client2.conn.Read(ctx)
	if err == nil {
		t.Error("client2 should not have received the response")
	}
}

func TestMultiWorkspaceSingleConnection(t *testing.T) {
	// Two channels on one connection: a request reaches its own workspace and the answer
	// its own channel.
	mgr := newMockProcessManager()
	reg := newTestRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	client := connectClient(t, url)

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

	client.receiveJSON(t) // ch=1 ready
	client.receiveJSON(t) // ch=2 ready

	awaitInt(t, 2, "one process per subscribed workspace", mgr.ProcessCount)

	client.channelID = 1
	client.workspace = "/tmp/project-a"
	client.language = "go"
	client.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      100,
		"method":  "textDocument/completion",
		"params":  map[string]any{},
	})

	sent := waitForSentMessages(t, mgr, 1)
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sent))
	}

	var m map[string]any
	json.Unmarshal(sent[0], &m)
	response := map[string]any{
		"jsonrpc": "2.0",
		"id":      m["id"],
		"result":  map[string]any{"items": []any{"from-a"}},
	}
	respData, _ := json.Marshal(response)
	mgr.Deliver(respData)

	resp := client.receiveJSON(t)
	if resp.ID == nil || resp.ID.String() != "100" {
		t.Errorf("expected id 100, got %v", resp.ID)
	}

	client.channelID = 2
	client.workspace = "/tmp/project-b"
	client.language = "go"
	client.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      200,
		"method":  "textDocument/completion",
		"params":  map[string]any{},
	})

	sent2 := waitForSentMessages(t, mgr, 2)
	if len(sent2) != 2 {
		t.Fatalf("expected 2 sent messages, got %d", len(sent2))
	}

	var m2 map[string]any
	json.Unmarshal(sent2[1], &m2)
	response2 := map[string]any{
		"jsonrpc": "2.0",
		"id":      m2["id"],
		"result":  map[string]any{"items": []any{"from-b"}},
	}
	respData2, _ := json.Marshal(response2)
	mgr.Deliver(respData2)

	resp2 := client.receiveJSON(t)
	if resp2.ID == nil || resp2.ID.String() != "200" {
		t.Errorf("expected id 200, got %v", resp2.ID)
	}
}

// TestRouteDropsFramesItCannotDispatch: a server request (id and method), a frame with neither, and
// an empty object have no destination: they reach no client and do not stop the router. The
// notification sent last is the observable half — a subscribed session receives every notification,
// so it is the only frame this client may get.
func TestRouteDropsFramesItCannotDispatch(t *testing.T) {
	mgr := newMockProcessManager()
	handler := NewLSPHandler(newTestRegistry(), mgr)
	defer handler.Close()

	url := startTestServer(t, handler)
	client := subscribeClientDefault(t, url, "/tmp/test-project", "main.go")

	for _, frame := range []json.RawMessage{
		json.RawMessage(`{"jsonrpc":"2.0","id":99,"method":"window/workDoneProgress","params":{"token":"abc"}}`),
		json.RawMessage(`{"jsonrpc":"2.0","result":{}}`),
		json.RawMessage(`{}`),
	} {
		mgr.Deliver(frame)
	}

	mgr.Deliver(json.RawMessage(`{"jsonrpc":"2.0","method":"window/logMessage","params":{"type":3,"message":"probe"}}`))

	first := client.receive(t)
	if !strings.Contains(string(first), "window/logMessage") {
		t.Errorf("first frame after the undeliverable ones = %s, want the probe notification", first)
	}
}

func TestUnknownExtensionRejected(t *testing.T) {
	mgr := newMockProcessManager()
	reg := newTestRegistry()
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, err := client.conn.Read(ctx)
	if err == nil {
		t.Error("expected connection to be closed for unknown extension")
	}
}

func TestLSPReadyNotification(t *testing.T) {
	mgr := newMockProcessManager()
	reg := newTestRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	client := subscribeClientDefault(t, url, "/tmp/test-project", "main.go")

	client.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"rootUri":      "file:///tmp/test-project",
			"capabilities": map[string]any{},
		},
	})

	sent := waitForSentMessages(t, mgr, 1)
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

	resp := client.receiveJSON(t)
	if resp.ID == nil || resp.ID.String() != "1" {
		t.Fatalf("expected initialize response with id=1, got id=%v", resp.ID)
	}
	if resp.Result == nil {
		t.Fatal("expected result in initialize response")
	}

	client.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  "initialized",
		"params":  map[string]any{},
	})

	readyMsg := client.receiveJSON(t)
	if readyMsg.Method != "$/lspReady" {
		t.Fatalf("expected $server/lspReady notification, got method=%s", readyMsg.Method)
	}
}

func TestLSPReadyNotificationCachedResponse(t *testing.T) {
	mgr := newMockProcessManager()
	reg := newTestRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	client1 := subscribeClientDefault(t, url, "/tmp/test-project", "main.go")

	client1.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"rootUri":      "file:///tmp/test-project",
			"capabilities": map[string]any{},
		},
	})
	sent := waitForSentMessages(t, mgr, 1)
	var initReq map[string]any
	json.Unmarshal(sent[0], &initReq)
	backendID := initReq["id"]

	initResponse := map[string]any{
		"jsonrpc": "2.0",
		"id":      backendID,
		"result": map[string]any{
			"capabilities": map[string]any{},
			"serverInfo":   map[string]any{"name": "gopls"},
		},
	}
	mgr.Deliver(json.RawMessage(mustMarshal(t, initResponse)))

	resp1 := client1.receiveJSON(t)
	if resp1.ID == nil || resp1.ID.String() != "1" {
		t.Fatalf("client1: expected id=1, got %v", resp1.ID)
	}

	client1.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  "initialized",
		"params":  map[string]any{},
	})
	ready1 := client1.receiveJSON(t)
	if ready1.Method != "$/lspReady" {
		t.Fatalf("client1: expected $server/lspReady, got %s", ready1.Method)
	}

	client2 := subscribeClient(t, url, 2, "/tmp/test-project", "main.go", "go")

	client2.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "initialize",
		"params": map[string]any{
			"rootUri":      "file:///tmp/test-project",
			"capabilities": map[string]any{},
		},
	})

	resp2 := client2.receiveJSON(t)
	if resp2.ID == nil || resp2.ID.String() != "2" {
		t.Fatalf("client2: expected id=2, got %v", resp2.ID)
	}
	if resp2.Result == nil {
		t.Fatal("client2: expected cached result")
	}

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

func TestSubscriptionRejectsDisallowedWorkspace(t *testing.T) {
	mgr := newMockProcessManager()
	reg := newTestRegistry()
	handler := NewLSPHandler(reg, mgr)
	handler.SetWorkspaceValidator(func(ws string) bool { return ws != "/etc" })
	defer handler.Close()

	url := startTestServer(t, handler)

	client := connectClient(t, url)
	client.channelID = 1
	client.workspace = "/etc"
	client.language = "go"
	client.send(t, map[string]any{
		"_ch":    1,
		"method": "workspace/subscribe",
		"params": map[string]any{"workspace": "/etc", "file": "main.go"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := client.conn.Read(ctx); err == nil {
		t.Fatal("expected connection to be closed for a disallowed workspace")
	}
	if got := mgr.ProcessCount(); got != 0 {
		t.Fatalf("expected no LSP process spawned, got %d", got)
	}
}

func TestSubscriptionAllowsAllowedWorkspace(t *testing.T) {
	mgr := newMockProcessManager()
	reg := newTestRegistry()
	handler := NewLSPHandler(reg, mgr)
	handler.SetWorkspaceValidator(func(ws string) bool { return ws == "/tmp/test-project" })
	defer handler.Close()

	url := startTestServer(t, handler)
	subscribeClientDefault(t, url, "/tmp/test-project", "main.go")
	if got := mgr.ProcessCount(); got != 1 {
		t.Fatalf("expected 1 LSP process, got %d", got)
	}
}

// waitForSentMessages waits until n messages have reached the language server.
func waitForSentMessages(t *testing.T, mgr *mockProcessManager, n int) []json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		sent := mgr.SentMessages()
		if len(sent) >= n {
			return sent
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d sent message(s), have %d", n, len(sent))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// sendInit sends an initialize with the routing hints and no workspace/subscribe.
func sendInit(t *testing.T, c *testClient, id int, workspace, file string) {
	t.Helper()
	c.send(t, map[string]any{
		"jsonrpc":    "2.0",
		"id":         id,
		"method":     "initialize",
		"_ch":        c.channelID,
		"_workspace": workspace,
		"_lang":      c.language,
		"_file":      file,
		"params":     map[string]any{"rootUri": "file://" + workspace, "capabilities": map[string]any{}},
	})
}

// TestLazyInitializeWithoutSubscribe: initialize alone creates the workspace. A mistake here is a
// double unlock of h.mu, which is a runtime throw: the process dies instead of the test failing.
func TestLazyInitializeWithoutSubscribe(t *testing.T) {
	mgr := newMockProcessManager()
	handler := NewLSPHandler(newTestRegistry(), mgr)
	handler.SetWorkspaceValidator(func(ws string) bool { return ws == "/tmp/test-project" })
	defer handler.Close()

	url := startTestServer(t, handler)
	client := connectClient(t, url)
	client.channelID = 1
	client.language = "go"
	sendInit(t, client, 1, "/tmp/test-project", "main.go")

	sent := waitForSentMessages(t, mgr, 1)
	if got := mgr.ProcessCount(); got != 1 {
		t.Fatalf("expected the lazy path to create one server, got %d", got)
	}

	var initReq map[string]any
	if err := json.Unmarshal(sent[0], &initReq); err != nil {
		t.Fatalf("sent initialize is not JSON: %v", err)
	}
	mgr.Deliver(mustMarshal(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      initReq["id"],
		"result":  map[string]any{"capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "gopls"}},
	}))

	resp := client.receiveJSON(t)
	if resp.ID == nil || resp.ID.String() != "1" {
		t.Fatalf("expected the initialize answer under id 1, got %v", resp.ID)
	}
	if resp.Result == nil {
		t.Fatal("expected a result on the lazy initialize answer")
	}

	// The entry is registered, not left pending: a second client gets the cached answer.
	client2 := connectClient(t, url)
	client2.channelID = 2
	client2.language = "go"
	sendInit(t, client2, 7, "/tmp/test-project", "main.go")

	resp2 := client2.receiveJSON(t)
	if resp2.ID == nil || resp2.ID.String() != "7" {
		t.Fatalf("expected the cached answer under id 7, got %v", resp2.ID)
	}
	if resp2.Result == nil {
		t.Fatal("expected the cached result for the second lazy initialize")
	}
	if got := mgr.ProcessCount(); got != 1 {
		t.Fatalf("the second lazy initialize spawned another server: %d", got)
	}
}

// TestLazyInitializeRejectsDisallowedWorkspace: the workspace hint is the client's and a
// lazy workspace is bind-mounted into the extension sandbox, so the validator guards this
// path too. The request is answered and the connection stays open.
func TestLazyInitializeRejectsDisallowedWorkspace(t *testing.T) {
	mgr := newMockProcessManager()
	handler := NewLSPHandler(newTestRegistry(), mgr)
	handler.SetWorkspaceValidator(func(ws string) bool { return ws == "/tmp/test-project" })
	defer handler.Close()

	url := startTestServer(t, handler)
	client := connectClient(t, url)
	client.channelID = 1
	client.language = "go"
	sendInit(t, client, 1, "/etc", "main.go")

	// receiveJSON fails the test if the connection was closed instead of answered.
	resp := client.receiveJSON(t)
	if resp.Error == nil {
		t.Fatalf("expected a JSON-RPC error for a disallowed workspace, got %+v", resp)
	}
	if resp.ID == nil || resp.ID.String() != "1" {
		t.Fatalf("expected the error under id 1, got %v", resp.ID)
	}
	if got := mgr.ProcessCount(); got != 0 {
		t.Fatalf("a rejected workspace must spawn nothing, got %d server(s)", got)
	}
	if got := len(mgr.SentMessages()); got != 0 {
		t.Fatalf("a rejected workspace must forward nothing, got %d message(s)", got)
	}
}

// TestLazyInitializeAnswersWhenNoServerOwnsTheFile: a lazy create that finds no server
// must still answer, or the editor waits forever for a reply that never comes.
func TestLazyInitializeAnswersWhenNoServerOwnsTheFile(t *testing.T) {
	mgr := newMockProcessManager()
	handler := NewLSPHandler(newTestRegistry(), mgr)
	defer handler.Close()

	url := startTestServer(t, handler)
	client := connectClient(t, url)
	client.channelID = 1
	client.language = "go"
	sendInit(t, client, 3, "/tmp/test-project", "notes.txt") // the test registry knows .go only

	resp := client.receiveJSON(t)
	if resp.Error == nil {
		t.Fatalf("expected a JSON-RPC error for a file no server owns, got %+v", resp)
	}
	if got := mgr.ProcessCount(); got != 0 {
		t.Fatalf("no server config means no spawn, got %d", got)
	}
}

func TestWebSocketOrigin_RejectsCrossOrigin(t *testing.T) {
	mgr := newMockProcessManager()
	reg := newTestRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	// A cross-origin handshake must be rejected (Origin != Host).
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := websocket.Dial(ctx, url+"/api/lsp", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example.com"}},
	})
	if err == nil {
		t.Fatal("expected cross-origin WebSocket handshake to be rejected")
	}
}

func TestWebSocketOrigin_AcceptsSameOrigin(t *testing.T) {
	mgr := newMockProcessManager()
	reg := newTestRegistry()
	handler := NewLSPHandler(reg, mgr)
	defer handler.Close()

	url := startTestServer(t, handler)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url+"/api/lsp", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{url}},
	})
	if err != nil {
		t.Fatalf("expected same-origin WebSocket handshake to succeed: %v", err)
	}
	defer conn.CloseNow()
}

// ─── HandleSandboxMemory (GET /api/lsp/memory) ──────────────────────────────

// The POST-is-refused case is asserted at backend/server/routes_method_test.go: the route pattern
// "GET /api/lsp/memory" carries the verb, so a handler-level call cannot reach that decision.

func TestHandleSandboxMemory_MissingParams(t *testing.T) {
	handler := NewLSPHandler(newTestRegistry(), newMockProcessManager())
	defer handler.Close()

	for _, path := range []string{
		"/api/lsp/memory",
		"/api/lsp/memory?workspace=/tmp/ws",
		"/api/lsp/memory?file=/tmp/ws/main.go",
	} {
		w := httptest.NewRecorder()
		handler.HandleSandboxMemory(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", path, w.Code, http.StatusBadRequest)
		}
	}
}

func TestHandleSandboxMemory_WorkspaceRejected(t *testing.T) {
	handler := NewLSPHandler(newTestRegistry(), newMockProcessManager())
	defer handler.Close()
	handler.SetWorkspaceValidator(func(ws string) bool { return ws == "/tmp/allowed" })

	w := httptest.NewRecorder()
	handler.HandleSandboxMemory(w, httptest.NewRequest(http.MethodGet, "/api/lsp/memory?workspace=/etc&file=/etc/passwd", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestHandleSandboxMemory_OK(t *testing.T) {
	handler := NewLSPHandler(newTestRegistry(), newMockProcessManager())
	defer handler.Close()

	w := httptest.NewRecorder()
	handler.HandleSandboxMemory(w, httptest.NewRequest(http.MethodGet, "/api/lsp/memory?workspace=/tmp/ws&file=/tmp/ws/main.go", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var stats types.SandboxMemoryStats
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatalf("unmarshal: %v (body: %s)", err, w.Body.String())
	}
	if !stats.Running || stats.CurrentMB != 42 || stats.LimitMB != 256 || stats.PeakMB != 64 || stats.OOMKills != 1 {
		t.Errorf("stats = %+v, want mock values", stats)
	}
}
