package sandbox

import (
	"testing"

	"kanban/pkg/bridge/protocol"
)

// TestClientCompiles verifies that the Client type compiles with all expected methods.
func TestClientCompiles(t *testing.T) {
	// These would not compile if the methods don't exist
	var c *Client
	_ = func() {
		_, _, _ = c.ReadFile("path", 0, 0)
		_, _ = c.WriteFile("path", "content")
		_, _ = c.EditFile("path", []protocol.Edit{})
		_, _ = c.Bash("cmd", 5)
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



// TestDialNonExistent verifies that Dial fails for a non-existent TCP address.
func TestDialNonExistent(t *testing.T) {
	_, err := Dial("127.0.0.1:59999")
	if err == nil {
		t.Fatal("expected error for non-existent address")
	}
}
