package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"kanban/backend/types"
)

// processGone reports whether pid is no longer running. A killed child is
// reparented to an init that may never reap it, so a zombie counts as gone.
func processGone(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		// No procfs: keep polling on the signal check rather than declare success.
		return false
	}
	// /proc/pid/stat is "pid (comm) state …" and comm may contain spaces —
	// anchor on the last ')'.
	stat := string(data)
	idx := strings.LastIndex(stat, ") ")
	if idx < 0 || idx+3 >= len(stat) {
		return false
	}
	switch stat[idx+2] {
	case 'Z', 'X', 'x':
		return true
	}
	return false
}

func TestNormalizeBashTimeout(t *testing.T) {
	for _, tc := range []struct{ in, want int64 }{
		{0, defaultBashTimeoutSec}, // unset → default
		{-3, defaultBashTimeoutSec},
		{30, 30},
		{maxBashTimeoutSec, maxBashTimeoutSec},
		{9999, maxBashTimeoutSec}, // clamped
	} {
		if got := normalizeBashTimeout(tc.in); got != tc.want {
			t.Errorf("normalizeBashTimeout(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// Regression: the direct path used to ignore the timeout argument, so a command
// that never exits pinned the bash tool (and its card) until the task was stopped.
func TestExecCommandStream_HonoursTimeout(t *testing.T) {
	start := time.Now()
	out, err := execCommandStream(context.Background(), t.TempDir(), "echo working; sleep 30", 1, nil)
	elapsed := time.Since(start)

	if elapsed > 4*time.Second {
		t.Errorf("took %v; the timeout argument must kill the command at ~1s", elapsed)
	}
	if err == nil {
		t.Fatalf("expected a timeout error, got output %q", out)
	}
	if !strings.Contains(err.Error(), "timed out after 1s") {
		t.Errorf("error should name the timeout, got %q", err)
	}
	// Output produced before the kill is still reported.
	if !strings.Contains(out, "working") {
		t.Errorf("output before the timeout was lost: %q", out)
	}
}

// A command that detaches a child (`npm run dev &`) must return as soon as the
// command itself exits: the child holds the pipe write ends, so waiting for EOF
// would hang forever.
func TestExecCommandStream_BackgroundChildDoesNotHang(t *testing.T) {
	start := time.Now()
	// Short sleep: the detached child would otherwise linger in the test env.
	out, err := execCommandStream(context.Background(), t.TempDir(), "echo ready; sleep 5 &", 10, nil)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Errorf("took %v with a detached child; the drain must stay bounded", elapsed)
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "ready") {
		t.Errorf("output lost: %q", out)
	}
}

// Killing only the direct child leaves grandchildren running with the pipes still
// open — the exact shape of the "still running" report.
func TestExecCommandStream_KillsProcessGroupOnTimeout(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	command := fmt.Sprintf("sleep 60 & echo $! > %s; wait", pidFile)

	_, err := execCommandStream(context.Background(), dir, command, 1, nil)
	if err == nil {
		t.Fatal("expected a timeout error")
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read grandchild pid file: %v", err)
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil {
		t.Fatalf("parse pid %q: %v", data, err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if processGone(pid) {
			return // grandchild is gone — the group was killed
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild pid %d survived the timeout — only the direct child was killed", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestExecCommandStream_StreamsLinesInOrder(t *testing.T) {
	dir := t.TempDir()
	var streamed []string
	out, err := execCommandStream(context.Background(), dir,
		"echo one; echo two >&2; echo three", 10, func(text string) { streamed = append(streamed, text) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "one") || !strings.Contains(out, "two") || !strings.Contains(out, "three") {
		t.Errorf("combined output missing lines: %q", out)
	}
	joined := strings.Join(streamed, "")
	for _, want := range []string{"one\n", "three\n"} {
		if !strings.Contains(joined, want) {
			t.Errorf("streamed lines missing %q: %q", want, joined)
		}
	}
	if strings.Index(joined, "one\n") > strings.Index(joined, "three\n") {
		t.Errorf("streamed lines out of order: %q", joined)
	}
}

func TestExecCommandStream_UsesWorktreeDir(t *testing.T) {
	dir := t.TempDir()
	out, err := execCommandStream(context.Background(), dir, "pwd", 10, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// macOS resolves /var → /private/var in pwd; compare on the basename.
	if !strings.HasSuffix(strings.TrimSpace(out), filepath.Base(dir)) {
		t.Errorf("ran in %q, want %q", strings.TrimSpace(out), dir)
	}
}

// A command whose last line has no newline must still be reported.
func TestExecCommandStream_TrailingLineWithoutNewline(t *testing.T) {
	var streamed []string
	out, err := execCommandStream(context.Background(), t.TempDir(), "printf 'no-newline'", 10,
		func(text string) { streamed = append(streamed, text) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "no-newline" {
		t.Errorf("output = %q, want %q", out, "no-newline")
	}
	if len(streamed) != 1 || streamed[0] != "no-newline\n" {
		t.Errorf("streamed = %q, want one line 'no-newline\\n'", streamed)
	}
}

// End-to-end through executeTool: the model must be told the command timed out.
func TestExecuteTool_Bash_DirectTimeout(t *testing.T) {
	te := &TaskExecutor{}
	result, ok := te.executeTool(context.Background(), "bash", map[string]any{
		"command": "sleep 30",
		"timeout": float64(1),
	}, t.TempDir(), &types.Task{}, &executionState{}, nil)
	if ok {
		t.Fatalf("expected failure, got success: %s", result)
	}
	if !strings.Contains(result, "timed out after 1s") {
		t.Errorf("expected a timeout message, got %q", result)
	}
}
