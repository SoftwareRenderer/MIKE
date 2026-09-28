package extensions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mike/backend/fsutil"
	"mike/backend/types"
	"mike/pkg/sandboxruntime"
)

// Extension sandboxes start with `--cap-drop ALL` and no host filesystem access beyond
// the mounts the app adds itself. This file checks what a caller can add on top: mounts
// and capabilities from `PUT /api/extensions/{id}`, or from a manifest at install time.
// The mounts are what can undo the sandbox — one mount of "/" hands over the host tree,
// and a ":" inside a container path turns an "ro" into an "rw". A capability cannot: it
// lands in a gVisor guest, so what is checked about one is its shape (see
// sandboxruntime.ValidateCapability) and, where it is granted, the runtime pairing
// sandboxruntime.GVisorIsolationForCaps describes.

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

// newMountPolicy builds a policy from raw root paths. Each is made absolute and
// symlink-resolved (a root that does not exist yet keeps its cleaned absolute
// form) so it matches the resolved form of a candidate path.
func newMountPolicy(roots []string) *MountPolicy {
	return &MountPolicy{roots: fsutil.NormalizeRoots(roots)}
}

// newMountPolicyWithReadOnly is newMountPolicy plus the subset of those roots that
// may only ever be bind-mounted read-only.
func newMountPolicyWithReadOnly(roots, readOnlyRoots []string) *MountPolicy {
	return &MountPolicy{roots: fsutil.NormalizeRoots(roots), readOnlyRoots: fsutil.NormalizeRoots(readOnlyRoots)}
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

// insideRoot reports whether the resolved path is one of the roots or below it.
func (p *MountPolicy) insideRoot(resolved string) bool {
	if p == nil {
		return false
	}
	return fsutil.InsideRoots(resolved, p.roots)
}

// ForceReadOnly reports whether a host path is one the sandbox may only read, whatever mode the
// caller asked for (see MountPolicy.readOnlyRoots). Call it with a resolved path (see
// ValidateHostPath).
func (p *MountPolicy) ForceReadOnly(resolved string) bool {
	if p == nil {
		return true
	}
	return fsutil.InsideRoots(resolved, p.readOnlyRoots)
}

// ValidateHostPath checks a mount's host path: absolute, no ".." segment, and
// symlink-resolved INSIDE one of the policy's roots. Returns the resolved path.
//
// Resolution is fsutil.ResolveSymlinks rather than an ancestor walk: a symlink standing where
// the mount source would be created has a target that does not exist, so it cannot be checked,
// and binding that path binds wherever the link points.
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
	resolved, err := fsutil.ResolveSymlinks(hostPath)
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
	if err := sandboxruntime.ValidateContainerPath(m.ContainerPath); err != nil {
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

// ValidateDependencyGrant returns the resolved host path of a dependency cache, or refuses it. The user
// typed it themselves, so it is judged on having a sensible shape rather than against the allowlist
// that MountPolicy applies to an extension's own requests.
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
	resolved, err := fsutil.ResolveSymlinks(trimmed)
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
	if err := checkGrantShape(resolved, hostHomeDirs(), dependencyGrant); err != nil {
		return "", err
	}
	return resolved, nil
}

// ValidateAgentMountGrant returns the grant to store for one agent sandbox mount. A missing mode means
// read-only. A folder that does not exist yet is accepted as long as a folder above it exists, because
// the agent creates its own config folder the first time it runs.
func ValidateAgentMountGrant(mount types.SandboxMount) (types.SandboxMount, error) {
	expanded := ExpandHostPath(strings.TrimSpace(mount.HostPath))
	if expanded == "" {
		return mount, fmt.Errorf("no directory given")
	}
	candidate := mount
	candidate.HostPath = expanded
	candidate.Mode = strings.TrimSpace(mount.Mode)
	if candidate.Mode == "" {
		candidate.Mode = "ro"
	}
	if err := types.ValidateSandboxMount(candidate); err != nil {
		return mount, err
	}
	resolved, err := fsutil.ResolveSymlinks(expanded)
	if err != nil {
		return mount, err
	}
	if err := checkGrantShape(resolved, hostHomeDirs(), agentMountKind); err != nil {
		return mount, err
	}
	if info, statErr := os.Stat(resolved); statErr == nil {
		if !info.IsDir() {
			return mount, fmt.Errorf("%s is not a directory", resolved)
		}
	} else if !hasExistingAncestor(resolved) {
		return mount, fmt.Errorf("%s does not exist and no directory above it does, so there is nowhere to create it", resolved)
	}
	candidate.HostPath = resolved
	return candidate, nil
}

// CheckGrantedHostPath re-checks a stored approval, so one saved before today's checks existed is still
// judged. Whether the folder exists is not asked, because the agent creates it.
func CheckGrantedHostPath(hostPath string) error {
	resolved, err := fsutil.ResolveSymlinks(ExpandHostPath(hostPath))
	if err != nil {
		return err
	}
	return checkGrantShape(resolved, hostHomeDirs(), agentMountKind)
}

func hasExistingAncestor(p string) bool {
	for cur := filepath.Dir(p); cur != "/" && cur != "."; cur = filepath.Dir(cur) {
		if info, err := os.Stat(cur); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// ExpandHostPath replaces a leading ~ with the home directory, because podman does not expand it.
func ExpandHostPath(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
}

// systemGrantRoots are host trees a dependency directory is never in, and that must never be handed to
// a language server at all. /opt is absent on purpose: real toolchain caches live there.
var systemGrantRoots = []string{"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/boot", "/dev", "/proc", "/sys", "/run", "/var"}

// credentialGrantDirs hold secrets rather than dependencies, relative to any home. A grant is readable
// from inside the extension's container, where what a language server does with a file it can open is
// nobody's promise, so a grant must not point at the stores an attacker would most like to read.
// A denylist of credential stores: host safety, not language knowledge.
var credentialGrantDirs = []string{".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".config/gcloud", ".config/gh", ".config/huggingface", ".config/pip"}

// Lets a refusal name the field the user filled in, instead of talking about dependencies in both cases.
type grantKind struct {
	thing     string // what the field names
	example   string // a directory of the user's own
	contents  string // what the tree is expected to hold
	witnesses string // who else could read it
}

var (
	dependencyGrant = grantKind{
		thing:     "a dependency directory",
		example:   "go/pkg/mod",
		contents:  "a project's dependencies",
		witnesses: "every language server in the install",
	}
	agentMountKind = grantKind{
		thing:     "a directory to mount",
		example:   ".pi/agent",
		contents:  "one agent's own files",
		witnesses: "every task that runs this agent",
	}
)

// A new kind of approved folder should add a grantKind rather than a new set of checks. The home folders
// are passed in so the rule can be tested without touching a real home directory.
func checkGrantShape(resolved string, homes []string, k grantKind) error {
	trimmed := strings.Trim(resolved, "/")
	if trimmed == "" {
		return fmt.Errorf("the filesystem root is not %s — name the directory you want inside the container", k.thing)
	}
	for _, root := range systemGrantRoots {
		if resolved == root || strings.HasPrefix(resolved, root+"/") {
			return fmt.Errorf("%s is inside %s, which belongs to the machine rather than to %s", resolved, root, k.contents)
		}
	}
	for _, home := range homes {
		if resolved == home {
			return fmt.Errorf("%s is a home directory, not %s — grant a directory inside it (for example %s/%s), so what becomes readable from %s is the files you named and not the person",
				resolved, k.thing, home, k.example, k.witnesses)
		}
		if strings.HasPrefix(home, resolved+"/") {
			return fmt.Errorf("%s contains a home directory, so granting it would expose every project and credential on this machine", resolved)
		}
		for _, cred := range credentialGrantDirs {
			if bad := filepath.Join(home, filepath.ToSlash(cred)); resolved == bad || strings.HasPrefix(resolved, bad+"/") {
				return fmt.Errorf("%s holds credentials, not %s", bad, k.contents)
			}
		}
	}
	// This runs after the home checks, because a folder such as /root is both a top-level folder and a home.
	if !strings.Contains(trimmed, "/") {
		return fmt.Errorf("%s is the whole top level of the filesystem — name a directory of your own under it (for example %s/%s)", resolved, resolved, k.example)
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
