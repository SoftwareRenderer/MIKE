package main

import (
	"cmp"
	"fmt"
	"io"
	"mike/pkg/bridge/protocol"
	"os"
	"slices"
	"strings"
)

// handleEdit reads a file, applies text replacements, and writes atomically.
func handleEdit(h *handler, out io.Writer, req *protocol.Request) error {
	p, err := toolParams(req, "edit", func(p *protocol.EditParams) string { return p.Path })
	if err != nil {
		return err
	}
	if len(p.Edits) == 0 {
		return fmt.Errorf("no edits provided")
	}

	// Acquire per-file lock to prevent concurrent read-modify-write races.
	// Only one edit request can read/modify/write a specific file at a time.
	unlock := h.lockFile(p.Path)
	defer unlock()

	// Read current file content
	data, err := os.ReadFile(p.Path)
	if err != nil {
		return fmt.Errorf("read %s: %w", p.Path, err)
	}
	content := string(data)

	// Find all oldText positions in the original content
	type editPos struct {
		index   int
		start   int
		end     int
		newText string
	}
	var positions []editPos
	for i, e := range p.Edits {
		if e.OldText == "" {
			return fmt.Errorf("edits[%d].oldText is empty", i)
		}
		idx := strings.Index(content, e.OldText)
		if idx == -1 {
			// Show context around where it might be expected.
			return fmt.Errorf("edits[%d].oldText not found in file. Ensure it matches exactly (whitespace, indentation). Got %d lines in file", i, len(strings.Split(content, "\n")))
		}
		// Check for duplicate matches
		secondIdx := strings.Index(content[idx+1:], e.OldText)
		if secondIdx != -1 {
			return fmt.Errorf("edits[%d].oldText appears multiple times in file", i)
		}
		positions = append(positions, editPos{index: i, start: idx, end: idx + len(e.OldText), newText: e.NewText})
	}

	// Sort by start index for overlap check
	slices.SortFunc(positions, func(a, b editPos) int { return cmp.Compare(a.start, b.start) })

	// Check for overlapping edits
	for i := range len(positions) - 1 {
		if positions[i].end > positions[i+1].start {
			return fmt.Errorf("edits[%d] and edits[%d] overlap", positions[i].index, positions[i+1].index)
		}
	}

	// Apply edits in a single forward pass
	var b strings.Builder
	b.Grow(len(content) + 64)
	prev := 0
	for _, p := range positions {
		b.WriteString(content[prev:p.start])
		b.WriteString(p.newText)
		prev = p.end
	}
	b.WriteString(content[prev:])
	result := b.String()

	n, err := writeAtomic(p.Path, result)
	if err != nil {
		return err
	}

	resultFrame := protocol.NewResult(req.MsgID, protocol.NewUint64Result(uint64(n)))
	return h.writeFrame(out, resultFrame)
}
