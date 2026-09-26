package lsp

// What one connection may make the handler hold: live sessions, the requests a session has
// waiting for an answer, and the initialize requests waiting behind the one in flight. Every
// bound answers what it refuses to hold — a dropped request is never a silent one.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mike/backend/types"

	"github.com/coder/websocket"
)

// deafManager stands in for a language server that accepts every request and answers none. A send
// that never returns is a different shape: the writer gives each request its own goroutine, and
// the session's bound is what keeps that count finite.
type deafManager struct {
	mu        sync.Mutex
	sent      []json.RawMessage
	processes map[string]*LSPProcess
	replies   chan json.RawMessage
}

func newDeafManager() *deafManager {
	return &deafManager{
		processes: make(map[string]*LSPProcess),
		replies:   make(chan json.RawMessage, 64),
	}
}

func (m *deafManager) GetOrCreate(_ context.Context, workspacePath, language string) (*LSPProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := processKey(workspacePath, language)
	if p, ok := m.processes[key]; ok {
		return p, nil
	}
	p := &LSPProcess{Workspace: workspacePath, Language: language}
	m.processes[key] = p
	return p, nil
}

func (m *deafManager) Release(workspacePath, language string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.processes, processKey(workspacePath, language))
}

func (m *deafManager) SendToServer(_ context.Context, _ *LSPProcess, msg json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func (m *deafManager) ServerMessageCh() <-chan json.RawMessage { return m.replies }

func (m *deafManager) SandboxMemory(workspacePath, filePath string) *types.SandboxMemoryStats {
	return nil
}

// answer delivers one server response, for the case where an answer does arrive and has to free
// the slot its request held as well as being delivered.
func (m *deafManager) answer(serverID any) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      serverID,
		"result":  map[string]any{"contents": "answered"},
	})
	m.replies <- body
}

// forwarded lists the ids the server was asked to answer for one method, in the order the sends
// happened.
func (m *deafManager) forwarded(method string) []float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []float64
	for _, raw := range m.sent {
		var msg struct {
			ID     *float64 `json:"id"`
			Method string   `json:"method"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if msg.Method == method && msg.ID != nil {
			ids = append(ids, *msg.ID)
		}
	}
	return ids
}

// waitUntil polls a condition the handler's own goroutines satisfy, and says what it waited for.
func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// liveSessions counts the sessions the handler has registered.
func liveSessions(h *LSPHandler) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// loneSession returns the one session the handler has registered.
func loneSession(t *testing.T, h *LSPHandler) *ClientSession {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) != 1 {
		t.Fatalf("%d sessions registered, want exactly 1", len(h.clients))
	}
	for _, s := range h.clients {
		return s
	}
	return nil
}

// outstanding counts the requests one session still has mappings for.
func outstanding(s *ClientSession) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.incomingIDs)
}

// bookkeepingAgrees reports whether the order the session evicts in still matches the mappings it
// routes answers with: an entry left in one and not the other is a leak or a phantom eviction.
func bookkeepingAgrees(s *ClientSession) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingOrder.Len() == len(s.incomingIDs) && len(s.pendingElems) == len(s.incomingIDs)
}

func sendRequest(t *testing.T, c *testClient, id int) {
	t.Helper()
	c.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "textDocument/hover",
		"params":  map[string]any{"textDocument": map[string]any{"uri": "file:///main.go"}},
	})
}

func sendInitialize(t *testing.T, c *testClient, id int) {
	t.Helper()
	c.sendLSP(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "initialize",
	})
}

// TestRequestMapsStopAtTheirBound: the maps that route answers back stay finite when the server
// answers nothing. Past the bound the oldest request loses its slot, and it is answered rather than
// dropped.
func TestRequestMapsStopAtTheirBound(t *testing.T) {
	mgr := newDeafManager()
	handler := NewLSPHandler(newTestRegistry(), mgr)
	handler.maxPendingReqs = 4
	defer handler.Close()

	url := startTestServer(t, handler)
	client := subscribeClientDefault(t, url, "/tmp/bounded-project", "main.go")

	const total = 12
	for i := 1; i <= total; i++ {
		sendRequest(t, client, i)
	}
	waitUntil(t, func() bool { return len(mgr.forwarded("textDocument/hover")) == total },
		"every request forwarded to the language server")

	// The oldest requests past the bound are the ones answered with a failure.
	const evictedCount = total - 4
	answered := map[string]bool{}
	for i := 0; i < evictedCount; i++ {
		msg := client.receiveJSON(t)
		if msg.Error == nil {
			t.Fatalf("an evicted request was answered without an error: %+v", msg)
		}
		if msg.ID == nil {
			t.Fatalf("an evicted request was answered without an id: %+v", msg)
		}
		answered[msg.ID.String()] = true
	}
	for i := 1; i <= evictedCount; i++ {
		if !answered[strconv.Itoa(i)] {
			t.Fatalf("request id=%d was neither kept nor answered: %+v", i, answered)
		}
	}

	session := loneSession(t, handler)
	if got := outstanding(session); got > 4 {
		t.Fatalf("the session holds %d outstanding requests, over its bound of 4", got)
	}
	if !bookkeepingAgrees(session) {
		t.Fatal("the session's eviction order and its answer routing no longer agree")
	}

	// The socket survived: a request sent after the burst is forwarded, and it evicts the next
	// oldest — which the client is told about under the id it asked with.
	sendRequest(t, client, 100)
	waitUntil(t, func() bool { return len(mgr.forwarded("textDocument/hover")) == total+1 },
		"the request sent after the burst forwarded")
	next := client.receiveJSON(t)
	if next.Error == nil || next.ID == nil || next.ID.String() != "9" {
		t.Fatalf("the request after the burst evicted %+v, want the oldest outstanding (id 9)", next)
	}
}

// TestAnAnswerFreesASlotUnderTheBound: the bound counts requests waiting for an answer, so a
// session whose server does answer is never made to pay for the bound.
func TestAnAnswerFreesASlotUnderTheBound(t *testing.T) {
	mgr := newDeafManager()
	handler := NewLSPHandler(newTestRegistry(), mgr)
	handler.maxPendingReqs = 2
	defer handler.Close()

	url := startTestServer(t, handler)
	client := subscribeClientDefault(t, url, "/tmp/freed-project", "main.go")

	sendRequest(t, client, 1)
	sendRequest(t, client, 2)
	waitUntil(t, func() bool { return len(mgr.forwarded("textDocument/hover")) == 2 },
		"both requests forwarded")

	// Requests reach the server concurrently, so the order it recorded them in says nothing about
	// who sent first. Backend ids ascend in the order the handler took them, so the smallest is the
	// one the browser sent first — id 1.
	serverIDs := mgr.forwarded("textDocument/hover")
	first := serverIDs[0]
	for _, id := range serverIDs[1:] {
		if id < first {
			first = id
		}
	}
	mgr.answer(first)
	if got := client.receiveJSON(t); got.ID == nil || got.ID.String() != "1" || got.Error != nil {
		t.Fatalf("the answer for request 1 arrived as %+v", got)
	}
	waitUntil(t, func() bool { return outstanding(loneSession(t, handler)) == 1 },
		"the answered request's slot freed")

	sendRequest(t, client, 3)
	waitUntil(t, func() bool { return len(mgr.forwarded("textDocument/hover")) == 3 },
		"the third request forwarded")

	// Nothing was dropped to make room: the socket is quiet. Last assertion because a read that
	// runs out of its own deadline hangs up the connection.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, _, err := client.conn.Read(ctx); err == nil {
		t.Fatal("a request that fit under the bound dropped another one anyway")
	}
}

// TestQueuedInitializesStopAtTheirBound: initialize requests that arrive while one is in flight
// queue behind it. Past the bound the whole queue is failed — the same way a send failure fails it
// — so a later initialize is forwarded instead of piling behind a request nobody answers.
func TestQueuedInitializesStopAtTheirBound(t *testing.T) {
	mgr := newDeafManager()
	handler := NewLSPHandler(newTestRegistry(), mgr)
	handler.maxQueuedInit = 2
	defer handler.Close()

	url := startTestServer(t, handler)
	client := subscribeClientDefault(t, url, "/tmp/queued-project", "main.go")

	sendInitialize(t, client, 1) // the one in flight: never answered
	waitUntil(t, func() bool { return len(mgr.forwarded("initialize")) == 1 },
		"the first initialize forwarded")
	sendInitialize(t, client, 2)
	sendInitialize(t, client, 3) // the bound
	sendInitialize(t, client, 4) // over it

	answered := map[string]bool{}
	for i := 0; i < 3; i++ {
		msg := client.receiveJSON(t)
		if msg.Error == nil || msg.ID == nil {
			t.Fatalf("an initialize left behind by the bound was answered as %+v", msg)
		}
		answered[msg.ID.String()] = true
	}
	for _, id := range []string{"2", "3", "4"} {
		if !answered[id] {
			t.Fatalf("initialize id=%s was left waiting: %+v", id, answered)
		}
	}

	sendInitialize(t, client, 5)
	waitUntil(t, func() bool { return len(mgr.forwarded("initialize")) == 2 },
		"a later initialize forwarded instead of queueing behind the unanswered one")
}

// TestSessionsStopAtTheAdmissionCap: past the cap the upgrade is refused with a 503, so the browser
// sees a refusal rather than a connection it has to work out is dead.
func TestSessionsStopAtTheAdmissionCap(t *testing.T) {
	handler := NewLSPHandler(newTestRegistry(), newDeafManager())
	handler.maxSessions = 1
	defer handler.Close()

	url := startTestServer(t, handler)
	first := connectClient(t, url)
	defer first.conn.Close(websocket.StatusNormalClosure, "done")

	waitUntil(t, func() bool { return liveSessions(handler) == 1 }, "the first session registered")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if conn, _, err := websocket.Dial(ctx, url+"/api/lsp", nil); err == nil {
		conn.CloseNow()
		t.Fatal("a second session was accepted at the cap of 1")
	} else if !strings.Contains(err.Error(), "503") {
		t.Fatalf("the second session failed for a reason that is not a refusal: %v", err)
	}
	if got := liveSessions(handler); got != 1 {
		t.Fatalf("refusing a session changed the live count to %d", got)
	}
}
