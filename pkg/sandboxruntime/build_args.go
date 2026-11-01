package sandboxruntime

import (
	"fmt"
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
