package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"kanban/pkg/bridge/protocol"
)

// committedBridge is the binary the containers actually run, staged at sandbox/sandbox-bridge.
var committedBridge = filepath.Join("..", "..", "..", "sandbox", "sandbox-bridge")

// What ships is not what is in this directory: bridge-check compares the sources an artifact was
// built from, not what they do, so an unbuilt bridge passes it while containers run the old rule.
func TestCommittedBridgeBinaryEnforcesTheReadLimit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("the committed bridge is built for linux, running on %s", runtime.GOOS)
	}
	if _, err := os.Stat(committedBridge); err != nil {
		t.Skipf("no committed sandbox-bridge to run (%v) — run `make bridge`", err)
	}

	t.Run("oversized read is refused before anything streams", func(t *testing.T) {
		huge := filepath.Join(t.TempDir(), "huge.bin")
		f, err := os.OpenFile(huge, os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		// Sparse: the bridge reads the declared size, not the disk blocks.
		if err := f.Truncate(protocol.MaxReadBytes + 1); err != nil {
			t.Fatal(err)
		}
		f.Close()

		frames := readThroughBridge(t, huge, 8)
		if len(frames) == 0 {
			t.Fatal("the committed bridge answered nothing")
		}
		for _, fr := range frames {
			if fr.Type == protocol.TypeChunk {
				t.Fatalf("the committed bridge streamed %d bytes out of the container for a file past the limit", len(fr.Payload))
			}
		}
		if first := frames[0]; first.Type != protocol.TypeError || !strings.Contains(first.ErrorMsg(), "limit") {
			t.Errorf("first frame = %s %q, want an error naming the limit",
				protocol.MessageTypeName(first.Type), first.ErrorMsg())
		}
	})

	t.Run("ordinary read still streams", func(t *testing.T) {
		small := filepath.Join(t.TempDir(), "small.txt")
		if err := os.WriteFile(small, []byte("hello\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		var got strings.Builder
		var sawResult bool
		for _, fr := range readThroughBridge(t, small, 8) {
			switch fr.Type {
			case protocol.TypeChunk:
				got.Write(fr.Payload)
			case protocol.TypeResult:
				sawResult = true
			case protocol.TypeError:
				t.Fatalf("unexpected error frame: %s", fr.ErrorMsg())
			}
		}
		if !sawResult || got.String() != "hello\n" {
			t.Errorf("got %q (result frame: %v), want \"hello\\n\" closed by a result", got.String(), sawResult)
		}
	})
}

// readThroughBridge asks one freshly started bridge for one file and returns the frames it
// answers, stopping at the Result/Error that ends the response or after limit frames. A bridge
// that ignores the limit writes into a pipe nobody drains, so the wait is bounded.
func readThroughBridge(t *testing.T, path string, limit int) []*protocol.Frame {
	t.Helper()

	params, err := json.Marshal(protocol.ReadParams{Path: path})
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(committedBridge)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start committed bridge: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	if err := protocol.WriteFrame(stdin, protocol.NewRequest(9, protocol.MethodRead, params)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	type frameOrErr struct {
		frame *protocol.Frame
		err   error
	}
	// Nothing drains stdout after this returns: a bridge that overruns is killed, not read.
	ch := make(chan frameOrErr, limit+1)
	go func() {
		for {
			frame, err := protocol.ReadFrame(stdout)
			if err != nil {
				ch <- frameOrErr{err: err}
				return
			}
			ch <- frameOrErr{frame: frame}
			if frame.Type == protocol.TypeResult || frame.Type == protocol.TypeError {
				return
			}
		}
	}()

	var frames []*protocol.Frame
	for len(frames) < limit {
		select {
		case got := <-ch:
			if got.err != nil {
				return frames
			}
			frames = append(frames, got.frame)
			if got.frame.Type == protocol.TypeResult || got.frame.Type == protocol.TypeError {
				return frames
			}
		case <-time.After(10 * time.Second):
			return frames
		}
	}
	return frames
}
