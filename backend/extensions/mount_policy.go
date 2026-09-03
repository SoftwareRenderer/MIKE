package extensions

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"kanban/backend/fsutil"
	"kanban/backend/types"
)

// Extension sandboxes start with `--cap-drop ALL` and no host filesystem access
// beyond the mounts the app adds itself. This file checks what a caller can add
// on top: mounts and capabilities from `PUT /api/extensions/{id}`, or from a
// manifest at install time. One mount of "/" or one CAP_SYS_ADMIN would
// otherwise make the gVisor boundary meaningless.

// EnvMountRoots lists extra host roots an extension sandbox may mount from,
// separated by os.PathListSeparator (':' on Linux). The implicit roots are the
// workspace dir, registered project paths, the extensions dir and the system
// temp dir; this is for deployments that keep extension data elsewhere.
const EnvMountRoots = "MIKE_EXT_MOUNT_ROOTS"

// containerPathPattern is the only shape accepted for a mount's container path.
// It excludes ':' and ',' on purpose: the podman mount spec is
// "<host>:<container>:rprivate,<mode>", so a ":rw" or ",rw" inside a
// user-supplied path would rewrite the options and make a read-only mount writable.
var containerPathPattern = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// capabilityPattern accepts plain capability names only, so a value cannot
// smuggle a flag, whitespace or a separator into the podman arguments.
var capabilityPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// deniedCapabilities are never re-added, no matter who asked. The sandbox is
// already `--cap-drop ALL` + `no-new-privileges`; these are the ones that reach
// past the container (host kernel, other processes, devices, network).
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

// MountPolicy is an allowlist of host roots an extension sandbox may
// bind-mount from. A nil policy is valid and fails closed: nothing is inside an
// empty allowlist.
type MountPolicy struct {
	roots []string
	// readOnlyRoots are roots the sandbox may read but never write. They are the
	// host paths a project's own Sandbox Configuration declares — borrowed so a
	// language server can see the same caches its workspace sees (a Go module
	// cache, a node_modules bind) — and a third-party extension must never be able
	// to turn one of them into a writable host directory.
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

// ForceReadOnly reports whether a host path comes from a project's Sandbox
// Configuration rather than from the app's own roots, in which case the mount is
// read-only whatever the caller asked for. Call it with a resolved path (see
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

// ValidateDependencyGrant validates the one host directory a user grants to one extension's language
// server for its resolved dependencies, and returns the cleaned absolute path to store.
//
// Deliberately a different rule than MountPolicy.ValidateHostPath: a module cache or Cargo home sits
// exactly where that allowlist does not reach, which is why a human is asked for it rather than an
// extension declaring it. Still required: absolute, no `..`, symlinks resolved before storing, not
// the filesystem root, and already existing — a missing directory would surface as a podman error
// nobody traces back to this field.
//
// Consequence: everything under the path is readable inside that extension's container, and writable
// by task sandboxes when the manifest asked for it. One directory per extension, never widened by
// anything the extension says.
func ValidateDependencyGrant(hostPath string) (string, error) {
	trimmed := strings.TrimSpace(hostPath)
	if trimmed == "" {
		return "", fmt.Errorf("no directory given")
	}
	if !strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("%q is not an absolute path — give the full path to the directory", trimmed)
	}
	if hasDotDotSegment(trimmed) {
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

// systemGrantRoots are host trees a dependency directory is never in, and that must never be handed
// read-write to a sandbox. /opt is absent on purpose: real toolchain caches live there.
var systemGrantRoots = []string{"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/boot", "/dev", "/proc", "/sys", "/run", "/var"}

// credentialGrantDirs hold secrets rather than dependencies, relative to any home. A grant is mounted
// read-write into every sandboxed task, where what runs is steered by repository content, so a grant
// must not point a task at the files an attack would rewrite. A denylist of credential stores: host
// safety, not language knowledge.
var credentialGrantDirs = []string{".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".config/gcloud", ".config/gh", ".config/huggingface", ".config/pip"}

// checkGrantShape refuses shapes that make a grant something other than a dependency cache. The
// language server reads it read-only, but every sandboxed task mounts it read-write so a build can
// fill it — the path is a host write surface. One text field must not be able to hand over a home
// directory or the host's /etc; a dependency cache is always somewhere specific, a few levels down.
func checkGrantShape(resolved string) error {
	return checkGrantShapeWithHomes(resolved, hostHomeDirs())
}

// checkGrantShapeWithHomes takes the homes as an argument so the rules are testable without touching
// a real home directory.
func checkGrantShapeWithHomes(resolved string, homes []string) error {
	if segments := strings.Count(strings.Trim(resolved, "/"), "/"); segments < 2 {
		return fmt.Errorf("%s is too close to the filesystem root to grant as a dependency directory — name a directory of your own, a couple of levels down (for example ~/go/pkg/mod)", resolved)
	}
	for _, root := range systemGrantRoots {
		if resolved == root || strings.HasPrefix(resolved, root+"/") {
			return fmt.Errorf("%s is inside %s, which belongs to the machine rather than to a project's dependencies", resolved, root)
		}
	}
	for _, home := range homes {
		if resolved == home {
			return fmt.Errorf("%s is a home directory, not a dependency directory — grant a directory inside it (for example %s/go/pkg/mod), because a task that fills the cache can also write anywhere it points", resolved, home)
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
