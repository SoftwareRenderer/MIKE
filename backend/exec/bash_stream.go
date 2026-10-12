package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"mike/backend/agent"
	"mike/backend/toolresult"
	"mike/pkg/procterm"
)

// Bash timeout bounds. pkg/procterm handles process group killing and output flushing.
const (
	defaultBashTimeoutSec = 5
	maxBashTimeoutSec     = 300
)

// normalizeBashTimeout clamps tool-supplied timeout to the supported range, defaulting when unset.
func normalizeBashTimeout(timeout int64) int64 {
	switch {
	case timeout <= 0:
		return defaultBashTimeoutSec
	case timeout > maxBashTimeoutSec:
		return maxBashTimeoutSec
	default:
		return timeout
	}
}

// streamLineWriter accumulates output and forwards completed lines to onLine. Mu allows
// concurrent writes from stdout and stderr. Total capture is bounded, but onLine is
// unbounded for UI following.
type streamLineWriter struct {
	mu       sync.Mutex
	captured toolresult.Capture
	onLine   func(text string)
	pend     []byte
}

// partialFlushBytes is the maximum buffer size held back waiting for a newline.
const partialFlushBytes = 64 << 10

// Write reports full length to prevent io.Copy from stopping.
func (w *streamLineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.captured.Write(p)
	w.pend = append(w.pend, p...)
	if i := bytes.LastIndexByte(w.pend, '\n'); i >= 0 {
		w.emitLocked(string(w.pend[:i+1]))
		w.pend = append(w.pend[:0:0], w.pend[i+1:]...)
	} else if len(w.pend) >= partialFlushBytes {
		w.emitLocked(string(w.pend))
		w.pend = nil
	}
	return len(p), nil
}

// WriteFrom streams a pipe into the writer until EOF or procterm.Drain closes the fd.
func (w *streamLineWriter) WriteFrom(r io.Reader) {
	_, _ = io.Copy(w, r)
}

// String returns captured output, including a marker if truncated.
func (w *streamLineWriter) String() string { return w.captured.String() }

// flush emits trailing partial lines.
func (w *streamLineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pend) == 0 {
		return
	}
	w.emitLocked(string(w.pend))
	w.pend = nil
}

// emitLocked splits chunk into lines and forwards them, trimming trailing \r.
func (w *streamLineWriter) emitLocked(chunk string) {
	if w.onLine == nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSuffix(chunk, "\n"), "\n") {
		w.onLine(strings.TrimSuffix(line, "\r") + "\n")
	}
}

// execCommandStream runs a shell command on the host in dir and forwards output to onLine.
// Process group is killed on timeout, but detached children continue.
//
// Only allowlisted environment variables are used to prevent leaking secrets and avoid credential prompts.
func execCommandStream(ctx context.Context, dir, command string, timeout int64, onLine func(text string)) (string, error) {
	secs := normalizeBashTimeout(timeout)
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(secs)*time.Second)
	defer cancel()

	c := exec.CommandContext(runCtx, "bash", "-c", command)
	c.Dir = dir
	c.Env = agent.SafeAgentEnv("GIT_TERMINAL_PROMPT=0")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Kill the process group. Error is ignored to let Wait() report the primary exit status.
	c.Cancel = func() error {
		_ = procterm.KillGroup(c.Process)
		return nil
	}

	// Manage pipe ends manually to avoid Wait() blocking on copy goroutines if a child process survives.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	defer stdoutR.Close()
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdoutW.Close()
		return "", fmt.Errorf("stderr pipe: %w", err)
	}
	defer stderrR.Close()
	c.Stdout = stdoutW
	c.Stderr = stderrW

	if err := c.Start(); err != nil {
		stdoutW.Close()
		stderrW.Close()
		return "", fmt.Errorf("start command: %w", err)
	}
	// The child has its own copies; drop ours or EOF never arrives.
	stdoutW.Close()
	stderrW.Close()

	w := &streamLineWriter{onLine: onLine}
	copiers := make(chan struct{}, 2)
	copyStream := func(r *os.File) {
		defer func() { copiers <- struct{}{} }()
		w.WriteFrom(r)
	}
	go copyStream(stdoutR)
	go copyStream(stderrR)

	waitErr := c.Wait() // returns at process exit; the copiers are drained below
	procterm.Drain(copiers, stdoutR, stderrR)
	w.flush()

	out := w.String()
	switch {
	case waitErr == nil:
		return out, nil
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return out, fmt.Errorf("timed out after %ds and was killed — pass a larger timeout (max %ds) if the command needs longer", secs, maxBashTimeoutSec)
	default:
		return out, waitErr
	}
}
