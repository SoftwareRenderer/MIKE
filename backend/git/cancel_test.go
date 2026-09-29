package git

// A cancelled operation must stop. Two things can make that false, both measured here:
//
//   - os/exec kills only the DIRECT child. git's helper (upload-pack, a credential helper, the `sleep`
//     used below) survives the kill and keeps the stderr pipe open, and cmd.Wait waits on that pipe.
//   - cmd.WaitDelay bounds how long a killed-but-pipe-held process is waited for, so GitWaitDelay is
//     what a cancelled clone can cost the caller.
//
// So the assertions are on WALL TIME, not on "an error came back".

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// hangHelperScript writes an executable that ignores its arguments and blocks, and returns its path.
// `git clone --upload-pack=<script> <src> <dst>` runs it in place of the local transport, giving a git
// subprocess that blocks on LOCAL state — no network and no timing luck.
func hangHelperScript(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hang.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatalf("write hang helper: %v", err)
	}
	return p
}

// seedSourceRepo makes a one-commit repo usable as a clone source.
func seedSourceRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runGit(t, dir, "init", "-q")
	writeFile(t, dir, "f.txt", "content\n")
	runGit(t, dir, "add", "f.txt")
	runGit(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "seed")
	return dir
}

// hungCloneCommand builds a clone that will not finish: the hang helper stands in for the transport.
// The helper sleeps only 5s so an orphaned one (killing git does not kill it) cleans itself up instead
// of lingering in the test environment for a minute.
func hungCloneCommand(ctx context.Context, t *testing.T) *exec.Cmd {
	t.Helper()
	hang := hangHelperScript(t)
	dst := filepath.Join(t.TempDir(), "dst")
	return CommandContext(ctx, "", "clone", "--progress", "--upload-pack="+hang, seedSourceRepo(t), dst)
}

// TestCancelledGitReturnsPromptly pins what the user feels: cancel the request and the subprocess dies
// and the call returns, quickly (~0s measured here).
//
// Do not cite it as the cmd.WaitDelay guard. Measured: deleting `cmd.WaitDelay = GitWaitDelay` leaves
// this test PASSING, because git closes the progress pipe when it dies. TestGitConstructorSetsWaitDelay
// is the one that fails when the assignment goes.
func TestCancelledGitReturnsPromptly(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := hungCloneCommand(ctx, t)

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // let the helper actually get as far as blocking
	cancel()

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if err == nil {
			t.Error("a cancelled clone reported success")
		}
		if elapsed > 5*time.Second {
			t.Errorf("Wait took %v after cancel; a cancelled clone must free its caller promptly", elapsed.Round(time.Millisecond))
		}
		t.Logf("cancelled git returned after %v: %v", elapsed.Round(time.Millisecond), err)
	case <-time.After(30 * time.Second):
		t.Fatal("Wait never returned after cancel — the caller is parked on a dead subprocess")
	}
}

// TestGitConstructorSetsWaitDelay pins the mechanism directly on both constructors, so the test above
// cannot be passing because of something the test itself did.
func TestGitConstructorSetsWaitDelay(t *testing.T) {
	t.Parallel()
	if got := Command("").WaitDelay; got != GitWaitDelay {
		t.Errorf("Command().WaitDelay = %v, want %v", got, GitWaitDelay)
	}
	if got := CommandContext(context.Background(), "").WaitDelay; got != GitWaitDelay {
		t.Errorf("CommandContext().WaitDelay = %v, want %v", got, GitWaitDelay)
	}
}

// TestCloneContextHonoursAnAlreadyCancelledContext pins that CloneContext passes its context to the
// process rather than building the command and dropping the context on the floor.
func TestCloneContextHonoursAnAlreadyCancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out, err := CloneContext(ctx, seedSourceRepo(t), filepath.Join(t.TempDir(), "dst"), nil)
	if err == nil {
		t.Fatalf("CloneContext ran a clone under an already-cancelled context (out=%q)", out)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
}

// TestCloneContextCompletesARealClone is the happy path for the clone entry point.
func TestCloneContextCompletesARealClone(t *testing.T) {
	t.Parallel()
	src := seedSourceRepo(t)
	dst := filepath.Join(t.TempDir(), "dst")

	seen := -1
	out, err := CloneContext(context.Background(), src, dst, func(pct int) { seen = pct })
	if err != nil {
		t.Fatalf("CloneContext: %v (out=%q)", err, out)
	}
	// A local clone of a one-commit repo legitimately reports no progress at all, so the callback
	// must not be asserted on — only that wiring one does not break the clone.
	_ = seen
	if _, err := os.Stat(filepath.Join(dst, "f.txt")); err != nil {
		t.Errorf("the clone produced no working tree: %v", err)
	}
}
