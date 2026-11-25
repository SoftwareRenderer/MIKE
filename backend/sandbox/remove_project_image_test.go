package sandbox

import (
	"strings"
	"testing"

	"mike/backend/testutil/sandboxcheck"
)

// A project's built image leaves with its folder; a name nothing ever built is not a failure.

func TestRemoveProjectImageTakesTheProjectsTag(t *testing.T) {
	sandboxcheck.Require(t, BaseImage)
	sm := NewSandboxManager(bootWithBridge(t))

	const projectName = "retire-me"
	ref := ProjectImageName(projectName)
	if _, err := sm.podmanExec("tag", BaseImage, ref); err != nil {
		t.Fatalf("stage the project's image name: %v", err)
	}
	t.Cleanup(func() { _, _ = sm.podmanExec("rmi", ref) })

	if err := sm.RemoveProjectImage(projectName); err != nil {
		t.Fatalf("RemoveProjectImage: %v", err)
	}
	out, err := sm.podmanExec("images", "-q", ref)
	if err != nil {
		t.Fatalf("read the name back: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("the project's image name survived: %s", strings.TrimSpace(string(out)))
	}
}

func TestRemoveProjectImageOfAnImageNeverBuilt(t *testing.T) {
	sandboxcheck.Require(t, BaseImage)
	sm := NewSandboxManager(bootWithBridge(t))

	if err := sm.RemoveProjectImage("no-such-project-at-all"); err != nil {
		t.Errorf("retiring an image that was never built = %v, want no error", err)
	}
}
