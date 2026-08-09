package exec

import (
	"strings"
	"testing"
)

// --- buildToolResultEvent Tests ---

func TestBuildToolResultEvent_BashStreamed(t *testing.T) {
	// Large bash output that will be truncated
	largeOutput := strings.Repeat("line of output text\n", 20)
	msgID := "test-msg-id-123"
	toolID := "call_abc123"

	data := buildToolResultEvent(toolID, "bash", largeOutput, true, 150,
		map[string]any{"command": "ls -la"},
		`{"command":"ls -la"}`,
		"", false, true, msgID)

	// Should contain standard fields
	if data["tool_id"] != toolID {
		t.Errorf("tool_id = %v, want %v", data["tool_id"], toolID)
	}
	if data["success"] != true {
		t.Errorf("success = %v, want true", data["success"])
	}
	if data["duration"] != int64(150) {
		t.Errorf("duration = %v, want 150", data["duration"])
	}

	// Should have truncated output (not full)
	output, ok := data["output"].(string)
	if !ok {
		t.Fatal("output should be a string")
	}
	if output == largeOutput {
		t.Error("bash output should be truncated, not full")
	}
	if !strings.Contains(output, "... (truncated)") {
		t.Error("output should contain truncation marker")
	}

	// Truncation metadata
	if data["result_truncated"] != true {
		t.Error("result_truncated should be true")
	}
	if data["original_lines"] != 21 {
		t.Errorf("original_lines = %v, want 21", data["original_lines"])
	}
	if data["original_bytes"] != len(largeOutput) {
		t.Errorf("original_bytes = %v, want %d", data["original_bytes"], len(largeOutput))
	}
	if data["streamed"] != true {
		t.Error("streamed should be true")
	}
	if data["message_id"] != msgID {
		t.Errorf("message_id = %v, want %v", data["message_id"], msgID)
	}
}

func TestBuildToolResultEvent_BashNotStreamed(t *testing.T) {
	// Large bash output but not streamed
	largeOutput := strings.Repeat("line of output text\n", 20)
	msgID := "test-msg-id-456"
	toolID := "call_def456"

	data := buildToolResultEvent(toolID, "bash", largeOutput, true, 200,
		map[string]any{"command": "cat file.txt"},
		`{"command":"cat file.txt"}`,
		"/path/to/file", true, false, msgID)

	output, ok := data["output"].(string)
	if !ok {
		t.Fatal("output should be a string")
	}
	if output == largeOutput {
		t.Error("bash output should be truncated")
	}

	if data["streamed"] != false {
		t.Error("streamed should be false")
	}
	if data["sandbox"] != true {
		t.Error("sandbox should be true")
	}
	if data["path"] != "/path/to/file" {
		t.Errorf("path = %v, want /path/to/file", data["path"])
	}
}

func TestBuildToolResultEvent_BashShortOutput(t *testing.T) {
	// Short bash output should not be truncated
	shortOutput := "hello world\n"
	msgID := "test-msg-short"
	toolID := "call_short"

	data := buildToolResultEvent(toolID, "bash", shortOutput, true, 50,
		map[string]any{"command": "echo hello"},
		`{"command":"echo hello"}`,
		"", false, true, msgID)

	output, ok := data["output"].(string)
	if !ok {
		t.Fatal("output should be a string")
	}
	if output != shortOutput {
		t.Errorf("short output should not be truncated: got %q, want %q", output, shortOutput)
	}
	if data["result_truncated"] != false {
		t.Error("result_truncated should be false for short output")
	}
}

func TestBuildToolResultEvent_ReadFileStreamed(t *testing.T) {
	// Large read_file output that will be truncated
	largeContent := strings.Repeat("const x = someValue;\n", 50)
	msgID := "readfile-msg"
	toolID := "call_read"

	data := buildToolResultEvent(toolID, "read_file", largeContent, true, 10,
		map[string]any{"path": "src/large.ts"},
		`{"path":"src/large.ts"}`,
		"src/large.ts", false, true, msgID)

	output, ok := data["output"].(string)
	if !ok {
		t.Fatal("output should be a string")
	}
	if output == largeContent {
		t.Error("read_file output should be truncated")
	}
	if !strings.Contains(output, "... (truncated)") {
		t.Error("output should contain truncation marker")
	}
	if data["result_truncated"] != true {
		t.Error("result_truncated should be true")
	}
	if data["streamed"] != true {
		t.Error("streamed should be true")
	}
	if data["message_id"] != msgID {
		t.Errorf("message_id = %v, want %v", data["message_id"], msgID)
	}
}

func TestBuildToolResultEvent_ReadFileNotStreamed(t *testing.T) {
	// Large read_file output, not streamed
	largeContent := strings.Repeat("line of code\n", 20)
	msgID := "readfile-not-streamed"
	toolID := "call_read2"

	data := buildToolResultEvent(toolID, "read_file", largeContent, true, 5,
		map[string]any{"path": "src/file.ts"},
		`{"path":"src/file.ts"}`,
		"src/file.ts", false, false, msgID)

	output, ok := data["output"].(string)
	if !ok {
		t.Fatal("output should be a string")
	}
	if output == largeContent {
		t.Error("read_file output should be truncated")
	}
	if data["streamed"] != false {
		t.Error("streamed should be false")
	}
}

func TestBuildToolResultEvent_ReadFileShort(t *testing.T) {
	// Short read_file output should not be truncated
	shortContent := "package main\n"
	msgID := "read-short"
	toolID := "call_read_short"

	data := buildToolResultEvent(toolID, "read_file", shortContent, true, 3,
		map[string]any{"path": "main.go"},
		`{"path":"main.go"}`,
		"main.go", false, false, msgID)

	output, ok := data["output"].(string)
	if !ok {
		t.Fatal("output should be a string")
	}
	if output != shortContent {
		t.Errorf("short read_file should not be truncated: got %q, want %q", output, shortContent)
	}
	if data["result_truncated"] != false {
		t.Error("result_truncated should be false for short output")
	}
}

func TestBuildToolResultEvent_WriteFile(t *testing.T) {
	// write_file is NOT bash/read_file — should have full output, no truncation fields
	output := "Wrote 12345 bytes to src/large-file.ts"
	msgID := "writefile-msg"
	toolID := "call_write"

	data := buildToolResultEvent(toolID, "write_file", output, true, 50,
		map[string]any{"path": "src/large-file.ts", "content": "long content..."},
		`{"path":"src/large-file.ts","content":"long content..."}`,
		"src/large-file.ts", false, false, msgID)

	if data["output"] != output {
		t.Errorf("write_file output should be full: got %q, want %q", data["output"], output)
	}
	// No truncation fields for non-bash/read_file tools
	if _, ok := data["result_truncated"]; ok {
		t.Error("write_file should not have result_truncated field")
	}
	if _, ok := data["original_lines"]; ok {
		t.Error("write_file should not have original_lines field")
	}
	if _, ok := data["original_bytes"]; ok {
		t.Error("write_file should not have original_bytes field")
	}
}

func TestBuildToolResultEvent_EditFile(t *testing.T) {
	// edit_file is NOT bash/read_file — should have full output
	output := "Edited 1 occurrences in src/file.ts"
	msgID := "editfile-msg"
	toolID := "call_edit"

	data := buildToolResultEvent(toolID, "edit_file", output, true, 30,
		map[string]any{"path": "src/file.ts"},
		`{"path":"src/file.ts"}`,
		"src/file.ts", false, false, msgID)

	if data["output"] != output {
		t.Errorf("edit_file output should be full: got %q, want %q", data["output"], output)
	}
	if _, ok := data["result_truncated"]; ok {
		t.Error("edit_file should not have result_truncated field")
	}
}

func TestBuildToolResultEvent_RequestReview(t *testing.T) {
	// request_review — not bash/read_file
	output := "Task complete"
	msgID := "review-msg"
	toolID := "call_review"

	data := buildToolResultEvent(toolID, "request_review", output, true, 0,
		map[string]any{},
		`{}`,
		"", false, false, msgID)

	if data["output"] != output {
		t.Errorf("request_review output should be full: got %q, want %q", data["output"], output)
	}
	if _, ok := data["result_truncated"]; ok {
		t.Error("request_review should not have result_truncated field")
	}
}

func TestBuildToolResultEvent_AskQuestion(t *testing.T) {
	// ask_question — not bash/read_file
	output := "What framework should I use?"
	msgID := "ask-msg"
	toolID := "call_ask"

	data := buildToolResultEvent(toolID, "ask_question", output, true, 0,
		map[string]any{"question": "What framework should I use?"},
		`{"question":"What framework should I use?"}`,
		"", false, false, msgID)

	if data["output"] != output {
		t.Errorf("ask_question output should be full: got %q, want %q", data["output"], output)
	}
	if _, ok := data["result_truncated"]; ok {
		t.Error("ask_question should not have result_truncated field")
	}
}

func TestBuildToolResultEvent_UnknownTool(t *testing.T) {
	// Unknown tool — not bash/read_file
	output := "Unknown tool: mystery"
	msgID := "unknown-msg"
	toolID := "call_unknown"

	data := buildToolResultEvent(toolID, "mystery_tool", output, false, 0,
		map[string]any{},
		`{}`,
		"", false, false, msgID)

	if data["output"] != output {
		t.Errorf("unknown tool output should be full: got %q, want %q", data["output"], output)
	}
	if data["success"] != false {
		t.Error("success should be false")
	}
	if _, ok := data["result_truncated"]; ok {
		t.Error("unknown tool should not have result_truncated field")
	}
}

func TestBuildToolResultEvent_BashWithArgs(t *testing.T) {
	// Verify args and raw_args are preserved
	largeOutput := strings.Repeat("output line\n", 15)
	msgID := "args-msg"
	toolID := "call_args"
	args := map[string]any{"command": "ls -la /tmp", "timeout": float64(30)}
	rawArgs := `{"command":"ls -la /tmp","timeout":30}`

	data := buildToolResultEvent(toolID, "bash", largeOutput, true, 100,
		args, rawArgs, "", false, true, msgID)

	argsData, ok := data["args"].(map[string]any)
	if !ok {
		t.Fatal("args should be a map")
	}
	if argsData["command"] != args["command"] {
		t.Errorf("args.command = %v, want %v", argsData["command"], args["command"])
	}
	if data["raw_args"] != rawArgs {
		t.Errorf("raw_args = %v, want %v", data["raw_args"], rawArgs)
	}
}

func TestBuildToolResultEvent_BashSingleLongLine(t *testing.T) {
	// Single very long line (>1024 bytes) — tests rune-slicing path
	toolID := "call_longline"
	msgID := "longline-msg"
	longLine := strings.Repeat("x", 2000)

	data := buildToolResultEvent(toolID, "bash", longLine, true, 50,
		map[string]any{"command": "echo long"},
		`{"command":"echo long"}`,
		"", false, false, msgID)

	output, ok := data["output"].(string)
	if !ok {
		t.Fatal("output should be a string")
	}
	if output == longLine {
		t.Error("single long line should be truncated")
	}
	if !strings.Contains(output, "... (truncated)") {
		t.Error("output should contain truncation marker")
	}
	if data["result_truncated"] != true {
		t.Error("result_truncated should be true")
	}
	// Original metadata should reflect the full output
	if data["original_lines"] != 1 {
		t.Errorf("original_lines = %v, want 1", data["original_lines"])
	}
	if data["original_bytes"] != 2000 {
		t.Errorf("original_bytes = %v, want 2000", data["original_bytes"])
	}
}

func TestBuildToolResultEvent_BashEmptyOutput(t *testing.T) {
	// Empty output — should not truncate
	toolID := "call_empty"
	msgID := "empty-msg"

	data := buildToolResultEvent(toolID, "bash", "", true, 10,
		map[string]any{"command": "true"},
		`{"command":"true"}`,
		"", false, true, msgID)

	output, ok := data["output"].(string)
	if !ok {
		t.Fatal("output should be a string")
	}
	if output != "" {
		t.Errorf("empty output should remain empty, got %q", output)
	}
	if data["result_truncated"] != false {
		t.Error("result_truncated should be false for empty output")
	}
}
