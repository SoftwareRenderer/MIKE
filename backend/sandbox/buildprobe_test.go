package sandbox

import (
	"context"
	"testing"

	"mike/backend/testutil/sandboxcheck"
	"mike/pkg/sandboxruntime"
)

// TestBuildProbeOfflineBuildsUnderRunsc holds ADR-0002's first measurement: whether an image build works at all
// under runsc. Later stages download a base image, so only `make build-probe` answers them.
func TestBuildProbeOfflineBuildsUnderRunsc(t *testing.T) {
	sandboxcheck.Require(t, sandboxruntime.BuilderImageDefault)

	rt := sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{})
	if !rt.ImageExists(sandboxruntime.BuilderImageDefault) {
		t.Skipf("builder image %s is not in this host's store; `podman pull %s` to run this test",
			sandboxruntime.BuilderImageDefault, sandboxruntime.BuilderImageDefault)
	}

	ctx, cancel := context.WithTimeout(t.Context(), sandboxruntime.BuildProbeStageTimeout)
	defer cancel()

	rep, err := rt.ProbeBuildOffline(ctx, sandboxruntime.BuilderImageDefault)
	for _, res := range rep.Results {
		t.Logf("stage %s: ok=%v %s", res.Stage, res.OK, res.Reason)
	}
	if err != nil {
		t.Fatalf("no offline stage built an image: %v", err)
	}
}
