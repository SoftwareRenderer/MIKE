package protocol

// What the LSP method owes the wire: its ID is 0x0A and stays 0x0A, and the raw JSON-RPC message it
// forwards arrives byte for byte — Raw is a raw-JSON type, so a plain []byte encoding would reach the
// language server as base64. Frame framing itself is protocol_test.go's; a start (command + args) is
// driven over the real dispatch path by the agent's lsp_test.go.

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"testing"
)

func TestLSPMethodConstant(t *testing.T) {
	if MethodLSP != 0x0A {
		t.Errorf("MethodLSP = 0x%02X, want 0x0A", MethodLSP)
	}
}

func TestLSPParams_RawMessage(t *testing.T) {
	rawJSON := jsontext.Value(`{"jsonrpc":"2.0","id":42,"method":"textDocument/diagnostics"}`)
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
