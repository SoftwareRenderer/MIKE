package exec

import (
	"context"
	"strings"
	"testing"

	"kanban/backend/toolresult"
)

// The host bash path writes a tool command's output straight into this process's memory;
// toolresult.Truncate caps only what the LLM sees, and runs on a string that is already whole.

func TestExecCommandStreamCapsHugeOutput(t *testing.T) {
	dir := t.TempDir()

	streamed := 0
	// ~3 MiB of output: three times the capture cap.
	out, err := execCommandStream(context.Background(), dir, "yes filler-line | head -n 400000", 30, func(string) { streamed++ })
	if err != nil {
		t.Fatalf("execCommandStream: %v", err)
	}

	marker := toolresult.CaptureMarker(0)
	if len(out) > toolresult.MaxCaptureBytes+len(marker)+64 {
		t.Errorf("result is %d bytes, past the cap plus its marker — the capture is not bounded", len(out))
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("a cut result must say it was cut, tail: %q", out[min(120, len(out)):])
	}
	if !strings.HasPrefix(out, "filler-line") {
		t.Error("the head of the output must survive the cap")
	}
	// The UI follows a running command line by line; capping the result must not stop the
	// stream, or a long build silently stops painting.
	if streamed < 1000 {
		t.Errorf("only %d lines reached the live stream; the cap belongs to the result, not the UI", streamed)
	}
}

func TestExecCommandStreamLeavesSmallOutputAlone(t *testing.T) {
	out, err := execCommandStream(context.Background(), t.TempDir(), "printf 'one\\ntwo\\n'", 10, nil)
	if err != nil {
		t.Fatalf("execCommandStream: %v", err)
	}
	if out != "one\ntwo\n" {
		t.Errorf("small output altered: %q", out)
	}
}
