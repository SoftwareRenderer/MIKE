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

	"kanban/backend/agent"
	"kanban/pkg/procterm"
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

// streamLineWriter accumulates a command's output and hands each completed line
// to onLine as it arrives. mu lets one writer serve both the stdout and stderr
// copiers.
type streamLineWriter struct {
	mu     sync.Mutex
	total  bytes.Buffer
	onLine func(text string)
	pend   []byte
}

func (w *streamLineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Never fails, so always report the full length: a short count would make
	// io.Copy stop and drop output.
	w.total.Write(p)
	w.pend = append(w.pend, p...)
	if i := bytes.LastIndexByte(w.pend, '\n'); i >= 0 {
		w.emitLocked(string(w.pend[:i+1]))
		w.pend = append(w.pend[:0:0], w.pend[i+1:]...)
	}
	return len(p), nil
}

// WriteFrom streams a pipe into the writer until EOF, or until procterm.Drain closes
// the fd (the normal end for a command that detached a child still holding it).
func (w *streamLineWriter) WriteFrom(r io.Reader) (int64, error) {
	return io.Copy(w, r)
}

// String returns everything captured so far.
func (w *streamLineWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.total.String()
}

// flush emits a trailing partial line — a command that ends without a newline
// still gets its last line reported.
func (w *streamLineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pend) == 0 {
		return
	}
	w.emitLocked(string(w.pend))
	w.pend = nil
}

// emitLocked splits a chunk into lines and forwards them (called with mu held, so
// the lines stay in order). Trailing \r is trimmed to match the sandbox path.
func (w *streamLineWriter) emitLocked(chunk string) {
	if w.onLine == nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSuffix(chunk, "\n"), "\n") {
		w.onLine(strings.TrimSuffix(line, "\r") + "\n")
	}
}

// execCommandStream runs a shell command directly (no sandbox) in dir, returns its
// combined output, and forwards it line by line to onLine (may be nil).
//
// The command gets the allowlisted environment only (agent.SafeAgentEnv): these
// commands are LLM-driven (prompt injection lifts instructions out of repo
// content) and run on the host for sandbox-disabled projects, so the server
// environment — above all MIKE_LOGIN_PASSWORD and MIKE_ENCRYPTION_KEY — must
// never reach them. GIT_TERMINAL_PROMPT=0 keeps an injected git transfer from
// blocking on a credential prompt until the tool timeout kills it.
//
// timeout is seconds and is enforced: the whole process group is killed when it
// expires and the error says so. A command that detaches a child returns as soon as
// the command itself exits — the detached child is left running on purpose.
func execCommandStream(ctx context.Context, dir, command string, timeout int64, onLine func(text string)) (string, error) {
	secs := normalizeBashTimeout(timeout)
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(secs)*time.Second)
	defer cancel()

	c := exec.CommandContext(runCtx, "bash", "-c", command)
	c.Dir = dir
	// See the doc comment above: allowlisted env only, never the server's own.
	c.Env = agent.SafeAgentEnv("GIT_TERMINAL_PROMPT=0")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Kill the group, not just bash. The error is swallowed on purpose: exec turns a
	// Cancel error into the Wait error, hiding the result of a process that had
	// already exited.
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
		_, _ = w.WriteFrom(r)
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
