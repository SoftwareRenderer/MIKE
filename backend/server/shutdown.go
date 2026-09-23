package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"mike/pkg/logging"
)

// ErrShutdownDeadline is returned when the shutdown sequence did not finish inside its budget.
var ErrShutdownDeadline = errors.New("shutdown deadline exceeded")

// ShutdownTimeout bounds the whole cleanup path. Every step waits on something that cannot be
// interrupted from the outside, and startup re-sweeps abandoned containers, which is what makes leaving
// them here safe.
const ShutdownTimeout = 15 * time.Second

// RunShutdownWithDeadline runs fn, but never waits for it longer than ShutdownTimeout. A bounded context
// would bound nothing, because a step blocked in a subprocess wait does not poll one: fn takes no
// context and the deadline is enforced by leaving.
func RunShutdownWithDeadline(fn func() error) error {
	return runShutdownWithDeadline(ShutdownTimeout, fn)
}

func runShutdownWithDeadline(timeout time.Duration, fn func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- fn() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// The "shutdown:" line above names the subsystem still running as the process exits.
		logging.For(logging.HTTP).Infof("shutdown: no step completed past %s — exiting anyway; the line above names what was still running", timeout)
		return fmt.Errorf("%w after %s", ErrShutdownDeadline, timeout)
	}
}
