package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"mike/pkg/bridge/protocol"
)

// TestBashCancelRequest verifies that bash_cancel frames are constructed and parsed correctly.
func TestBashCancelRequest(t *testing.T) {
	msgID := uint32(42)
	targetMsgID := uint32(100)

	frame := protocol.NewBashCancelRequest(msgID, targetMsgID)

	if frame.MsgID != msgID {
		t.Errorf("expected MsgID %d, got %d", msgID, frame.MsgID)
	}
	if frame.Type != protocol.TypeRequest {
		t.Errorf("expected TypeRequest, got %v", frame.Type)
	}

	params, err := protocol.ParseBashCancelParams(frame)
	if err != nil {
		t.Fatalf("ParseBashCancelParams: %v", err)
	}
	if params.TargetMsgID != targetMsgID {
		t.Errorf("expected TargetMsgID %d, got %d", targetMsgID, params.TargetMsgID)
	}
}

// TestBashContextCancellation verifies that cancelling the context sends a cancel frame.
// A mock agent reads frames from the client and sends a result back.
func TestBashContextCancellation(t *testing.T) {
	// Pipes: client writes to agentReadPw, agent writes to clientReadPw
	agentReadPr, agentReadPw := io.Pipe()   // client → test (frames sent by client)
	clientReadPr, clientReadPw := io.Pipe() // agent → client (responses)

	client := NewClient(agentReadPw, clientReadPr)

	// Mock agent: read frames from client, capture bash + cancel, send result
	received := make(chan *protocol.Frame, 4)
	go func() {
		for {
			frame, err := protocol.ReadFrame(agentReadPr)
			if err != nil {
				return
			}
			select {
			case received <- frame:
			default:
			}
		}
	}()

	// Cancel the context immediately
	ctx, cancelCtx := context.WithCancel(context.Background())
	cancelCtx()

	holder, err := client.Bash(ctx, "sleep 10", 300)
	if err != nil {
		t.Fatalf("Bash: %v", err)
	}

	// First frame: bash request
	bashFrame := waitForFrame(t, received, "bash request")
	bashReq, err := protocol.ParseRequest(bashFrame)
	if err != nil {
		t.Fatalf("parse bash request: %v", err)
	}
	if bashReq.Method != protocol.MethodBash {
		t.Fatalf("expected MethodBash, got %v", bashReq.Method)
	}
	bashMsgID := bashReq.MsgID

	// Second frame: cancel request
	cancelFrame := waitForFrame(t, received, "cancel request")
	params, err := protocol.ParseBashCancelParams(cancelFrame)
	if err != nil {
		t.Fatalf("parse cancel: %v", err)
	}
	if params.TargetMsgID != bashMsgID {
		t.Errorf("cancel targets MsgID %d, want %d", params.TargetMsgID, bashMsgID)
	}

	// Send result so client goroutine exits
	result := protocol.NewResult(bashMsgID, protocol.NewBashResult(137, []byte(`{"cancelled":true}`)))
	_ = protocol.WriteFrame(clientReadPw, result)

	// Drain the holder
	exitCode := <-holder.ExitCode()
	if exitCode != 137 {
		t.Errorf("exit code = %d, want 137", exitCode)
	}
	client.Close()
}

// TestIsAliveFalseAfterClose verifies that IsAlive() returns false as soon as
// Close() marks the client closed — not only after the readLoop goroutine
// notices the closed pipe and exits.
//
// This is the root cause of persistent "read: client closed" tool failures:
// SandboxManager.GetClient and recoverBridge use IsAlive() to decide whether a
// stored client can be reused. If IsAlive() returns true for a closed client,
// recovery "succeeds" by swapping in the same closed client, the retry fails,
// and every tool call reports "Error: read: client closed".
func TestIsAliveFalseAfterClose(t *testing.T) {
	agentReadPr, agentReadPw := io.Pipe()   // client → agent
	clientReadPr, clientReadPw := io.Pipe() // agent → client
	defer clientReadPw.Close()

	client := NewClient(agentReadPw, clientReadPr)
	if !client.IsAlive() {
		t.Fatal("expected IsAlive() true before Close")
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Immediately after Close the client must report not alive, even though the
	// readLoop goroutine may not have exited yet.
	if client.IsAlive() {
		t.Fatal("IsAlive() returned true after Close — client is closed and unusable")
	}

	// Every request against the closed client fails fast with "client closed".
	if _, _, err := client.ReadFile(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "client closed") {
		t.Fatalf("ReadFile on closed client: err=%v, want 'client closed'", err)
	}
	_ = agentReadPr.Close()
}

// TestReadFileFailsFastWhenReadLoopExits verifies that a request made after the
// readLoop has exited returns "connection closed" immediately instead of
// hanging forever. This lets executeTool trigger bridge recovery rather than
// stalling the whole LLM turn.
func TestReadFileFailsFastWhenReadLoopExits(t *testing.T) {
	agentReadPr, agentReadPw := io.Pipe()   // client → agent
	clientReadPr, clientReadPw := io.Pipe() // agent → client

	client := NewClient(agentReadPw, clientReadPr)
	defer client.Close()

	// Close the agent → client pipe so the readLoop exits (simulates the bridge
	// dying without Client.Close() being called).
	clientReadPw.Close()

	// Wait for the readLoop to actually exit.
	deadline := time.Now().Add(5 * time.Second)
	for client.IsAlive() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if client.IsAlive() {
		t.Fatal("readLoop did not exit after pipe close")
	}

	// The request must fail fast, well before the context deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, _, err := client.ReadFile(ctx, "x")
	if err == nil {
		t.Fatal("expected error from ReadFile on dead connection")
	}
	if !strings.Contains(err.Error(), "connection closed") {
		t.Fatalf("unexpected error: %v, want 'connection closed'", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("ReadFile did not fail fast: took %v", elapsed)
	}
	_ = agentReadPr.Close()
}

// TestPing verifies the liveness probe: a responsive bridge answers (even with
// an error frame) so Ping succeeds; a dead connection fails quickly.
func TestPing(t *testing.T) {
	t.Run("responsive bridge", func(t *testing.T) {
		agentReadPr, agentReadPw := io.Pipe()   // client → agent
		clientReadPr, clientReadPw := io.Pipe() // agent → client
		defer agentReadPr.Close()
		defer clientReadPw.Close()

		client := NewClient(agentReadPw, clientReadPr)
		defer client.Close()

		// Mock bridge: reply to every request with an error frame (proving the
		// transport works — a TypeError still counts as a successful ping).
		go func() {
			for {
				frame, err := protocol.ReadFrame(agentReadPr)
				if err != nil {
					return
				}
				_ = protocol.WriteFrame(clientReadPw, protocol.NewError(frame.MsgID, "probe error"))
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := client.Ping(ctx); err != nil {
			t.Fatalf("Ping on responsive bridge: %v", err)
		}
	})

	t.Run("dead connection", func(t *testing.T) {
		agentReadPr, agentReadPw := io.Pipe()   // client → agent
		clientReadPr, clientReadPw := io.Pipe() // agent → client

		client := NewClient(agentReadPw, clientReadPr)
		defer client.Close()

		// Kill the readLoop by closing the agent → client pipe.
		clientReadPw.Close()
		deadline := time.Now().Add(5 * time.Second)
		for client.IsAlive() && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if client.IsAlive() {
			t.Fatal("readLoop did not exit after pipe close")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		start := time.Now()
		if err := client.Ping(ctx); err == nil {
			t.Fatal("expected Ping to fail on dead connection")
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("Ping did not fail fast: took %v", elapsed)
		}
		_ = agentReadPr.Close()
	})
}

// TestConcurrentWritesNoCorruption verifies that writeFrame serializes concurrent
// frame writes. Payloads here are larger than PIPE_BUF, so one frame spans
// several Write calls on the underlying pipe; without the write lock two
// goroutines would interleave bytes and corrupt both frames.
//
// Frames are sent as bash requests (JSON params), which ParseRequest also
// validates — a torn frame fails JSON parsing before the payload check, so
// corruption is caught even earlier.
func TestConcurrentWritesNoCorruption(t *testing.T) {
	agentReadPr, agentReadPw := io.Pipe() // client → reader (frames written by client)
	clientReadPr, _ := io.Pipe()          // reader → client (responses; unused here)

	client := NewClient(agentReadPw, clientReadPr)
	defer client.Close()

	const goroutines = 16
	const payloadSize = 8 * 1024 // larger than PIPE_BUF so frames span multiple Write calls

	seen := make(map[string]bool)
	var seenMu sync.Mutex
	var wg sync.WaitGroup

	// Reader: parse every frame and record which payloads arrived intact.
	recvErr := make(chan error, 1)
	go func() {
		for {
			frame, err := protocol.ReadFrame(agentReadPr)
			if err != nil {
				return // pipe closed
			}
			req, err := protocol.ParseRequest(frame)
			if err != nil {
				recvErr <- fmt.Errorf("corrupted/parse error frame: %v", err)
				return
			}
			var p struct {
				Payload string `json:"payload"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				recvErr <- fmt.Errorf("corrupted params frame: %v", err)
				return
			}
			seenMu.Lock()
			seen[p.Payload] = true
			seenMu.Unlock()
		}
	}()

	// Fire all writers concurrently from distinct goroutines.
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			payload := strings.Repeat(string(rune('a'+i%26)), payloadSize)
			body, err := json.Marshal(map[string]string{"payload": payload})
			if err != nil {
				return
			}
			frame := protocol.NewRequest(uint32(i+1), protocol.MethodBash, body)
			_ = client.writeFrame(frame)
		}(i)
	}
	close(start)
	wg.Wait()

	// Give the reader a moment to drain the pipe.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-recvErr:
			t.Fatalf("reader reported corruption: %v", err)
		default:
		}
		seenMu.Lock()
		n := len(seen)
		seenMu.Unlock()
		if n >= goroutines {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	seenMu.Lock()
	defer seenMu.Unlock()
	if len(seen) != goroutines {
		t.Fatalf("received %d/%d intact frames; protocol corrupted or frames lost", len(seen), goroutines)
	}

	_ = agentReadPw.Close() // end the reader goroutine
}

// TestReadFileCancelDuringChunkFlood reproduces a "panic: send on closed
// channel" race in the client readLoop. When a request's context is cancelled
// (or times out) while the bridge is still streaming chunk frames, waitForCtx
// runs cleanup(), which closes the pending request's chunk channel while
// holding c.mu. The readLoop, however, dispatched earlier chunks after
// releasing c.mu — the lookup of the pending request and the send to its
// chunkCh were not atomic with cleanup()'s close, so the send could land on a
// closed channel.
//
// The mock agent floods many chunks and the context is cancelled mid-flood,
// forcing cleanup() to close chunkCh while the readLoop is still dispatching.
// Under -race this fails on the old code with a DATA RACE; without -race the
// flood+iteration count makes the send-on-closed-channel panic likely.
func TestReadFileCancelDuringChunkFlood(t *testing.T) {
	const iterations = 400
	const chunksPerFlood = 4000

	for iter := 0; iter < iterations; iter++ {
		agentReadPr, agentReadPw := io.Pipe()   // client → agent
		clientReadPr, clientReadPw := io.Pipe() // agent → client

		client := NewClient(agentReadPw, clientReadPr)

		// Mock agent: reply to each request by flooding chunks then a result.
		go func() {
			for {
				frame, err := protocol.ReadFrame(agentReadPr)
				if err != nil {
					return
				}
				msgID := frame.MsgID
				for i := 0; i < chunksPerFlood; i++ {
					if err := protocol.WriteFrame(clientReadPw, protocol.NewChunk(msgID, []byte("x"))); err != nil {
						return
					}
				}
				if err := protocol.WriteFrame(clientReadPw, protocol.NewResult(msgID, protocol.NewUint64Result(uint64(len("x"))*chunksPerFlood))); err != nil {
					return
				}
			}
		}()

		// Cancel the context shortly after the request is sent, mid-flood, so
		// waitForCtx runs cleanup() while the readLoop is still dispatching
		// chunks for the same request.
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			// The package source, not a local *rand.Rand: one is shared by every iteration's
			// goroutine, and two of them overlap once a ReadFile returns early.
			time.Sleep(time.Duration(rand.Intn(2000)) * time.Microsecond)
			cancel()
		}()

		// Must not panic. Either result is acceptable: the request can succeed
		// if the result wins the race, or fail with the cancellation error.
		_, _, _ = client.ReadFile(ctx, "x")

		client.Close()
		_ = agentReadPr.Close()
		_ = clientReadPw.Close()
	}
}

func waitForFrame(t *testing.T, ch <-chan *protocol.Frame, label string) *protocol.Frame {
	t.Helper()
	select {
	case frame, ok := <-ch:
		if !ok {
			t.Fatalf("channel closed waiting for %s", label)
		}
		return frame
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
	}
	return nil // unreachable
}

// withShortLSPRoundTrip shrinks the client-side LSP floor for one test.
func withShortLSPRoundTrip(t *testing.T, d time.Duration) {
	t.Helper()
	prev := maxLSPRoundTrip
	maxLSPRoundTrip = d
	t.Cleanup(func() { maxLSPRoundTrip = prev })
}

// wedgedBridge returns a client whose peer neither reads nor writes, so no send can finish.
func wedgedBridge(t *testing.T) *Client {
	t.Helper()
	unread, clientWrite := io.Pipe() // never drained
	bridgeRead, _ := io.Pipe()       // never written
	t.Cleanup(func() {
		_ = unread.Close()
		_ = bridgeRead.Close()
	})
	return NewClient(clientWrite, bridgeRead)
}

// TestLSPNotifyAbandonsWedgedWrite: LSPNotify honours its context, so a bridge that stops
// reading cannot park the task's executor goroutine.
func TestLSPNotifyAbandonsWedgedWrite(t *testing.T) {
	withShortLSPRoundTrip(t, 50*time.Millisecond)
	client := wedgedBridge(t)

	raw := json.RawMessage(`{"jsonrpc":"2.0","method":"textDocument/didOpen"}`)
	call := make(chan error, 1)
	go func() { call <- client.LSPNotify(context.Background(), raw) }()

	var err error
	select {
	case err = <-call:
	case <-time.After(5 * time.Second):
		t.Fatal("LSPNotify never returned: a wedged bridge parks the caller forever")
	}

	if !errors.Is(err, ErrWriteStalled) {
		t.Errorf("LSPNotify() error = %v, want it to wrap ErrWriteStalled", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("LSPNotify() error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	// It must not close itself: see writeFrameCtx.
	if !client.IsAlive() {
		t.Error("IsAlive() = false after a stalled write; the client must not close itself")
	}
}

// TestLSPNotifyCancellationKeepsClientUsable: a caller walking away is not a broken bridge.
func TestLSPNotifyCancellationKeepsClientUsable(t *testing.T) {
	prev := maxLSPRoundTrip
	maxLSPRoundTrip = time.Hour // so only cancellation can end this
	t.Cleanup(func() { maxLSPRoundTrip = prev })
	client := wedgedBridge(t)

	ctx, cancel := context.WithCancel(context.Background())
	call := make(chan error, 1)
	go func() { call <- client.LSPNotify(ctx, json.RawMessage(`{"jsonrpc":"2.0","method":"x"}`)) }()

	time.Sleep(50 * time.Millisecond) // let the write get stuck
	cancel()

	var err error
	select {
	case err = <-call:
	case <-time.After(5 * time.Second):
		t.Fatal("LSPNotify ignored cancellation")
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("LSPNotify() error = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrWriteStalled) {
		t.Error("a cancelled caller was reported as ErrWriteStalled; that error means the bridge stopped reading")
	}
	if !client.IsAlive() {
		t.Error("IsAlive() = false after the caller cancelled; cancellation does not retire the bridge")
	}
}

// TestLSPStartBoundedWithoutCallerDeadline: the client imposes its own round-trip floor, so a
// bridge that never answers is bounded even when the caller passed no deadline.
func TestLSPStartBoundedWithoutCallerDeadline(t *testing.T) {
	withShortLSPRoundTrip(t, 100*time.Millisecond)

	agentRead, clientWrite := io.Pipe() // read by the peer, never answered
	bridgeRead, bridgeWrite := io.Pipe()
	client := NewClient(clientWrite, bridgeRead)
	t.Cleanup(func() {
		client.Close()
		_ = agentRead.Close()
		_ = bridgeWrite.Close()
	})
	go func() {
		for {
			if _, err := protocol.ReadFrame(agentRead); err != nil {
				return
			}
		}
	}()

	call := make(chan error, 1)
	go func() {
		call <- client.LSPStart(context.Background(), "typescript-language-server", nil)
	}()

	var err error
	select {
	case err = <-call:
	case <-time.After(5 * time.Second):
		t.Fatal("LSPStart(context.Background()) waited forever on a silent bridge")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("LSPStart() error = %v, want it to wrap context.DeadlineExceeded", err)
	}
}

// TestLSPCallCtx: a tighter caller deadline survives, a looser or absent one gets the floor.
func TestLSPCallCtx(t *testing.T) {
	withShortLSPRoundTrip(t, time.Minute)

	t.Run("tighter caller deadline wins", func(t *testing.T) {
		ctx, cancel := lspCallCtx(deadlineCtx(t, 10*time.Millisecond))
		defer cancel()
		dl, ok := ctx.Deadline()
		if !ok || time.Until(dl) > 50*time.Millisecond {
			t.Errorf("deadline = %v (ok=%v), want the caller's ~10ms", time.Until(dl), ok)
		}
	})

	t.Run("no deadline gets the floor", func(t *testing.T) {
		ctx, cancel := lspCallCtx(context.Background())
		defer cancel()
		dl, ok := ctx.Deadline()
		if !ok {
			t.Fatal("no deadline; an unbounded caller could hang forever")
		}
		if left := time.Until(dl); left > time.Minute || left < 500*time.Millisecond {
			t.Errorf("deadline = %v, want ~%v", left, maxLSPRoundTrip)
		}
	})

	t.Run("looser caller deadline is pulled in", func(t *testing.T) {
		ctx, cancel := lspCallCtx(deadlineCtx(t, time.Hour))
		defer cancel()
		if dl, _ := ctx.Deadline(); time.Until(dl) > time.Minute {
			t.Errorf("deadline = %v, want the %v floor", time.Until(dl), maxLSPRoundTrip)
		}
	})
}

// deadlineCtx returns a context with a deadline, cancelled on cleanup.
func deadlineCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
