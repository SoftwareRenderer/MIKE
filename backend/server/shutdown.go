package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"kanban/pkg/logging"
)

// ErrShutdownDeadline is returned when the shutdown sequence did not finish inside its budget.
var ErrShutdownDeadline = errors.New("shutdown deadline exceeded")

// ShutdownTimeout bounds the whole cleanup path. Every step waits on something that cannot be
// interrupted from the outside (a container that is not answering podman, an LSP bridge whose peer
// died). Startup re-sweeps abandoned containers, which is what makes leaving them here safe.
const ShutdownTimeout = 15 * time.Second

// RunShutdownWithDeadline runs fn, but never waits for it longer than ShutdownTimeout.
//
// A bounded context would bound nothing: a step blocked in a subprocess wait does not poll one, so
// the only guarantee is deciding to leave. fn logs each step first, so the last line names what was
// still running.
func RunShutdownWithDeadline(fn func(ctx context.Context) error) error {
	return runShutdownWithDeadline(ShutdownTimeout, fn)
}

func runShutdownWithDeadline(timeout time.Duration, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- fn(ctx) }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// Logged, not swallowed: this is the evidence of which subsystem wedged (the last
		// "shutdown:" line above it) and the process is exiting with it still running.
		logging.For(logging.HTTP).Infof("shutdown: no step completed past %s — exiting anyway; the line above names what was still running", timeout)
		return fmt.Errorf("%w after %s", ErrShutdownDeadline, timeout)
	}
}
