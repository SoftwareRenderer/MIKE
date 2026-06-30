package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"

	"kanban/pkg/bridge/protocol"
)

// readChunkSize is the size of each chunk sent for file reads.
const readChunkSize = 64 * 1024 // 64KB

// handleRead reads a file and sends content as Chunk frames + Result(size).
func handleRead(h *handler, conn net.Conn, req *protocol.Request) error {
	var p protocol.ReadParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return fmt.Errorf("parse read params: %w", err)
	}
	if p.Path == "" {
		return fmt.Errorf("path is required")
	}

	// Validate path stays within CWD
	if err := validatePath(p.Path); err != nil {
		return err
	}

	f, err := os.Open(p.Path)
	if err != nil {
		return fmt.Errorf("open %s: %w", p.Path, err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", p.Path, err)
	}

	totalSize := stat.Size()

	// Apply offset
	if p.Offset > 0 {
		if _, err := f.Seek(p.Offset, 0); err != nil {
			return fmt.Errorf("seek offset %d: %w", p.Offset, err)
		}
		// Recalculate remaining size after offset
		pos, _ := f.Seek(0, 2) // seek to end
		_, _ = f.Seek(p.Offset, 0) // seek back
		totalSize = pos - p.Offset
		if totalSize < 0 {
			totalSize = 0
		}
	}

	// Apply limit
	if p.Limit > 0 && totalSize > p.Limit {
		totalSize = p.Limit
	}

	// Read and send in chunks
	buf := make([]byte, readChunkSize)
	remaining := totalSize
	for remaining > 0 {
		toRead := int64(len(buf))
		if toRead > remaining {
			toRead = remaining
		}
		n, err := f.Read(buf[:toRead])
		if err != nil {
			return fmt.Errorf("read %s: %w", p.Path, err)
		}
		if n == 0 {
			break
		}

		chunk := protocol.NewChunk(req.MsgID, buf[:n])
		if err := h.writeFrame(conn, chunk); err != nil {
			return fmt.Errorf("send chunk: %w", err)
		}
		remaining -= int64(n)
	}

	// Send result with total size
	result := protocol.NewResult(req.MsgID, protocol.NewUint64Result(uint64(totalSize)))
	return h.writeFrame(conn, result)
}
