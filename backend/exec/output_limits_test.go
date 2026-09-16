package exec

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"mike/backend/toolresult"
	"mike/pkg/bridge/protocol"
)

// A sandboxed command writes whatever it likes and the bridge hands over the whole stream, while
// the result is truncated to 50 KB for the model: keeping everything buys nothing.
func TestCaptureOutputStopsAtTheCaptureLimit(t *testing.T) {
	const produced = 4 * toolresult.MaxCaptureBytes
	out := captureOutput(io.LimitReader(strings.NewReader(strings.Repeat("x", produced)), produced))

	if len(out) > toolresult.MaxCaptureBytes+len(toolresult.CaptureMarker(produced)) {
		t.Errorf("captured %d bytes, want at most the %d-byte limit plus a marker", len(out), toolresult.MaxCaptureBytes)
	}
	if !strings.Contains(out, "more bytes dropped") {
		t.Errorf("a cut must be reported, not silent: %q", out[len(out)-80:])
	}
	// The marker must report how much was dropped, or a cut reads as the whole log.
	if !strings.Contains(out, strconv.Itoa(produced-toolresult.MaxCaptureBytes)) {
		t.Errorf("the marker should report the dropped byte count, got tail %q", out[len(out)-80:])
	}
}

// The same rule GET /api/fs/read applies: stat first, refuse, do not read the host into memory.
func TestReadFileCappedRefusesAFilePastTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.bin")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(protocol.MaxReadBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	data, err := readFileCapped(path)
	if err == nil {
		t.Fatalf("read %d bytes past the %d-byte limit", len(data), protocol.MaxReadBytes)
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("the refusal should name the limit, got: %v", err)
	}
}

func TestReadFileCappedReadsAFileWithinTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := readFileCapped(path)
	if err != nil || string(data) != "keep me" {
		t.Errorf("got %q, %v", data, err)
	}
}

// The tool path, not only the helper: the executor refuses and still answers the next call.
func TestReadFileToolRefusesAFilePastTheLimit(t *testing.T) {
	worktree := t.TempDir()
	te := newReadTestExecutor(t, worktree)
	task := makeTestTask(t, te.store, "proj-readcap", "task-readcap")

	path := filepath.Join(t.TempDir(), "huge.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(protocol.MaxReadBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{"path": path},
		worktree, task, &executionState{}, nil)
	if ok {
		t.Fatalf("read_file returned %d bytes for a file past the limit", len(result))
	}
	if !strings.Contains(result, "limit") {
		t.Errorf("the model should be told the limit, got: %q", result)
	}
}
