// Package sandboxcheck skips a test when this host cannot start a gVisor container, and says why.
//
// It probes with a real container rather than trusting IsAvailable ("runsc is installed"), which a
// broken podman storage passes right up until every create fails.
//
// Three questions are asked here, and a test belongs to exactly one of them: whether a container can
// start (Require), whether the product's own sandbox gate is on (RequireAvailable), and whether this
// host can build an image at all (RequireBuild). A test that reads a sandbox setting, or asserts a
// refusal that never reaches podman, belongs to none and must run.
//
// Test-only: nothing outside _test files may import it.
package sandboxcheck

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mike/pkg/sandboxruntime"
)

// verdicts caches one probe per image: the answer belongs to the host, not to the test that asked.
var verdicts sync.Map // image -> reason ("" when the host can run a container)

// buildVerdict caches the one host-wide answer to "can this machine build an image at all".
var buildVerdict sync.Map // "build" -> reason ("" when a trivial image builds)

// buildProbeTimeout bounds the throwaway build. A `FROM scratch` context fetches nothing, so a wait
// past this is a wedged podman, and only a cancelled context bounds the child.
const buildProbeTimeout = 90 * time.Second

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

// RequireBuild skips the calling test when this host cannot build an image, and says why in podman's
// own words. It is the other container question: Require asks whether a container starts, this asks
// whether the host can produce an image to start. A test that stages a base image of its own needs
// this answer and no probe of a running container can give it.
func RequireBuild(t *testing.T) {
	t.Helper()
	if reason := probeBuild(t); reason != "" {
		t.Skipf("this host cannot build an image — %s; tests that stage an image of their own are skipped", reason)
	}
}

// probeBuild builds one throwaway `FROM scratch` image per process. Nothing is fetched: the point is
// the host's build step, not a base.
func probeBuild(t *testing.T) string {
	t.Helper()
	if cached, ok := buildVerdict.Load("build"); ok {
		return cached.(string)
	}
	reason := ""
	if err := buildThrowawayImage(t); err != nil {
		reason = err.Error()
	}
	buildVerdict.Store("build", reason)
	return reason
}

func buildThrowawayImage(t *testing.T) error {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("probe\n"), 0o644); err != nil {
		return err
	}
	dockerfile := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM scratch\nCOPY marker /marker\n"), 0o644); err != nil {
		return err
	}
	const ref = "localhost/mike-sandboxcheck-probe:1"
	ctx, cancel := context.WithTimeout(t.Context(), buildProbeTimeout)
	defer cancel()
	// PodmanCommandContext is the only way to start podman here: it pins the binary, the environment
	// allowlist and the WaitDelay, and an invariant test fails a call site that builds its own.
	out, err := sandboxruntime.PodmanCommandContext(ctx, "build", "-t", ref, "-f", dockerfile, dir).CombinedOutput()
	if err != nil {
		return &buildError{cause: err, output: string(out)}
	}
	_ = sandboxruntime.PodmanCommandContext(ctx, "rmi", ref).Run()
	return nil
}

// buildError carries podman's own words beside the exit status, which is what a skip reason is made
// of: the reader has to be able to tell a missing graph driver from a missing binary.
type buildError struct {
	cause  error
	output string
}

func (e *buildError) Error() string {
	if e.output == "" {
		return e.cause.Error()
	}
	return e.cause.Error() + ": " + e.output
}
