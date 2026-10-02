// Package procterm ends a spawned command: kill its whole process group, then flush what
// it already wrote without waiting for an EOF that may never arrive.
//
// Both bash runners need the pair — backend/exec on the host, the sandbox-bridge inside a
// container — and the halves are one rule. A detached grandchild keeps the output pipe
// open, so killing the group is what ends the stream, and draining is what still delivers
// the output already produced.
package procterm

import (
	"os"
	"syscall"
	"time"
)

const (
	// DrainGrace is how long readers may keep flushing after the command exited.
	DrainGrace = 250 * time.Millisecond

	// DrainCap is the hard stop for that flush.
	DrainCap = 2 * time.Second
)

// KillGroup SIGKILLs the command's process group (commands run with Setpgid, so the pgid
// equals the pid), falling back to the direct child if the group is gone or the signal is
// refused. Killing only the child leaves grandchildren alive with stdout still open, so the
// output never ends.
//
// Stop using this once the leader has been reaped — its pid, which is the pgid, can then
// be recycled onto an unrelated process.
func KillGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil {
		return p.Kill()
	}
	return nil
}

// Drain waits for every reader to signal on done (one value each), closing the given read
// ends at the grace deadline and again at the hard stop. That close is what frees a reader
// stuck on a pipe a detached child still holds; closing twice is an error, ignored here.
func Drain(done <-chan struct{}, readers ...*os.File) {
	drain(done, DrainGrace, DrainCap, readers...)
}

// drain is Drain with the bounds injected, so they are testable.
func drain(done <-chan struct{}, grace, hardStop time.Duration, readers ...*os.File) {
	graceTimer := time.NewTimer(grace)
	defer graceTimer.Stop()
	hardStopTimer := time.NewTimer(hardStop)
	defer hardStopTimer.Stop()

	forceEOF := func() {
		for _, r := range readers {
			_ = r.Close()
		}
	}

	drained := 0
loop:
	for drained < len(readers) {
		select {
		case <-done:
			drained++
		case <-graceTimer.C:
			forceEOF()
		case <-hardStopTimer.C:
			forceEOF()
			break loop
		}
	}
}
