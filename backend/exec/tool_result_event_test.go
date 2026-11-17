package exec

import (
	"reflect"
	"strings"
	"testing"

	"mike/backend/types"
)

// The card has to tell a call the user stopped from one that failed, which is more than success
// says, so the frame carries the distinction and always carries it.
func TestBuildToolResultEvent_Interrupted(t *testing.T) {
	t.Parallel()
	interrupted := buildToolResultEvent("call_i", "bash", interruptedToolResult, false, true, 12,
		map[string]any{"command": "sleep 120"},
		`{"command":"sleep 120"}`,
		"", false, true, "msg-i")
	if interrupted["interrupted"] != true {
		t.Errorf("interrupted = %v, want true", interrupted["interrupted"])
	}
	if interrupted["success"] != false {
		t.Error("an interrupted call is not a successful one")
	}

	settled := buildToolResultEvent("call_ok", "bash", "done", true, false, 12,
		map[string]any{"command": "true"},
		`{"command":"true"}`,
		"", false, false, "msg-ok")
	if settled["interrupted"] != false {
		t.Errorf("interrupted = %v, want false: the key is always present so the frame shape is stable",
			settled["interrupted"])
	}
}

// The frame a finished tool call broadcasts. bash and read_file are previewed and carry the counts
// of what the preview left out; every other tool's answer is short by nature and is sent whole, with
// no truncation keys at all — the card shows the cut UI from those keys, so a frame that carries them
// for a write would offer a "show more" over nothing. The remaining keys are the card's identity and
// labels, and they are carried whatever the tool was.
func TestBuildToolResultEvent_Frame(t *testing.T) {
	t.Parallel()
	const marker = "... (truncated)"
	manyLines := strings.Repeat("line of output text\n", 20)
	manyCode := strings.Repeat("const x = someValue;\n", 50)

	cases := []struct {
		name      string
		toolName  string
		result    string
		success   bool
		duration  int64
		args      map[string]any
		rawArgs   string
		path      string
		sandbox   bool
		streamed  bool
		messageID string
		// expectations
		wantCut   bool // the preview was cut rather than sent whole
		wantLines int  // the ORIGINAL line count, for a cut preview
		wantBytes int  // the ORIGINAL byte count, for a cut preview
	}{
		{
			name: "bash streamed", toolName: "bash", result: manyLines, success: true, duration: 150,
			args: map[string]any{"command": "ls -la"}, rawArgs: `{"command":"ls -la"}`,
			streamed: true, messageID: "test-msg-id-123",
			wantCut: true, wantLines: 21, wantBytes: len(manyLines),
		},
		{
			name: "bash not streamed", toolName: "bash", result: manyLines, success: true, duration: 200,
			args: map[string]any{"command": "cat file.txt"}, rawArgs: `{"command":"cat file.txt"}`,
			path: "/path/to/file", sandbox: true, messageID: "test-msg-id-456",
			wantCut: true, wantLines: 21, wantBytes: len(manyLines),
		},
		{
			name: "bash short", toolName: "bash", result: "hello world\n", success: true, duration: 50,
			args: map[string]any{"command": "echo hello"}, rawArgs: `{"command":"echo hello"}`,
			streamed: true, messageID: "test-msg-short",
		},
		{
			name: "bash empty", toolName: "bash", result: "", success: true, duration: 10,
			args: map[string]any{"command": "true"}, rawArgs: `{"command":"true"}`,
			streamed: true, messageID: "empty-msg",
		},
		{
			name: "bash single long line", toolName: "bash", result: strings.Repeat("x", 2000),
			success: true, duration: 50,
			args: map[string]any{"command": "echo long"}, rawArgs: `{"command":"echo long"}`,
			messageID: "longline-msg",
			wantCut:   true, wantLines: 1, wantBytes: 2000,
		},
		{
			name: "bash arguments are carried", toolName: "bash", result: strings.Repeat("output line\n", 15),
			success: true, duration: 100,
			args:     map[string]any{"command": "ls -la /tmp", "timeout": float64(30)},
			rawArgs:  `{"command":"ls -la /tmp","timeout":30}`,
			streamed: true, messageID: "args-msg",
			wantCut: true, wantLines: 16, wantBytes: len(strings.Repeat("output line\n", 15)),
		},
		{
			name: "read_file streamed", toolName: "read_file", result: manyCode, success: true, duration: 10,
			args: map[string]any{"path": "src/large.ts"}, rawArgs: `{"path":"src/large.ts"}`,
			path: "src/large.ts", streamed: true, messageID: "readfile-msg",
			wantCut: true, wantLines: 51, wantBytes: len(manyCode),
		},
		{
			name: "read_file not streamed", toolName: "read_file", result: strings.Repeat("line of code\n", 20),
			success: true, duration: 5,
			args: map[string]any{"path": "src/file.ts"}, rawArgs: `{"path":"src/file.ts"}`,
			path: "src/file.ts", messageID: "readfile-not-streamed",
			wantCut: true, wantLines: 21, wantBytes: len(strings.Repeat("line of code\n", 20)),
		},
		{
			name: "read_file short", toolName: "read_file", result: "package main\n", success: true, duration: 3,
			args: map[string]any{"path": "main.go"}, rawArgs: `{"path":"main.go"}`,
			path: "main.go", messageID: "read-short",
		},
		{
			name: "write_file is sent whole", toolName: "write_file",
			result: "Wrote 12345 bytes to src/large-file.ts", success: true, duration: 50,
			args:      map[string]any{"path": "src/large-file.ts", "content": "long content..."},
			rawArgs:   `{"path":"src/large-file.ts","content":"long content..."}`,
			path:      "src/large-file.ts",
			messageID: "writefile-msg",
		},
		{
			name: "a tool with no answer is sent whole", toolName: "mystery_tool",
			result: "Unknown tool: mystery", success: false,
			args: map[string]any{}, rawArgs: `{}`, messageID: "unknown-msg",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			toolID := "call_" + strings.ReplaceAll(tc.name, " ", "_")
			data := buildToolResultEvent(toolID, tc.toolName, tc.result, tc.success, false, tc.duration,
				tc.args, tc.rawArgs, tc.path, tc.sandbox, tc.streamed, tc.messageID)

			if data["tool_id"] != toolID {
				t.Errorf("tool_id = %v, want %v", data["tool_id"], toolID)
			}
			if data["success"] != tc.success {
				t.Errorf("success = %v, want %v", data["success"], tc.success)
			}
			if data["duration"] != tc.duration {
				t.Errorf("duration = %v, want %v", data["duration"], tc.duration)
			}
			if data["raw_args"] != tc.rawArgs {
				t.Errorf("raw_args = %v, want %v", data["raw_args"], tc.rawArgs)
			}
			if got := data["args"]; !reflect.DeepEqual(got, tc.args) {
				t.Errorf("args = %v, want %v", got, tc.args)
			}
			if data["path"] != tc.path {
				t.Errorf("path = %v, want %v", data["path"], tc.path)
			}
			if data["sandbox"] != tc.sandbox {
				t.Errorf("sandbox = %v, want %v", data["sandbox"], tc.sandbox)
			}
			if data["interrupted"] != false {
				t.Errorf("interrupted = %v, want the key present and false", data["interrupted"])
			}

			previewed := tc.toolName == types.ToolBash || tc.toolName == types.ToolReadFile
			output, isString := data["output"].(string)
			if !isString {
				t.Fatalf("output is %T, want a string", data["output"])
			}
			if !previewed {
				if output != tc.result {
					t.Errorf("output = %q, want the tool's answer whole", output)
				}
				for _, key := range []string{"result_truncated", "original_lines", "original_bytes"} {
					if _, ok := data[key]; ok {
						t.Errorf("%s must not be in a frame for a tool that is never previewed", key)
					}
				}
				return
			}

			if data["streamed"] != tc.streamed {
				t.Errorf("streamed = %v, want %v", data["streamed"], tc.streamed)
			}
			if data["message_id"] != tc.messageID {
				t.Errorf("message_id = %v, want %v", data["message_id"], tc.messageID)
			}
			if !tc.wantCut {
				if output != tc.result {
					t.Errorf("a result under the preview limits was rewritten: %q", output)
				}
				if data["result_truncated"] != false {
					t.Errorf("result_truncated = %v, want false", data["result_truncated"])
				}
				return
			}
			if output == tc.result {
				t.Error("the preview is the whole result")
			}
			if !strings.Contains(output, marker) {
				t.Errorf("a cut preview must carry %q, got tail %q", marker, lastChars(output, 60))
			}
			if data["result_truncated"] != true {
				t.Errorf("result_truncated = %v, want true", data["result_truncated"])
			}
			if data["original_lines"] != tc.wantLines {
				t.Errorf("original_lines = %v, want the %d lines the tool really produced", data["original_lines"], tc.wantLines)
			}
			if data["original_bytes"] != tc.wantBytes {
				t.Errorf("original_bytes = %v, want the %d bytes the tool really produced", data["original_bytes"], tc.wantBytes)
			}
			// The counts are the whole reason the card can offer to show the rest, and they
			// describe the result, never the preview.
			if data["original_bytes"] != len(tc.result) {
				t.Errorf("original_bytes counts the preview, not the result")
			}
		})
	}
}

// lastChars returns the tail of s, for a failure message that must show what survived.
func lastChars(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
