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
	"mike/backend/types"
	"mike/pkg/bridge/protocol"
	"mike/pkg/logging"
)

// A sandboxed command writes whatever it likes and the bridge hands over the whole stream, while
// the result is truncated to 50 KB for the model: keeping everything buys nothing.
func TestCaptureOutputStopsAtTheCaptureLimit(t *testing.T) {
	t.Parallel()
	const produced = 4 * toolresult.MaxCaptureBytes
	out := captureOutput("task-cap", io.LimitReader(strings.NewReader(strings.Repeat("x", produced)), produced))

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

// The operator's half of the same cut: the marker goes to the model, the count goes to the log with
// the task it belongs to. Not t.Parallel — TestSink redirects the process's log for its duration.
func TestCaptureOutputReportsDroppedBytesToTheLog(t *testing.T) {
	buf, restore := logging.TestSink()
	defer restore()

	const produced = 2 * toolresult.MaxCaptureBytes
	captureOutput("task-report", io.LimitReader(strings.NewReader(strings.Repeat("y", produced)), produced))

	got := buf.String()
	if !strings.Contains(got, "component=tools") || !strings.Contains(got, "task_id=task-report") {
		t.Errorf("a dropped-output report must be attributable, got %q", got)
	}
	if !strings.Contains(got, strconv.Itoa(produced-toolresult.MaxCaptureBytes)) {
		t.Errorf("the report should name the dropped count, got %q", got)
	}
}

func TestCaptureOutputStaysSilentUnderTheLimit(t *testing.T) {
	buf, restore := logging.TestSink()
	defer restore()

	captureOutput("task-uncut", strings.NewReader("all of it fits"))

	// Only this task's silence is asserted: other tests' goroutines write to the same sink.
	if strings.Contains(buf.String(), "task-uncut") {
		t.Errorf("a command that fit must not be reported: %q", buf.String())
	}
}

// The same rule GET /api/fs/read applies: stat first, refuse, do not read the host into memory.
func TestReadFileCappedRefusesAFilePastTheLimit(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	worktree := t.TempDir()
	te := newToolTestExecutor(t, worktree)
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

// A command that exits non-zero prints the same bytes a succeeding one does, and its output is a tool result
// like any other.
func TestBashOutputIsCappedWhicheverWayTheCommandEnds(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	te := newToolTestExecutor(t, worktree)
	task := makeTestTask(t, te.store, "proj-bashcap", "task-bashcap")

	for _, tc := range []struct {
		name    string
		command string
		wantOK  bool
	}{
		{name: "succeeds", command: "yes filler-line | head -n 200000", wantOK: true},
		{name: "fails", command: "yes filler-line | head -n 200000; exit 3", wantOK: false},
	} {
		result, ok := te.executeTool(context.Background(), types.ToolBash,
			map[string]any{"command": tc.command, "timeout": float64(30)}, worktree, task, &executionState{}, nil)

		if ok != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
		}
		// The failure reason rides in front of the output, which is the part the cap holds.
		output := result
		if line, rest, found := strings.Cut(result, "\n"); found && strings.HasPrefix(line, "Failed:") {
			output = rest
		}
		if len(output) > maxToolResultBytes+len(commandCutNotice) {
			t.Errorf("%s: the model was handed %d bytes of output, want the cap of %d", tc.name, len(output), maxToolResultBytes)
		}
		if !strings.Contains(result, "output truncated at") {
			t.Errorf("%s: the cut is silent, so the tail reads as the whole log", tc.name)
		}
	}
}

// This test is not parallel, because TestSink redirects the process log for its duration.
func TestBashOutputCapReportsTheDroppedBytesToTheLog(t *testing.T) {
	buf, restore := logging.TestSink()
	defer restore()

	worktree := t.TempDir()
	te := newToolTestExecutor(t, worktree)
	task := makeTestTask(t, te.store, "proj-cutlog", "task-cutlog")

	te.executeTool(context.Background(), types.ToolBash,
		map[string]any{"command": "yes filler-line | head -n 200000", "timeout": float64(30)},
		worktree, task, &executionState{}, nil)

	got := buf.String()
	if !strings.Contains(got, "component=tools") || !strings.Contains(got, "task_id=task-cutlog") {
		t.Fatalf("the cut must be attributable, got %q", got)
	}
	if !strings.Contains(got, strconv.Itoa(maxToolResultBytes)) {
		t.Errorf("the report should name the cap it applied, got %q", got)
	}
	dropped := 0
	if i := strings.Index(got, "dropped "); i >= 0 {
		for _, field := range strings.Fields(got[i:]) {
			if n, err := strconv.Atoi(field); err == nil {
				dropped = n
				break
			}
		}
	}
	if dropped <= 0 {
		t.Errorf("a cut must report the bytes it dropped, got %q", got)
	}
}
