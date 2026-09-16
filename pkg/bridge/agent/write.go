package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"mike/pkg/bridge/protocol"
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
	return h.writeFrame(conn, result)
}

// handleWriteBytes writes arbitrary bytes to a file atomically (see WriteBytesParams for why they
// cannot travel as a string). The size is refused here as well as on the host: a bound held only
// outside the container is no bound against a peer that does not share this build's limits.
func handleWriteBytes(h *handler, conn net.Conn, req *protocol.Request) error {
	var p protocol.WriteBytesParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return fmt.Errorf("parse write_bytes params: %w", err)
	}
	if p.Path == "" {
		return fmt.Errorf("path is required")
	}
	if len(p.Data) > protocol.MaxWriteBytes {
		return fmt.Errorf("%w: %s is %d bytes, limit %d",
			protocol.ErrResponseTooLarge, p.Path, len(p.Data), protocol.MaxWriteBytes)
	}

	dir := filepath.Dir(p.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	n, err := writeAtomicBytes(p.Path, p.Data)
	if err != nil {
		return err
	}

	result := protocol.NewResult(req.MsgID, protocol.NewUint64Result(uint64(n)))
	return h.writeFrame(conn, result)
}
