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

// TestDrainEndsByEOFOrByDeadline: the flush has three ways to end and only one of them is the child
// cooperating. Readers that reach EOF are counted and Drain returns; readers still blocked when the
// grace deadline arrives are released by having their read ends closed; and a reader that never
// signals at all cannot hold the wait past the hard stop.
func TestDrainEndsByEOFOrByDeadline(t *testing.T) {
	cases := []struct {
		name       string
		grace      time.Duration
		hardStop   time.Duration
		readers    bool // start one reader per pipe
		eof        bool // close the write ends, which is what a real exit does
		wantClosed bool // the read ends must have been closed on the way out
		bound      time.Duration
	}{
		{"readers finishing is the normal end", time.Hour, time.Hour, true, true, false, 2 * time.Second},
		{"the grace deadline releases the readers", 20 * time.Millisecond, time.Hour, true, false, true, time.Second},
		{"the hard stop ends a wait nobody signals", time.Hour, 20 * time.Millisecond, false, false, true, time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pipes := []pipePair{newPipePair(t), newPipePair(t)}
			done := make(chan struct{}, len(pipes))
			var finished []<-chan struct{}
			if tc.readers {
				finished = newReaders(pipes, done)
			}
			if tc.eof {
				for _, p := range pipes {
					p.write.Close() // real EOF
				}
			} else {
				// The child stays alive holding the write ends — deliberately not closed here.
				t.Cleanup(func() {
					for _, p := range pipes {
						p.write.Close()
					}
				})
			}

			start := time.Now()
			drain(done, tc.grace, tc.hardStop, readEnds(pipes)...)
			if elapsed := time.Since(start); elapsed > tc.bound {
				t.Fatalf("drain took %v, past its %v bound: a deadline did not end the wait", elapsed, tc.bound)
			}

			for i, f := range finished {
				select {
				case <-f:
				case <-time.After(2 * time.Second):
					t.Fatalf("reader %d never finished — its read end was not closed", i)
				}
			}
			if !tc.wantClosed {
				return
			}
			for i, p := range pipes {
				if _, err := p.read.Read(make([]byte, 1)); err == nil {
					t.Fatalf("read end %d still open after the drain", i)
				}
			}
		})
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
