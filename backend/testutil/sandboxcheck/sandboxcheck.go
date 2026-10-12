// Package sandboxcheck skips a test when this host cannot start a gVisor container, and says why.
//
// It probes with a real container rather than trusting IsAvailable ("runsc is installed"), which a
// broken podman storage passes right up until every create fails.
//
// Only a test that starts a container belongs here. A test that reads a sandbox setting, or asserts a
// refusal that never reaches podman, has no environment prerequisite and must run.
//
// Test-only: nothing outside _test files may import it.
package sandboxcheck

import (
	"context"
	"sync"
	"testing"

	"mike/pkg/sandboxruntime"
)

// verdicts caches one probe per image: the answer belongs to the host, not to the test that asked.
var verdicts sync.Map // image -> reason ("" when the host can run a container)

// Require skips the calling test unless this host can start a gVisor container from image. A missing
// image is not a skip: the store answered, so the sandbox works and the test may report the image.
func Require(t *testing.T, image string) {
	t.Helper()
	if reason := probe(t, image); reason != "" {
		t.Skipf("sandbox is not fully supported in this environment — %s; tests that start a container are skipped", reason)
	}
}

func probe(t *testing.T, image string) string {
	t.Helper()
	if cached, ok := verdicts.Load(image); ok {
		return cached.(string)
	}
	// The probe blocks inside the calling test, so that test's context is the one worth cancelling.
	ctx, cancel := context.WithTimeout(t.Context(), sandboxruntime.ProbeTimeout)
	defer cancel()
	reason := ""
	if err := sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}).ProbeSandbox(ctx, image); err != nil {
		reason = err.Error()
	}
	verdicts.Store(image, reason)
	return reason
}
