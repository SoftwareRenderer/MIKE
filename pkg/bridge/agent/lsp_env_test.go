package main

// The language server's environment is the container's, once, at creation. This pins that the
// bridge protocol has no second door for it: no field on the wire, and nothing applied to the
// spawned process. A per-start environment would be a second source of truth for where a server's
// dependencies are — and it would be a source the extension's own environment check never sees, so
// the check and the server could disagree about the same workspace with nothing on screen changed.

import (
	"encoding/json"
	"strings"
	"testing"

	"kanban/pkg/bridge/protocol"
)

func TestLSPStartCarriesNoEnvironment(t *testing.T) {
	raw, err := json.Marshal(protocol.LSPParams{Command: "gopls", Args: []string{"serve"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "env") || strings.Contains(string(raw), "Env") {
		t.Errorf("the start request grew an environment field: %s", raw)
	}

	// And a start request that arrives carrying one anyway (an older host, say) must not acquire
	// behaviour it does not have: unknown keys are ignored by the decoder, so the server starts
	// with the container environment exactly as it would otherwise.
	var p protocol.LSPParams
	if err := json.Unmarshal([]byte(`{"raw":null,"command":"gopls","env":["GOMODCACHE=/elsewhere"]}`), &p); err != nil {
		t.Fatalf("a request with an unknown key must still decode: %v", err)
	}
	if p.Command != "gopls" {
		t.Errorf("command = %q, want gopls", p.Command)
	}

	sess, err := newLSPSession("/bin/sleep", []string{"30"})
	if err != nil {
		t.Fatalf("newLSPSession: %v", err)
	}
	defer sess.close()
	if sess.cmd.Env != nil {
		t.Errorf("cmd.Env = %d entries, want nil so the process inherits the container environment alone", len(sess.cmd.Env))
	}
}
