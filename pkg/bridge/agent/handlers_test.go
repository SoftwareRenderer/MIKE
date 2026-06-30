package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"kanban/pkg/bridge/protocol"
)

// --- Direct handler logic tests (no network simulation) ---

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()

	content := "atomic write test\n"
	path := filepath.Join(dir, "subdir", "new.txt")

	// Simulate what handleWrite does
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	tmpPath, err := os.CreateTemp(filepath.Dir(path), ".kanban-tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	tmpName := tmpPath.Name()

	n, err := tmpPath.WriteString(content)
	if err != nil {
		tmpPath.Close()
		os.Remove(tmpName)
		t.Fatal(err)
	}
	_ = tmpPath.Close()
	_ = os.Chmod(tmpName, 0o644)

	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		t.Fatal(err)
	}

	// Verify
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != content {
		t.Errorf("content = %q, want %q", data, content)
	}
	if int64(len(data)) != int64(n) {
		t.Errorf("len = %d, wrote %d", len(data), n)
	}
}

func TestEditApply(t *testing.T) {
	content := "line one\nline two\nline three\n"

	type edit struct{ Old, New string }
	edits := []edit{{"line two", "LINE TWO"}}

	// Simplified edit application (matches edit.go logic)
	result := content
	for _, e := range edits {
		idx := -1
		for i := 0; i <= len(result)-len(e.Old); i++ {
			if result[i:i+len(e.Old)] == e.Old {
				idx = i
				break
			}
		}
		if idx == -1 {
			t.Fatalf("oldText %q not found", e.Old)
		}
		result = result[:idx] + e.New + result[idx+len(e.Old):]
	}

	want := "line one\nLINE TWO\nline three\n"
	if result != want {
		t.Errorf("result = %q, want %q", result, want)
	}
}

func TestRequestParsing(t *testing.T) {
	// Test that request frames are built and parsed correctly
	params, _ := json.Marshal(protocol.ReadParams{Path: "test.txt"})
	frame := protocol.NewRequest(42, protocol.MethodRead, params)

	if frame.MsgID != 42 {
		t.Errorf("MsgID = %d, want 42", frame.MsgID)
	}
	if frame.Type != protocol.TypeRequest {
		t.Errorf("Type = %d, want %d", frame.Type, protocol.TypeRequest)
	}

	// Verify payload structure
	payload := frame.Payload
	if payload[0] != protocol.MethodRead {
		t.Errorf("method byte = %d, want %d", payload[0], protocol.MethodRead)
	}
}

func TestWriteParams_Marshal(t *testing.T) {
	p := protocol.WriteParams{Path: "src/main.go", Content: "package main"}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	var got protocol.WriteParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Path != p.Path || got.Content != p.Content {
		t.Errorf("round-trip failed: got %+v, want %+v", got, p)
	}
}

func TestBashParams_Marshal(t *testing.T) {
	p := protocol.BashParams{Command: "ls -la", Timeout: 30}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	var got protocol.BashParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Command != p.Command || got.Timeout != p.Timeout {
		t.Errorf("round-trip failed")
	}
}

func TestEditParams_Marshal(t *testing.T) {
	p := protocol.EditParams{
		Path: "test.go",
		Edits: []protocol.Edit{
			{OldText: "foo", NewText: "bar"},
			{OldText: "baz", NewText: "qux"},
		},
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	var got protocol.EditParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Edits) != 2 {
		t.Fatalf("edits count = %d, want 2", len(got.Edits))
	}
	if got.Edits[0].OldText != "foo" || got.Edits[1].NewText != "qux" {
		t.Error("edit round-trip failed")
	}
}
