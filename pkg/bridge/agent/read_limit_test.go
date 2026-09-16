package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/pkg/bridge/protocol"
)

// runReadRequest drives handleRead over a net.Pipe and returns every frame the agent wrote,
// stopping at the Result or Error that ends the response.
func runReadRequest(t *testing.T, path string) []*protocol.Frame {
	t.Helper()
	h := newHandler()
	h.closed.Store(false) // caller marks the connection ready (see newHandler)
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	params, err := json.Marshal(protocol.ReadParams{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	req := &protocol.Request{MsgID: 3, Method: protocol.MethodRead, Params: params}

	go h.dispatch(serverConn, req)

	var frames []*protocol.Frame
	for {
		frame, readErr := protocol.ReadFrame(clientConn)
		if readErr != nil {
			return frames
		}
		frames = append(frames, frame)
		if frame.Type == protocol.TypeResult || frame.Type == protocol.TypeError {
			return frames
		}
	}
}

// The guest holds the file; the host is what runs out of memory. handleRead answers with one
// error frame, so the bytes never leave the container.
func TestHandleReadRefusesAFilePastTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.bin")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	// Sparse: the bridge reads the declared size, not the disk blocks.
	if err := f.Truncate(protocol.MaxReadBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	frames := runReadRequest(t, path)

	if len(frames) != 1 {
		t.Fatalf("want one error frame, got %d frames", len(frames))
	}
	if frames[0].Type != protocol.TypeError {
		t.Fatalf("frame type = %s, want Error", protocol.MessageTypeName(frames[0].Type))
	}
	if !strings.Contains(frames[0].ErrorMsg(), "limit") {
		t.Errorf("the refusal should name the limit, got: %s", frames[0].ErrorMsg())
	}
}

// The refusal is a ceiling, not a disabled tool.
func TestHandleReadStreamsAFileWithinTheLimit(t *testing.T) {
	body := strings.Repeat("hello\n", 5000)
	path := filepath.Join(t.TempDir(), "small.txt")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	frames := runReadRequest(t, path)

	var got strings.Builder
	var sawResult bool
	for _, f := range frames {
		switch f.Type {
		case protocol.TypeChunk:
			got.Write(f.Payload)
		case protocol.TypeResult:
			sawResult = true
		case protocol.TypeError:
			t.Fatalf("unexpected error frame: %s", f.ErrorMsg())
		}
	}
	if !sawResult {
		t.Fatalf("no result frame after %d frames", len(frames))
	}
	if got.String() != body {
		t.Errorf("streamed %d bytes, want %d", got.Len(), len(body))
	}
}
