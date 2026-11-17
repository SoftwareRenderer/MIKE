package lsp

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
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
	sentMessages []jsontext.Value
	// sendErr, when set, is what every send to the server answers with.
	sendErr   error
	messageCh chan jsontext.Value
	// memoryQuery records the last (workspace, file) the memory endpoint asked about, so a
	// refusal can be proved to have asked the manager nothing.
	memoryQuery [2]string
}

// mockProcess wraps LSPProcess with a test-only reference count.
type mockProcess struct {
	*LSPProcess
	refCount int
}

func newMockProcessManager() *mockProcessManager {
	return &mockProcessManager{
		processes: make(map[string]*mockProcess),
		messageCh: make(chan jsontext.Value, 1024),
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

func (m *mockProcessManager) SendToServer(_ context.Context, process *LSPProcess, msg jsontext.Value) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentMessages = append(m.sentMessages, msg)
	return m.sendErr
}

func (m *mockProcessManager) ServerMessageCh() <-chan jsontext.Value {
	return m.messageCh
}

func (m *mockProcessManager) SandboxMemory(workspacePath, filePath string) *types.SandboxMemoryStats {
	m.mu.Lock()
	m.memoryQuery = [2]string{workspacePath, filePath}
	m.mu.Unlock()
	return &types.SandboxMemoryStats{Running: true, CurrentMB: 42, LimitMB: 256, PeakMB: 64, OOMKills: 1}
}

func (m *mockProcessManager) memoryAsked() (string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.memoryQuery[0], m.memoryQuery[1]
}

func (m *mockProcessManager) Deliver(msg jsontext.Value) {
	select {
	case m.messageCh <- msg:
	default:
	}
}

func (m *mockProcessManager) SentMessages() []jsontext.Value {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]jsontext.Value, len(m.sentMessages))
	copy(copied, m.sentMessages)
	return copied
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
func (c *testClient) receive(t *testing.T) jsontext.Value {
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
	time.Sleep(50 * time.Millisecond)

	// Drain the $server/lspReady notification sent by handleSubscription
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

// awaitSentMethod waits until n messages with the method have reached the language server.
func awaitSentMethod(t *testing.T, mgr *mockProcessManager, method string, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := countSentMethods(mgr.SentMessages(), method)
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d %q messages reached the language server, want %d", got, method, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRequestIDsAreRemappedAndAnswersRouteBack: the browser and this handler number requests
// independently, so a request never leaves with the id it arrived with, and its answer comes back
// under the browser's own id. Sent several at once, which is what an editor with open tabs does:
// the numbering has to be unique, because a repeated backend id routes one answer to the wrong
// waiter.
func TestRequestIDsAreRemappedAndAnswersRouteBack(t *testing.T) {
	browserIDs := []int{5, 20, 30}
	mgr := newMockProcessManager()
	handler := NewLSPHandler(newTestRegistry(), mgr)
	defer handler.Close()

	url := startTestServer(t, handler)
	client := subscribeClientDefault(t, url, "/tmp/test-project", "main.go")

	for _, id := range browserIDs {
		client.sendLSP(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  "textDocument/definition",
			"params":  map[string]any{"textDocument": map[string]any{"uri": "file:///main.go"}},
		})
	}

	sent := waitForSentMessages(t, mgr, len(browserIDs))
	if len(sent) != len(browserIDs) {
		t.Fatalf("%d requests reached the language server, want %d", len(sent), len(browserIDs))
	}
	backendIDs := map[string]bool{}
	for _, msg := range sent {
		var m map[string]any
		if err := json.Unmarshal(msg, &m); err != nil {
			t.Fatalf("a forwarded request is not JSON: %v", err)
		}
		backendID := fmt.Sprint(m["id"])
		if backendIDs[backendID] {
			t.Errorf("two requests were forwarded under backend id %s", backendID)
		}
		for _, want := range browserIDs {
			if backendID == fmt.Sprint(want) {
				t.Errorf("request %s reached the language server under the browser's own id", backendID)
			}
		}
		backendIDs[backendID] = true
	}

	for _, msg := range sent {
		var m map[string]any
		if err := json.Unmarshal(msg, &m); err != nil {
			t.Fatalf("a forwarded request is not JSON: %v", err)
		}
		resp, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": []any{}})
		if err != nil {
			t.Fatal(err)
		}
		mgr.Deliver(resp)
	}

	answered := map[string]bool{}
	for range browserIDs {
		resp := client.receiveJSON(t)
		if resp.ID == nil {
			t.Fatal("an answer arrived with no id")
		}
		if resp.Result == nil {
			t.Errorf("the answer for id=%s carried no result", resp.ID)
		}
		answered[resp.ID.String()] = true
	}
	for _, want := range browserIDs {
		if !answered[fmt.Sprint(want)] {
			t.Errorf("the browser never got an answer under its own id %d; got %v", want, answered)
		}
	}
}

// TestInitializeIsForwardedOnceAndReplayedToEveryClient: one language server per workspace, so one
// initialize ever reaches it. The first client is answered when the server answers; every client
// after that is answered from the cached reply, under its own id and on its own channel, and each
// client is told $/lspReady — the signal the editor waits for before asking about a document.
func TestInitializeIsForwardedOnceAndReplayedToEveryClient(t *testing.T) {
	const workspace = "/tmp/test-project"
	mgr := newMockProcessManager()
	handler := NewLSPHandler(newTestRegistry(), mgr)
	defer handler.Close()

	url := startTestServer(t, handler)
	first := subscribeClientDefault(t, url, workspace, "main.go")

	first.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{"rootUri": "file://" + workspace, "capabilities": map[string]any{}},
	})
	backendID := awaitSentMethodID(t, mgr, "initialize")
	if got := countSentMethods(mgr.SentMessages(), "initialize"); got != 1 {
		t.Fatalf("%d initialize requests reached the language server, want 1", got)
	}

	mgr.Deliver(mustMarshal(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      backendID,
		"result": map[string]any{
			"capabilities": map[string]any{
				"textDocumentSync":   1,
				"completionProvider": map[string]any{"resolveProvider": true},
			},
			"serverInfo": map[string]any{"name": "gopls", "version": "v1.0"},
		},
	}))
	receiveResponseFor(t, first, "1", 1)

	// The client's own `initialized` reaches the server, and the handler signals readiness back.
	first.sendLSP(t, map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
	awaitSentMethod(t, mgr, "initialized", 1)
	if ready := first.receiveJSON(t); ready.Method != "$/lspReady" {
		t.Fatalf("after initialized the client got method=%s, want $/lspReady", ready.Method)
	}

	second := subscribeClient(t, url, 2, workspace, "main.go", "go")
	second.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "initialize",
		"params":  map[string]any{"rootUri": "file://" + workspace, "capabilities": map[string]any{}},
	})
	cached := receiveResponseFor(t, second, "2", 2)
	var result map[string]any
	if err := json.Unmarshal(cached.Result, &result); err != nil {
		t.Fatalf("the cached answer is not JSON: %v", err)
	}
	if caps, ok := result["capabilities"].(map[string]any); !ok || caps["textDocumentSync"].(float64) != 1 {
		t.Errorf("the cached answer lost the server's capabilities: %s", cached.Result)
	}
	if ready := second.receiveJSON(t); ready.Method != "$/lspReady" {
		t.Fatalf("the second client got method=%s, want $/lspReady", ready.Method)
	}
	if got := countSentMethods(mgr.SentMessages(), "initialize"); got != 1 {
		t.Errorf("%d initialize requests reached the language server, want 1 — this workspace has its server already", got)
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

func countSentMethods(sent []jsontext.Value, method string) int {
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
			// response cleared the pending marker, so give it that same grace.
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

	// Cleanup is asynchronous.
	time.Sleep(100 * time.Millisecond)

	if mgr.ProcessCount() != 0 {
		t.Errorf("expected 0 processes after disconnect, got %d", mgr.ProcessCount())
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

	time.Sleep(50 * time.Millisecond)

	sent := mgr.SentMessages()
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

	time.Sleep(50 * time.Millisecond)

	client.receiveJSON(t) // ch=1 ready
	client.receiveJSON(t) // ch=2 ready

	if mgr.ProcessCount() != 2 {
		t.Errorf("expected 2 processes, got %d", mgr.ProcessCount())
	}

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

	time.Sleep(50 * time.Millisecond)

	sent2 := mgr.SentMessages()
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

	for _, frame := range []jsontext.Value{
		jsontext.Value(`{"jsonrpc":"2.0","id":99,"method":"window/workDoneProgress","params":{"token":"abc"}}`),
		jsontext.Value(`{"jsonrpc":"2.0","result":{}}`),
		jsontext.Value(`{}`),
	} {
		mgr.Deliver(frame)
	}

	mgr.Deliver(jsontext.Value(`{"jsonrpc":"2.0","method":"window/logMessage","params":{"type":3,"message":"probe"}}`))

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

// TestSubscriptionHonoursTheWorkspaceValidator: the workspace a browser names is the client's, and a
// subscribed workspace is bind-mounted into the extension sandbox — so a refused workspace spawns
// nothing and ends the connection, and an accepted one spawns exactly the one server it asked for.
func TestSubscriptionHonoursTheWorkspaceValidator(t *testing.T) {
	allowed := "/tmp/test-project"
	validator := func(ws string) bool { return ws == allowed }

	t.Run("refused spawns nothing", func(t *testing.T) {
		mgr := newMockProcessManager()
		handler := NewLSPHandler(newTestRegistry(), mgr)
		handler.SetWorkspaceValidator(validator)
		defer handler.Close()

		client := connectClient(t, startTestServer(t, handler))
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
			t.Fatal("a disallowed workspace was subscribed anyway")
		}
		if got := mgr.ProcessCount(); got != 0 {
			t.Fatalf("a refused workspace spawned %d server(s)", got)
		}
	})

	t.Run("accepted spawns one", func(t *testing.T) {
		mgr := newMockProcessManager()
		handler := NewLSPHandler(newTestRegistry(), mgr)
		handler.SetWorkspaceValidator(validator)
		defer handler.Close()

		subscribeClientDefault(t, startTestServer(t, handler), allowed, "main.go")
		if got := mgr.ProcessCount(); got != 1 {
			t.Fatalf("an allowed workspace spawned %d servers, want 1", got)
		}
	})
}

// TestWebSocketOriginIsPinnedToTheHost: the socket serves this install's own page and nobody else's.
func TestWebSocketOriginIsPinnedToTheHost(t *testing.T) {
	mgr := newMockProcessManager()
	handler := NewLSPHandler(newTestRegistry(), mgr)
	defer handler.Close()
	url := startTestServer(t, handler)

	t.Run("another origin is refused", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _, err := websocket.Dial(ctx, url+"/api/lsp", &websocket.DialOptions{
			HTTPHeader: http.Header{"Origin": []string{"https://evil.example.com"}},
		})
		if err == nil {
			t.Fatal("a cross-origin WebSocket handshake was accepted")
		}
	})

	t.Run("the page's own origin is accepted", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(ctx, url+"/api/lsp", &websocket.DialOptions{
			HTTPHeader: http.Header{"Origin": []string{url}},
		})
		if err != nil {
			t.Fatalf("the same-origin handshake failed: %v", err)
		}
		defer conn.CloseNow()
	})
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	return data
}

// waitForSentMessages waits until n messages have reached the language server.
func waitForSentMessages(t *testing.T, mgr *mockProcessManager, n int) []jsontext.Value {
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

// ─── HandleSandboxMemory (GET /api/lsp/memory) ──────────────────────────────

// The POST-is-refused case is asserted at backend/server/routes_method_test.go: the route pattern
// "GET /api/lsp/memory" carries the verb, so a handler-level call cannot reach that decision.

// TestHandleSandboxMemoryAnswersTheQuery: the memory panel asks by query pair, and the two checks
// that guard it happen before anything is asked of the manager that owns the container — a missing
// half of the pair is a 400, a workspace the validator refuses is a 403, and only an allowed pair
// reaches the manager and comes back as the stats object the panel decodes.
func TestHandleSandboxMemoryAnswersTheQuery(t *testing.T) {
	const workspace, file = "/tmp/ws", "/tmp/ws/main.go"
	cases := []struct {
		name      string
		target    string
		validator func(string) bool
		want      int
		asked     bool
	}{
		{"no query at all", "/api/lsp/memory", nil, http.StatusBadRequest, false},
		{"no file", "/api/lsp/memory?workspace=" + workspace, nil, http.StatusBadRequest, false},
		{"no workspace", "/api/lsp/memory?file=" + file, nil, http.StatusBadRequest, false},
		{"a workspace the validator refuses", "/api/lsp/memory?workspace=/etc&file=/etc/passwd",
			func(ws string) bool { return ws == workspace }, http.StatusForbidden, false},
		{"the allowed pair is served", "/api/lsp/memory?workspace=" + workspace + "&file=" + file,
			nil, http.StatusOK, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr := newMockProcessManager()
			handler := NewLSPHandler(newTestRegistry(), mgr)
			if tc.validator != nil {
				handler.SetWorkspaceValidator(tc.validator)
			}
			defer handler.Close()

			w := httptest.NewRecorder()
			handler.HandleSandboxMemory(w, httptest.NewRequest(http.MethodGet, tc.target, nil))

			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.want, w.Body.String())
			}
			askedWorkspace, askedFile := mgr.memoryAsked()
			if !tc.asked {
				if askedWorkspace != "" || askedFile != "" {
					t.Errorf("a refused request asked the manager about %q/%q", askedWorkspace, askedFile)
				}
				return
			}
			if askedWorkspace != workspace || askedFile != file {
				t.Errorf("the manager was asked about %q/%q, want %q/%q", askedWorkspace, askedFile, workspace, file)
			}
			var stats types.SandboxMemoryStats
			if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
				t.Fatalf("the body is not the stats object the panel reads: %v (body %s)", err, w.Body.String())
			}
			if !stats.Running || stats.CurrentMB != 42 || stats.LimitMB != 256 || stats.PeakMB != 64 || stats.OOMKills != 1 {
				t.Errorf("stats = %+v, want the manager's answer delivered intact", stats)
			}
		})
	}
}
