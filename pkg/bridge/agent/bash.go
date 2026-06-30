package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os/exec"
	"time"

	"kanban/pkg/bridge/protocol"
)

const (
	bashStreamStdout = 0
	bashStreamStderr = 1
	defaultBashTimeout = 5 // seconds
	maxBashTimeout     = 300
)

// handleBash runs a shell command and streams output as Chunk frames.
// Sends a Result frame with exit code when the command completes.
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

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start command: %w", err)
	}

	// Stream output from both stdout and stderr
	streamOutput := func(reader io.Reader, stream uint8) {
		buf := make([]byte, 32*1024) // 32KB chunks
		for {
			n, readErr := reader.Read(buf)
			if n > 0 {
				chunk := protocol.NewChunk(req.MsgID, protocol.NewBashChunk(stream, buf[:n]))
				_ = h.writeFrame(conn, chunk)
			}
			if readErr != nil {
				return
			}
		}
	}

	go streamOutput(stdout, bashStreamStdout)
	go streamOutput(stderr, bashStreamStderr)

	// Wait for command to complete with timeout
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	waitDone := make(chan int32, 1)
	go func() {
		err := cmd.Wait()
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
	select {
	case <-timer.C:
		_ = cmd.Process.Kill()
		<-waitDone // reap
		exitCode = 124 // standard timeout exit code
	case exitCode = <-waitDone:
	}

	// Build result: exitCode + empty meta
	meta, _ := json.Marshal(map[string]any{
		"duration_ms": int64(0), // not tracked yet
	})
	payload := protocol.NewBashResult(exitCode, meta)
	result := protocol.NewResult(req.MsgID, payload)
	return h.writeFrame(conn, result)
}
