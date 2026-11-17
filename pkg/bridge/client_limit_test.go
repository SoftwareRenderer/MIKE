package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"mike/pkg/bridge/protocol"
)

// streamingAgent answers every request with `chunks` frames of `size` bytes and then a Result
// reporting the whole stream. Frame i carries a payload naming i, so an assembled body is checked
// byte for byte rather than just for length. A pause between frames keeps the client's queue
// drained, so the caller under test is the size budget rather than backpressure.
func streamingAgent(t *testing.T, chunks, size int, pause time.Duration) (*Client, func(), []byte) {
	t.Helper()
	payloads := make([][]byte, chunks)
	var want []byte
	for i := range payloads {
		payloads[i] = []byte(fmt.Sprintf("%08d", i) + strings.Repeat("f", size-8))
		want = append(want, payloads[i]...)
	}

	agentReadPr, agentReadPw := io.Pipe()   // client → mock agent
	clientReadPr, clientReadPw := io.Pipe() // mock agent → client

	go func() {
		frame, err := protocol.ReadFrame(agentReadPr)
		if err != nil {
			return
		}
		for i := 0; i < chunks; i++ {
			if pause > 0 {
				time.Sleep(pause)
			}
			if err := protocol.WriteFrame(clientReadPw, protocol.NewChunk(frame.MsgID, payloads[i])); err != nil {
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
	}, want
}

// TestReadFileBoundsTheStream holds one budget over one chunked response, counted as the frames
// arrive rather than at assembly — a total checked when the body is put together has already cost
// the host the whole response. It is a ceiling and not a ban: a stream that fits comes back whole,
// and a stream that does not is refused naming the limit. A faster peer outruns the queue instead,
// and losing a frame must be an error: a short file answered as a success would have a task edit a
// file it believes it read whole.
func TestReadFileBoundsTheStream(t *testing.T) {
	const chunkSize = 64 * 1024

	t.Run("a stream past the limit is refused while it arrives", func(t *testing.T) {
		const chunks = protocol.MaxReadBytes/chunkSize + 8 // deliberately over

		client, cleanup, _ := streamingAgent(t, chunks, chunkSize, time.Millisecond)
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
	})

	t.Run("a client that falls behind fails rather than answering short", func(t *testing.T) {
		const chunks = protocol.MaxReadBytes/chunkSize + 8

		client, cleanup, _ := streamingAgent(t, chunks, chunkSize, 0)
		defer cleanup()

		content, size, err := client.ReadFile(context.Background(), "huge.bin")
		if err == nil {
			t.Fatalf("a %d-byte stream answered as success: %d bytes of content, agent reported %d",
				chunks*chunkSize, len(content), size)
		}
		if content != "" {
			t.Errorf("a failed read returned %d bytes of content", len(content))
		}
	})

	t.Run("a stream within the limit comes back whole", func(t *testing.T) {
		const chunks = 4 // 256 KB across four frames

		client, cleanup, want := streamingAgent(t, chunks, chunkSize, 0)
		defer cleanup()

		content, size, err := client.ReadFile(context.Background(), "ok.bin")
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if content != string(want) || int(size) != len(want) {
			t.Errorf("got %d bytes (size %d), want the %d bytes in order", len(content), size, len(want))
		}
	})
}
