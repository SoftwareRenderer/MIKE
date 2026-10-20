package sandboxruntime

import (
	"strings"
	"testing"
)

// These tests pin the lexical rule that keeps a ref out of podman's short-name resolution —
// including the names that look alike and behave differently.

func TestIsQualifiedImageRef(t *testing.T) {
	tests := []struct {
		image string
		want  bool
		why   string
	}{
		{"docker.io/library/ubuntu:24.04", true, "explicit registry"},
		{"docker.io/alpine/git", true, "explicit registry, no tag"},
		{"quay.io/prometheus/node-exporter:v1.0", true, "third-party registry"},
		{"localhost/sandbox-mike", true, "local build"},
		{"localhost:5000/img:1", true, "registry with a port"},
		{"127.0.0.1:5000/team/img@sha256:abc", true, "ip registry with digest"},
		{"ghcr.io/o/r@sha256:abc", true, "digest, no tag"},
		{"sandbox-getmike.dev", false, "single component with a dot is a repo, not a host"},
		{"sandbox-getmike.dev:latest", false, "same, with a tag"},
		{"localhost", false, "a bare host is not a ref"},
		{"busybox:latest", false, "single component"},
		{"library/busybox:1.36", false, "namespace/name with no host"},
		{"myuser/img", false, "user/name with no host"},
		{"", false, "empty is not a ref at all"},
	}
	for _, tc := range tests {
		if got := IsQualifiedImageRef(tc.image); got != tc.want {
			t.Errorf("IsQualifiedImageRef(%q) = %v, want %v (%s)", tc.image, got, tc.want, tc.why)
		}
	}
}

func TestQualifyDockerImageRef(t *testing.T) {
	tests := []struct {
		image string
		want  string
	}{
		{"ubuntu:24.04", "docker.io/library/ubuntu:24.04"},
		{"busybox", "docker.io/library/busybox"},
		{"library/busybox:1.36", "docker.io/library/busybox:1.36"},
		{"myuser/img", "docker.io/myuser/img"},
		{"myuser/img:1.2.3", "docker.io/myuser/img:1.2.3"},
		{"myuser/img@sha256:abc", "docker.io/myuser/img@sha256:abc"},
		// Already qualified, or empty: untouched.
		{"docker.io/library/ubuntu:24.04", "docker.io/library/ubuntu:24.04"},
		{"localhost/sandbox-mike", "localhost/sandbox-mike"},
		{"quay.io/o/r", "quay.io/o/r"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := QualifyDockerImageRef(tc.image); got != tc.want {
			t.Errorf("QualifyDockerImageRef(%q) = %q, want %q", tc.image, got, tc.want)
		}
	}
}

func TestQualifyLocalImageRef(t *testing.T) {
	tests := []struct {
		image string
		want  string
	}{
		{"sandbox-getmike.dev", "localhost/sandbox-getmike.dev"},
		{"kanban-ext-gopls:1.0.0", "localhost/kanban-ext-gopls:1.0.0"},
		{"localhost/sandbox-mike", "localhost/sandbox-mike"},
		{"docker.io/library/ubuntu:24.04", "docker.io/library/ubuntu:24.04"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := QualifyLocalImageRef(tc.image); got != tc.want {
			t.Errorf("QualifyLocalImageRef(%q) = %q, want %q", tc.image, got, tc.want)
		}
	}
}

// ResolveImageRefWith with the probe injected, so the test touches neither podman nor a registry.
func TestResolveImageRefWith(t *testing.T) {
	// Only the locally-built extension image exists on this "host".
	exists := func(image string) bool {
		return image == "localhost/kanban-ext-gopls:1.0.0"
	}

	tests := []struct {
		image string
		want  string
		why   string
	}{
		{"kanban-ext-gopls:1.0.0", "localhost/kanban-ext-gopls:1.0.0", "legacy short name that IS a local build"},
		{"myuser/img:1", "docker.io/myuser/img:1", "legacy short name that is not local → docker semantics"},
		{"docker.io/library/ubuntu:24.04", "docker.io/library/ubuntu:24.04", "qualified refs are never rewritten"},
		{"localhost/sandbox-mike", "localhost/sandbox-mike", "qualified local refs are never rewritten"},
		{"", "", "empty stays empty for the caller's default"},
	}
	for _, tc := range tests {
		if got := ResolveImageRefWith(tc.image, exists); got != tc.want {
			t.Errorf("ResolveImageRefWith(%q) = %q, want %q (%s)", tc.image, got, tc.want, tc.why)
		}
	}

	// A qualified ref must not cost a probe call — that is the common path and it must stay free.
	probes := 0
	counting := func(string) bool { probes++; return false }
	_ = ResolveImageRefWith("docker.io/library/ubuntu:24.04", counting)
	_ = ResolveImageRefWith("localhost/x", counting)
	_ = ResolveImageRefWith("", counting)
	if probes != 0 {
		t.Errorf("qualified refs probed local storage %d times, want 0", probes)
	}
	// A short ref probes exactly once.
	if _ = ResolveImageRefWith("busybox:latest", counting); probes != 1 {
		t.Errorf("short ref probed local storage %d times, want 1", probes)
	}
}

// Podman reports "I could not get this image" indirectly. The mapping must name the condition the
// user can act on, and must not touch output that means something else.
func TestImageAvailabilityError(t *testing.T) {
	const image = "localhost/sandbox-getmike.dev"
	masked := []struct {
		output     string
		wantPhrase string
	}{
		{"Error: localhost/sandbox-getmike.dev: image not known", "not available locally"},
		{"Error: short-name resolution enforced but cannot prompt without a TTY", "could not be resolved to a registry"},
		{"Error: unable to find image \"x\" locally", "not available locally"},
		{"Error: unable to copy from source: requested access to the resource is denied", "could not be pulled"},
		{"Error: reading manifest latest in docker.io/library/x: manifest unknown", "could not be pulled"},
	}
	for _, tc := range masked {
		err := ImageAvailabilityError(image, []byte(tc.output))
		if err == nil {
			t.Errorf("ImageAvailabilityError(%q) = nil, want an actionable error", tc.output)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantPhrase) {
			t.Errorf("ImageAvailabilityError(%q) = %q, want it to contain %q", tc.output, err, tc.wantPhrase)
		}
		if !strings.Contains(err.Error(), image) {
			t.Errorf("ImageAvailabilityError(%q) = %q, want it to name the image", tc.output, err)
		}
	}

	// Anything else is not an image problem: the caller reports podman's own error.
	for _, other := range []string{
		"Error: a dependency to the network backend with the same name might be used",
		"Error: container name already in use",
		"",
	} {
		if err := ImageAvailabilityError(image, []byte(other)); err != nil {
			t.Errorf("ImageAvailabilityError(%q) = %v, want nil", other, err)
		}
	}
}

func TestValidateConfiguredImageRef(t *testing.T) {
	valid := []string{
		"", // not configured → the default base image
		"docker.io/library/ubuntu:24.04",
		"docker.io/alpine/git",
		"localhost/my-build",
		"quay.io/o/r@sha256:abc",
		"localhost:5000/team/img:1",
	}
	for _, image := range valid {
		if err := ValidateConfiguredImageRef(image); err != nil {
			t.Errorf("ValidateConfiguredImageRef(%q) = %v, want nil", image, err)
		}
	}

	unqualified := []string{"ubuntu:24.04", "alpine/git", "sandbox-getmike.dev"}
	for _, image := range unqualified {
		err := ValidateConfiguredImageRef(image)
		if err == nil {
			t.Fatalf("ValidateConfiguredImageRef(%q) = nil, want a fully-qualified-name error", image)
		}
		if !strings.Contains(err.Error(), "fully qualified") {
			t.Errorf("ValidateConfiguredImageRef(%q) error = %q, want it to say what is required", image, err)
		}
		// The message must name a usable alternative, not just refuse.
		if !strings.Contains(err.Error(), QualifyDockerImageRef(image)) && !strings.Contains(err.Error(), "localhost/") {
			t.Errorf("ValidateConfiguredImageRef(%q) error = %q, want an example ref", image, err)
		}
	}

	// The charset rule still applies (a ref must never become a podman flag).
	for _, image := range []string{"-v /etc:/host", "$(rm -rf /)"} {
		if err := ValidateConfiguredImageRef(image); err == nil {
			t.Errorf("ValidateConfiguredImageRef(%q) = nil, want rejection", image)
		}
	}
}

// A reference podman could misread (empty, or a leading dash) is answered "not available" without
// ever asking podman.
func TestImageExistsRefusesUnusableRefs(t *testing.T) {
	rt := NewRuntimeWithBoot(Boot{})
	for _, image := range []string{"", "-rm", "--volume=/tmp:/etc", "not a ref"} {
		if rt.ImageExists(image) {
			t.Errorf("ImageExists(%q) = true, want false without asking podman", image)
		}
	}
}
