package sandboxruntime

import (
	"strings"
	"testing"
)

// TestValidateContainerPathSharedRule is the tape every container's mounts are
// measured with, task sandbox or extension sandbox. The two characters that must
// never appear are the ones the podman mount spec is built out of: a container path of
// "/workspace:rw" is not a weird name, it is a second option field, and it is how a
// read-only mount quietly becomes writable.
func TestValidateContainerPathSharedRule(t *testing.T) {
	for _, ok := range []string{"/workspace/node_modules", "/go", "/tmp/x.y-z_w", "/a/b/c"} {
		if err := ValidateContainerPath(ok); err != nil {
			t.Errorf("ValidateContainerPath(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"", "relative/path", "/", "//", "/.", "/workspace:rw", "/workspace,rw",
		"/workspace/../etc", "..", "/etc/../", "/workspace/..", "/home/$(id)", "/a b",
		"/work\tspace",
	} {
		err := ValidateContainerPath(bad)
		if err == nil {
			t.Errorf("ValidateContainerPath(%q) = nil, want error", bad)
			continue
		}
		if bad == "/workspace:rw" && !strings.Contains(err.Error(), "rewrite the podman mount spec") {
			t.Errorf("ValidateContainerPath(%q) should say why: %v", bad, err)
		}
	}
}

// TestHasDotDotSegment is the near-miss half of the rule: a segment that merely
// contains ".." is a legal file name, and refusing one would refuse real mounts.
func TestHasDotDotSegment(t *testing.T) {
	for _, p := range []string{"/a/../b", "..", "/../a", "a/..", "/a/.."} {
		if !HasDotDotSegment(p) {
			t.Errorf("HasDotDotSegment(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"/a/..b", "/a/b..", "/..hidden/a", "/a"} {
		if HasDotDotSegment(p) {
			t.Errorf("HasDotDotSegment(%q) = true, want false", p)
		}
	}
}

// TestValidateCapabilityRule pins what the capability rule is now: the *shape* of the
// value, never its name. A capability granted to a gVisor container is a guest privilege
// (see GVisorIsolationForCaps), so SYS_ADMIN is accepted here and the runtime check at the
// call site is what keeps that true. What stays refused is anything that would arrive at
// `--cap-add` as something other than one name — that is argument injection, and no
// runtime makes it safe.
func TestValidateCapabilityRule(t *testing.T) {
	for _, ok := range []string{
		"NET_RAW", "CHOWN", "SETFCAP", "cap_net_raw", "SYS_TIME", "fowner",
		// Every name the denylist used to refuse: allowed now, because the container
		// they land in is a gVisor guest that started from --cap-drop ALL.
		"SYS_ADMIN", "SYS_PTRACE", "SYS_MODULE", "SYS_RAWIO", "DAC_OVERRIDE",
		"DAC_READ_SEARCH", "NET_ADMIN", "MKNOD", "SETUID", "SETGID", "SYS_BOOT",
		"LINUX_IMMUTABLE",
	} {
		if err := ValidateCapability(ok); err != nil {
			t.Errorf("ValidateCapability(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"", "   ", "--privileged", "--cap-add", "-v", "SYS_ADMIN ALL", "SYS_ADMIN,ALL",
		"SYS-ADMIN", "SYS.ADMIN", "0SYS", "SYS;REBOOT", "SYS\nADMIN",
	} {
		if err := ValidateCapability(bad); err == nil {
			t.Errorf("ValidateCapability(%q) = nil, want error", bad)
		}
	}
	// The list form names the first offender, because it answers a 400.
	if err := ValidateCapabilities([]string{"CHOWN", "--privileged"}); err == nil {
		t.Error("ValidateCapabilities accepted a list holding an option-shaped value")
	} else if !strings.Contains(err.Error(), "--privileged") {
		t.Errorf("error %q should name the offending entry", err.Error())
	}
	if err := ValidateCapabilities([]string{"CHOWN", "SYS_ADMIN"}); err != nil {
		t.Errorf("ValidateCapabilities(valid names) = %v, want nil", err)
	}
}

// TestGVisorIsolationForCaps pins the pairing a capability grant now relies on. The
// denylist used to be the defence; it is gone, and what is left is the fact that the
// container is runsc-isolated and started with every capability dropped. A command line
// missing either turns `--cap-add SYS_ADMIN` into a host-kernel privilege, which is
// exactly the regression this refuses.
func TestGVisorIsolationForCaps(t *testing.T) {
	isolated := []string{"create", "--runtime", "runsc", "--cap-drop", "ALL"}
	if err := GVisorIsolationForCaps(isolated); err != nil {
		t.Errorf("GVisorIsolationForCaps(%v) = %v, want nil", isolated, err)
	}

	cases := map[string][]string{
		"no runtime selected":    {"create", "--cap-drop", "ALL"},
		"a different runtime":    {"create", "--runtime", "crun", "--cap-drop", "ALL"},
		"caps not dropped":       {"create", "--runtime", "runsc"},
		"caps partially dropped": {"create", "--runtime", "runsc", "--cap-drop", "SYS_ADMIN"},
	}
	for name, args := range cases {
		if err := GVisorIsolationForCaps(args); err == nil {
			t.Errorf("%s: GVisorIsolationForCaps(%v) = nil, want a refusal", name, args)
		}
	}
}
