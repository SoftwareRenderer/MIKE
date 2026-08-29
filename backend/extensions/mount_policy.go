package extensions

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"kanban/backend/types"
)

// Extension sandbox containers are created with `--cap-drop ALL` and no host
// filesystem access beyond the mounts the app itself installs (the read-only
// project mounts). Everything a caller can add on top — mounts and capabilities
// from `PUT /api/extensions/{id}`, or from an extension manifest at install
// time — is validated by this file. Without it a single mount of "/" (or
// CAP_SYS_ADMIN) turns the gVisor boundary decorative.

// EnvMountRoots is a path-list separated (os.PathListSeparator) list of extra
// host roots an extension sandbox may mount from, in addition to the implicit
// roots (workspace dir, registered project paths, the extensions dir and the
// system temp dir). Provided for deployments that legitimately keep extension
// data elsewhere (e.g. /srv/ext-data:/opt/models).
const EnvMountRoots = "MIKE_EXT_MOUNT_ROOTS"

// containerPathPattern is the only shape accepted for a mount's container path.
// The charset deliberately excludes ':' and ',': the podman mount spec is
// "<host>:<container>:rprivate,<mode>", so either character inside a
// user-supplied container path rewrites the spec — a trailing ":rw" or ",rw"
// turns a read-only mount into a writable one, and a "," can replace the
// mount options entirely.
var containerPathPattern = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// capabilityPattern guards against a capability value being interpreted as
// something other than a capability name (leading '-', whitespace, separators).
var capabilityPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// deniedCapabilities are never re-added to an extension sandbox, no matter who
// asked. The sandbox is already `--cap-drop ALL` + `no-new-privileges`; these
// are the capabilities that reach past the container (host kernel, other
// processes, devices, network namespace) rather than staying inside it.
var deniedCapabilities = map[string]string{
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

// deniedCapabilityNames lists the denylist in a stable order (tests, messages).
func deniedCapabilityNames() []string {
	names := make([]string, 0, len(deniedCapabilities))
	for name := range deniedCapabilities {
		names = append(names, name)
	}
	return names
}

// MountPolicy is an immutable allowlist of host roots an extension sandbox may
// bind-mount from, plus the mount/capability shape rules. A nil policy is valid
// and fails closed: nothing resolves inside an empty allowlist.
type MountPolicy struct {
	roots []string
}

// newMountPolicy builds a policy from raw root paths. Each root is made
// absolute and symlink-resolved (best effort — a root that does not exist yet
// is kept as its cleaned absolute form) so containment is compared against the
// same resolved form as the candidate path. Empty roots are dropped, duplicates
// collapsed.
func newMountPolicy(roots []string) *MountPolicy {
	p := &MountPolicy{}
	seen := map[string]bool{}
	for _, raw := range roots {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		abs := raw
		if !filepath.IsAbs(abs) {
			if a, err := filepath.Abs(abs); err == nil {
				abs = a
			}
		}
		abs = filepath.Clean(abs)
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = filepath.Clean(resolved)
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		p.roots = append(p.roots, abs)
	}
	return p
}

// Roots returns a copy of the policy's allowlist roots.
func (p *MountPolicy) Roots() []string {
	if p == nil {
		return nil
	}
	out := make([]string, len(p.roots))
	copy(out, p.roots)
	return out
}

// envMountRoots returns the extra roots from EnvMountRoots (path-list separated).
func envMountRoots() []string {
	raw := strings.TrimSpace(os.Getenv(EnvMountRoots))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, string(os.PathListSeparator))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// mountRoots assembles the default allowlist: the configured workspace dir, the
// registered project paths, the extensions dir, the system temp dir and the
// EnvMountRoots extras.
func mountRoots(extensionsDir, workspaceDir string, projectPaths []string) []string {
	roots := make([]string, 0, len(projectPaths)+4)
	if workspaceDir != "" {
		roots = append(roots, workspaceDir)
	}
	roots = append(roots, projectPaths...)
	if extensionsDir == "" {
		extensionsDir = (&ExtensionConfig{}).ExtensionsDirOrDefault()
	}
	roots = append(roots, extensionsDir)
	if tmp := os.TempDir(); tmp != "" {
		roots = append(roots, tmp)
	}
	return append(roots, envMountRoots()...)
}

// hasDotDotSegment reports whether p contains a ".." path segment.
func hasDotDotSegment(p string) bool {
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool {
		return r == '/' || r == filepath.Separator
	}) {
		if seg == ".." {
			return true
		}
	}
	return false
}

// ValidateContainerPath checks a mount's in-container path: absolute, free of
// ".." segments and limited to the charset that cannot rewrite the podman
// mount spec (see containerPathPattern).
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
	if hasDotDotSegment(containerPath) {
		return fmt.Errorf("mount container path %q must not contain '..'", containerPath)
	}
	return nil
}

// resolvePath symlink-resolves p. When p (or a deep component of it) does not
// exist yet — a mount target podman would create — the nearest existing ancestor
// is resolved and the remaining components re-joined, so a symlink anywhere on
// the existing part of the path is still followed.
func resolvePath(p string) (string, error) {
	clean := filepath.Clean(p)
	var missing []string
	cur := clean
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			out := resolved
			for i := len(missing) - 1; i >= 0; i-- {
				out = filepath.Join(out, missing[i])
			}
			return filepath.Clean(out), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("no existing ancestor of %q can be resolved", p)
		}
		missing = append(missing, filepath.Base(cur))
		cur = parent
	}
}

// insideRoot reports whether the resolved path is one of the roots or below it.
func (p *MountPolicy) insideRoot(resolved string) bool {
	if p == nil {
		return false
	}
	for _, root := range p.roots {
		if resolved == root || strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// ValidateHostPath checks a mount's host path: absolute, no ".." segment, and
// symlink-resolved INSIDE one of the policy's roots. Returns the resolved path.
func (p *MountPolicy) ValidateHostPath(hostPath string) (string, error) {
	if hostPath == "" {
		return "", fmt.Errorf("mount host path is empty")
	}
	if !filepath.IsAbs(hostPath) {
		return "", fmt.Errorf("mount host path %q is not absolute", hostPath)
	}
	if hasDotDotSegment(hostPath) {
		return "", fmt.Errorf("mount host path %q must not contain '..'", hostPath)
	}
	resolved, err := resolvePath(hostPath)
	if err != nil {
		return "", fmt.Errorf("mount host path %q cannot be resolved: %w", hostPath, err)
	}
	if !p.insideRoot(resolved) {
		return "", fmt.Errorf("mount host path %q resolves to %q, which is outside the allowed mount roots [%s]", hostPath, resolved, strings.Join(p.Roots(), ", "))
	}
	return resolved, nil
}

// withExtraRoots returns a policy with additional roots added to p's own
// (re-normalized, deduplicated).
func (p *MountPolicy) withExtraRoots(roots ...string) *MountPolicy {
	return newMountPolicy(append(p.Roots(), roots...))
}

// ValidateMount validates a single extension sandbox mount.
func (p *MountPolicy) ValidateMount(m types.SandboxMount) error {
	if err := ValidateContainerPath(m.ContainerPath); err != nil {
		return err
	}
	if _, err := p.ValidateHostPath(m.HostPath); err != nil {
		return err
	}
	if m.Mode != "" && m.Mode != "ro" && m.Mode != "rw" {
		return fmt.Errorf("mount mode %q is invalid: must be \"ro\" or \"rw\"", m.Mode)
	}
	return nil
}

// ValidateMounts validates a whole mount list, naming the first offender.
func (p *MountPolicy) ValidateMounts(mounts []types.SandboxMount) error {
	for i, m := range mounts {
		if err := p.ValidateMount(m); err != nil {
			return fmt.Errorf("mount %d: %w", i, err)
		}
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
// the denylist, naming the capability in the message.
func ValidateCapability(cap string) error {
	if strings.TrimSpace(cap) == "" {
		return fmt.Errorf("capability is empty")
	}
	if !capabilityPattern.MatchString(cap) {
		return fmt.Errorf("capability %q is invalid: only letters, digits and '_' are allowed", cap)
	}
	name := normalizeCapability(cap)
	if reason, denied := deniedCapabilities[name]; denied {
		return fmt.Errorf("capability %s is not allowed in extension sandboxes: %s", name, reason)
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

// ValidateSandboxAdditions checks the two fields that can widen a sandbox past
// its purpose (host mounts and Linux capabilities) against the policy.
func (p *MountPolicy) ValidateSandboxAdditions(mounts []types.SandboxMount, capabilities []string) error {
	if err := p.ValidateMounts(mounts); err != nil {
		return err
	}
	return ValidateCapabilities(capabilities)
}
