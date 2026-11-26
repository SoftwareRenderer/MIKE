// Package textedit is the grammar of the edit_file tool: every replacement is located in the original
// content, the call is refused whole if one cannot be placed, and the rest is built in one forward
// pass. The server runs it for a task that is not sandboxed and the bridge for one that is, so a
// refusal reaches the model in one wording whichever half held the file.
package textedit

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrNoEdits answers a call that arrived with an empty edit list. Each caller checks it before reading
// the file, so Apply never sees one.
var ErrNoEdits = errors.New("no edits provided")

// Edit is one replacement: text the file holds, and the text that takes its place.
type Edit struct {
	OldText string
	NewText string
}

// Apply puts every edit into content and returns the result, or a refusal naming the edit that could
// not be placed and leaving nothing written. Each refusal sentence is what the model rewrites from.
//
// Every position is found in the original content, so an oldText must be present exactly once and no
// two edits may overlap.
func Apply(content string, edits []Edit) (string, error) {
	type editPos struct {
		index   int
		start   int
		end     int
		newText string
	}

	var positions []editPos
	for i, e := range edits {
		if e.OldText == "" {
			return "", fmt.Errorf("edits[%d].oldText is empty", i)
		}
		idx := strings.Index(content, e.OldText)
		if idx == -1 {
			return "", fmt.Errorf("edits[%d].oldText not found in file. Ensure it matches exactly (whitespace, indentation). Got %d lines in file",
				i, len(strings.Split(content, "\n")))
		}
		if strings.Contains(content[idx+1:], e.OldText) {
			return "", fmt.Errorf("edits[%d].oldText appears multiple times in file. Make it more specific by including surrounding context", i)
		}
		positions = append(positions, editPos{index: i, start: idx, end: idx + len(e.OldText), newText: e.NewText})
	}

	slices.SortFunc(positions, func(a, b editPos) int { return cmp.Compare(a.start, b.start) })
	for i := range len(positions) - 1 {
		if positions[i].end > positions[i+1].start {
			return "", fmt.Errorf("edits[%d] and edits[%d] overlap. Merge them into a single edit or ensure they don't overlap",
				positions[i].index, positions[i+1].index)
		}
	}

	var b strings.Builder
	b.Grow(len(content) + 64)
	prev := 0
	for _, p := range positions {
		b.WriteString(content[prev:p.start])
		b.WriteString(p.newText)
		prev = p.end
	}
	b.WriteString(content[prev:])
	return b.String(), nil
}
