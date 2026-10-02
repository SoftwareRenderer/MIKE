package bridge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"mike/pkg/bridge/protocol"
)

// Nothing about a chunk stream says how big it will be, and the peer chose the file, so the
// client stops while collecting rather than after assembling.
func TestReadFileRefusesAStreamPastTheLimit(t *testing.T) {
	const chunkSize = 64 * 1024
	chunks := protocol.MaxReadBytes/chunkSize + 8 // deliberately over

	client, cleanup := mockStreamingAgent(t, chunks, chunkSize, time.Millisecond)
	defer cleanup()

	content, _, err := client.ReadFile(context.Background(), "huge.bin")
	if !errors.Is(err, protocol.ErrResponseTooLarge) {
		t.Fatalf("want ErrResponseTooLarge, got %v (content=%d bytes)", err, len(content))
	}
	if content != "" {
		t.Errorf("a refused read returned %d bytes of content", len(content))
	}
	if !strings.Contains(err.Error(), strconv.Itoa(protocol.MaxReadBytes)) {
		t.Errorf("the refusal must name the limit, got: %v", err)
	}
}

// A faster peer outruns the queue instead, and losing a frame must be an error: a short file answered
// as a success would have a task edit a file it believes it read whole.
func TestReadFileNeverReturnsAShortBodyWhenTheClientFallsBehind(t *testing.T) {
	const chunkSize = 64 * 1024
	const chunks = protocol.MaxReadBytes/chunkSize + 8

	client, cleanup := mockStreamingAgent(t, chunks, chunkSize, 0)
	defer cleanup()

	content, size, err := client.ReadFile(context.Background(), "huge.bin")
	if err == nil {
		t.Fatalf("a %d-byte stream answered as success: %d bytes of content, agent reported %d",
			chunks*chunkSize, len(content), size)
	}
	if content != "" {
		t.Errorf("a failed read returned %d bytes of content", len(content))
	}
}

// mockStreamingAgent answers every request with `chunks` frames of `size` bytes and then a
// Result reporting the whole stream. A pause between frames keeps the client's queue drained, so
// the caller under test is the size budget rather than backpressure.
func mockStreamingAgent(t *testing.T, chunks, size int, pause time.Duration) (*Client, func()) {
	t.Helper()
	agentReadPr, agentReadPw := io.Pipe()   // client → mock agent
	clientReadPr, clientReadPw := io.Pipe() // mock agent → client

	go func() {
		frame, err := protocol.ReadFrame(agentReadPr)
		if err != nil {
			return
		}
		payload := bytes.Repeat([]byte("f"), size)
		for i := 0; i < chunks; i++ {
			if pause > 0 {
				time.Sleep(pause)
			}
			if err := protocol.WriteFrame(clientReadPw, protocol.NewChunk(frame.MsgID, payload)); err != nil {
				return
			}
		}
		_ = protocol.WriteFrame(clientReadPw, protocol.NewResult(frame.MsgID, protocol.NewUint64Result(uint64(chunks*size))))
	}()

	client := NewClient(agentReadPw, clientReadPr)
	return client, func() {
		client.Close()
		_ = agentReadPr.Close()
		_ = clientReadPw.Close()
	}
}

// The budget must not break the ordinary case: a file that fits comes back complete.
func TestReadFileReturnsAStreamWithinTheLimit(t *testing.T) {
	body := strings.Repeat("line\n", 40000) // ~200 KB, several chunks

	agentReadPr, agentReadPw := io.Pipe()
	clientReadPr, clientReadPw := io.Pipe()
	client := NewClient(agentReadPw, clientReadPr)
	defer func() {
		client.Close()
		_ = agentReadPr.Close()
		_ = clientReadPw.Close()
	}()

	go func() {
		frame, err := protocol.ReadFrame(agentReadPr)
		if err != nil {
			return
		}
		for i := 0; i < len(body); i += 64 * 1024 {
			end := min(i+64*1024, len(body))
			if err := protocol.WriteFrame(clientReadPw, protocol.NewChunk(frame.MsgID, []byte(body[i:end]))); err != nil {
				return
			}
		}
		_ = protocol.WriteFrame(clientReadPw, protocol.NewResult(frame.MsgID, protocol.NewUint64Result(uint64(len(body)))))
	}()

	content, size, err := client.ReadFile(context.Background(), "ok.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if content != body || int(size) != len(body) {
		t.Errorf("got %d bytes (size %d), want %d", len(content), size, len(body))
	}
}
