package sandboxruntime

// The parts of a `podman create` command line that arrive from outside this process: a
// Linux capability someone asked for, and the in-container path of a bind mount. Both
// are interpolated into the command, and either can undo the sandbox on its own: one
// mount of "/" or one CAP_SYS_ADMIN makes the gVisor boundary meaningless, and a ":" or
// "," inside a container path rewrites the mount's options — an "ro" becomes "rw"
// without anyone asking. The rule lives here, once, for every caller that builds
// container arguments: a task sandbox (backend/sandbox), an extension sandbox
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
// flag, whitespace or a separator into the podman arguments.
var capabilityPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// DeniedCapabilities are never re-added, no matter who asked: a sandbox already starts
// with `--cap-drop ALL` and no-new-privileges, and these reach past the container into
// the host kernel, other processes, devices or the network.
var DeniedCapabilities = map[string]string{
	"SYS_ADMIN":       "grants near-root control of the container's namespaces and the host kernel interface",
	"SYS_PTRACE":      "ptraces other processes",
	"SYS_MODULE":      "loads kernel modules",
	"SYS_RAWIO":       "raw I/O port access (iopl/ioperm)",
	"DAC_OVERRIDE":    "bypasses file read/write/execute permission checks",
	"DAC_READ_SEARCH": "bypasses file read permission checks and directory traversal",
	"NET_ADMIN":       "reconfigures network interfaces and tables, defeating network isolation",
	"MKNOD":           "creates device nodes",
	"SETUID":          "changes the process UID",
	"SETGID":          "changes the process GID",
	"SYS_BOOT":        "reboots the host",
	"LINUX_IMMUTABLE": "mounts read-only bind mounts read-write",
}

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

// normalizeCapability uppercases and strips the optional "CAP_" prefix so
// "cap_sys_admin" and "SYS_ADMIN" are the same request.
func normalizeCapability(cap string) string {
	c := strings.ToUpper(strings.TrimSpace(cap))
	return strings.TrimPrefix(c, "CAP_")
}

// ValidateCapability rejects malformed capability strings and every entry on
// DeniedCapabilities, naming the capability in the message.
func ValidateCapability(cap string) error {
	if strings.TrimSpace(cap) == "" {
		return fmt.Errorf("capability is empty")
	}
	if !capabilityPattern.MatchString(cap) {
		return fmt.Errorf("capability %q is invalid: only letters, digits and '_' are allowed", cap)
	}
	name := normalizeCapability(cap)
	if reason, denied := DeniedCapabilities[name]; denied {
		return fmt.Errorf("capability %s is not allowed in sandboxes: %s", name, reason)
	}
	return nil
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
