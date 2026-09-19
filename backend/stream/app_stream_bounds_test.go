package stream

// What one client may make the server hold: connections, subscriptions, and the bytes a
// replay buffer keeps.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mike/backend/types"

	"github.com/coder/websocket"
)

// TestRingBufferHoldsItsByteBudgetNotJustItsFrameCount: a frame count is not a byte bound.
func TestRingBufferHoldsItsByteBudgetNotJustItsFrameCount(t *testing.T) {
	asm := NewAppStreamManager(nil, nil)
	asm.clientBufBound = 4096
	t.Cleanup(asm.Close)

	dialStream(t, asm)
	client := clientAlone(t, asm)

	frame := bytes.Repeat([]byte("x"), 1000)
	for i := 0; i < 50; i++ {
		asm.bufferRaw(frame)
	}

	client.bufMu.Lock()
	frames, held := len(client.ringBuffer), client.bufBytes
	client.bufMu.Unlock()

	if held > asm.clientBufBound {
		t.Fatalf("one client's replay buffer holds %d bytes, over its %d budget", held, asm.clientBufBound)
	}
	if frames >= 50 {
		t.Fatalf("all %d buffered frames survived a %d byte budget", frames, asm.clientBufBound)
	}
}

// TestTaskReplayBuffersStayInsideTheirByteAndKeyLimits: a task that streams forever trims
// its buffer from the oldest end, and the number of buffers is capped, oldest giving way.
func TestTaskReplayBuffersStayInsideTheirByteAndKeyLimits(t *testing.T) {
	asm := NewAppStreamManager(nil, nil)
	asm.taskBufBound = 4096
	asm.taskBufKeys = 3
	t.Cleanup(asm.Close)

	for i := 0; i < 20; i++ {
		asm.bufferTask("proj:t0", bytes.Repeat([]byte("y"), 1000))
	}
	asm.taskBufMu.Lock()
	held := asm.taskBufLoad["proj:t0"]
	asm.taskBufMu.Unlock()
	if held > asm.taskBufBound {
		t.Fatalf("one task's replay buffer holds %d bytes, over its %d budget", held, asm.taskBufBound)
	}

	for i := 1; i <= 5; i++ {
		asm.bufferTask(fmt.Sprintf("proj:t%d", i), []byte(`{"type":"task_event"}`))
	}
	asm.taskBufMu.Lock()
	keys := len(asm.taskBuffers)
	_, oldestOpen := asm.taskBuffers["proj:t0"]
	_, newestOpen := asm.taskBuffers["proj:t5"]
	asm.taskBufMu.Unlock()

	if keys > asm.taskBufKeys {
		t.Fatalf("%d task buffers open, over the cap of %d", keys, asm.taskBufKeys)
	}
	if oldestOpen {
		t.Fatal("the oldest task buffer survived the cap")
	}
	if !newestOpen {
		t.Fatal("the newest task was not buffered")
	}
}

// TestClosingATaskBufferTakesItsBookkeepingWithIt: the eviction order holds exactly the
// buffers that are open, not one entry per task the server has ever run.
func TestClosingATaskBufferTakesItsBookkeepingWithIt(t *testing.T) {
	asm := NewAppStreamManager(nil, nil)
	t.Cleanup(asm.Close)

	const turns = 4 * DefaultTaskBufferKeys
	for i := 0; i < turns; i++ {
		key := fmt.Sprintf("proj:t%d", i)
		asm.bufferTask(key, []byte(`{"type":"task_event"}`))
		asm.clearTaskBuffer(key)
	}

	asm.taskBufMu.Lock()
	open, ordered, loaded := len(asm.taskBuffers), len(asm.taskBufOrder), len(asm.taskBufLoad)
	asm.taskBufMu.Unlock()

	if ordered != open {
		t.Fatalf("%d eviction-order entries for %d open task buffers", ordered, open)
	}
	if loaded != open {
		t.Fatalf("%d byte counters for %d open task buffers", loaded, open)
	}
}

// TestSubscriptionsStopAtThePerConnectionCap: subscription keys arrive as text, so an
// uncapped map is a client choosing how much the server holds for it.
func TestSubscriptionsStopAtThePerConnectionCap(t *testing.T) {
	_, s, _, wm, _ := setupDoneSnapshotTest(t)
	asm := NewAppStreamManager(s, wm)
	t.Cleanup(asm.Close)
	asm.maxTaskSubs = 2

	const projectID = "proj-1"
	// Warm frames are buffered before anyone subscribes: a replay is what proves a
	// subscription was applied.
	for _, taskID := range []string{"task-1", "task-2", "task-3"} {
		asm.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": "warm-" + taskID}})
	}

	c := dialStream(t, asm)
	for _, taskID := range []string{"task-1", "task-2", "task-3"} {
		c.sendSubscription(t, "subscribe_task", projectID, taskID)
	}
	// One drain for both inside-cap replays: each is written by its own goroutine, so which
	// reaches the wire first is not decided, and an await for one of them would consume the
	// other out of the channel.
	if counts := c.tally(2*time.Second,
		isTaskEventFor("task-1", "warm-task-1"),
		isTaskEventFor("task-2", "warm-task-2")); counts[0] != 1 || counts[1] != 1 {
		t.Fatalf("replays for the two subscriptions inside the cap: task-1=%d task-2=%d, want one of each",
			counts[0], counts[1])
	}

	// The read loop is sequential, so the initial-data burst that closes subscribe_project
	// proves the third subscribe_task has already been decided either way.
	c.sendSubscription(t, "subscribe_project", projectID, "")
	c.await(t, 5*time.Second, isType("project_branch"), 1, "project subscriber initial data")
	c.quietWindow(t, time.Second, isTaskEventFor("task-3", "warm-task-3"), "replay for a subscription past the cap")

	asm.SendTaskEvent(projectID, "task-3", types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": "after-cap"}})
	c.quietWindow(t, time.Second, isTaskEventFor("task-3", "after-cap"), "live events for a subscription past the cap")

	asm.SendTaskEvent(projectID, "task-1", types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": "inside-cap"}})
	c.await(t, 5*time.Second, isTaskEventFor("task-1", "inside-cap"), 1, "a subscription inside the cap is still served")
}

// TestStreamRefusesConnectionsBeyondItsCap: past the cap the upgrade is refused with a 503,
// so the browser sees a refusal rather than a connection it has to notice is dead.
func TestStreamRefusesConnectionsBeyondItsCap(t *testing.T) {
	asm := NewAppStreamManager(nil, nil)
	asm.maxConns = 1
	t.Cleanup(asm.Close)
	srv := httptest.NewServer(http.HandlerFunc(asm.HandleStream))
	t.Cleanup(srv.Close)

	dialSilent(t, srv.URL, "")
	clientAlone(t, asm)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/?second"
	if conn, _, err := websocket.Dial(ctx, wsURL, nil); err == nil {
		conn.CloseNow()
		t.Fatal("a second connection was accepted at the cap of 1")
	} else if !strings.Contains(err.Error(), "503") {
		t.Fatalf("the second connection failed for a reason that is not a refusal: %v", err)
	}
}

// TestConcurrentHandshakesDoNotOvershootTheConnectionCap: the cap is a bound on registered
// connections, so counting them and joining the map is one decision. A count read before the
// handshake and an insert after it lets every connection that arrives during the gap in.
func TestConcurrentHandshakesDoNotOvershootTheConnectionCap(t *testing.T) {
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.maxConns = 1
	t.Cleanup(asm.Close)
	srv := httptest.NewServer(http.HandlerFunc(asm.HandleStream))
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	const attempts = 64
	// A connection the manager registered is told the project counts the moment it arrives; one
	// refused past the cap is closed instead, so its first read fails.
	live := make([]bool, attempts)
	start := make(chan struct{})
	resolved := make(chan struct{}, attempts)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			<-start
			conn, _, err := websocket.Dial(ctx, wsURL+"/?i="+strconv.Itoa(i), nil)
			if err == nil {
				defer conn.CloseNow()
				_, _, err = conn.Read(ctx)
			}
			live[i] = err == nil
			resolved <- struct{}{}
			// A live connection is held until the count has been taken: one that closes hands its
			// slot to a later handshake, and that admission is legitimate but not what is here.
			select {
			case <-release:
			case <-time.After(10 * time.Second):
			}
		}(i)
	}
	close(start)
	for i := 0; i < attempts; i++ {
		<-resolved
	}

	held := 0
	for _, isLive := range live {
		if isLive {
			held++
		}
	}
	close(release)
	wg.Wait()

	if held != 1 {
		t.Fatalf("%d of %d concurrent handshakes got a live stream connection at the cap of 1", held, attempts)
	}
}
