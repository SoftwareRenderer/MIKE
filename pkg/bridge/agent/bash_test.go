package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"mike/pkg/bridge/protocol"
)

// bashHarness drives handleBash over in-memory pipes and records every frame the
// agent writes back, in wire order — which is exactly what the ordering contract
// under test needs ("all Chunk frames before the Result frame").
type bashHarness struct {
	t       *testing.T
	h       *handler
	toAgent io.WriteCloser
	frames  <-chan *protocol.Frame
}

func newBashHarness(t *testing.T) *bashHarness {
	t.Helper()
	testToAgentR, testToAgentW := io.Pipe()
	agentToTestR, agentToTestW := io.Pipe()

	h := newHandler()
	h.closed.Store(false)

	frames := make(chan *protocol.Frame, 4096)
	go func() {
		defer close(frames)
		for {
			f, err := protocol.ReadFrame(agentToTestR)
			if err != nil {
				return
			}
			frames <- f
		}
	}()
	go func() { _ = h.handleConn(&testPipeConn{reader: testToAgentR, writer: agentToTestW}) }()

	bh := &bashHarness{t: t, h: h, toAgent: testToAgentW, frames: frames}
	t.Cleanup(func() {
		h.close()
		_ = testToAgentW.Close()
		_ = agentToTestR.Close()
	})
	return bh
}

// run sends a bash request.
func (bh *bashHarness) run(msgID uint32, command string, timeout int64) {
	bh.t.Helper()
	params, _ := json.Marshal(protocol.BashParams{Command: command, Timeout: timeout})
	if err := protocol.WriteFrame(bh.toAgent, protocol.NewRequest(msgID, protocol.MethodBash, params)); err != nil {
		bh.t.Fatalf("write bash request: %v", err)
	}
}

// collect reads frames until a Result/Error arrives or the deadline passes.
// Returns the frames in arrival order (Result included) and the elapsed time.
func (bh *bashHarness) collect(deadline time.Duration) []*protocol.Frame {
	bh.t.Helper()
	var out []*protocol.Frame
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		select {
		case f, ok := <-bh.frames:
			if !ok {
				return out
			}
			out = append(out, f)
			if f.Type == protocol.TypeResult || f.Type == protocol.TypeError {
				return out
			}
		case <-timer.C:
			bh.t.Fatalf("no Result frame within %v (got %d frames)", deadline, len(out))
		}
	}
}

// stdoutOf joins the payload of every stdout chunk, in arrival order.
func stdoutOf(t *testing.T, frames []*protocol.Frame) string {
	t.Helper()
	var b strings.Builder
	for _, f := range frames {
		if f.Type != protocol.TypeChunk {
			continue
		}
		c, err := protocol.ParseBashChunk(f.Payload)
		if err != nil {
			t.Fatalf("parse chunk: %v", err)
		}
		if c.Stream == bashStreamStdout {
			b.Write(c.Data)
		}
	}
	return b.String()
}

// resultOf returns the trailing Result frame.
func resultOf(t *testing.T, frames []*protocol.Frame) *protocol.Frame {
	t.Helper()
	if len(frames) == 0 {
		t.Fatal("no frames")
	}
	last := frames[len(frames)-1]
	if last.Type != protocol.TypeResult {
		t.Fatalf("last frame type = %d, want TypeResult (error: %s)", last.Type, last.ErrorMsg())
	}
	return last
}

// Regression: output must be completely flushed before the Result frame.
// cmd.StdoutPipe + cmd.Wait closed the pipes under the readers, so trailing
// output was lost and the card showed a command as done with output missing.
func TestHandleBash_AllOutputFlushedBeforeResult(t *testing.T) {
	bh := newBashHarness(t)
	// ~110 KB: many 32 KB chunk reads, so a truncated tail is impossible to miss.
	const lines = 10000
	bh.run(1, fmt.Sprintf("seq 1 %d", lines), 30)
	frames := bh.collect(20 * time.Second)

	result := resultOf(t, frames)
	br, err := protocol.ParseBashResult(result.Payload)
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if br.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", br.ExitCode)
	}

	wantLines := make([]string, lines)
	for i := range wantLines {
		wantLines[i] = fmt.Sprintf("%d", i+1)
	}
	want := strings.Join(wantLines, "\n") + "\n"
	got := stdoutOf(t, frames)
	if got != want {
		t.Errorf("streamed stdout lost data: got %d bytes, want %d bytes (tail %q)",
			len(got), len(want), tail(want, 40))
	}
	// Nothing may follow the Result frame.
	if frames[len(frames)-1].Type != protocol.TypeResult {
		t.Error("Result frame is not the last frame")
	}
}

// A command that detaches a background child must still finish promptly: the
// child inherits the stdout write end, so a real EOF never comes.
func TestHandleBash_BackgroundChildDoesNotPinHandler(t *testing.T) {
	bh := newBashHarness(t)
	// Short sleep: a detached child legitimately outlives the command (shell semantics),
	// so this only keeps the test environment tidy.
	bh.run(1, "echo ready; sleep 5 &", 30)

	start := time.Now()
	frames := bh.collect(5 * time.Second)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Errorf("Result took %v with a detached child holding stdout; drain must stay bounded", elapsed)
	}
	if !strings.Contains(stdoutOf(t, frames), "ready") {
		t.Errorf("output before the background child was lost: %q", stdoutOf(t, frames))
	}
	br, _ := protocol.ParseBashResult(resultOf(t, frames).Payload)
	if br.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", br.ExitCode)
	}
}

// Timeout must take the whole process group with it — the grandchild is the
// process that lingers (and keeps pipes open) when only bash is killed.
func TestHandleBash_TimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	bh := newBashHarness(t)
	// bash backgrounds a grandchild, records its pid, then waits on it — so bash
	// itself is still alive when the timeout fires.
	cmd := fmt.Sprintf("sleep 60 & echo $! > %s; wait", pidFile)
	bh.run(1, cmd, 1)

	frames := bh.collect(15 * time.Second)
	br, err := protocol.ParseBashResult(resultOf(t, frames).Payload)
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if br.ExitCode != 124 {
		t.Errorf("exit code = %d, want 124 (timeout)", br.ExitCode)
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read grandchild pid file: %v", err)
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil {
		t.Fatalf("parse pid %q: %v", data, err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if processGone(pid) {
			return // grandchild is gone — the group was killed
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild pid %d survived the timeout — only the direct child was killed", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The Result meta must carry real timing (was hardcoded 0).
func TestHandleBash_ResultReportsDuration(t *testing.T) {
	bh := newBashHarness(t)
	bh.run(1, "sleep 0.4", 10)
	frames := bh.collect(15 * time.Second)

	br, err := protocol.ParseBashResult(resultOf(t, frames).Payload)
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	var meta struct {
		DurationMS int64 `json:"duration_ms"`
		Cancelled  bool  `json:"cancelled"`
	}
	if err := json.Unmarshal(br.Meta, &meta); err != nil {
		t.Fatalf("parse meta %q: %v", br.Meta, err)
	}
	if meta.DurationMS < 300 {
		t.Errorf("duration_ms = %d, want >= 300 for `sleep 0.4`", meta.DurationMS)
	}
	if meta.Cancelled {
		t.Error("cancelled = true for a command that ran to completion")
	}
}

// stderr is reported on its own stream and still precedes the Result frame.
func TestHandleBash_StderrStreamedBeforeResult(t *testing.T) {
	bh := newBashHarness(t)
	bh.run(1, "echo to-err >&2; echo to-out", 10)
	frames := bh.collect(15 * time.Second)

	var out, errOut strings.Builder
	for _, f := range frames {
		if f.Type != protocol.TypeChunk {
			continue
		}
		c, err := protocol.ParseBashChunk(f.Payload)
		if err != nil {
			t.Fatalf("parse chunk: %v", err)
		}
		if c.Stream == bashStreamStderr {
			errOut.Write(c.Data)
		} else {
			out.Write(c.Data)
		}
	}
	if !strings.Contains(errOut.String(), "to-err") {
		t.Errorf("stderr not streamed on the stderr stream: %q", errOut.String())
	}
	if !strings.Contains(out.String(), "to-out") {
		t.Errorf("stdout not streamed: %q", out.String())
	}
	br, _ := protocol.ParseBashResult(resultOf(t, frames).Payload)
	if br.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", br.ExitCode)
	}
}

// processGone reports whether pid is no longer running. A killed child whose
// parent is gone is reparented to init — which in a container may be a process
// that never reaps it — so a zombie counts as gone.
func processGone(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		// No procfs (or the entry is already gone) — keep polling on the signal
		// check alone rather than declaring success, so the test stays meaningful.
		return false
	}
	// /proc/pid/stat is "pid (comm) state …" and comm may contain spaces —
	// anchor on the last ')'.
	stat := string(data)
	idx := strings.LastIndex(stat, ") ")
	if idx < 0 || idx+3 >= len(stat) {
		return false
	}
	switch stat[idx+2] {
	case 'Z', 'X', 'x':
		return true
	}
	return false
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
