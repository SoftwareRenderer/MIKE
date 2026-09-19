package stream

// Delivery of one frame is bounded, and every writer of a connection gives its lock back: a
// browser tab that stops reading must not cost the broadcast that walked into it.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mike/backend/store"

	"github.com/coder/websocket"
)

// writeTestStore gives BroadcastProjectCounts a store to read, which is what puts a frame
// into a new client's replay buffer.
func writeTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "delivery.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// dialSilent opens a stream connection that never reads, so every write to it backs up.
func dialSilent(t *testing.T, srvURL, query string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srvURL, "http")+query, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

// awaitClientCount waits for the connection count to fall to n, the point at which every
// writeMu a legitimate holder took has come back.
func awaitClientCount(t *testing.T, asm *AppStreamManager, n int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		asm.mu.RLock()
		count := len(asm.clients)
		asm.mu.RUnlock()
		if count <= n {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("%d connection(s) still registered, want %d", count, n)
		case <-time.After(time.Millisecond):
		}
	}
}

// clientAlone returns the single registered client, waiting for it to appear.
func clientAlone(t *testing.T, asm *AppStreamManager) *clientState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		asm.mu.RLock()
		var found *clientState
		if len(asm.clients) == 1 {
			for _, c := range asm.clients {
				found = c
			}
		}
		asm.mu.RUnlock()
		if found != nil {
			return found
		}
		if time.Now().After(deadline) {
			t.Fatal("exactly one registered client never appeared")
		}
		time.Sleep(time.Millisecond)
	}
}

// clientOtherThan returns the registered client that is not the one named.
func clientOtherThan(t *testing.T, asm *AppStreamManager, not *clientState) *clientState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		asm.mu.RLock()
		var found *clientState
		for _, c := range asm.clients {
			if c != not {
				found = c
			}
		}
		asm.mu.RUnlock()
		if found != nil {
			return found
		}
		if time.Now().After(deadline) {
			t.Fatal("a second registered client never appeared")
		}
		time.Sleep(time.Millisecond)
	}
}

// tryLockWithin reports whether mu was free within the window.
func tryLockWithin(mu *sync.Mutex, window time.Duration) bool {
	deadline := time.Now().Add(window)
	for {
		if mu.TryLock() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSyncReplayReleasesTheWriteLockWhenThePeerVanishesMidReplay: a writeMu left locked
// outlives its connection, and a broadcast that had snapshotted that client waits on it forever.
func TestSyncReplayReleasesTheWriteLockWhenThePeerVanishesMidReplay(t *testing.T) {
	asm := NewAppStreamManager(writeTestStore(t), nil)
	t.Cleanup(asm.Close)
	srv := httptest.NewServer(http.HandlerFunc(asm.HandleStream))
	t.Cleanup(srv.Close)

	// A backlog long enough that the replay is still writing when the peer goes; bufferRaw
	// only appends, so filling it never waits on a connection.
	stop := make(chan struct{})
	var fill sync.WaitGroup
	fill.Add(1)
	go func() {
		defer fill.Done()
		frame := []byte(`{"type":"board_refresh","pad":"` + strings.Repeat("a", 200*1024) + `"}`)
		for {
			select {
			case <-stop:
				return
			default:
			}
			asm.bufferRaw(frame)
		}
	}()
	defer func() { close(stop); fill.Wait() }()

	conn := dialSilent(t, srv.URL, "/?sync=1")
	client := clientAlone(t, asm)

	// Nothing was ever read on that socket, so the replay is mid-backlog when the peer vanishes.
	conn.CloseNow()
	awaitClientCount(t, asm, 0)

	// Nothing is connected any more, so only a leaked lock can still be held.
	if !tryLockWithin(&client.writeMu, 2*time.Second) {
		t.Fatal("clientState.writeMu is still locked after the sync=1 replay lost its peer")
	}
	client.writeMu.Unlock()
}

// TestSilentPeerCannotStallTheFanOut: a peer that stopped reading costs it its own queue and
// nothing else — the fan-out that walked into it hands the frame over and returns, and the live
// client on the same fan-out is served by its own sender.
func TestSilentPeerCannotStallTheFanOut(t *testing.T) {
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.writeTimeout = 5 * time.Second
	t.Cleanup(asm.Close)
	srv := httptest.NewServer(http.HandlerFunc(asm.HandleStream))
	t.Cleanup(srv.Close)

	dialSilent(t, srv.URL, "")
	stalledClient := clientAlone(t, asm)

	live := dialStream(t, asm)
	liveClient := clientOtherThan(t, asm, stalledClient)

	big := []byte(`{"type":"board_refresh","pad":"` + strings.Repeat("a", 200*1024) + `"}`)
	// Nothing is read on that socket, so its sender is proved stuck writing once the socket fills.
	awaitStuckSender(t, stalledClient, func() { asm.deliver([]*clientState{stalledClient}, big) })

	probe, _ := json.Marshal(map[string]any{"type": "board_refresh", "probe": "still-here"})
	started := time.Now()
	asm.deliver([]*clientState{stalledClient, liveClient}, probe)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("the fan-out into a stalled peer took %v; it queues and returns", elapsed)
	}
	live.awaitFrame(t, 5*time.Second, func(f map[string]any) bool { return f["probe"] == "still-here" },
		"frame to the live client behind a stalled one in the same fan-out")
}

// awaitStuckSender keeps calling fill until the client's sender is proved to be inside a write:
// writeMu held and staying held. Nothing else in the server waits behind that write, and that is
// what the delivery path has to guarantee.
func awaitStuckSender(t *testing.T, client *clientState, fill func()) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		fill()
		if !tryLockWithin(&client.writeMu, 300*time.Millisecond) {
			return
		}
		client.writeMu.Unlock()
		select {
		case <-deadline.C:
			t.Fatal("this peer never stopped reading, so nothing was ever stuck")
		case <-time.After(time.Millisecond):
		}
	}
}

// TestConcurrentConnectionsDoNotRaceTheClientMap: the accept log reads asm.clients while
// other connections add and remove themselves, which one dial at a time never shows.
func TestConcurrentConnectionsDoNotRaceTheClientMap(t *testing.T) {
	asm := NewAppStreamManager(writeTestStore(t), nil)
	t.Cleanup(asm.Close)
	srv := httptest.NewServer(http.HandlerFunc(asm.HandleStream))
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conn, _, err := websocket.Dial(ctx, wsURL+"?i="+strconv.Itoa(i), nil)
			if err != nil {
				t.Errorf("dial %d: %v", i, err)
				return
			}
			body, _ := json.Marshal(map[string]any{
				"type": "subscribe_project",
				"data": map[string]string{"project_id": "proj-gone"},
			})
			_ = conn.Write(ctx, websocket.MessageText, body)
			conn.CloseNow()
		}(i)
	}
	wg.Wait()
	awaitClientCount(t, asm, 0)
}
