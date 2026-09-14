package exec

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"kanban/backend/sandbox"
	"kanban/backend/toolresult"
	"kanban/backend/types"
	"kanban/pkg/bridge"
	"kanban/pkg/bridge/protocol"

	"kanban/pkg/logging"
)

// execFunc runs a shell command. Provided by the sandbox in container mode.
type execFunc func(ctx context.Context, command string, timeout int64) (string, error)

// execStreamFunc runs a shell command with streaming output.
// outputCallback is called for each chunk of output as it arrives.
type execStreamFunc func(ctx context.Context, command string, timeout int64, outputCallback func(text string)) (string, error)

// readFunc reads a file. Provided by the sandbox in container mode.
type readFunc func(ctx context.Context, path string) ([]byte, error)

// writeFunc writes a file. Provided by the sandbox in container mode.
type writeFunc func(ctx context.Context, path, content string) error

// editFunc applies text replacements to a file atomically in the sandbox.
// Provided by the sandbox in container mode; nil means fall back to read+edit+write.
type editFunc func(ctx context.Context, path string, edits []protocol.Edit) error

// captureOutput reads a command's whole output stream up to toolresult.MaxCaptureBytes: the
// bridge hands over everything a command printed, and the host bash path stops at the same limit.
func captureOutput(r io.Reader) string {
	sink := toolresult.NewCapture()
	_, _ = io.Copy(sink, r)
	return sink.String()
}

// readFileCapped refuses a file it cannot hold instead of learning halfway through. The limit is
// the bridge's own, so a sandboxed and an unsandboxed task read the same set of files; the copy
// carries the bound, not the stat, because a file can grow in between.
func readFileCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > protocol.MaxReadBytes {
		return nil, fmt.Errorf("%w: %s is %d bytes, limit %d",
			protocol.ErrResponseTooLarge, path, info.Size(), protocol.MaxReadBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, protocol.MaxReadBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > protocol.MaxReadBytes {
		return nil, fmt.Errorf("%w: %s passed the limit of %d bytes mid-read",
			protocol.ErrResponseTooLarge, path, protocol.MaxReadBytes)
	}
	return data, nil
}

// makeSandboxCallbacks creates exec/read/write/edit callbacks that route through
// the sandbox agent client inside a container.
func makeSandboxCallbacks(client *bridge.Client) (execFunc, execStreamFunc, readFunc, writeFunc, editFunc) {
	execCb := func(ctx context.Context, command string, timeout int64) (string, error) {
		holder, err := client.Bash(ctx, command, timeout)
		if err != nil {
			return "", fmt.Errorf("bash: %w", err)
		}
		out := captureOutput(holder.Output())
		exitCode := <-holder.ExitCode()
		if exitCode != 0 {
			return out, fmt.Errorf("exit code %d", exitCode)
		}
		return out, nil
	}
	execStreamCb := func(ctx context.Context, command string, timeout int64, outputCallback func(text string)) (string, error) {
		holder, err := client.Bash(ctx, command, timeout)
		if err != nil {
			return "", fmt.Errorf("bash: %w", err)
		}
		sink := toolresult.NewCapture()
		streamReader := holder.Output()
		scanner := bufio.NewScanner(streamReader)
		for scanner.Scan() {
			line := scanner.Text() + "\n"
			_, _ = sink.Write([]byte(line))
			if outputCallback != nil {
				outputCallback(line)
			}
		}
		exitCode := <-holder.ExitCode()
		if exitCode != 0 {
			return sink.String(), fmt.Errorf("exit code %d", exitCode)
		}
		return sink.String(), nil
	}
	readCb := func(ctx context.Context, path string) ([]byte, error) {
		content, _, err := client.ReadFile(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		return []byte(content), nil
	}
	writeCb := func(ctx context.Context, path, content string) error {
		_, err := client.WriteFile(ctx, path, content)
		if err != nil {
			return fmt.Errorf("write: %w", err)
		}
		return nil
	}
	editCb := func(ctx context.Context, path string, edits []protocol.Edit) error {
		_, err := client.EditFile(ctx, path, edits)
		if err != nil {
			return fmt.Errorf("edit: %w", err)
		}
		return nil
	}
	return execCb, execStreamCb, readCb, writeCb, editCb
}

// executeTool dispatches one tool call and clears the bridge-recovery latch a success earns: a
// healthy call proves the bridge is healthy (or unused). The clear is why this wrapper exists —
// those attempts are taskMu-guarded, and the runner has no way to take that lock.
func (te *TaskExecutor) executeTool(ctx context.Context, name string, args map[string]any, worktreePath string, task *types.Task, state *executionState, outputCallback func(toolID string, text string)) (string, bool) {
	result, ok := te.tools.run(ctx, name, args, worktreePath, task, state, outputCallback)
	if ok && state != nil && state.sandbox().recoverBridge != nil {
		te.resetBridgeRecoveryAttempts(state)
	}
	return result, ok
}

func parseToolArgs(s string) map[string]any {
	result := make(map[string]any)
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" {
		return result
	}
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		var innerStr string
		if err2 := json.Unmarshal([]byte(s), &innerStr); err2 == nil && innerStr != "" {
			if err3 := json.Unmarshal([]byte(innerStr), &result); err3 == nil {
				return result
			}
		}
		if len(s) > 1 && s[0] == '{' && s[1] == '{' {
			trimmed := s[1:]
			if err4 := json.Unmarshal([]byte(trimmed), &result); err4 == nil {
				return result
			}
		}
		// Don't log for clearly incomplete (streaming) JSON — e.g. just "{" or "{"path":"
		if len(s) >= 1 && s[0] == '{' && s[len(s)-1] != '}' {
			return result
		}
		logging.For(logging.Tools).Warnf("failed to parse args: %v (raw: %q)", err, s)
		return map[string]any{"raw": s}
	}
	return result
}

// normalizePath strips the sandbox mount prefix from absolute paths, so a path written the way the
// container sees it names the same file on both sides. A relative path is returned as-is and
// resolved against the worktree by the caller; any other absolute path stays absolute.
func normalizePath(projectName, path string) string {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return cleaned
	}
	// Strip sandbox mount prefix: /home/sandbox/<projectName>/…
	if rel, ok := sandbox.TrimMountPrefix(projectName, cleaned); ok {
		return rel
	}
	// Other absolute paths are what they name.
	return cleaned
}

// findSymbolInText searches file content for an identifier and returns its LSP position
// (0-indexed line, 0-indexed character). Uses word boundary matching to avoid partial matches.
func findSymbolInText(content, symbol string) map[string]any {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		idx := findWordIndex(line, symbol)
		if idx >= 0 {
			return map[string]any{"line": i, "character": idx}
		}
	}
	return nil
}

// findWordIndex finds the start position of a word in a line, matching whole words only.
// Returns -1 if not found.
func findWordIndex(line, word string) int {
	for i := 0; i <= len(line)-len(word); i++ {
		if line[i:i+len(word)] == word {
			// Check word boundaries
			beforeOK := i == 0 || !isIdentChar(line[i-1])
			afterOK := i+len(word) >= len(line) || !isIdentChar(line[i+len(word)])
			if beforeOK && afterOK {
				return i
			}
		}
	}
	return -1
}

// isIdentChar checks if a character is valid in an identifier (letter, digit, underscore).
func isIdentChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

// formatLSPResponse formats a raw LSP JSON response into a human-readable string.
func formatLSPResponse(response json.RawMessage, method string, worktreePath string) string {
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return string(response)
	}
	if envelope.Error != nil {
		return fmt.Sprintf("LSP error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if envelope.Result == nil || string(envelope.Result) == "null" {
		return "(no result)"
	}
	var result any
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return string(envelope.Result)
	}

	switch method {
	case "textDocument/references":
		return formatLocationArray(result, worktreePath, "references")
	case "textDocument/definition":
		return formatDefinitionResult(result, worktreePath)
	case "textDocument/hover":
		return formatHoverResult(result)
	default:
		data, _ := json.MarshalIndent(result, "", "  ")
		return string(data)
	}
}

// formatLocationArray formats an array of LSP Location objects.
func formatLocationArray(result any, worktreePath string, label string) string {
	locs, ok := result.([]any)
	if !ok {
		data, _ := json.MarshalIndent(result, "", "  ")
		return string(data)
	}
	if len(locs) == 0 {
		return fmt.Sprintf("No %s found", label)
	}

	var buf strings.Builder
	fmt.Fprintf(&buf, "Found %d %s:\n", len(locs), label)
	for i, locAny := range locs {
		loc, ok := locAny.(map[string]any)
		if !ok {
			continue
		}
		uri, _ := loc["uri"].(string)
		rangeMap, _ := loc["range"].(map[string]any)
		start, _ := rangeMap["start"].(map[string]any)
		line, _ := start["line"].(float64)
		character, _ := start["character"].(float64)

		// Extract relative path from URI
		path := strings.TrimPrefix(uri, "file://")
		rel, _ := filepath.Rel(worktreePath, path)

		fmt.Fprintf(&buf, "  %d. %s:%d:%d\n", i+1, rel, int(line)+1, int(character)+1)
	}
	return buf.String()
}

// formatDefinitionResult formats a definition result (Location or Location[]).
func formatDefinitionResult(result any, worktreePath string) string {
	// Can be a single Location or LocationLink[] or Location[]
	switch v := result.(type) {
	case map[string]any:
		// Single Location
		loc := v
		uri, _ := loc["uri"].(string)
		rangeMap, _ := loc["range"].(map[string]any)
		start, _ := rangeMap["start"].(map[string]any)
		line, _ := start["line"].(float64)
		character, _ := start["character"].(float64)

		path := strings.TrimPrefix(uri, "file://")
		rel, _ := filepath.Rel(worktreePath, path)

		return fmt.Sprintf("Definition found at %s:%d:%d", rel, int(line)+1, int(character)+1)
	case []any:
		// Location[] or LocationLink[]
		if len(v) == 0 {
			return "No definition found"
		}
		var buf strings.Builder
		fmt.Fprintf(&buf, "Found %d definition(s):\n", len(v))
		for i, locAny := range v {
			loc, ok := locAny.(map[string]any)
			if !ok {
				continue
			}
			uri, _ := loc["uri"].(string)
			// Handle LocationLink (targetRange) vs Location (range)
			rangeMap, _ := loc["range"].(map[string]any)
			if rangeMap == nil {
				rangeMap, _ = loc["targetSelectionRange"].(map[string]any)
			}
			start, _ := rangeMap["start"].(map[string]any)
			line, _ := start["line"].(float64)
			character, _ := start["character"].(float64)

			path := strings.TrimPrefix(uri, "file://")
			rel, _ := filepath.Rel(worktreePath, path)

			fmt.Fprintf(&buf, "  %d. %s:%d:%d\n", i+1, rel, int(line)+1, int(character)+1)
		}
		return buf.String()
	default:
		data, _ := json.MarshalIndent(result, "", "  ")
		return string(data)
	}
}

// formatHoverResult formats hover information result.
func formatHoverResult(result any) string {
	hover, ok := result.(map[string]any)
	if !ok {
		data, _ := json.MarshalIndent(result, "", "  ")
		return string(data)
	}

	contents, _ := hover["contents"].(map[string]any)
	if contents == nil {
		return "No hover information available"
	}

	// Handle MarkupContent { kind, value }
	if kind, ok := contents["kind"].(string); ok {
		value, _ := contents["value"].(string)
		return fmt.Sprintf("[%s]\n%s", kind, value)
	}

	// Handle MarkupContent as plain string
	if value, ok := contents["value"].(string); ok {
		return value
	}

	// Fallback: marshal the whole thing
	data, _ := json.MarshalIndent(contents, "", "  ")
	return string(data)
}

// stripANSI removes ANSI escape sequences from terminal output.
// Handles CSI (\x1b[...m), OSC (\x1b]...\x07), and single-byte escapes (\x1bH).
// CSI final bytes are in range 0x40–0x7E (@ through ~).
func stripANSI(s string) string {
	var buf strings.Builder
	buf.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' {
			i++
			if i < len(s) && s[i] == '[' {
				// CSI sequence — skip parameters/intermediates until final byte (>= 0x40)
				i++
				for i < len(s) && s[i] < '@' {
					i++
				}
				if i < len(s) {
					i++ // skip the final byte
				}
			} else if i < len(s) && s[i] == ']' {
				// OSC sequence — skip until ST (\x1b\\ or \x07)
				i++
				for i < len(s) {
					if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					if s[i] == '\x07' {
						i++
						break
					}
					i++
				}
			} else {
				// Other single-byte escape: \x1bA, \x1bH, \x1bM, etc.
				i++ // skip the next byte too
			}
		} else {
			buf.WriteByte(s[i])
			i++
		}
	}
	return buf.String()
}
