package procterm

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// pipePair is one output pipe: the read end handed to a reader, and the write end held
// by the (detached) child.
type pipePair struct {
	read  *os.File
	write *os.File
}

func newPipePair(t *testing.T) pipePair {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return pipePair{read: r, write: w}
}

// reader drains r and reports when it stops. finished is for the test, done is what
// Drain consumes: reading both from one channel would race the implementation for it.
func reader(r *os.File, done chan<- struct{}, finished chan<- struct{}) {
	_, _ = io.Copy(io.Discard, r)
	close(finished)
	done <- struct{}{}
}

// newReaders starts one reader per pipe and returns their completion channels.
func newReaders(pipes []pipePair, done chan<- struct{}) []<-chan struct{} {
	finished := make([]<-chan struct{}, 0, len(pipes))
	for _, p := range pipes {
		f := make(chan struct{})
		go reader(p.read, done, f)
		finished = append(finished, f)
	}
	return finished
}

// TestDrainReturnsWhenReadersFinish: the normal end — EOF arrives and the deadlines are
// never reached.
func TestDrainReturnsWhenReadersFinish(t *testing.T) {
	pipes := []pipePair{newPipePair(t), newPipePair(t)}
	done := make(chan struct{}, len(pipes))
	finished := newReaders(pipes, done)
	for _, p := range pipes {
		p.write.Close() // real EOF
	}

	start := time.Now()
	drain(done, time.Hour, time.Hour, readEnds(pipes)...)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("drain blocked for %s after EOF", elapsed)
	}
	for i, f := range finished {
		select {
		case <-f:
		case <-time.After(time.Second):
			t.Fatalf("reader %d did not finish on EOF", i)
		}
	}
}

// TestDrainClosesReadersAtGrace: the write end stays open, so EOF never arrives and the
// grace deadline must end the wait.
func TestDrainClosesReadersAtGrace(t *testing.T) {
	pipes := []pipePair{newPipePair(t), newPipePair(t)}
	done := make(chan struct{}, len(pipes))
	finished := newReaders(pipes, done)
	// The child stays alive holding the write ends — deliberately not closed here.
	t.Cleanup(func() {
		for _, p := range pipes {
			p.write.Close()
		}
	})

	drain(done, 20*time.Millisecond, time.Hour, readEnds(pipes)...)

	// Ending the wait closes the read ends, which is also what released the readers.
	for i, f := range finished {
		select {
		case <-f:
		case <-time.After(2 * time.Second):
			t.Fatalf("reader %d never finished — its read end was not closed", i)
		}
	}
	if _, err := pipes[0].read.Read(make([]byte, 1)); err == nil {
		t.Fatal("read end still open after the grace deadline")
	}
}

// TestDrainStopsAtHardStop: a reader that never signals must not hold the wait open, and
// the read ends are closed on the way out rather than leaked.
func TestDrainStopsAtHardStop(t *testing.T) {
	pipes := []pipePair{newPipePair(t), newPipePair(t)}
	done := make(chan struct{}, len(pipes)) // nobody ever signals

	start := time.Now()
	drain(done, time.Hour, 20*time.Millisecond, readEnds(pipes)...)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("hard stop did not end the drain (took %s)", elapsed)
	}
	for i, p := range pipes {
		if _, err := p.read.Read(make([]byte, 1)); err == nil {
			t.Fatalf("read end %d still open at the hard stop", i)
		}
	}
}

// TestDrainUsesDocumentedBounds pins the shared timings both bash runners rely on.
func TestDrainUsesDocumentedBounds(t *testing.T) {
	if DrainGrace != 250*time.Millisecond || DrainCap != 2*time.Second {
		t.Fatalf("unexpected drain bounds: grace=%s cap=%s", DrainGrace, DrainCap)
	}
}

// TestKillGroupReachesGrandchildren: killing only the direct child leaves a detached
// child running with the inherited pipes.
func TestKillGroupReachesGrandchildren(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")

	cmd := exec.Command("bash", "-c", "sleep 30 & echo $! > "+pidFile+"; sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Reaped by the cleanup below. KillGroup must run before that: after the reap the
	// leader's pid can be recycled.
	t.Cleanup(func() { _, _ = cmd.Process.Wait() })

	grandchild := waitForGrandchildPID(t, pidFile)
	// If KillGroup is broken, the 30 s sleep must not outlive the test.
	t.Cleanup(func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) })

	if err := KillGroup(cmd.Process); err != nil {
		t.Fatalf("KillGroup: %v", err)
	}
	if _, err := cmd.Process.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	// Polled, and a zombie counts as gone: the killed grandchild is reparented to an
	// init that may never reap it, and signal 0 succeeds against a zombie.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if processGone(grandchild) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived — only the direct child was killed", grandchild)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestKillGroupNilProcessIsNoop covers the cancelled run whose Start failed.
func TestKillGroupNilProcessIsNoop(t *testing.T) {
	if err := KillGroup(nil); err != nil {
		t.Fatalf("KillGroup(nil) = %v, want nil", err)
	}
}

// waitForGrandchildPID reads the pid the command wrote once its child is up.
func waitForGrandchildPID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(pidFile); err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the command never reported its child's pid")
	return 0
}

// processGone reports whether pid is still running. A killed child is reparented to an
// init that may never reap it, so a zombie counts as gone.
func processGone(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		// No procfs: keep polling on the signal check rather than declare success.
		return false
	}
	// /proc/pid/stat is "pid (comm) state …" and comm may contain spaces, so anchor on
	// the last ')'.
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

func readEnds(pipes []pipePair) []*os.File {
	ends := make([]*os.File, 0, len(pipes))
	for _, p := range pipes {
		ends = append(ends, p.read)
	}
	return ends
}
