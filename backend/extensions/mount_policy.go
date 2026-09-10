package extensions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kanban/backend/fsutil"
	"kanban/backend/types"
	"kanban/pkg/sandboxruntime"
)

// Extension sandboxes start with `--cap-drop ALL` and no host filesystem access beyond
// the mounts the app adds itself. This file checks what a caller can add on top: mounts
// and capabilities from `PUT /api/extensions/{id}`, or from a manifest at install time.
// The mounts are what can undo the sandbox — one mount of "/" hands over the host tree,
// and a ":" inside a container path turns an "ro" into an "rw". A capability cannot: it
// lands in a gVisor guest, so what is checked about one is its shape (see
// sandboxruntime.ValidateCapability) and, where it is granted, the runtime pairing
// sandboxruntime.GVisorIsolationForCaps describes.

// EnvMountRoots names the variable listing extra host roots an extension sandbox may mount from,
// separated by os.PathListSeparator (':' on Linux). The implicit roots are the
// workspace dir, registered project paths, the extensions dir and the system
// temp dir; this is for deployments that keep extension data elsewhere. The value is resolved once
// into startup.Config.ExtMountRoots; this constant is the name, quoted by startup's table of names.
const EnvMountRoots = "MIKE_EXT_MOUNT_ROOTS"

// MountPolicy is an allowlist of host roots an extension sandbox may
// bind-mount from. A nil policy is valid and fails closed: nothing is inside an
// empty allowlist.
type MountPolicy struct {
	roots []string
	// readOnlyRoots are roots the sandbox may read but never write: the workspace dir (every
	// project's task worktrees live under it), the registered project checkouts, and the host
	// paths a project's Sandbox Configuration declares. A manifest is registry text, so naming
	// one of them is a read, not a write grant. The remaining roots — extensions dir, temp dir,
	// MIKE_EXT_MOUNT_ROOTS extras — are where an extension writes its own files.
	readOnlyRoots []string
}

// normalizeRoots makes each root absolute, symlink-resolved and cleaned, dropping
// blanks and duplicates, so a candidate path is compared against the same form it
// will be resolved to.
func normalizeRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
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
		out = append(out, abs)
	}
	return out
}

// newMountPolicy builds a policy from raw root paths. Each is made absolute and
// symlink-resolved (a root that does not exist yet keeps its cleaned absolute
// form) so it matches the resolved form of a candidate path.
func newMountPolicy(roots []string) *MountPolicy {
	return &MountPolicy{roots: normalizeRoots(roots)}
}

// newMountPolicyWithReadOnly is newMountPolicy plus the subset of those roots that
// may only ever be bind-mounted read-only.
func newMountPolicyWithReadOnly(roots, readOnlyRoots []string) *MountPolicy {
	return &MountPolicy{roots: normalizeRoots(roots), readOnlyRoots: normalizeRoots(readOnlyRoots)}
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

// mountRoots assembles the default allowlist: the configured workspace dir, the
// registered project paths, the extensions dir, the system temp dir and the
// MIKE_EXT_MOUNT_ROOTS extras.
//
// extraRoots is the resolved value of that variable (startup.Config.ExtMountRoots), passed in by the
// sandbox manager that was wired with it. Reading it here instead would let one policy in the
// install path and another in the create path disagree about what the operator declared.
func mountRoots(extensionsDir, workspaceDir string, projectPaths, extraRoots []string) []string {
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
	return append(roots, extraRoots...)
}

// ValidateContainerPath checks a mount's in-container path: absolute, free of ".."
// segments, and limited to the charset that cannot rewrite the podman mount spec — a
// ":" or "," there turns "ro" into "rw". The rule is sandboxruntime's.
func ValidateContainerPath(containerPath string) error {
	return sandboxruntime.ValidateContainerPath(containerPath)
}

// resolvePath resolves a mount's host path with the shared fsutil rule, which —
// unlike a bare ancestor walk — rejects a dangling symlink standing where the
// path would be created. Binding that path would bind wherever the link points,
// and the target does not exist yet, so it cannot be checked.
func resolvePath(p string) (string, error) {
	return fsutil.ResolveSymlinks(p)
}

// insideAny reports whether the resolved path is one of roots or below it.
func insideAny(resolved string, roots []string) bool {
	for _, root := range roots {
		if resolved == root || strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// insideRoot reports whether the resolved path is one of the roots or below it.
func (p *MountPolicy) insideRoot(resolved string) bool {
	if p == nil {
		return false
	}
	return insideAny(resolved, p.roots)
}

// ForceReadOnly reports whether a host path is one the sandbox may only read, whatever mode the
// caller asked for (see MountPolicy.readOnlyRoots). Call it with a resolved path (see
// ValidateHostPath).
func (p *MountPolicy) ForceReadOnly(resolved string) bool {
	if p == nil {
		return true
	}
	return insideAny(resolved, p.readOnlyRoots)
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
	if sandboxruntime.HasDotDotSegment(hostPath) {
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
// (re-normalized, deduplicated). Adding a root must not un-pin the existing ones.
func (p *MountPolicy) withExtraRoots(roots ...string) *MountPolicy {
	if p == nil {
		return newMountPolicy(roots)
	}
	return newMountPolicyWithReadOnly(append(p.Roots(), roots...), p.readOnlyRoots)
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

// ValidateCapability rejects a malformed capability string, naming it. There is no denylist: a
// grant lands in a gVisor guest that started from --cap-drop ALL, so callers judge it by what it
// can reach (sandboxruntime.GVisorIsolationForCaps).
func ValidateCapability(cap string) error { return sandboxruntime.ValidateCapability(cap) }

// ValidateCapabilities validates a capability list, naming the first offender.
func ValidateCapabilities(caps []string) error { return sandboxruntime.ValidateCapabilities(caps) }

// ValidateSandboxAdditions checks the two fields that can widen a sandbox: host
// mounts and Linux capabilities.
func (p *MountPolicy) ValidateSandboxAdditions(mounts []types.SandboxMount, capabilities []string) error {
	if err := p.ValidateMounts(mounts); err != nil {
		return err
	}
	return ValidateCapabilities(capabilities)
}

// ShadowsToolState reports whether a path would cover the language server's own writable state in
// its container: the tmpfs, or the container user's cache directory.
//
// Both users mount it read-only, and a server with a read-only cache publishes nothing about a file
// it cannot load — no error, no warning — so a broken file looks clean. A borrowed Sandbox
// Configuration mount over one of these paths is therefore dropped, and a manifest naming one as its
// dependency_cache container_path is refused at install instead of unmounted at every start.
func ShadowsToolState(containerPath string) bool {
	// Only the tmpfs roots themselves; a read-only mount over /tmp makes the whole scratch space
	// unusable, while a subtree of it is fine.
	switch containerPath {
	case "/tmp", "/var/tmp":
		return true
	}
	// A .cache directory of the container's own user (/root/.cache, /home/node/.cache) or
	// anything under it. Depth-bounded so a project's own .cache, deep under /mnt, stays borrowed.
	parts := strings.Split(strings.TrimPrefix(containerPath, "/"), "/")
	for i, part := range parts {
		if i > 2 {
			break
		}
		if part == ".cache" {
			return true
		}
	}
	return false
}

// ValidateDependencyGrant checks the host directory a person grants to one extension's language server
// for its resolved dependencies, and returns the cleaned absolute path to store. It is deliberately
// not MountPolicy's check: this is a directory the operator chose, not a path a manifest asked for, so
// it is judged on being an absolute path and not being someone's home tree.
func ValidateDependencyGrant(hostPath string) (string, error) {
	trimmed := strings.TrimSpace(hostPath)
	if trimmed == "" {
		return "", fmt.Errorf("no directory given")
	}
	if !strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("%q is not an absolute path — give the full path to the directory", trimmed)
	}
	if sandboxruntime.HasDotDotSegment(trimmed) {
		return "", fmt.Errorf("%q must not contain '..'", trimmed)
	}
	resolved, err := resolvePath(trimmed)
	if err != nil {
		return "", err
	}
	if resolved == "/" {
		return "", fmt.Errorf("the filesystem root is not a dependency directory")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("%s cannot be read: %w (create it first — a task running the toolchain's download command is the usual way)", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", resolved)
	}
	if err := checkGrantShapeWithHomes(resolved, hostHomeDirs()); err != nil {
		return "", err
	}
	return resolved, nil
}

// systemGrantRoots are host trees a dependency directory is never in, and that must never be handed to
// a language server at all. /opt is absent on purpose: real toolchain caches live there.
var systemGrantRoots = []string{"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/boot", "/dev", "/proc", "/sys", "/run", "/var"}

// credentialGrantDirs hold secrets rather than dependencies, relative to any home. A grant is readable
// from inside the extension's container, where what a language server does with a file it can open is
// nobody's promise, so a grant must not point at the stores an attacker would most like to read.
// A denylist of credential stores: host safety, not language knowledge.
var credentialGrantDirs = []string{".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".config/gcloud", ".config/gh", ".config/huggingface", ".config/pip"}

// checkGrantShapeWithHomes is the shape rule a dependency directory has to satisfy, and it
// costs the user nothing to satisfy. A manifest cannot carry a host path, so the user's own
// answer is the only input and this is the only place to catch a wrong one. Homes are passed
// in so the rules are testable without touching a real home directory.
func checkGrantShapeWithHomes(resolved string, homes []string) error {
	// Not the root, and not the bare top level of the tree. Either hands a language server an entire
	// mount instead of the directory of dependencies under it, and a whole mount is the widest thing
	// this field could possibly be pointed at. A named directory of the user's own — /data/go/pkg/mod,
	// /tank/gomodcache — is what a dependency directory actually is.
	trimmed := strings.Trim(resolved, "/")
	if trimmed == "" {
		return fmt.Errorf("the filesystem root is not a dependency directory — name the directory your toolchain's files live in")
	}
	if !strings.Contains(trimmed, "/") {
		return fmt.Errorf("%s is the whole top level of the filesystem — name a directory of your own under it (for example %s/go/pkg/mod)", resolved, resolved)
	}
	for _, root := range systemGrantRoots {
		if resolved == root || strings.HasPrefix(resolved, root+"/") {
			return fmt.Errorf("%s is inside %s, which belongs to the machine rather than to a project's dependencies", resolved, root)
		}
	}
	for _, home := range homes {
		if resolved == home {
			return fmt.Errorf("%s is a home directory, not a dependency directory — grant a directory inside it (for example %s/go/pkg/mod), so what becomes readable from every language server in the install is the dependencies and not the person", resolved, home)
		}
		if strings.HasPrefix(home, resolved+"/") {
			return fmt.Errorf("%s contains a home directory, so granting it would expose every project and credential on this machine", resolved)
		}
		for _, cred := range credentialGrantDirs {
			if bad := filepath.Join(home, filepath.ToSlash(cred)); resolved == bad || strings.HasPrefix(resolved, bad+"/") {
				return fmt.Errorf("%s holds credentials, not dependencies", bad)
			}
		}
	}
	return nil
}

// hostHomeDirs returns the server's own home, /root, and one level of /home and /Users. Bounded on
// purpose: a safety check on a typed path, not a directory service.
func hostHomeDirs() []string {
	homes := []string{}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		homes = append(homes, filepath.Clean(h))
	}
	homes = append(homes, "/root")
	for _, parent := range []string{"/home", "/Users"} {
		if entries, err := os.ReadDir(parent); err == nil {
			for _, e := range entries {
				if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
					homes = append(homes, filepath.Join(parent, e.Name()))
				}
			}
		}
	}
	return homes
}
