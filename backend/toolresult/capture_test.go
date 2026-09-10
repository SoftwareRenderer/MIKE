package toolresult_test

import (
	"fmt"
	"io"
	"kanban/backend/toolresult"
	"strings"
	"sync"
	"testing"
)

// The capture cap exists because toolresult.Truncate is too late: by the time a result is truncated for
// the LLM the whole output is already in memory. These pin the earlier point.

func TestCaptureKeepsSmallOutputUntouched(t *testing.T) {
	c := toolresult.NewCapture()
	if n, err := c.Write([]byte("hello\n")); err != nil || n != 6 {
		t.Fatalf("Write = (%d, %v), want (6, nil) — a short count stops io.Copy", n, err)
	}
	if got := c.String(); got != "hello\n" {
		t.Errorf("String() = %q, want the bytes unchanged", got)
	}
	if truncated, dropped := c.Truncated(); truncated || dropped != 0 {
		t.Errorf("small output reported truncated (%v, %d)", truncated, dropped)
	}
}

func TestCaptureBoundsAVeryLargeCommand(t *testing.T) {
	c := toolresult.NewCapture()

	// 4× the cap, in chunks the size a pipe copier actually hands over.
	const total = 4 * toolresult.MaxCaptureBytes
	chunk := strings.Repeat("x", 32*1024)
	wantDropped := 0
	for written := 0; written < total; written += len(chunk) {
		n, err := c.Write([]byte(chunk))
		if err != nil || n != len(chunk) {
			t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(chunk))
		}
		wantDropped += len(chunk)
	}
	// Everything past the cap was dropped.
	wantDropped -= toolresult.MaxCaptureBytes

	got := c.String()
	if len(got) > toolresult.MaxCaptureBytes+len(toolresult.CaptureMarker(wantDropped)) {
		t.Errorf("captured string is %d bytes, over the cap plus its marker", len(got))
	}
	if !strings.HasPrefix(got, strings.Repeat("x", 1024)) {
		t.Error("the head of the output is missing — a cap that keeps the tail is a different bug")
	}
	truncated, dropped := c.Truncated()
	if !truncated || dropped != wantDropped {
		t.Errorf("Truncated() = (%v, %d), want (true, %d)", truncated, dropped, wantDropped)
	}
	// The marker names the limit and the amount dropped, so a cut never reads as an ending.
	if !strings.Contains(got, fmt.Sprint(toolresult.MaxCaptureBytes)) || !strings.Contains(got, fmt.Sprint(dropped)) {
		t.Errorf("marker must name the cap and what was dropped: %s", got[len(got)-120:])
	}
	// A capture that stopped is not a command that ended: writes stay accepted (and reported
	// in full) so the copier never aborts.
	if n, err := io.WriteString(c, "still going"); err != nil || n != 11 {
		t.Errorf("write after the cap = (%d, %v)", n, err)
	}
}

func TestCaptureIsSafeUnderTwoCopiers(t *testing.T) {
	// The host bash path wires stdout and stderr to two pipes writing one capture.
	c := toolresult.NewCapture()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = c.Write([]byte(strings.Repeat("y", 4096)))
			}
		}()
	}
	wg.Wait()
	if n := len(c.String()); n > toolresult.MaxCaptureBytes+len(toolresult.CaptureMarker(0))+64 {
		t.Errorf("captured %d bytes under concurrent writers", n)
	}
	if truncated, _ := c.Truncated(); !truncated {
		t.Error("2 MiB of writes must report truncation")
	}
}

// toolresult.Truncate is unchanged by the cap: it still describes what an LLM sees of a result it holds.
func TestTruncateStillIgnoresSmallOutput(t *testing.T) {
	preview, lines, bytes, truncated := toolresult.Truncate("one\ntwo\n")
	if truncated || preview != "one\ntwo\n" || lines != 3 || bytes != 8 {
		t.Fatalf("toolresult.Truncate changed for small output: %q %d %d %v", preview, lines, bytes, truncated)
	}
}
