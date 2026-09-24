package toolresult

import (
	"bytes"
	"strconv"
	"sync"
)

// MaxCaptureBytes bounds how many bytes of a command's output this process keeps. It is not
// Truncate, which runs after the whole output is already in memory: a tool command writes
// whatever it likes, so the capture itself has to stop somewhere.
const MaxCaptureBytes = 1 << 20

// CaptureMarker is appended once when output was dropped; a silent cut reads as "that was
// all the command printed".
func CaptureMarker(dropped int) string {
	return "\n… (output truncated at " + strconv.Itoa(MaxCaptureBytes) + " bytes; " +
		strconv.Itoa(dropped) + " more bytes dropped)"
}

// Capture accumulates a command's output up to MaxCaptureBytes and implements io.Writer, so
// it goes straight into exec.Cmd.Stdout/Stderr. Write always reports the full length and a
// nil error: a short count makes io.Copy stop and lose the rest. The mutex is for callers
// that hand one Capture to both copiers — os/exec guarantees a single goroutine only when
// Stdout and Stderr are the same writer object.
type Capture struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	dropped int
}

func NewCapture() *Capture { return &Capture{} }

func (c *Capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	room := MaxCaptureBytes - c.buf.Len()
	switch {
	case room <= 0:
		c.dropped += len(p)
	case len(p) <= room:
		c.buf.Write(p)
	default:
		c.buf.Write(p[:room])
		c.dropped += len(p) - room
	}
	return len(p), nil
}

// String returns what was captured, with the truncation marker when bytes were dropped.
func (c *Capture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dropped == 0 {
		return c.buf.String()
	}
	return c.buf.String() + CaptureMarker(c.dropped)
}

// Truncated reports whether bytes were dropped and how many. String carries the marker a person
// reads; this is the same fact without parsing that sentence.
func (c *Capture) Truncated() (bool, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped > 0, c.dropped
}
