package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"

	"kanban/pkg/bridge/protocol"
)

// shellSession manages a single interactive shell process with PTY.
type shellSession struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	ptmx *os.File
	conn net.Conn // agent's connection to host (for sending chunks)
	done chan struct{}
}

// handleShellOpen starts a new shell session with a PTY.
func (h *handler) handleShellOpen(conn net.Conn, req *protocol.Request) error {
	var p protocol.ShellOpenParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return fmt.Errorf("parse shell_open params: %w", err)
	}

	shell := p.Shell
	if shell == "" {
		shell = "bash"
	}
	// Fallback: prefer bash, then sh
	if _, err := exec.LookPath(shell); err != nil {
		if shell != "bash" {
			if _, err := exec.LookPath("bash"); err == nil {
				shell = "bash"
			} else {
				shell = "sh"
			}
		}
	}

	rows := p.Rows
	cols := p.Cols
	if rows == 0 {
		rows = 40
	}
	if cols == 0 {
		cols = 120
	}

	cmd := exec.Command(shell)
	cmd.Env = append(cmd.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return fmt.Errorf("start %s: %w", shell, err)
	}

	h.mu.Lock()
	sessionID := h.nextSession
	h.nextSession++
	sess := &shellSession{
		cmd:  cmd,
		ptmx: ptmx,
		conn: conn,
		done: make(chan struct{}),
	}
	h.sessions[sessionID] = sess
	h.mu.Unlock()

	// Start read goroutine to stream PTY output to host
	go sess.readLoop(sessionID)

	log.Printf("agent: shell session %d started (%s)", sessionID, shell)

	// Send result with session ID
	result := protocol.NewResult(req.MsgID, protocol.NewShellOpenResult(sessionID))
	return protocol.WriteFrame(conn, result)
}

// readLoop reads from the PTY and sends output as ShellChunk frames to the host.
func (s *shellSession) readLoop(sessionID uint32) {
	buf := make([]byte, 32*1024) // 32KB
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := protocol.NewChunk(sessionID, protocol.NewShellChunk(sessionID, buf[:n]))
			s.mu.Lock()
			conn := s.conn
			s.mu.Unlock()
			if conn != nil {
				_ = protocol.WriteFrame(conn, chunk)
			}
		}
		if err != nil {
			// PTY closed — shell exited
			s.mu.Lock()
			if s.conn != nil {
				// Send empty chunk as exit signal
				_ = protocol.WriteFrame(s.conn, protocol.NewChunk(sessionID, protocol.NewShellChunk(sessionID, nil)))
			}
			s.mu.Unlock()
			log.Printf("agent: shell session %d exited", sessionID)

			// Clean up from handler
			// The handler will remove the session when shell_close is received,
			// but if it wasn't received, we need to clean up here.
			_ = s.cmd.Wait() // reap
			close(s.done)
			return
		}
	}
}

// write sends input data to the PTY.
func (s *shellSession) write(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ptmx != nil {
		_, _ = s.ptmx.Write(data)
	}
}

// resize updates the PTY window size.
func (s *shellSession) resize(rows, cols uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ptmx != nil {
		_ = pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
	}
}

// close terminates the shell process and cleans up resources.
func (s *shellSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn != nil {
		s.conn = nil // prevent further writes
	}

	if s.ptmx != nil {
		s.ptmx.Close()
		s.ptmx = nil
	}

	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
		s.cmd = nil
	}
}
