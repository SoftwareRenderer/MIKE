package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"mike/pkg/bridge/protocol"
	"mike/pkg/procterm"
)

const (
	bashStreamStdout = 0
	bashStreamStderr = 1

	defaultBashTimeout = 5   // seconds
	maxBashTimeout     = 300 // seconds
)

// handleBash runs a shell command, streams output as Chunk frames and sends a
// Result frame with the exit code when it completes. The process is tracked in
// h.bashProcs so bash_cancel can kill it.
//
// Ordering contract: all Chunk frames precede the Result frame. The client stops
// reading chunks at Result, so a later chunk is lost output.
func handleBash(h *handler, conn net.Conn, req *protocol.Request) error {
	var p protocol.BashParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return fmt.Errorf("parse bash params: %w", err)
	}
	if p.Command == "" {
		return fmt.Errorf("command is required")
	}

	timeout := time.Duration(p.Timeout) * time.Second
	if timeout == 0 {
		timeout = defaultBashTimeout * time.Second
	}
	if timeout > maxBashTimeout*time.Second {
		timeout = maxBashTimeout * time.Second
	}

	// Prefer bash for compatibility, fall back to sh (e.g. Alpine containers)
	shellCmd := "/bin/sh"
	if _, err := exec.LookPath("bash"); err == nil {
		shellCmd = "bash"
	}
	cmd := exec.Command(shellCmd, "-c", p.Command)

	// Own pipe ends: cmd.Wait() closes a StdoutPipe under the reader, which races
	// the copiers below and can drop output.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	defer stdoutR.Close()
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdoutW.Close()
		return fmt.Errorf("stderr pipe: %w", err)
	}
	defer stderrR.Close()
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	// Own process group: timeout/cancel kill the tree, not just the shell.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Register the process for cancellation before starting
	bp := &bashProc{cmd: cmd, done: make(chan struct{})}
	h.mu.Lock()
	h.bashProcs[req.MsgID] = bp
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.bashProcs, req.MsgID)
		h.mu.Unlock()
		bp.kill() // no-op unless we returned early with the process still alive
	}()

	started := time.Now()
	// Start writes cmd.Process; hold mu so a bash_cancel that already found this
	// entry can't see a half-written Process.
	bp.mu.Lock()
	startErr := cmd.Start()
	bp.mu.Unlock()
	if startErr != nil {
		stdoutW.Close()
		stderrW.Close()
		return fmt.Errorf("start command: %w", startErr)
	}
	// Close the parent's write ends, or the readers never see EOF.
	stdoutW.Close()
	stderrW.Close()

	// Stream output from both stdout and stderr
	readersDone := make(chan struct{}, 2)
	streamOutput := func(reader *os.File, stream uint8) {
		defer func() { readersDone <- struct{}{} }()
		buf := make([]byte, 32*1024) // 32KB chunks
		for {
			n, readErr := reader.Read(buf)
			if n > 0 {
				h.writeBashChunk(conn, bp, req.MsgID, stream, buf[:n])
			}
			if readErr != nil {
				return
			}
		}
	}

	go streamOutput(stdoutR, bashStreamStdout)
	go streamOutput(stderrR, bashStreamStderr)

	// Wait for command to complete with timeout or cancellation
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	waitDone := make(chan int32, 1)
	go func() {
		err := cmd.Wait()
		// Reaped: the pid (== the pgid) may be recycled, so kill() must stop
		// signalling this group from here on.
		bp.reaped.Store(true)
		exitCode := int32(0)
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = int32(exitErr.ExitCode())
			} else {
				exitCode = 1
			}
		}
		waitDone <- exitCode
	}()

	var exitCode int32
	var cancelled bool
	select {
	case <-timer.C:
		bp.kill()
		<-waitDone     // reap
		exitCode = 124 // standard timeout exit code
	case exitCode = <-waitDone:
		// Process exited normally or was killed
	case <-bp.done:
		// Cancelled by bash_cancel — kill the process tree
		cancelled = true
		bp.kill()
		exitCode = <-waitDone // reap
		// Use 137 (128 + 9 = SIGKILL) to signal cancellation
		if exitCode != 137 {
			exitCode = 137
		}
	}

	// Flush what the command already wrote before the Result frame closes the
	// client's chunk stream (bounded — see pkg/procterm for why the readers cannot
	// simply be waited for).
	procterm.Drain(readersDone, stdoutR, stderrR)

	// Build result: exitCode + meta with timing and cancelled flag
	meta, _ := json.Marshal(map[string]any{
		"duration_ms": time.Since(started).Milliseconds(),
		"cancelled":   cancelled,
	})
	payload := protocol.NewBashResult(exitCode, meta)
	return h.writeBashResult(conn, bp, req.MsgID, payload)
}

// writeBashChunk sends one output chunk, unless the Result frame already went out
// (a chunk after it would be dropped by the client).
func (h *handler) writeBashChunk(conn net.Conn, bp *bashProc, msgID uint32, stream uint8, data []byte) {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	if bp.finished {
		return
	}
	_ = h.writeFrame(conn, protocol.NewChunk(msgID, protocol.NewBashChunk(stream, data)))
}

// writeBashResult sends the Result frame and refuses any further chunk.
func (h *handler) writeBashResult(conn net.Conn, bp *bashProc, msgID uint32, payload []byte) error {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	bp.finished = true
	return h.writeFrame(conn, protocol.NewResult(msgID, payload))
}
