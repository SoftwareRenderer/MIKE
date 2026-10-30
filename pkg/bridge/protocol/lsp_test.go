package protocol

// What the LSP method owes the wire: its ID is 0x0A and stays 0x0A, and the two shapes of LSPParams
// — a start (command + args) and a forwarded message (Raw) — arrive with every field they carry.
// Frame framing itself is protocol_test.go's; driving a session is the agent's.

import (
	"encoding/json"
	"testing"
)

func TestLSPMethodConstant(t *testing.T) {
	if MethodLSP != 0x0A {
		t.Errorf("MethodLSP = 0x%02X, want 0x0A", MethodLSP)
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
		t.Errorf("Command = %q, want %q", got.Command, "gopls")
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
