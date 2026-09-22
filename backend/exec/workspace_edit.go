package exec

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"mike/backend/fsutil"
)

// errEditOutsideWorkspace indicates a rename edit targets a path outside the
// task workspace root. Renames only ever modify files within the worktree.
var errEditOutsideWorkspace = errors.New("edit target outside workspace")

// lspPosition is a 0-indexed LSP text position.
type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// lspRange is a LSP start/end position pair.
type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

// lspTextEdit is a LSP TextEdit: replace Range with NewText.
type lspTextEdit struct {
	Range   lspRange `json:"range"`
	NewText string   `json:"newText"`
}

// lineStartOffsets returns the byte offset of each line start in content.
func lineStartOffsets(content string) []int {
	offsets := []int{0}
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			offsets = append(offsets, i+1)
		}
	}
	return offsets
}

// posToOffset converts a 0-indexed LSP position to a byte offset.
func posToOffset(content string, p lspPosition, lineStarts []int) (int, error) {
	if p.Line < 0 || p.Line >= len(lineStarts) {
		return 0, fmt.Errorf("line %d out of range (file has %d lines)", p.Line, len(lineStarts))
	}
	lineStart := lineStarts[p.Line]
	var lineLen int
	if p.Line+1 < len(lineStarts) {
		lineLen = lineStarts[p.Line+1] - lineStart - 1 // exclude the newline
	} else {
		lineLen = len(content) - lineStart // last line, no trailing newline
	}
	if p.Character > lineLen {
		return 0, fmt.Errorf("character %d beyond line %d length (%d)", p.Character, p.Line, lineLen)
	}
	return lineStart + p.Character, nil
}

// applyTextEdits applies LSP TextEdits to content. Edits may arrive in any
// order; they are applied from the end of the file backwards so earlier
// offsets stay valid. Overlapping or invalid edits are rejected.
func applyTextEdits(content string, edits []lspTextEdit) (string, error) {
	if len(edits) == 0 {
		return content, nil
	}

	lineStarts := lineStartOffsets(content)
	type offsetEdit struct {
		start, end int
		newText    string
	}
	resolved := make([]offsetEdit, 0, len(edits))
	for _, e := range edits {
		// Range validity: end must not precede start.
		if e.Range.End.Line < e.Range.Start.Line || (e.Range.End.Line == e.Range.Start.Line && e.Range.End.Character < e.Range.Start.Character) {
			return "", fmt.Errorf("invalid edit range: end %v precedes start %v", e.Range.End, e.Range.Start)
		}
		start, err := posToOffset(content, e.Range.Start, lineStarts)
		if err != nil {
			return "", fmt.Errorf("edit start: %w", err)
		}
		end, err := posToOffset(content, e.Range.End, lineStarts)
		if err != nil {
			return "", fmt.Errorf("edit end: %w", err)
		}
		resolved = append(resolved, offsetEdit{start: start, end: end, newText: e.NewText})
	}

	// Sort by start (then end) ascending, and check for overlaps.
	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].start != resolved[j].start {
			return resolved[i].start < resolved[j].start
		}
		return resolved[i].end < resolved[j].end
	})
	for i := 1; i < len(resolved); i++ {
		if resolved[i].start < resolved[i-1].end {
			return "", fmt.Errorf("overlapping edits at offset %d and %d", resolved[i-1].start, resolved[i].start)
		}
	}

	// Apply backwards so earlier offsets remain valid.
	out := content
	for i := len(resolved) - 1; i >= 0; i-- {
		e := resolved[i]
		out = out[:e.start] + e.newText + out[e.end:]
	}
	return out, nil
}

// parseWorkspaceEdit parses a JSON-RPC rename response envelope into per-URI
// edits. A null result (nothing renamed) yields (nil, nil). An LSP error
// envelope yields an error carrying the code and message.
func parseWorkspaceEdit(response json.RawMessage) (map[string][]lspTextEdit, error) {
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal rename response: %w", err)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("LSP error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil, nil
	}

	var edit struct {
		Changes         map[string][]lspTextEdit `json:"changes"`
		DocumentChanges []struct {
			Kind         string `json:"kind"`
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Edits []lspTextEdit `json:"edits"`
		} `json:"documentChanges"`
	}
	if err := json.Unmarshal(envelope.Result, &edit); err != nil {
		return nil, fmt.Errorf("unmarshal rename result: %w", err)
	}

	out := make(map[string][]lspTextEdit)
	if len(edit.Changes) > 0 {
		for uri, edits := range edit.Changes {
			out[uri] = edits
		}
		return out, nil
	}
	if len(edit.DocumentChanges) > 0 {
		for _, dc := range edit.DocumentChanges {
			if dc.Kind != "" && dc.Kind != "text" {
				return nil, fmt.Errorf("unsupported documentChanges kind %q (only text edits are applied)", dc.Kind)
			}
			out[dc.TextDocument.URI] = append(out[dc.TextDocument.URI], dc.Edits...)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unexpected rename result: no changes or documentChanges")
}

// workspaceEditTargets validates that every edit URI resolves to a path inside
// workspaceRoot and returns a map of URI → workspace-relative path.
func workspaceEditTargets(workspaceRoot string, uris []string) (map[string]string, error) {
	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	rel := make(map[string]string, len(uris))
	for _, uri := range uris {
		if !strings.HasPrefix(uri, "file://") {
			return nil, fmt.Errorf("unsupported edit URI scheme: %q", uri)
		}
		p := strings.TrimPrefix(uri, "file://")
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("edit URI is not an absolute path: %q", uri)
		}
		r, err := filepath.Rel(absRoot, p)
		if err != nil {
			return nil, fmt.Errorf("resolve %q against workspace: %w", uri, err)
		}
		if !fsutil.RelInsideRoot(r) {
			return nil, fmt.Errorf("%w: %q", errEditOutsideWorkspace, p)
		}
		rel[uri] = r
	}
	return rel, nil
}
