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

// Bounds for the tool's bash timeout. Ending the command — killing its group, flushing
// its output — is pkg/procterm, shared with the sandbox-bridge's bash handler.
const (
	defaultBashTimeoutSec = 5
	maxBashTimeoutSec     = 300
)

// normalizeBashTimeout clamps a tool-supplied timeout (seconds) to the supported
// range, defaulting when unset.
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

// streamLineWriter accumulates a command's output and hands each completed line to onLine as it
// arrives. mu lets one writer serve both the stdout and stderr copiers. The accumulator is a
// toolresult.Capture because the bytes come from an LLM-written command; the line stream to the
// browser is not bounded, since the UI follows a running command.
type streamLineWriter struct {
	mu       sync.Mutex
	captured toolresult.Capture
	onLine   func(text string)
	pend     []byte
}

// partialFlushBytes bounds the buffer held back waiting for a newline.
const partialFlushBytes = 64 << 10

// Write always reports the full length: a short count makes io.Copy stop and drop output.
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

// WriteFrom streams a pipe into the writer until EOF, or until procterm.Drain closes the fd
// (the normal end for a command that detached a child still holding it). What was written is
// already in the accumulator and on the line stream, so neither count is reported back.
func (w *streamLineWriter) WriteFrom(r io.Reader) {
	_, _ = io.Copy(w, r)
}

// String returns what was captured — up to the cap, plus the marker naming what was dropped.
func (w *streamLineWriter) String() string { return w.captured.String() }

// flush emits a trailing partial line, so a command that ends without a newline still gets
// its last line reported.
func (w *streamLineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pend) == 0 {
		return
	}
	w.emitLocked(string(w.pend))
	w.pend = nil
}

// emitLocked splits a chunk into lines and forwards them (called with mu held, so lines stay
// in order). Trailing \r is trimmed to match the sandbox path.
func (w *streamLineWriter) emitLocked(chunk string) {
	if w.onLine == nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSuffix(chunk, "\n"), "\n") {
		w.onLine(strings.TrimSuffix(line, "\r") + "\n")
	}
}

// execCommandStream runs a shell command on the host in dir, returns its combined output, and
// forwards it line by line to onLine (may be nil). timeout is seconds: the whole process group
// is killed when it expires, while a child the command detached keeps running, on purpose.
//
// Only the allowlisted environment reaches the command: these are LLM-written commands on the
// host, so MIKE_LOGIN_PASSWORD and MIKE_ENCRYPTION_KEY must never be in their environment, and
// GIT_TERMINAL_PROMPT=0 keeps an injected git transfer off a credential prompt.
func execCommandStream(ctx context.Context, dir, command string, timeout int64, onLine func(text string)) (string, error) {
	secs := normalizeBashTimeout(timeout)
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(secs)*time.Second)
	defer cancel()

	c := exec.CommandContext(runCtx, "bash", "-c", command)
	c.Dir = dir
	c.Env = agent.SafeAgentEnv("GIT_TERMINAL_PROMPT=0")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Kill the group, not just bash. The error is swallowed on purpose: exec turns a Cancel
	// error into the Wait error, hiding the result of a process that had already exited.
	c.Cancel = func() error {
		_ = procterm.KillGroup(c.Process)
		return nil
	}

	// Own the pipe ends. With exec-managed pipes, Wait() blocks on its copy
	// goroutines for the whole WaitDelay whenever a child survives — owning the fds
	// keeps the flush to procterm's grace period.
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
