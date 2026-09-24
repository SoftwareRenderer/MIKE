package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mike/pkg/bridge/protocol"
)

// handleWrite writes content to a file atomically (temp file + rename).
func handleWrite(h *handler, out io.Writer, req *protocol.Request) error {
	var p protocol.WriteParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return fmt.Errorf("parse write params: %w", err)
	}
	if p.Path == "" {
		return fmt.Errorf("path is required")
	}

	// Create parent directories
	dir := filepath.Dir(p.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	n, err := writeAtomic(p.Path, p.Content)
	if err != nil {
		return err
	}

	// Send result with bytes written
	result := protocol.NewResult(req.MsgID, protocol.NewUint64Result(uint64(n)))
	return h.writeFrame(out, result)
}
