package sandboxruntime

import (
	"fmt"
	"path/filepath"
	"strings"
)

// A project image build runs in a container of its own (docs/adr/0002), and this file owns what that
// container is called and the flags every later command about it needs.

// BuildContainerPrefix is the name family a one-shot build container lives in. It sits inside the swept
// families on purpose: a build that died with the server leaves a stopped container behind, and
// CleanupStaleContainers is exactly the rule that reclaims one (running containers stay).
const BuildContainerPrefix = "kanban-build"

// StaleBuildFilter is podman's name filter for the containers an interrupted build left behind.
const StaleBuildFilter = BuildContainerPrefix + "-"

// BuildOutputMount is the guest path of a build container's one writable directory, whose host side is the
// build's scratch output directory that the size sampler walks. It lives here rather than in backend/sandbox
// because the build probe mounts the same shape.
const BuildOutputMount = "/mike-out"

// buildTempName is the one spelling of the builder's temp directory's name, shared by the guest path and by
// every host directory mounted to carry it.
const buildTempName = "buildtmp"

// BuildTempDir is where the nested builder keeps its own files, and it has to be on a bind: buildah mounts an
// overlay over the build context on every build and puts that mount's upper under `$TMPDIR`, and gVisor takes
// only a tmpfs or a gofer filesystem as an overlay upper, so the default temp path fails the build before step 1.
//
// It is a subdirectory of the output mount rather than a tmpfs because `$TMPDIR` also carries base images copied
// into the guest's store, and those bytes belong on the host's disk under the build's disk bound rather than in
// guest RAM against the project's memory limit. docs/sandbox-runtime.md → A project image builds inside a
// sandbox container.
const BuildTempDir = BuildOutputMount + "/" + buildTempName

// BuildTempHostDir names the host directory behind BuildTempDir, given the host directory mounted at
// BuildOutputMount. Whoever mounts the writable directory makes this one inside it, so the build always finds
// the path it was told about.
func BuildTempHostDir(hostOutput string) string { return filepath.Join(hostOutput, buildTempName) }

// RuntimeFlagsForExistingContainer repeats the runtime flags a gVisor container was created with.
//
// They are not remembered in the container's state: starting a created container without
// `ignore-cgroups` fails inside runsc at `/sys/fs/cgroup/cgroup.subtree_control`, which reads as a
// permissions bug and is not one.
func RuntimeFlagsForExistingContainer() []string {
	return []string{"--runtime-flag", "ignore-cgroups=true"}
}

// BuildContainerName names one build. The scratch id carries the project, the time and a sequence, so a
// container left by a killed server says which build left it and two builds of one project never collide.
func BuildContainerName(projectID, scratchID string) string {
	safe := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-").Replace(scratchID)
	var b strings.Builder
	fmt.Fprintf(&b, "%s-", BuildContainerPrefix)
	for _, r := range safe {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	name := b.String()
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}
