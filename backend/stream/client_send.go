package stream

// One goroutine per connection is the only writer to its socket besides the keepalive pinger.
// This separates the turn producer from the delivery pace of any single slow browser.

import (
	"context"
	"sync"

	"mike/backend/httputil"

	"github.com/coder/websocket"
	"mike/pkg/logging"
)

// sendQueue holds the frames waiting for one connection. It uses a fixed frame and byte budget,
// dropping the oldest frames first. A smaller budget is reserved for replays to prevent resyncs
// when subscribing to large buffers.
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

// replayBudget sets the budget for a single replay batch.
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

// pushReplay queues frames a subscriber missed. Replays are placed behind existing replays
// and ahead of live frames. Batches exceeding the replay budget are trimmed (oldest first).
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

// fitToBudget trims a batch to fit the budget, ensuring at least one frame survives.
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

// close closes the queue and clears pending frames.
func (q *sendQueue) close() {
	q.mu.Lock()
	q.live, q.replayed, q.bytes = nil, 0, 0
	q.closed = true
	q.mu.Unlock()
}

// trimLocked drops oldest frames until the queue is within bounds, ensuring at least one frame remains.
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

// notify wakes the sender goroutine.
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
	// Schedules the replay budget as half of the total queue capacity.
	client.out.replayBudget(asm.queueFrames/2, asm.queueBytes/2)
	return client
}

// deliver queues a frame for each target. Delivery is asynchronous.
func (asm *AppStreamManager) deliver(targets []*clientState, frame []byte, bucket string) {
	for _, client := range targets {
		client.out.push(frame, bucket)
	}
}

// runSender writes queued frames to the connection. Synchronizes with the keepalive pinger via writeMu.
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

// endConnectionForResync closes a connection that missed frames. This forces the client to
// establish a new connection and resync its state.
func endConnectionForResync(client *clientState, missed int) {
	logging.For(logging.Stream).Warnf("dropped %d frame(s) for a connection that stopped reading; ending it so the client resyncs", missed)
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	_ = client.conn.Close(websocket.StatusTryAgainLater, "too far behind")
}
