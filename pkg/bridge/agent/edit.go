package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kanban/pkg/bridge/protocol"
)

// handleEdit reads a file, applies text replacements, and writes atomically.
func handleEdit(h *handler, conn net.Conn, req *protocol.Request) error {
	var p protocol.EditParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return fmt.Errorf("parse edit params: %w", err)
	}
	if p.Path == "" {
		return fmt.Errorf("path is required")
	}
	if len(p.Edits) == 0 {
		return fmt.Errorf("no edits provided")
	}

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
			return fmt.Errorf("edits[%d].oldText not found in file", i)
		}
		// Check for duplicate matches
		secondIdx := strings.Index(content[idx+1:], e.OldText)
		if secondIdx != -1 {
			return fmt.Errorf("edits[%d].oldText appears multiple times in file", i)
		}
		positions = append(positions, editPos{index: i, start: idx, end: idx + len(e.OldText), newText: e.NewText})
	}

	// Sort by start index for overlap check
	sort.Slice(positions, func(i, j int) bool {
		return positions[i].start < positions[j].start
	})

	// Check for overlapping edits
	for i := 0; i < len(positions)-1; i++ {
		if positions[i].end > positions[i+1].start {
			return fmt.Errorf("edits[%d] and edits[%d] overlap", positions[i].index, positions[i+1].index)
		}
	}

	// Apply edits in reverse order to preserve positions
	result := content
	for i := len(positions) - 1; i >= 0; i-- {
		p := positions[i]
		result = result[:p.start] + p.newText + result[p.end:]
	}

	// Write atomically (temp file + rename)
	dir := filepath.Dir(p.Path)
	tmpPath, err := os.CreateTemp(dir, ".kanban-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmpPath.Name()

	n, err := tmpPath.WriteString(result)
	if err != nil {
		_ = tmpPath.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write content: %w", err)
	}

	if err := tmpPath.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := os.Rename(tmpName, p.Path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename to %s: %w", p.Path, err)
	}

	resultFrame := protocol.NewResult(req.MsgID, protocol.NewUint64Result(uint64(n)))
	return h.writeFrame(conn, resultFrame)
}
