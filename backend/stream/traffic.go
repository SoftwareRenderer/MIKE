package stream

// Count frames sent to one connection. Counts bytes as written to the socket,
// representing actual bandwidth used rather than estimates.

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"mike/backend/types"
)

// outboundFrame is one frame to be written to a socket and its bucket name.
// The name is taken from the map the sender built, never from the encoded bytes:
// a tool payload can contain the text `"type":"`.
type outboundFrame struct {
	data   []byte
	bucket string
}

// frameBucket determines the bucket name for a frame. A task_event envelope
// carries every per-turn event, so those count by the event inside them;
// every other frame counts by its own type.
func frameBucket(frame map[string]any) string {
	name, _ := frame["type"].(string)
	if name == string(types.WSTypeTaskEvent) {
		if inner, _ := frame["event"].(string); inner != "" {
			return inner
		}
	}
	if name == "" {
		return "untagged"
	}
	return name
}

// trafficLedger counts the frames written to one connection, per bucket.
type trafficLedger struct {
	mu     sync.Mutex
	frames map[string]int
	bytes  map[string]int
}

func newTrafficLedger() *trafficLedger {
	return &trafficLedger{frames: make(map[string]int), bytes: make(map[string]int)}
}

func (l *trafficLedger) record(bucket string, n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.frames[bucket]++
	l.bytes[bucket] += n
}

// totals returns the total frames and bytes written to the connection.
func (l *trafficLedger) totals() (frames, bytes int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, f := range l.frames {
		frames += f
	}
	for _, b := range l.bytes {
		bytes += b
	}
	return frames, bytes
}

// bucketBytes reports the bytes written for one bucket.
func (l *trafficLedger) bucketBytes(bucket string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bytes[bucket]
}

func (l *trafficLedger) bucketFrames(bucket string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.frames[bucket]
}

// report returns a summary of the ledger sorted by bytes descending.
func (l *trafficLedger) report() string {
	buckets := make([]string, 0, len(l.bytes))
	l.mu.Lock()
	for bucket := range l.bytes {
		buckets = append(buckets, bucket)
	}
	l.mu.Unlock()

	sort.Slice(buckets, func(i, j int) bool {
		bi, bj := l.bucketBytes(buckets[i]), l.bucketBytes(buckets[j])
		if bi != bj {
			return bi > bj
		}
		return buckets[i] < buckets[j]
	})

	totalFrames, totalBytes := l.totals()
	parts := make([]string, 0, len(buckets))
	for _, bucket := range buckets {
		parts = append(parts, fmt.Sprintf("%s %d/%s", bucket, l.bucketFrames(bucket), formatBytes(l.bucketBytes(bucket))))
	}
	return fmt.Sprintf("%d frames/%s written [%s]", totalFrames, formatBytes(totalBytes), strings.Join(parts, " "))
}

// formatBytes formats a byte count for readability.
func formatBytes(n int) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.1fK", float64(n)/float64(unit))
}
