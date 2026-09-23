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
// A replay gets a smaller budget than the queue it is written into (replayFrame/replayByte): a frame the
// queue discards is a hole in a live stream, while a frame a replay never queues is content the
// subscriber reads from the database seconds later. Without the reservation, subscribing to a task whose
// buffer fills the queue would force the resync the replay is meant to avoid.
//
// Over the bound the OLDEST frames go and never the newest: a client that stopped reading is
// caught up on the tail. Every drop is counted, because a gap the client cannot see is a chat that
// stays wrong until it is reloaded.
type sendQueue struct {
	wake  chan struct{} // buffered 1; signals that the deque is non-empty
	frame int
	byte  int
	// replayFrame and replayByte bound one replay batch.
	replayFrame int
	replayByte  int

	mu       sync.Mutex
	live     []outboundFrame
	replayed int // the leading frames of live, held with mu, that a replay put there
	bytes    int
	dropped  int
	closed   bool
}

func newSendQueue(frameBound, byteBound int) *sendQueue {
	return &sendQueue{
		wake:        make(chan struct{}, 1),
		frame:       frameBound,
		byte:        byteBound,
		replayFrame: frameBound,
		replayByte:  byteBound,
	}
}

// replayBudget narrows what one replay may queue.
func (q *sendQueue) replayBudget(frames, bytes int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if frames > 0 && frames < q.replayFrame {
		q.replayFrame = frames
	}
	if bytes > 0 && bytes < q.replayByte {
		q.replayByte = bytes
	}
}

// push appends a live frame. It never blocks and never waits on the socket.
func (q *sendQueue) push(frame []byte, bucket string) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.live = append(q.live, outboundFrame{data: frame, bucket: bucket})
	q.bytes += len(frame)
	q.trimLocked()
	q.mu.Unlock()
	q.notify()
}

// pushReplay queues the frames a subscriber missed: behind any replay already queued, so replays keep the
// order they were asked for, and ahead of every live frame, so a replay is never interrupted by what the
// task said meanwhile. A batch over the replay budget is trimmed here, oldest first, and the count left
// behind is returned: those frames are not missed, because the subscriber reads the conversation from the
// database right after asking.
func (q *sendQueue) pushReplay(batch []outboundFrame) int {
	if len(batch) == 0 {
		return 0
	}
	q.mu.Lock()
	batch, refused := fitToBudget(batch, q.replayFrame, q.replayByte)
	if q.closed {
		q.mu.Unlock()
		return refused
	}
	merged := make([]outboundFrame, 0, len(q.live)+len(batch))
	merged = append(merged, q.live[:q.replayed]...)
	merged = append(merged, batch...)
	merged = append(merged, q.live[q.replayed:]...)
	q.live = merged
	q.replayed += len(batch)
	for _, frame := range batch {
		q.bytes += len(frame.data)
	}
	q.trimLocked()
	q.mu.Unlock()
	q.notify()
	return refused
}

// fitToBudget trims a replay batch to the replay budget, oldest frames first. One frame always
// survives: a subscriber owed something gets at least the newest of it.
func fitToBudget(batch []outboundFrame, frames, bytes int) ([]outboundFrame, int) {
	total := 0
	for _, f := range batch {
		total += len(f.data)
	}
	refused := 0
	for len(batch) > 1 && (len(batch) > frames || total > bytes) {
		total -= len(batch[0].data)
		batch = batch[1:]
		refused++
	}
	return batch, refused
}

// pop takes the next frame owed.
func (q *sendQueue) pop() (outboundFrame, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.live) == 0 {
		return outboundFrame{}, false
	}
	frame := q.live[0]
	q.live[0] = outboundFrame{}
	q.live = q.live[1:]
	if q.replayed > 0 {
		q.replayed--
	}
	q.bytes -= len(frame.data)
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
		q.live[0] = outboundFrame{}
		q.live = q.live[1:]
		if q.replayed > 0 {
			q.replayed--
		}
		q.bytes -= len(frame.data)
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
	client := &clientState{
		conn:        conn,
		projectSub:  make(map[string]struct{}),
		taskSub:     make(map[string]struct{}),
		noRowsTask:  make(map[string]struct{}),
		initPending: make(map[string]AppStreamSubscription),
		out:         newSendQueue(asm.queueFrames, asm.queueBytes),
		traffic:     newTrafficLedger(),
	}
	// Half the queue stays for live frames; see sendQueue.
	client.out.replayBudget(asm.queueFrames/2, asm.queueBytes/2)
	return client
}

// deliver hands a marshalled frame to each target's queue. Returning here means the frame is
// queued, not written: which client is slow is that client's problem.
func (asm *AppStreamManager) deliver(targets []*clientState, frame []byte, bucket string) {
	for _, client := range targets {
		client.out.push(frame, bucket)
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
			err := httputil.WriteText(ctx, client.conn, frame.data, asm.writeTimeout)
			client.writeMu.Unlock()
			if err != nil {
				return // the peer is gone; its read loop unregisters the client
			}
			client.traffic.record(frame.bucket, len(frame.data))
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
