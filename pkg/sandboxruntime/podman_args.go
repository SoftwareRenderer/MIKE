package sandboxruntime

// The parts of a `podman create` command line that arrive from outside this process: a
// Linux capability someone asked for, and the in-container path of a bind mount. Both are
// interpolated into the command. The mount rules carry the real security weight: one mount
// of "/" hands the sandbox the host tree, and a ":" or "," inside a container path
// rewrites the mount's options — an "ro" becomes "rw" without anyone asking. A capability
// does not: every container MIKE creates runs under gVisor with `--cap-drop ALL`, so a
// granted capability is a privilege inside the guest kernel, and the guest cannot reach
// through the gofer to write a read-only bind mount, see a host PID, or mount its way onto
// a host path (see GVisorIsolationForCaps for what was measured). What is checked here is
// therefore the *shape* of a capability plus, at the call site that grants one, the runtime
// that makes granting it safe. The rule lives in this package once, for every caller that
// builds container arguments: a task sandbox (backend/sandbox), an extension sandbox
// (backend/extensions), and the API that stores both settings.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// containerPathPattern is the only shape accepted for a mount's container path; see
// ValidateContainerPath for why ':' and ',' are excluded.
var containerPathPattern = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// capabilityPattern accepts plain capability names only, so a value cannot smuggle a
// flag, whitespace or a separator into the podman arguments. This is the part of the old
// capability denylist that was never about the capability: "--privileged" and
// "SYS_ADMIN ALL" reach `--cap-add` as *arguments*, and no runtime makes that safe.
var capabilityPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// HasDotDotSegment reports whether p contains a ".." path segment.
func HasDotDotSegment(p string) bool {
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool {
		return r == '/' || r == filepath.Separator
	}) {
		if seg == ".." {
			return true
		}
	}
	return false
}

// ValidateContainerPath checks a mount's in-container path: absolute, free of ".."
// segments, and limited to a charset that cannot rewrite the podman mount spec
// "<host>:<container>:rprivate,<mode>" — a ":rw" or ",rw" inside the path would turn a
// read-only mount into a writable one.
func ValidateContainerPath(containerPath string) error {
	if containerPath == "" {
		return fmt.Errorf("mount container path is empty")
	}
	if !strings.HasPrefix(containerPath, "/") {
		return fmt.Errorf("mount container path %q is not absolute", containerPath)
	}
	if !containerPathPattern.MatchString(containerPath) {
		return fmt.Errorf("mount container path %q is invalid: only '/', letters, digits, '.', '_' and '-' are allowed (':' and ',' would rewrite the podman mount spec)", containerPath)
	}
	if containerPath == "/" || filepath.Clean(containerPath) == "/" {
		return fmt.Errorf("mount container path %q must not be the container root", containerPath)
	}
	if HasDotDotSegment(containerPath) {
		return fmt.Errorf("mount container path %q must not contain '..'", containerPath)
	}
	return nil
}

// ValidateCapability rejects a capability string that is not a plain capability name.
// The name itself is not judged: a grant lands in a gVisor guest that started from
// `--cap-drop ALL`, and GVisorIsolationForCaps is what holds the caller to that.
func ValidateCapability(cap string) error {
	if strings.TrimSpace(cap) == "" {
		return fmt.Errorf("capability is empty")
	}
	if !capabilityPattern.MatchString(cap) {
		return fmt.Errorf("capability %q is invalid: only letters, digits and '_' are allowed", cap)
	}
	return nil
}

// GVisorIsolationForCaps reports whether a `podman create` argument list isolates the
// container the way granting a capability assumes it is isolated: gVisor as the runtime,
// and every capability dropped before the grant. Anywhere else, `--cap-add` is a
// privilege on the host kernel, so a caller that is about to add one and fails this check
// must not add it.
//
// This replaced a denylist of capability *names*, on the measurement that under runsc the
// names were not the boundary: a guest holding SYS_ADMIN could not write through a
// read-only bind mount (the guest remounts it "rw" in its own mount table and the gofer
// still refuses), saw only its own four PIDs, and its `mount --bind / x` exposed the
// guest's own overlay rather than the host tree. The runtime is what that safety rests
// on, so the runtime is what gets checked.
func GVisorIsolationForCaps(args []string) error {
	if !hasArgPair(args, "--runtime", "runsc") {
		return fmt.Errorf("capabilities may only be granted to a gVisor (runsc) container; this command line does not select that runtime")
	}
	if !hasArgPair(args, "--cap-drop", "ALL") {
		return fmt.Errorf("capabilities may only be granted on top of a full `--cap-drop ALL`; this command line does not drop the default set first")
	}
	return nil
}

// hasArgPair reports whether flag appears immediately followed by value.
func hasArgPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// ValidateCapabilities validates a capability list, naming the first offender.
func ValidateCapabilities(caps []string) error {
	for _, cap := range caps {
		if err := ValidateCapability(cap); err != nil {
			return err
		}
	}
	return nil
}
