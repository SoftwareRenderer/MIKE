package exec

import (
	"context"
	"strings"
	"testing"

	"mike/backend/toolresult"
)

// The host bash path writes a tool command's output straight into this process's memory;
// toolresult.Truncate caps only what the LLM sees, and runs on a string that is already whole. So the
// capture itself must stop at the limit, and only past it — a command that fits comes back byte for
// byte, and the live stream is never capped, or a long build silently stops painting.
func TestExecCommandStreamCapsTheCaptureNotTheRun(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		command string
		// wantStreamed is the minimum number of lines the live callback must have seen.
		wantStreamed int
		wantCapped   bool
		want         string // the exact output, when nothing was capped
	}{
		{
			// ~3 MiB of output: three times the capture cap.
			name:         "a command that prints megabytes",
			command:      "yes filler-line | head -n 400000",
			wantStreamed: 1000,
			wantCapped:   true,
		},
		{
			name:    "a command that prints two lines",
			command: "printf 'one\ntwo\n'",
			want:    "one\ntwo\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			streamed := 0
			out, err := execCommandStream(context.Background(), t.TempDir(), tc.command, 30,
				func(string) { streamed++ })
			if err != nil {
				t.Fatalf("execCommandStream: %v", err)
			}

			if !tc.wantCapped {
				if out != tc.want {
					t.Errorf("small output altered: %q, want %q", out, tc.want)
				}
				return
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
			if streamed < tc.wantStreamed {
				t.Errorf("only %d lines reached the live stream; the cap belongs to the result, not the UI", streamed)
			}
		})
	}
}
