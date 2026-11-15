// Package sandboxcheck skips a test when this host cannot start a gVisor container, and says why.
//
// It probes with a real container rather than trusting IsAvailable ("runsc is installed"), which a
// broken podman storage passes right up until every create fails.
//
// Two questions are asked here, and a test belongs to exactly one of them: whether a container can
// start (Require), and whether the product's own sandbox gate is on (RequireAvailable). A test that
// reads a sandbox setting, or asserts a refusal that never reaches podman, belongs to neither and
// must run.
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

// Setting answers the question the server asks before it honours a sandboxed project — what
// sandbox.SandboxManager.IsAvailable reports.
type Setting interface {
	IsAvailable() bool
}

// RequireAvailable skips the calling test when this host has no gVisor runtime installed, which is
// the prerequisite of a claim about what happens when the sandbox IS honoured: a sandboxed project's
// file request reaching the host filesystem, a container that stays stopped. The product refuses such
// a request at its own gate before it reaches any filesystem, so the claim has no host to make on.
//
// It asks the installed-runtime question the handler itself asks and starts no container, which is why
// it cannot be replaced by Require: a host whose podman storage cannot create a container still
// honours these requests. A test that starts a container belongs to Require, never here.
func RequireAvailable(t *testing.T, setting Setting) {
	t.Helper()
	if !setting.IsAvailable() {
		t.Skip("sandbox is not available in this environment, so a sandboxed project's request is refused before it reaches any filesystem")
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
