package subproc

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestInheritKeepsOnlyWhatTheFilterAccepts is the whole reason a filter is passed in rather than an
// allowlist being built here: the names belong to the package that decides them.
func TestInheritKeepsOnlyWhatTheFilterAccepts(t *testing.T) {
	t.Setenv("SUBPROC_TEST_KEPT", "value")
	t.Setenv("SUBPROC_TEST_OTHER", "value")

	got := Inherit(func(name string) bool { return name == "SUBPROC_TEST_KEPT" }, DropEmpty)

	if !slices.Contains(got, "SUBPROC_TEST_KEPT=value") {
		t.Errorf("an allowed variable was not inherited: %v", got)
	}
	for _, kv := range got {
		if strings.HasPrefix(kv, "SUBPROC_TEST_OTHER") {
			t.Errorf("a variable outside the allowlist was inherited: %q", kv)
		}
	}
}

// The two policies are the one place the three families differ, so each is pinned rather than left to
// whichever call site remembers.
func TestEmptyPolicyIsStatedNotDiscovered(t *testing.T) {
	t.Setenv("SUBPROC_TEST_EMPTY", "")
	allow := func(name string) bool { return name == "SUBPROC_TEST_EMPTY" }

	if got := Inherit(allow, DropEmpty); len(got) != 0 {
		t.Errorf("DropEmpty emitted %v; a set-but-empty SSH_AUTH_SOCK reads as a broken agent, not an unset one", got)
	}
	got := Inherit(allow, KeepEmpty)
	if !slices.Contains(got, "SUBPROC_TEST_EMPTY=") {
		t.Errorf("KeepEmpty emitted %v; an adapter that distinguishes cleared from unset needs the name", got)
	}
}

// TestBindSetsBothFields is the pairing rule. They are not independent: the wait delay is what stops a
// killed child's still-open output pipes from parking cmd.Wait forever, so a helper that set only the
// environment would leave the bug this package exists to prevent.
func TestBindSetsBothFields(t *testing.T) {
	env := []string{"PATH=/usr/bin"}
	cmd := Bind(exec.Command("true"), env, 7*time.Second)

	if cmd.Env == nil || cmd.Env[0] != "PATH=/usr/bin" {
		t.Errorf("Env = %v, want the list handed in", cmd.Env)
	}
	if cmd.WaitDelay != 7*time.Second {
		t.Errorf("WaitDelay = %v, want 7s", cmd.WaitDelay)
	}
}

// The delay must survive the real spawn path, not just the field assignment. This is the property
// AGENTS.md records as F21: a cancelled context kills the child but does **not** close its output
// pipes, so cmd.Wait — and the request goroutine inside it — waits for whoever still holds them. Here
// the child hands its stdout to a grandchild and dies, which is the shape that used to wedge callers.
func TestBoundCommandReturnsAfterACancelEvenThoughThePipeStaysOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// `sleep 30 &` inherits the shell's stdout and outlives it, so the pipe stays open after the
	// process named by the command is gone.
	cmd := Bind(exec.CommandContext(ctx, "sh", "-c", "sleep 30 & echo started"), []string{"PATH=/usr/bin:/bin"}, 300*time.Millisecond)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := cmd.Output()
	elapsed := time.Since(start)

	if err == nil {
		t.Error("a cancelled command reported success")
	}
	// Killed at ~100ms, then WaitDelay gives the pipes 300ms. Without the delay this returns ~30s
	// later, which is the failure this pairing prevents; 5s is far above both and far below the child.
	if elapsed > 5*time.Second {
		t.Errorf("Output() took %v — WaitDelay did not fire, so a wedged pipe holds the caller", elapsed)
	}
}

func TestWatchReportsASubprocessThatOutlastsTheThreshold(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	record := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}

	cmd := exec.Command("sleep", "0.3")
	stop := Watch("sleep-under-test", 30*time.Millisecond, record)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	err := cmd.Wait()
	stop()
	if err != nil {
		t.Fatalf("sleep failed: %v", err)
	}

	deadline := time.After(500 * time.Millisecond)
	for {
		mu.Lock()
		n := len(lines)
		mu.Unlock()
		if n == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("watch reported %d line(s), want exactly 1: %v", n, lines)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if !strings.Contains(lines[0], "sleep-under-test") {
		t.Errorf("the report must name the operation, got %q", lines[0])
	}
}

// The half that a watchdog without a stop function gets wrong: a completed operation must not be
// reported, or the log line means "something is slow" and stops meaning anything.
func TestWatchStopsReportingOnceTheSubprocessFinished(t *testing.T) {
	var mu sync.Mutex
	var fired int
	record := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		fired++
	}

	cmd := exec.Command("true")
	stop := Watch("true-under-test", 20*time.Millisecond, record)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start true: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("true failed: %v", err)
	}
	stop()

	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if fired != 0 {
		t.Errorf("a finished subprocess reported %d time(s), want 0", fired)
	}
}
