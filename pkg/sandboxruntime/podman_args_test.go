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

// TestValidateCapabilitySharedRule covers the denylist that used to be an
// extension-only rule: a project asking for SYS_ADMIN was never refused, so the
// sandbox around a task was only as strong as the extension policy it did not share.
func TestValidateCapabilitySharedRule(t *testing.T) {
	for _, ok := range []string{"NET_RAW", "CHOWN", "SETFCAP", "cap_net_raw"} {
		if err := ValidateCapability(ok); err != nil {
			t.Errorf("ValidateCapability(%q) = %v, want nil", ok, err)
		}
	}
	for _, denied := range []string{"SYS_ADMIN", "cap_sys_admin", "DAC_OVERRIDE", "SYS_PTRACE", "LINUX_IMMUTABLE"} {
		err := ValidateCapability(denied)
		if err == nil {
			t.Errorf("ValidateCapability(%q) = nil, want a denial", denied)
			continue
		}
		if !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("ValidateCapability(%q) should deny by name: %v", denied, err)
		}
	}
	// Everything else is rejected as malformed: a value that reaches `--cap-add`
	// must never look like another flag or carry a separator.
	for _, bad := range []string{"", "   ", "--privileged", "SYS_ADMIN ALL", "SYS_ADMIN,ALL", "SYS-ADMIN", "0SYS", "SYS.ADMIN"} {
		if err := ValidateCapability(bad); err == nil {
			t.Errorf("ValidateCapability(%q) = nil, want error", bad)
		}
	}
	if err := ValidateCapabilities([]string{"CHOWN", "SYS_ADMIN"}); err == nil {
		t.Error("ValidateCapabilities accepted a list containing a denied capability")
	}
}

// TestDeniedCapabilitiesAreNamed asserts the table is self-explanatory, because every
// entry shows up in an error message a user has to act on.
func TestDeniedCapabilitiesAreNamed(t *testing.T) {
	if len(DeniedCapabilities) == 0 {
		t.Fatal("the capability denylist is empty")
	}
	for cap, reason := range DeniedCapabilities {
		if cap != normalizeCapability(cap) {
			t.Errorf("DeniedCapabilities key %q is not in its normalized form", cap)
		}
		if reason == "" {
			t.Errorf("denied capability %q has no reason to show the user", cap)
		}
	}
}
