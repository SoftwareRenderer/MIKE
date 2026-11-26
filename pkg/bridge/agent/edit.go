package main

import (
	"fmt"
	"io"
	"os"

	"mike/pkg/atomicfile"
	"mike/pkg/bridge/protocol"
	"mike/pkg/textedit"
)

// handleEdit reads a file, applies the replacements through textedit, and writes it atomically.
func handleEdit(h *handler, out io.Writer, req *protocol.Request) error {
	p, err := toolParams(req, "edit", func(p *protocol.EditParams) string { return p.Path })
	if err != nil {
		return err
	}
	if len(p.Edits) == 0 {
		return textedit.ErrNoEdits
	}

	// One edit per file at a time: the read-modify-write must not interleave.
	unlock := h.lockFile(p.Path)
	defer unlock()

	data, err := os.ReadFile(p.Path)
	if err != nil {
		return fmt.Errorf("read %s: %w", p.Path, err)
	}

	edits := make([]textedit.Edit, len(p.Edits))
	for i, e := range p.Edits {
		edits[i] = textedit.Edit{OldText: e.OldText, NewText: e.NewText}
	}
	result, err := textedit.Apply(string(data), edits)
	if err != nil {
		return err
	}

	n, err := atomicfile.Write(p.Path, []byte(result), 0o644)
	if err != nil {
		return err
	}

	resultFrame := protocol.NewResult(req.MsgID, protocol.NewUint64Result(uint64(n)))
	return h.writeFrame(out, resultFrame)
}
