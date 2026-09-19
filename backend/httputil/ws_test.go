package httputil

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The keepalive loop keeps idle connections alive and tells the browser the link is
// still up. Both are timing behaviour inside a goroutine — the kind a refactor drops
// silently.

// wsPair accepts one connection on a test server and dials a client against it. It
// returns the server's end and a channel of the text frames the client receives.
//
// Both ends read continuously, as they do in production — see the drain below.
// The dial is same-origin, so AcceptWS's Origin check passes for the right reason.
func wsPair(t *testing.T) (server *websocket.Conn, frames <-chan string) {
	t.Helper()

	connCh := make(chan *websocket.Conn, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, ok := AcceptWS(w, r, "test")
		if !ok {
			return
		}
		connCh <- c
		// Keep the handler open: returning would end the request context and stop the
		// keepalive on its own, making every assertion below vacuous.
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/ws"
	client, _, err := websocket.Dial(dialCtx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { client.Close(websocket.StatusNormalClosure, "done") })

	received := make(chan string, 32)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			_, data, err := client.Read(context.Background())
			if err != nil {
				return
			}
			received <- string(data)
		}
	}()
	t.Cleanup(func() {
		// Unblock the reader so it cannot outlive the test holding the connection.
		_ = client.Close(websocket.StatusNormalClosure, "test over")
		<-readDone
	})

	select {
	case sc := <-connCh:
		// Drain the server's read side. Only Read() delivers a pong, and Ping waits for
		// the reader on the sending side too — without this loop every ping times out and
		// reports a live peer dead. Every production endpoint has this loop already.
		go func() {
			for {
				if _, _, err := sc.Read(context.Background()); err != nil {
					return
				}
			}
		}()
		return sc, received
	case <-dialCtx.Done():
		t.Fatal("server never accepted the connection")
		return nil, nil
	}
}

// nextFrame waits for one frame, failing the test if none arrives.
func nextFrame(t *testing.T, frames <-chan string) string {
	t.Helper()
	select {
	case f := <-frames:
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("no keepalive frame reached the client")
		return ""
	}
}

// expectQuiet asserts nothing arrives for d.
func expectQuiet(t *testing.T, frames <-chan string, d time.Duration) bool {
	t.Helper()
	select {
	case f := <-frames:
		t.Errorf("unexpected frame while expecting silence: %q", f)
		return false
	case <-time.After(d):
		return true
	}
}

// testTick/testTimeout drive the keepalive's injected timings: a tick fast enough to
// keep the test quick, and a ping timeout long enough that hitting one means the peer
// really did stop answering.
const (
	testTick    = 10 * time.Millisecond
	testTimeout = time.Second
)

// TestKeepaliveFrameReachesTheClient: the frame the browser can actually see.
func TestKeepaliveFrameReachesTheClient(t *testing.T) {
	server, frames := wsPair(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	keepalive(ctx, server, testTick, testTimeout, nil, nil)

	if got := nextFrame(t, frames); got != KeepaliveFrame {
		t.Errorf("keepalive frame = %q, want %q", got, KeepaliveFrame)
	}
}

// TestKeepaliveIsSerialisedByWriteMu: while the lock is held, nothing may go out.
func TestKeepaliveIsSerialisedByWriteMu(t *testing.T) {
	server, frames := wsPair(t)

	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	keepalive(ctx, server, testTick, testTimeout, &mu, nil)

	mu.Lock()
	quiet := expectQuiet(t, frames, 200*time.Millisecond)
	mu.Unlock()
	if !quiet {
		return
	}

	// Released, the next tick gets through.
	nextFrame(t, frames)
}

// TestKeepaliveStopsWithTheContext: the pinger must end with the request, or it keeps
// writing on a connection nobody owns.
func TestKeepaliveStopsWithTheContext(t *testing.T) {
	server, frames := wsPair(t)

	ctx, cancel := context.WithCancel(context.Background())
	keepalive(ctx, server, testTick, testTimeout, nil, nil)

	// Prove it was running, then stop it.
	nextFrame(t, frames)
	cancel()

	expectQuiet(t, frames, 300*time.Millisecond)
}

// TestKeepaliveReportsADeadPeer: a peer that went silent never closes the connection,
// so the failed ping has to reach the caller — that callback is what unblocks a read
// waiting on a silent peer.
func TestKeepaliveReportsADeadPeer(t *testing.T) {
	server, _ := wsPair(t)

	// Closing the server's end is the dead-peer case without timing games.
	deadline := time.AfterFunc(3*time.Second, func() { t.Error("closing the connection hung") })
	defer deadline.Stop()
	if err := server.Close(websocket.StatusNormalClosure, "peer gone"); err != nil {
		t.Fatalf("close: %v", err)
	}

	dead := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	keepalive(ctx, server, 200*time.Millisecond, 200*time.Millisecond, nil, func() { close(dead) })

	select {
	case <-dead:
	case <-time.After(3 * time.Second):
		t.Fatal("a failed ping did not reach the caller — the read loop would hang forever")
	}
}

// acceptReader serves AcceptWS and reports each frame's size, or -1 once its read fails.
func acceptReader(t *testing.T) (sizes <-chan int, clientURL string) {
	t.Helper()

	seen := make(chan int, 4)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, ok := AcceptWS(w, r, "test")
		if !ok {
			return
		}
		for {
			_, data, err := c.Read(context.Background())
			if err != nil {
				seen <- -1
				return
			}
			seen <- len(data)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return seen, "ws" + srv.URL[len("http"):] + "/ws"
}

func dialWriter(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { client.CloseNow() })
	return client
}

// TestAcceptWSReadLimitCoversALargeClientFrame: a frame past the read limit closes the
// connection, which the browser reads as a dropped link and reconnects into.
func TestAcceptWSReadLimitCoversALargeClientFrame(t *testing.T) {
	sizes, url := acceptReader(t)
	client := dialWriter(t, url)

	payload := bytes.Repeat([]byte("a"), 64*1024) // past the library default, inside ReadLimit
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case n := <-sizes:
		if n != len(payload) {
			t.Fatalf("server read %d bytes, want %d — the frame was not accepted", n, len(payload))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server never read the frame")
	}
}

// TestAcceptWSReadLimitIsABound says the limit is a number, not absent.
func TestAcceptWSReadLimitIsABound(t *testing.T) {
	sizes, url := acceptReader(t)
	client := dialWriter(t, url)

	payload := bytes.Repeat([]byte("a"), ReadLimit+1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Write(ctx, websocket.MessageText, payload); err != nil {
		return // a write that refuses is the same bound, seen from this end
	}

	select {
	case n := <-sizes:
		if n > 0 {
			t.Fatalf("a frame of %d bytes above ReadLimit was read (limit %d)", n, ReadLimit)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the oversized frame was neither read nor refused")
	}
}
