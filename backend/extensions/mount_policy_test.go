package extensions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/types"
	"mike/pkg/sandboxruntime"
)

// policyWith returns a policy allowing only the given roots.
func policyWith(roots ...string) *MountPolicy {
	return newMountPolicy(roots)
}

// TestValidateContainerPath is the tape an extension's mount is measured with before it reaches
// podman: ':' and ',' are the two characters that can rewrite a `-v` spec, so they are refused
// anywhere in a container path — a user value can never change the mount mode. The rule itself
// lives in pkg/sandboxruntime; what is pinned here is every shape a manifest or a settings form
// can arrive with, including the one that opens an option field and leaves it dangling.
func TestValidateContainerPath(t *testing.T) {
	valid := []string{
		"/workspace",
		"/mnt/node_modules",
		"/a.b-c_d/e.f",
		"/go/pkg/mod",
	}
	for _, p := range valid {
		if err := sandboxruntime.ValidateContainerPath(p); err != nil {
			t.Errorf("sandboxruntime.ValidateContainerPath(%q) = %v, want nil", p, err)
		}
	}

	// named is whether the refusal repeats the value; a control character reaches the message
	// through %q, so it cannot be matched byte for byte.
	invalid := []struct {
		value string
		named bool
	}{
		{"", false},
		{"/", true},                           // the container root itself
		{"//", true},                          // ... spelled another way
		{"workspace", true},                   // not absolute
		{"./workspace", true},                 // not absolute
		{"/work space", true},                 // space
		{"/workspace;rm -rf /", true},         // shell metacharacters
		{"/workspace,rw", true},               // ',' rewrites the mount options
		{"/workspace:rw", true},               // ':' rewrites the spec (and the mode)
		{"/workspace:/etc:rprivate,rw", true}, // full spec injection
		{"/x:ro,", true},                      // the option field opened and left dangling
		{"/x:,ro", true},
		{"/workspace\x00", false},             // control character
		{"/mnt/../etc", true},                 // '..' segment
		{"/mnt/node_modules/../../etc", true}, // '..' segment
		{"/work\u00fcst", true},               // non-ASCII
		{"-v/etc:/host", true},                // leading dash = podman flag
	}
	for _, tc := range invalid {
		err := sandboxruntime.ValidateContainerPath(tc.value)
		if err == nil {
			t.Errorf("sandboxruntime.ValidateContainerPath(%q) = nil, want error", tc.value)
			continue
		}
		// The refusal is shown to the person who wrote the mount, so it repeats the value.
		if tc.named && !strings.Contains(err.Error(), tc.value) {
			t.Errorf("error %q does not name the offending value %q", err, tc.value)
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
			t.Fatalf("symlinks unavailable: %v", err)
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
		t.Fatalf("symlinks unavailable: %v", err)
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

// TestValidateSandboxAdditions is the validator behind install, update and
// PUT /api/extensions/{id}: a mount outside the allowlist and a capability that is not one plain
// name are both refused before anything is stored. Which capability *names* are acceptable is not
// judged here — the grant lands in a gVisor guest that started from --cap-drop ALL — and the shape
// rule it reaches is the one every container shares (sandboxruntime.ValidateCapability, whose table
// TestValidateCapabilityRule in pkg/sandboxruntime pins); the pairing that makes a name like
// SYS_ADMIN safe is TestBuildExtensionCreateArgs_CapabilityPosture.
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

	// A mount inside the allowlist is fine; a capability that is not a plain name is not.
	err = policy.ValidateSandboxAdditions([]types.SandboxMount{
		{HostPath: filepath.Join(root, "d"), ContainerPath: "/d"},
	}, []string{"--privileged"})
	if err == nil || !strings.Contains(err.Error(), "--privileged") {
		t.Errorf("ValidateSandboxAdditions = %v, want an error naming --privileged", err)
	}
	// And the names themselves are not judged — the container is a gVisor guest.
	if err := policy.ValidateSandboxAdditions([]types.SandboxMount{
		{HostPath: filepath.Join(root, "d"), ContainerPath: "/d"},
	}, []string{"SYS_ADMIN", "SYS_PTRACE"}); err != nil {
		t.Errorf("ValidateSandboxAdditions(plain names) = %v, want nil", err)
	}
}

// TestMountRootsIncludesTheWiredExtras: MIKE_EXT_MOUNT_ROOTS arrives as an argument (resolved by
// startup, wired by NewServer) and lands in the allowlist beside the implicit roots.
func TestMountRootsIncludesTheWiredExtras(t *testing.T) {
	ws := t.TempDir()
	proj := t.TempDir()
	extDir := t.TempDir()
	extraA := t.TempDir()
	extraB := t.TempDir()

	policy := newMountPolicy(mountRoots(extDir, ws, []string{proj}, []string{extraA, extraB}))

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

func TestMountRootsWithoutExtras(t *testing.T) {
	policy := newMountPolicy(mountRoots("", "", nil, nil))
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

// --- Project-configured roots (shared LSP sandbox) ---------------------------
//
// The LSP sandbox borrows the caches a project already bind-mounts for its own
// task sandbox. Borrowing is allowed; writing through them is not.

func TestMountPolicyProjectRootsAreAllowedButPinnedReadOnly(t *testing.T) {
	dir := t.TempDir()
	appRoot := filepath.Join(dir, "app")
	projectCache := filepath.Join(dir, "go-cache")
	for _, d := range []string{appRoot, projectCache} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := newMountPolicyWithReadOnly([]string{appRoot, projectCache}, []string{projectCache})

	// The borrowed path is mountable at all — this is the whole point: without it
	// a language server never sees the modules its workspace can compile against.
	borrowed, err := p.ValidateHostPath(filepath.Join(projectCache, "pkg", "mod"))
	if err != nil {
		t.Fatalf("project-configured path rejected: %v", err)
	}
	if !p.ForceReadOnly(borrowed) {
		t.Error("a project's own Sandbox Configuration path must be pinned read-only")
	}

	owned, err := p.ValidateHostPath(filepath.Join(appRoot, "data"))
	if err != nil {
		t.Fatalf("app-owned path rejected: %v", err)
	}
	if p.ForceReadOnly(owned) {
		t.Error("an app-owned path should keep whatever mode the caller asked for")
	}

	// Widening for the borrowed cache must not widen anything else.
	if _, err := p.ValidateHostPath("/etc/shadow"); err == nil {
		t.Error("a path outside every root was accepted")
	}
}

func TestMountPolicyNilPolicyIsReadOnly(t *testing.T) {
	var p *MountPolicy
	if !p.ForceReadOnly("/anything") {
		t.Error("a nil policy must fail closed to read-only")
	}
}

func TestNewMountPolicyWithReadOnlyNormalizesBothLists(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cache")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	// Same directory reached two ways must not become two roots.
	p := newMountPolicyWithReadOnly([]string{target, target + "/./", "  "}, []string{"./", target})
	if len(p.Roots()) != 1 {
		t.Errorf("roots = %v, want exactly one", p.Roots())
	}
	resolved, err := p.ValidateHostPath(target)
	if err != nil {
		t.Fatal(err)
	}
	if !p.ForceReadOnly(resolved) {
		t.Error("read-only root not recognized after normalization")
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
		t.Fatalf("symlinks unavailable: %v", err)
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
