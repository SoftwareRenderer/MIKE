package extensions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/types"
)

// policyWith returns a policy allowing only the given roots.
func policyWith(roots ...string) *MountPolicy {
	return newMountPolicy(roots)
}

func TestValidateContainerPath(t *testing.T) {
	valid := []string{
		"/workspace",
		"/mnt/node_modules",
		"/a.b-c_d/e.f",
		"/go/pkg/mod",
	}
	for _, p := range valid {
		if err := ValidateContainerPath(p); err != nil {
			t.Errorf("ValidateContainerPath(%q) = %v, want nil", p, err)
		}
	}

	invalid := []string{
		"",
		"/",                           // the container root itself
		"//",                          // ... spelled another way
		"workspace",                   // not absolute
		"./workspace",                 // not absolute
		"/work space",                 // space
		"/workspace;rm -rf /",         // shell metacharacters
		"/workspace,rw",               // ',' rewrites the mount options
		"/workspace:rw",               // ':' rewrites the spec (and the mode)
		"/workspace:/etc:rprivate,rw", // full spec injection
		"/workspace\x00",              // control character
		"/mnt/../etc",                 // '..' segment
		"/mnt/node_modules/../../etc", // '..' segment
		"/work\u00fcst",               // non-ASCII
		"-v/etc:/host",                // leading dash = podman flag
	}
	for _, p := range invalid {
		if err := ValidateContainerPath(p); err == nil {
			t.Errorf("ValidateContainerPath(%q) = nil, want error", p)
		}
	}
}

// TestValidateContainerPath_BlocksWithoutSpecChars: ':' and ',' are the two characters
// that can rewrite a podman `-v` spec, so they are refused anywhere in a container
// path — a user value can never change the mount mode.
func TestValidateContainerPath_BlocksWithoutSpecChars(t *testing.T) {
	for _, p := range []string{"/x:rw", "/x,rw", "/x:ro,", "/x:,ro"} {
		err := ValidateContainerPath(p)
		if err == nil {
			t.Fatalf("ValidateContainerPath(%q) = nil, want error", p)
		}
		if !strings.Contains(err.Error(), p) {
			t.Errorf("error %q does not name the offending value %q", err.Error(), p)
		}
	}
}

func TestMountPolicyValidateHostPath(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "data", "models")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	policy := policyWith(root)

	t.Run("path inside the allowlist is accepted", func(t *testing.T) {
		got, err := policy.ValidateHostPath(inside)
		if err != nil {
			t.Fatalf("ValidateHostPath(%q) = %v, want nil", inside, err)
		}
		if got != inside {
			t.Errorf("resolved = %q, want %q", got, inside)
		}
	})

	t.Run("the root itself is accepted", func(t *testing.T) {
		if _, err := policy.ValidateHostPath(root); err != nil {
			t.Errorf("ValidateHostPath(root) = %v, want nil", err)
		}
	})

	t.Run("a not-yet-created path inside the root is accepted", func(t *testing.T) {
		if _, err := policy.ValidateHostPath(filepath.Join(root, "new", "deeper")); err != nil {
			t.Errorf("ValidateHostPath(missing inside) = %v, want nil", err)
		}
	})

	t.Run("path outside the allowlist is rejected", func(t *testing.T) {
		for _, p := range []string{"/", "/etc", "/etc/passwd", "/home", "/tmp/outside-check"} {
			_, err := policy.ValidateHostPath(p)
			if err == nil {
				t.Errorf("ValidateHostPath(%q) = nil, want error", p)
				continue
			}
			if !strings.Contains(err.Error(), p) {
				t.Errorf("error %q does not name the offending value %q", err.Error(), p)
			}
		}
	})

	t.Run("'..' segment is rejected even when it lands inside", func(t *testing.T) {
		for _, p := range []string{
			root + "/../etc",
			root + "/data/../../etc/passwd",
			root + "/data/..",
		} {
			if _, err := policy.ValidateHostPath(p); err == nil {
				t.Errorf("ValidateHostPath(%q) = nil, want error", p)
			} else if !strings.Contains(err.Error(), "'..'") {
				t.Errorf("error %q should mention '..'", err.Error())
			}
		}
	})

	t.Run("relative path is rejected", func(t *testing.T) {
		for _, p := range []string{"", "data", "./data", "../data", "~/.ssh"} {
			if _, err := policy.ValidateHostPath(p); err == nil {
				t.Errorf("ValidateHostPath(%q) = nil, want error", p)
			}
		}
	})

	t.Run("symlink escape out of the allowlist is rejected", func(t *testing.T) {
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		// The literal path looks like it is inside the allowlist...
		target := filepath.Join(link, "secret")
		_, err := policy.ValidateHostPath(target)
		if err == nil {
			t.Fatalf("ValidateHostPath(%q) = nil, want error (symlink resolves to %s)", target, outside)
		}
		if !strings.Contains(err.Error(), "outside the allowed mount roots") {
			t.Errorf("error %q should report an allowlist escape", err.Error())
		}

		// A symlink pointing directly at a file outside the root too.
		direct := filepath.Join(root, "direct")
		if err := os.Symlink(filepath.Join(outside, "secret"), direct); err != nil {
			t.Fatal(err)
		}
		if _, err := policy.ValidateHostPath(direct); err == nil {
			t.Errorf("ValidateHostPath(%q) = nil, want error", direct)
		}
	})

	t.Run("nil policy allows nothing", func(t *testing.T) {
		var nilPolicy *MountPolicy
		if _, err := nilPolicy.ValidateHostPath(inside); err == nil {
			t.Error("nil policy must permit no mount, got nil error")
		}
		if len(nilPolicy.Roots()) != 0 {
			t.Error("nil policy must report no roots")
		}
	})
}

func TestMountPolicySymlinkedRootIsFollowed(t *testing.T) {
	// A root that is itself reached through a symlink must still contain its
	// own files: the candidate path and the root are compared after resolution.
	real := t.TempDir()
	inner := filepath.Join(real, "ext")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	linkRoot := filepath.Join(base, "link-root")
	if err := os.Symlink(inner, linkRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	policy := policyWith(linkRoot)
	if _, err := policy.ValidateHostPath(filepath.Join(real, "ext", "file")); err != nil {
		t.Errorf("ValidateHostPath under a symlinked root = %v, want nil", err)
	}
}

func TestValidateMount(t *testing.T) {
	root := t.TempDir()
	policy := policyWith(root)

	ok := types.SandboxMount{HostPath: filepath.Join(root, "nm"), ContainerPath: "/workspace/node_modules", Mode: "ro"}
	if err := policy.ValidateMount(ok); err != nil {
		t.Errorf("ValidateMount(valid) = %v, want nil", err)
	}
	// Empty mode is fine (callers normalize it to ro).
	if err := policy.ValidateMount(types.SandboxMount{HostPath: filepath.Join(root, "nm"), ContainerPath: "/workspace/node_modules"}); err != nil {
		t.Errorf("ValidateMount(empty mode) = %v, want nil", err)
	}

	bad := []types.SandboxMount{
		{HostPath: filepath.Join(root, "nm"), ContainerPath: "/workspace:rw", Mode: "ro"},
		{HostPath: filepath.Join(root, "nm"), ContainerPath: "/workspace,rw", Mode: "ro"},
		{HostPath: "/etc", ContainerPath: "/etc"},
		{HostPath: root + "/../etc", ContainerPath: "/etc"},
		{HostPath: "relative", ContainerPath: "/x"},
		{HostPath: filepath.Join(root, "nm"), ContainerPath: "/x", Mode: "Z"},
		{HostPath: filepath.Join(root, "nm"), ContainerPath: "/x", Mode: "rw,noexec"},
	}
	for _, m := range bad {
		if err := policy.ValidateMount(m); err == nil {
			t.Errorf("ValidateMount(%+v) = nil, want error", m)
		}
	}

	// ValidateMounts names the offending entry.
	err := policy.ValidateMounts([]types.SandboxMount{ok, {HostPath: "/etc", ContainerPath: "/etc"}})
	if err == nil {
		t.Fatal("ValidateMounts with one bad entry = nil, want error")
	}
	if !strings.Contains(err.Error(), "mount 1") || !strings.Contains(err.Error(), "/etc") {
		t.Errorf("error %q should name the entry index and the offending value", err.Error())
	}
}

func TestValidateCapability(t *testing.T) {
	denied := []string{
		"SYS_ADMIN", "SYS_PTRACE", "SYS_MODULE", "SYS_RAWIO",
		"DAC_OVERRIDE", "DAC_READ_SEARCH", "NET_ADMIN", "MKNOD",
		"SETUID", "SETGID", "SYS_BOOT", "LINUX_IMMUTABLE",
	}
	for _, cap := range denied {
		err := ValidateCapability(cap)
		if err == nil {
			t.Errorf("ValidateCapability(%q) = nil, want error", cap)
			continue
		}
		if !strings.Contains(err.Error(), cap) {
			t.Errorf("error %q must name the capability %q", err.Error(), cap)
		}
	}

	// Case and the optional CAP_ prefix do not smuggle a denied capability in.
	for _, variant := range []string{"sys_admin", "Sys_Admin", "CAP_SYS_ADMIN", "cap_net_admin", " CAP_SYS_PTRACE "} {
		if err := ValidateCapability(variant); err == nil {
			t.Errorf("ValidateCapability(%q) = nil, want error", variant)
		}
	}

	allowed := []string{"NET_BIND_SERVICE", "CHOWN", "SETPCAP", "SYS_TIME", "fowner", "CAP_SYS_NICE"}
	for _, cap := range allowed {
		if err := ValidateCapability(cap); err != nil {
			t.Errorf("ValidateCapability(%q) = %v, want nil", cap, err)
		}
	}

	malformed := []string{"", "   ", "-v", "--cap-add", "SYS ADMIN", "SYS;REBOOT", "SYS\nADMIN", "1SYS", "SYS-ADMIN"}
	for _, cap := range malformed {
		if err := ValidateCapability(cap); err == nil {
			t.Errorf("ValidateCapability(%q) = nil, want error", cap)
		}
	}
}

func TestValidateCapabilities(t *testing.T) {
	if err := ValidateCapabilities(nil); err != nil {
		t.Errorf("ValidateCapabilities(nil) = %v, want nil", err)
	}
	if err := ValidateCapabilities([]string{"CHOWN", "NET_BIND_SERVICE"}); err != nil {
		t.Errorf("ValidateCapabilities(valid) = %v, want nil", err)
	}
	err := ValidateCapabilities([]string{"CHOWN", "SYS_ADMIN"})
	if err == nil {
		t.Fatal("ValidateCapabilities with a denied cap = nil, want error")
	}
	if !strings.Contains(err.Error(), "SYS_ADMIN") {
		t.Errorf("error %q must name SYS_ADMIN", err.Error())
	}
}

func TestValidateSandboxAdditions(t *testing.T) {
	root := t.TempDir()
	policy := policyWith(root)

	if err := policy.ValidateSandboxAdditions(nil, nil); err != nil {
		t.Errorf("empty additions = %v, want nil", err)
	}

	err := policy.ValidateSandboxAdditions([]types.SandboxMount{
		{HostPath: "/", ContainerPath: "/host", Mode: "rw"},
	}, nil)
	if err == nil {
		t.Fatal("mounting / = nil error, want rejection")
	}
	if !strings.Contains(err.Error(), "\"/\"") && !strings.Contains(err.Error(), "/") {
		t.Errorf("error %q must name the offending host path", err.Error())
	}

	err = policy.ValidateSandboxAdditions([]types.SandboxMount{
		{HostPath: filepath.Join(root, "d"), ContainerPath: "/d"},
	}, []string{"SYS_PTRACE"})
	if err == nil || !strings.Contains(err.Error(), "SYS_PTRACE") {
		t.Errorf("ValidateSandboxAdditions = %v, want error naming SYS_PTRACE", err)
	}
}

func TestMountRootsIncludesEnvRoots(t *testing.T) {
	ws := t.TempDir()
	proj := t.TempDir()
	extDir := t.TempDir()
	extraA := t.TempDir()
	extraB := t.TempDir()
	t.Setenv(EnvMountRoots, extraA+string(os.PathListSeparator)+extraB)

	policy := newMountPolicy(mountRoots(extDir, ws, []string{proj}))

	for _, want := range []string{ws, proj, extDir, extraA, extraB, filepath.Clean(os.TempDir())} {
		found := false
		for _, r := range policy.Roots() {
			if r == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("roots %v missing %q", policy.Roots(), want)
		}
	}

	// A path under an env-provided root is now mountable.
	sub := filepath.Join(extraB, "models")
	if _, err := policy.ValidateHostPath(sub); err != nil {
		t.Errorf("ValidateHostPath under MIKE_EXT_MOUNT_ROOTS = %v, want nil", err)
	}
}

func TestMountRootsWithoutEnvRoots(t *testing.T) {
	t.Setenv(EnvMountRoots, "")
	policy := newMountPolicy(mountRoots("", "", nil))
	// With no explicit roots, the default extensions dir + temp dir remain.
	if len(policy.Roots()) == 0 {
		t.Fatal("expected default roots (extensions dir + temp dir)")
	}
	if _, err := policy.ValidateHostPath("/etc/passwd"); err == nil {
		t.Error("/etc/passwd must not be mountable by default")
	}
}

func TestNewMountPolicyNormalization(t *testing.T) {
	root := t.TempDir()
	// Empty/blank roots are dropped, duplicates collapsed, relative made absolute.
	policy := newMountPolicy([]string{"", "   ", root, root + "/", filepath.Clean(root)})
	if len(policy.Roots()) != 1 || policy.Roots()[0] != filepath.Clean(root) {
		t.Errorf("Roots() = %v, want [%q]", policy.Roots(), filepath.Clean(root))
	}
	// withExtraRoots keeps the originals and adds the new one.
	extra := t.TempDir()
	merged := policy.withExtraRoots(extra, "")
	if len(merged.Roots()) != 2 {
		t.Errorf("merged roots = %v, want 2 entries", merged.Roots())
	}
	// The original policy is untouched.
	if len(policy.Roots()) != 1 {
		t.Errorf("withExtraRoots mutated the receiver: %v", policy.Roots())
	}
}

func TestHasDotDotSegment(t *testing.T) {
	for _, p := range []string{"/a/../b", "..", "/../a", "a/..", "/a/.."} {
		if !hasDotDotSegment(p) {
			t.Errorf("hasDotDotSegment(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"/a/..b", "/a/b..", "/..hidden/a", "/a"} {
		if hasDotDotSegment(p) {
			t.Errorf("hasDotDotSegment(%q) = true, want false", p)
		}
	}
}

// TestValidateHostPath_RejectsDanglingSymlink: a host path that is a symlink to a
// target that does not exist yet cannot be checked against the roots, so it is
// refused rather than binding wherever the link later points.
func TestValidateHostPath_RejectsDanglingSymlink(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(base, "not-created-yet", "payload"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	p := policyWith(root)
	if _, err := p.ValidateHostPath(link); err == nil {
		t.Error("a dangling symlink accepted as a mount host path")
	}

	// A mount path that simply does not exist yet (podman creates it) stays
	// allowed — only the symlink is refused.
	if _, err := p.ValidateHostPath(filepath.Join(root, "new", "data")); err != nil {
		t.Errorf("not-yet-existing mount under an allowed root = %v, want nil", err)
	}
}
