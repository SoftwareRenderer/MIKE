package sandbox

import (
	"context"
	"io"
	"testing"
	"time"

	"kanban/pkg/bridge/protocol"
)

// TestClientCompiles verifies that the Client type compiles with all expected methods.
func TestClientCompiles(t *testing.T) {
	// These would not compile if the methods don't exist
	var c *Client
	_ = func() {
		_, _, _ = c.ReadFile(context.Background(), "path")
		_, _ = c.WriteFile(context.Background(), "path", "content")
		_, _ = c.EditFile(context.Background(), "path", []protocol.Edit{})
		_, _ = c.Bash(context.Background(), "cmd", 5)
		_, _ = c.ShellOpen("bash", 40, 120)
		_ = c.ShellIO(1, []byte("ls\n"))
		_ = c.ShellOutput(1)
		_ = c.ShellResize(1, 40, 120)
		_ = c.ShellClose(1)
		_ = c.Close()
	}
}

// TestBashResultHolderCompiles verifies that BashResultHolder has expected methods.
func TestBashResultHolderCompiles(t *testing.T) {
	var b *BashResultHolder
	_ = func() {
		_ = b.Output()
		_ = b.ExitCode()
	}
}

// TestBashCancelRequest verifies that bash_cancel frames are constructed and parsed correctly.
func TestBashCancelRequest(t *testing.T) {
	msgID := uint32(42)
	targetMsgID := uint32(100)

	frame := protocol.NewBashCancelRequest(msgID, targetMsgID)

	if frame.MsgID != msgID {
		t.Errorf("expected MsgID %d, got %d", msgID, frame.MsgID)
	}
	if frame.Type != protocol.TypeRequest {
		t.Errorf("expected TypeRequest, got %v", frame.Type)
	}

	params, err := protocol.ParseBashCancelParams(frame)
	if err != nil {
		t.Fatalf("ParseBashCancelParams: %v", err)
	}
	if params.TargetMsgID != targetMsgID {
		t.Errorf("expected TargetMsgID %d, got %d", targetMsgID, params.TargetMsgID)
	}
}

// TestBashContextCancellation verifies that cancelling the context sends a cancel frame.
// A mock agent reads frames from the client and sends a result back.
func TestBashContextCancellation(t *testing.T) {
	// Pipes: client writes to agentReadPw, agent writes to clientReadPw
	agentReadPr, agentReadPw := io.Pipe()   // client → test (frames sent by client)
	clientReadPr, clientReadPw := io.Pipe() // agent → client (responses)

	client := NewClient(agentReadPw, clientReadPr)

	// Mock agent: read frames from client, capture bash + cancel, send result
	received := make(chan *protocol.Frame, 4)
	go func() {
		for {
			frame, err := protocol.ReadFrame(agentReadPr)
			if err != nil {
				return
			}
			select {
			case received <- frame:
			default:
			}
		}
	}()

	// Cancel the context immediately
	ctx, cancelCtx := context.WithCancel(context.Background())
	cancelCtx()

	holder, err := client.Bash(ctx, "sleep 10", 300)
	if err != nil {
		t.Fatalf("Bash: %v", err)
	}

	// First frame: bash request
	bashFrame := waitForFrame(t, received, "bash request")
	bashReq, err := protocol.ParseRequest(bashFrame)
	if err != nil {
		t.Fatalf("parse bash request: %v", err)
	}
	if bashReq.Method != protocol.MethodBash {
		t.Fatalf("expected MethodBash, got %v", bashReq.Method)
	}
	bashMsgID := bashReq.MsgID

	// Second frame: cancel request
	cancelFrame := waitForFrame(t, received, "cancel request")
	params, err := protocol.ParseBashCancelParams(cancelFrame)
	if err != nil {
		t.Fatalf("parse cancel: %v", err)
	}
	if params.TargetMsgID != bashMsgID {
		t.Errorf("cancel targets MsgID %d, want %d", params.TargetMsgID, bashMsgID)
	}

	// Send result so client goroutine exits
	result := protocol.NewResult(bashMsgID, protocol.NewBashResult(137, []byte(`{"cancelled":true}`)))
	_ = protocol.WriteFrame(clientReadPw, result)

	// Drain the holder
	exitCode := <-holder.ExitCode()
	if exitCode != 137 {
		t.Errorf("exit code = %d, want 137", exitCode)
	}
	client.Close()
}

func waitForFrame(t *testing.T, ch <-chan *protocol.Frame, label string) *protocol.Frame {
	t.Helper()
	select {
	case frame, ok := <-ch:
		if !ok {
			t.Fatalf("channel closed waiting for %s", label)
		}
		return frame
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
	}
	return nil // unreachable
}





