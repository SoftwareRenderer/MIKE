package sandbox

import (
	"context"
	"os"
	"testing"
)

func TestAgentClient(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("KANBAN_AGENT_PATH", agentPath)
	defer os.Unsetenv("KANBAN_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-agent-client"

	err, _ := sm.Create(taskID, dir, "", "test-proj", "", "", false, 0, nil, "", "", nil, nil, false)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	defer sm.Stop(taskID)

	client, err := sm.GetClient(taskID)
	if err != nil {
		t.Fatalf("get client: %v", err)
	}
	defer client.Close()

	// Write a file
	n, err := client.WriteFile(context.Background(), "hello.txt", "world")
	if err != nil {
		t.Fatalf("write file: %v", err)
	}
	if n != 5 {
		t.Errorf("write returned %d bytes, want 5", n)
	}

	// Read it back
	content, size, err := client.ReadFile(context.Background(), "hello.txt")
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if content != "world" {
		t.Errorf("read content = %q, want %q", content, "world")
	}
	if size != 5 {
		t.Errorf("read size = %d, want 5", size)
	}

	// Run a bash command
	holder, err := client.Bash(context.Background(), "echo hello from sandbox", 5)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	var out []byte
	buf := make([]byte, 1024)
	for {
		n, err := holder.Output().Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	exitCode := <-holder.ExitCode()
	if exitCode != 0 {
		t.Errorf("bash exit code = %d, want 0 (output: %s)", exitCode, out)
	}
	if string(out) != "hello from sandbox\n" {
		t.Errorf("bash output = %q, want %q", out, "hello from sandbox\n")
	}

	// GetClient is cached — second call returns same client
	client2, err := sm.GetClient(taskID)
	if err != nil {
		t.Fatalf("get client again: %v", err)
	}
	if client2 != client {
		t.Error("expected cached client")
	}
}
