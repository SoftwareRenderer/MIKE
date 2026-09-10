package sandboxruntime

// The parts of a `podman create` command line that are a security decision: the isolation preface
// every container MIKE builds starts from (GVisorCreateArgs), the one append path for a capability
// grant (AppendCapabilities), and the values that arrive from outside this process — a capability
// name, and a bind mount's in-container path. Mounts carry the weight: one mount of "/" hands the
// sandbox the host tree, and a ":" or "," in a container path rewrites the mount's options — an
// "ro" becomes "rw". A capability is judged by shape instead, because every container MIKE creates
// runs under gVisor with `--cap-drop ALL` (GVisorIsolationForCaps records what was measured) and
// the grant goes through the one function that checks that runtime before appending.
//
// Every caller that builds container arguments takes these from here.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// containerPathPattern is the only shape accepted for a mount's container path; see
// ValidateContainerPath for why ':' and ',' are excluded.
var containerPathPattern = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// capabilityPattern accepts plain capability names only, so a value cannot smuggle a flag,
// whitespace or a separator into the podman arguments: "--privileged" and "SYS_ADMIN ALL" reach
// `--cap-add` as *arguments*, and no runtime makes that safe.
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

// GVisorCreateArgs is the arguments every `podman create` MIKE builds begins with; callers append their differences.
func GVisorCreateArgs(containerName string) []string {
	return []string{
		"--runtime-flag=overlay2=root:self",
		"--runtime-flag=ignore-cgroups",
		"create",
		"--pull", "never",
		"--name", containerName,
		"--runtime", "runsc",
		"--security-opt", "label=disable",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--entrypoint", "",
	}
}

// GVisorIsolationForCaps reports whether a `podman create` argument list isolates the container
// the way granting a capability assumes: gVisor as the runtime, and every capability dropped
// before the grant. Anywhere else `--cap-add` is a privilege on the host kernel, so a caller that
// fails this check must not add one.
//
// This replaced a denylist of capability names, measured not to be the boundary: under runsc a
// guest with SYS_ADMIN still cannot write through a read-only bind mount, see a host PID, or
// bind-mount the host tree. The runtime is what the safety rests on.
func GVisorIsolationForCaps(args []string) error {
	if !hasArgPair(args, "--runtime", "runsc") {
		return fmt.Errorf("capabilities may only be granted to a gVisor (runsc) container; this command line does not select that runtime")
	}
	if !hasArgPair(args, "--cap-drop", "ALL") {
		return fmt.Errorf("capabilities may only be granted on top of a full `--cap-drop ALL`; this command line does not drop the default set first")
	}
	return nil
}

// AppendCapabilities is the only way this repo appends a --cap-add. It writes the runsc and
// --cap-drop ALL preface before the first capability, so a grant cannot land on a container that does
// not start from that isolation, and no call site can get the ordering wrong.
func AppendCapabilities(args []string, caps []string) ([]string, error) {
	if err := GVisorIsolationForCaps(args); err != nil {
		return args, err
	}
	for _, cap := range caps {
		args = append(args, "--cap-add", cap)
	}
	return args, nil
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
