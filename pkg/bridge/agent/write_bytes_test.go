package main

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/pkg/bridge/protocol"
)

// runBytesWrite drives handleWriteBytes over a net.Pipe, which is unbuffered: the handler writes
// before the test reads, so the dispatch runs concurrently.
func runBytesWrite(t *testing.T, params any) *protocol.Frame {
	t.Helper()
	h := newHandler()
	h.closed.Store(false) // caller marks the connection ready (see newHandler)
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	payload, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	go h.dispatch(serverConn, &protocol.Request{MsgID: 7, Method: protocol.MethodWriteBytes, Params: payload})

	frame, readErr := protocol.ReadFrame(clientConn)
	if readErr != nil {
		t.Fatalf("read response: %v", readErr)
	}
	return frame
}

func TestWriteBytesPreservesBytesThatAreNotText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uploads", "shot.png")
	payload := []byte{0x89, 0x50, 0x4e, 0x47, 0xff, 0xfe, 0x00, 0x80, 0xc3, 0x28, 0xa0}

	frame := runBytesWrite(t, protocol.WriteBytesParams{Path: path, Data: payload})
	if frame.Type != protocol.TypeResult {
		t.Fatalf("want a Result, got %s: %s", protocol.MessageTypeName(frame.Type), string(frame.Payload))
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("bytes changed on the way through the frame:\n got %x\nwant %x", got, payload)
	}
}

func TestTextWriteStillWritesText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")

	h := newHandler()
	h.closed.Store(false)
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	params, _ := json.Marshal(protocol.WriteParams{Path: path, Content: "héllo"})
	go h.dispatch(serverConn, &protocol.Request{MsgID: 1, Method: protocol.MethodWrite, Params: params})
	if _, err := protocol.ReadFrame(clientConn); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "héllo" {
		t.Errorf("the text write changed its content: %q", got)
	}
}

func TestWriteBytesRefusesAPayloadPastTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "too-big.bin")

	// The bound is on the decoded payload, so unlike the read limit's sparse stand-in this one
	// has to exist: 20 MB of zeros is ~27 MB of base64, inside the frame cap.
	frame := runBytesWrite(t, protocol.WriteBytesParams{Path: path, Data: make([]byte, protocol.MaxWriteBytes+1)})

	if frame.Type != protocol.TypeError {
		t.Fatalf("an oversized payload must be refused, got %s", protocol.MessageTypeName(frame.Type))
	}
	if !strings.Contains(string(frame.Payload), "limit") {
		t.Errorf("the refusal should name the limit: %s", frame.Payload)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a refused write left %s behind (%v)", path, err)
	}
}

func TestWriteBytesRequiresAPath(t *testing.T) {
	frame := runBytesWrite(t, protocol.WriteBytesParams{Data: []byte{0x01}})
	if frame.Type != protocol.TypeError {
		t.Fatalf("want an Error, got %s", protocol.MessageTypeName(frame.Type))
	}
}

// The branch a stale guest takes: see protocol.MethodWriteBytes.
func TestUnknownMethodIsRefusedNotMisrouted(t *testing.T) {
	h := newHandler()
	h.closed.Store(false)
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	go h.dispatch(serverConn, &protocol.Request{MsgID: 9, Method: 0x0C, Params: []byte("{}")})

	frame, err := protocol.ReadFrame(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Type != protocol.TypeError || !strings.Contains(string(frame.Payload), "unknown method") {
		t.Fatalf("want an unknown-method error, got %s: %s", protocol.MessageTypeName(frame.Type), frame.Payload)
	}
}
