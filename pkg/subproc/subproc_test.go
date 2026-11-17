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
	"unicode/utf8"
)

// TestInheritEmitsExactlyWhatTheFilterAccepts is the whole reason a filter is passed in rather than
// an allowlist being built here: the names belong to the package that decides them. The two empty
// policies are the one place the three families differ, so each is pinned rather than left to
// whichever call site remembers — a set-but-empty SSH_AUTH_SOCK reads as a broken agent, not as an
// unset one, and an adapter that distinguishes cleared from unset needs the name to arrive.
func TestInheritEmitsExactlyWhatTheFilterAccepts(t *testing.T) {
	t.Setenv("SUBPROC_TEST_KEPT", "value")
	t.Setenv("SUBPROC_TEST_OTHER", "value")
	t.Setenv("SUBPROC_TEST_EMPTY", "")

	named := func(want ...string) func(string) bool {
		return func(name string) bool { return slices.Contains(want, name) }
	}
	cases := []struct {
		name  string
		keep  func(string) bool
		empty EmptyPolicy
		want  []string
	}{
		{"an allowed name arrives, a name outside the allowlist does not",
			named("SUBPROC_TEST_KEPT"), DropEmpty, []string{"SUBPROC_TEST_KEPT=value"}},
		{"every name a prefix accepts arrives",
			named("SUBPROC_TEST_KEPT", "SUBPROC_TEST_OTHER", "SUBPROC_TEST_EMPTY"), DropEmpty,
			[]string{"SUBPROC_TEST_KEPT=value", "SUBPROC_TEST_OTHER=value"}},
		{"DropEmpty drops a set-but-empty value", named("SUBPROC_TEST_EMPTY"), DropEmpty, nil},
		{"KeepEmpty keeps it as a name with no value", named("SUBPROC_TEST_EMPTY"), KeepEmpty, []string{"SUBPROC_TEST_EMPTY="}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Inherit(tc.keep, tc.empty)
			if !slices.Equal(got, tc.want) {
				t.Errorf("Inherit() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBindSetsBothFields: the environment and the wait delay are not independent — the delay is what
// stops a killed child's still-open output pipes from parking cmd.Wait forever.
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

// The delay must survive the real spawn path, not just the field assignment. A cancelled context
// kills the child but does not close its output pipes, so cmd.Wait — and the request goroutine inside
// it — waits for whoever still holds them. Here the child hands its stdout to a grandchild and dies.
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

// TestWatchBoundsASubprocessThatOutlastsItsThreshold: a subprocess still running when the threshold
// passes becomes visible, once; a subprocess that finished must not be reported at all, or the line
// means "something is slow" and stops meaning anything. Both halves need the stop function.
func TestWatchBoundsASubprocessThatOutlastsItsThreshold(t *testing.T) {
	cases := []struct {
		name    string
		what    string
		argv    []string
		after   time.Duration
		reports int
		settle  time.Duration
	}{
		{"a subprocess that outlasts the threshold is reported", "sleep-under-test", []string{"sleep", "0.3"}, 30 * time.Millisecond, 1, 500 * time.Millisecond},
		{"a finished subprocess is never reported", "true-under-test", []string{"true"}, 20 * time.Millisecond, 0, 120 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var lines []string
			record := func(format string, args ...any) {
				mu.Lock()
				defer mu.Unlock()
				lines = append(lines, fmt.Sprintf(format, args...))
			}

			cmd := exec.Command(tc.argv[0], tc.argv[1:]...)
			stop := Watch(tc.what, tc.after, record)
			if err := cmd.Start(); err != nil {
				t.Fatalf("start %v: %v", tc.argv, err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("%v failed: %v", tc.argv, err)
			}
			stop()

			// The report fires on a timer, so absence has to be waited for rather than sampled.
			deadline := time.Now().Add(tc.settle)
			for time.Now().Before(deadline) {
				mu.Lock()
				n := len(lines)
				mu.Unlock()
				if n >= tc.reports {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(lines) != tc.reports {
				t.Fatalf("%s reported %d time(s), want %d: %v", tc.what, len(lines), tc.reports, lines)
			}
			if tc.reports > 0 && !strings.Contains(lines[0], tc.what) {
				t.Errorf("the report must name the operation, got %q", lines[0])
			}
		})
	}
}

func TestSummarizeBoundsSubprocessOutput(t *testing.T) {
	if got := Summarize([]byte("fatal: could not read\nfrom remote\n"), AnswerMax); got != "fatal: could not read from remote" {
		t.Errorf("Summarize() = %q, want the single-line summary", got)
	}
	if got := Summarize(nil, AnswerMax); got != "" {
		t.Errorf("Summarize(nil) = %q, want nothing", got)
	}
	long := strings.Repeat("x", 5000)
	if got := Summarize([]byte(long), AnswerMax); len(got) > AnswerMax+len("…") {
		t.Errorf("Summarize did not bound a %d-byte output (got %d)", len(long), len(got))
	}

	if got := Summarize([]byte(strings.Repeat("ä", 400)), 100); !utf8.ValidString(got) {
		t.Errorf("Summarize cut through a rune: %q", got)
	}
}
