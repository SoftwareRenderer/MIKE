package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"kanban/pkg/bridge/protocol"
)

// handleWrite writes content to a file atomically (temp file + rename).
func handleWrite(h *handler, conn net.Conn, req *protocol.Request) error {
	var p protocol.WriteParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return fmt.Errorf("parse write params: %w", err)
	}
	if p.Path == "" {
		return fmt.Errorf("path is required")
	}

	// Validate path stays within CWD
	if err := validatePath(p.Path); err != nil {
		return err
	}

	// Create parent directories
	dir := filepath.Dir(p.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	// Write to temp file in same directory (ensures same filesystem for atomic rename)
	tmpPath, err := os.CreateTemp(dir, ".kanban-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmpPath.Name()

	n, err := tmpPath.WriteString(p.Content)
	if err != nil {
		_ = tmpPath.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write content: %w", err)
	}

	if err := tmpPath.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}

	// Set file permissions
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("chmod temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpName, p.Path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename to %s: %w", p.Path, err)
	}

	// Send result with bytes written
	result := protocol.NewResult(req.MsgID, protocol.NewUint64Result(uint64(n)))
	return h.writeFrame(conn, result)
}
