package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mike/pkg/atomicfile"
	"mike/pkg/bridge/protocol"
)

// handleWrite writes content to a file atomically (temp file + rename).
func handleWrite(h *handler, out io.Writer, req *protocol.Request) error {
	p, err := toolParams(req, "write", func(p *protocol.WriteParams) string { return p.Path })
	if err != nil {
		return err
	}

	dir := filepath.Dir(p.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	n, err := atomicfile.Write(p.Path, []byte(p.Content), 0o644)
	if err != nil {
		return err
	}

	result := protocol.NewResult(req.MsgID, protocol.NewUint64Result(uint64(n)))
	return h.writeFrame(out, result)
}
