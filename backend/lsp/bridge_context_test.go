package lsp

import (
	"context"
	"encoding/json/jsontext"
	"io"
	"testing"
	"time"

	"mike/pkg/bridge"
)

// A bridge whose peer never reads is a language server that stopped answering. This pins that it is
// the caller's context, not a minted ceiling, that ends the call.

// wedgedBridge returns a live bridge client that will never make progress. Nothing drains the
// write half, so Write blocks; nothing writes to the read half, so the readLoop blocks and
// IsAlive stays true — which is what makes the caller's deadline the only thing that can end
// a call.
func wedgedBridge(t *testing.T) *bridge.Client {
	t.Helper()
	_, writeEnd := io.Pipe() // its reader never reads, so every Write blocks forever
	readEnd, _ := io.Pipe()  // nothing ever writes to it, so the readLoop blocks forever
	cl := bridge.NewClient(writeEnd, readEnd)
	t.Cleanup(func() {
		_ = writeEnd.Close()
		_ = readEnd.Close()
		cl.Close()
	})
	if !cl.IsAlive() {
		t.Fatal("wedged bridge fixture reports dead; the test would prove nothing")
	}
	return cl
}

func wedgedManager(t *testing.T) (*ExtensionLSPManager, *LSPProcess) {
	t.Helper()
	m := &ExtensionLSPManager{
		clients:  map[string]*sandboxClient{},
		serverCh: make(chan jsontext.Value, 1),
	}
	m.clients[processKey("/work/tree", "go")] = newSandboxClient(wedgedBridge(t), "/work/tree", "go", "ext-1", nil)
	return m, &LSPProcess{Workspace: "/work/tree", Language: "go"}
}

// TestSendToServerEndsAtTheCallersContext: a bridge that cannot answer must be ended by the caller,
// not by a ceiling this package mints — the caller is the task that is stopping or the hover whose
// wait was called off.
func TestSendToServerEndsAtTheCallersContext(t *testing.T) {
	initialize := jsontext.Value(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)

	t.Run("the caller's deadline ends the call", func(t *testing.T) {
		m, proc := wedgedManager(t)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		start := time.Now()
		err := m.SendToServer(ctx, proc, initialize)
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("SendToServer answered a bridge that can never answer; the wedged fixture is being bypassed")
		}
		if elapsed > 3*time.Second {
			t.Errorf("SendToServer took %v: the caller's 200ms deadline must end the call, not the %v ceiling", elapsed, lspSendTimeout)
		}
	})

	t.Run("a caller that already left ends it at once", func(t *testing.T) {
		m, proc := wedgedManager(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		done := make(chan error, 1)
		go func() { done <- m.SendToServer(ctx, proc, initialize) }()

		select {
		case err := <-done:
			if err == nil {
				t.Error("a call on an already-cancelled context reported success")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("a call on an already-cancelled context is still waiting; stopping a task must end it at once")
		}
	})
}
