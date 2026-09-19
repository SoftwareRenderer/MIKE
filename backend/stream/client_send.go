package stream

// One goroutine per connection is the only writer to its socket besides the keepalive pinger: the
// producer of a frame — an executor turn, a watcher, a broadcast — hands it over and returns,
// because a browser that stopped reading may not set the pace for the turn that produced the frame,
// for the other subscribers of it, or for the pinger that holds the same lock.

import (
	"context"
	"sync"

	"mike/backend/httputil"

	"github.com/coder/websocket"
	"mike/pkg/logging"
)

// sendQueue holds the frames waiting for one connection.
//
// Its bound is the replay buffer's — DefaultBufferSize frames and DefaultBufferBytes bytes per
// connection — because an overflowing queue and a truncated replay mean the same thing to the
// client: frames it will never see. maxFrameBufferSize caps one frame, which is what makes the
// frame bound a byte bound as well.
//
// Over the bound the OLDEST frames go and never the newest: a client that stopped reading is
// caught up on the tail. Every drop is counted, because a gap the client cannot see is a chat that
// stays wrong until it is reloaded.
type sendQueue struct {
	wake  chan struct{} // buffered 1; signals that the deque is non-empty
	frame int
	byte  int

	mu       sync.Mutex
	live     [][]byte
	replayed int // the leading frames of live, held with mu, that a replay put there
	bytes    int
	dropped  int
	closed   bool
}

func newSendQueue(frameBound, byteBound int) *sendQueue {
	return &sendQueue{
		wake:  make(chan struct{}, 1),
		frame: frameBound,
		byte:  byteBound,
	}
}

// push appends a live frame. It never blocks and never waits on the socket.
func (q *sendQueue) push(frame []byte) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.live = append(q.live, frame)
	q.bytes += len(frame)
	q.trimLocked()
	q.mu.Unlock()
	q.notify()
}

// pushReplay queues the frames a subscriber missed: behind any replay already queued, so replays
// keep the order they were asked for, and ahead of every live frame, so a replay is never
// interrupted by what the task said meanwhile. The frames the bound cannot hold are the oldest in
// the queue, which is how a truncated replay already reads.
func (q *sendQueue) pushReplay(batch [][]byte) {
	if len(batch) == 0 {
		return
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	merged := make([][]byte, 0, len(q.live)+len(batch))
	merged = append(merged, q.live[:q.replayed]...)
	merged = append(merged, batch...)
	merged = append(merged, q.live[q.replayed:]...)
	q.live = merged
	q.replayed += len(batch)
	for _, frame := range batch {
		q.bytes += len(frame)
	}
	q.trimLocked()
	q.mu.Unlock()
	q.notify()
}

// pop takes the next frame owed.
func (q *sendQueue) pop() ([]byte, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.live) == 0 {
		return nil, false
	}
	frame := q.live[0]
	q.live[0] = nil
	q.live = q.live[1:]
	if q.replayed > 0 {
		q.replayed--
	}
	q.bytes -= len(frame)
	if len(q.live) == 0 {
		q.live = nil // release the array the trimming walked past
	}
	return frame, true
}

// droppedFrames is how many frames this connection missed.
func (q *sendQueue) droppedFrames() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}

// close refuses anything further: the sender has gone, so nothing will drain this again.
func (q *sendQueue) close() {
	q.mu.Lock()
	q.live, q.replayed, q.bytes = nil, 0, 0
	q.closed = true
	q.mu.Unlock()
}

// trimLocked drops from the oldest end until the queue is inside both bounds. One frame always
// survives: dropping the newest frame is never the answer, and one oversized frame is what the
// caller asked to be delivered.
func (q *sendQueue) trimLocked() {
	for len(q.live) > 1 && (len(q.live) > q.frame || q.bytes > q.byte) {
		frame := q.live[0]
		q.live[0] = nil
		q.live = q.live[1:]
		if q.replayed > 0 {
			q.replayed--
		}
		q.bytes -= len(frame)
		q.dropped++
	}
}

// notify wakes the sender. A coalesced signal cannot strand a frame: the sender drains the queue
// before it waits again, so a wake it never saw is one it does not need.
func (q *sendQueue) notify() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// newClientState builds the state HandleStream serves one connection from.
func newClientState(conn *websocket.Conn, asm *AppStreamManager) *clientState {
	return &clientState{
		conn:        conn,
		projectSub:  make(map[string]struct{}),
		taskSub:     make(map[string]struct{}),
		initPending: make(map[string]AppStreamSubscription),
		ringBuffer:  make([][]byte, 0, DefaultBufferSize),
		bufSize:     DefaultBufferSize,
		out:         newSendQueue(asm.queueFrames, asm.queueBytes),
	}
}

// deliver hands a marshalled frame to each target's queue. Returning here means the frame is
// queued, not written: which client is slow is that client's problem.
func (asm *AppStreamManager) deliver(targets []*clientState, frame []byte) {
	for _, client := range targets {
		client.out.push(frame)
	}
}

// runSender writes this connection's queued frames until the connection is over. It is the only
// writer to conn besides the keepalive pinger, which is why writeMu is the two of them's only
// point of contact.
func (asm *AppStreamManager) runSender(ctx context.Context, client *clientState) {
	defer client.out.close()
	for {
		select {
		case <-ctx.Done():
			return
		case <-client.out.wake:
		}
		for {
			frame, ok := client.out.pop()
			if !ok {
				break
			}
			client.writeMu.Lock()
			err := httputil.WriteText(ctx, client.conn, frame, asm.writeTimeout)
			client.writeMu.Unlock()
			if err != nil {
				return // the peer is gone; its read loop unregisters the client
			}
		}
		if missed := client.out.droppedFrames(); missed > 0 {
			endConnectionForResync(client, missed)
			return
		}
	}
}

// endConnectionForResync ends a connection that missed frames, once it has been sent everything
// the queue still held, so what the client did receive is a contiguous tail with the hole behind
// it.
//
// The app has one resync and it belongs to a new connection: onConnected(true) runs silentRefresh()
// and TaskChat.loadHistory(). A wait for this server never gets a deadline, so a client that fell
// behind is released to resync rather than timed out, and rather than left streaming a gap it has
// no way to notice.
func endConnectionForResync(client *clientState, missed int) {
	logging.For(logging.Stream).Warnf("dropped %d frame(s) for a connection that stopped reading; ending it so the client resyncs", missed)
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	_ = client.conn.Close(websocket.StatusTryAgainLater, "too far behind")
}
