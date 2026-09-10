package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestLSPMethodConstant(t *testing.T) {
	if MethodLSP != 0x0A {
		t.Errorf("MethodLSP = 0x%02X, want 0x0A", MethodLSP)
	}
}

func TestMethodString_LSP(t *testing.T) {
	if got := methodString(MethodLSP); got != "lsp" {
		t.Errorf("methodString(MethodLSP) = %q, want %q", got, "lsp")
	}
}

func TestNewRequest_LSP(t *testing.T) {
	rawJSON := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"rootUri":"file:///workspace"}}`)
	// Wrap in LSPParams for the request
	params := LSPParams{Raw: rawJSON}
	paramsJSON, _ := json.Marshal(params)
	frame := NewRequest(1, MethodLSP, paramsJSON)

	if frame.MsgID != 1 {
		t.Errorf("MsgID = %d, want 1", frame.MsgID)
	}
	if frame.Type != TypeRequest {
		t.Errorf("Type = %s, want Request", MessageTypeName(frame.Type))
	}

	// Parse back
	req, err := ParseRequest(frame)
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	if req.Method != MethodLSP {
		t.Errorf("Method = %s, want lsp", req.MethodString())
	}

	// Verify params
	var got LSPParams
	if err := json.Unmarshal(req.Params, &got); err != nil {
		t.Fatalf("Unmarshal params: %v", err)
	}
	if string(got.Raw) != string(rawJSON) {
		t.Errorf("Raw = %s, want %s", got.Raw, rawJSON)
	}
}

func TestLSPParams_StartCommand(t *testing.T) {
	params := LSPParams{
		Command: "gopls",
		Args:    []string{"serve"},
	}
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got LSPParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Command != "gopls" {
		t.Errorf("Command = %q, want gopls", got.Command)
	}
	if len(got.Args) != 1 || got.Args[0] != "serve" {
		t.Errorf("Args = %v, want [serve]", got.Args)
	}
}

func TestLSPParams_RawMessage(t *testing.T) {
	rawJSON := json.RawMessage(`{"jsonrpc":"2.0","id":42,"method":"textDocument/diagnostics"}`)
	params := LSPParams{Raw: rawJSON}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got LSPParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if string(got.Raw) != string(rawJSON) {
		t.Errorf("Raw = %s, want %s", got.Raw, rawJSON)
	}
}

func TestParseRequest_LSP(t *testing.T) {
	rawJSON := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	frame := NewRequest(100, MethodLSP, rawJSON)

	// Write frame to buffer
	var buf bytes.Buffer
	if err := WriteFrame(&buf, frame); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	f, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	req, err := ParseRequest(f)
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	if req.MsgID != 100 {
		t.Errorf("MsgID = %d, want 100", req.MsgID)
	}
	if req.Method != MethodLSP {
		t.Errorf("Method = %s, want lsp", req.MethodString())
	}
}

func TestLSPParams_ComplexMessage(t *testing.T) {
	// Complex JSON-RPC message with nested params
	rawJSON := json.RawMessage(`{
		"jsonrpc": "2.0",
		"id": 1,
		"method": "initialize",
		"params": {
			"rootUri": "file:///workspace",
			"capabilities": {
				"textDocument": {
					"completion": {
						"completionItem": {
							"snippetSupport": true
						}
					}
				}
			},
			"initializationOptions": {
				"hover": {"actions": {"enabled": true}},
				"usePlaceholders": true,
				"staticcheck": true
			}
		}
	}`)

	params := LSPParams{Raw: rawJSON}
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got LSPParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// Verify the raw message is preserved exactly
	var orig, parsed map[string]any
	if err := json.Unmarshal(rawJSON, &orig); err != nil {
		t.Fatalf("Unmarshal orig: %v", err)
	}
	if err := json.Unmarshal(got.Raw, &parsed); err != nil {
		t.Fatalf("Unmarshal parsed: %v", err)
	}
	if orig["method"] != parsed["method"] {
		t.Errorf("method = %v, want %v", parsed["method"], orig["method"])
	}
	if orig["id"] != parsed["id"] {
		t.Errorf("id = %v, want %v", parsed["id"], orig["id"])
	}
}
