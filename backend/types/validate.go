package types

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"kanban/pkg/sandboxruntime"
)

// charTable marks ASCII alphanumeric plus the given extras, as a lookup table for the
// validators below.
func charTable(extra ...byte) [256]bool {
	var t [256]bool
	for c := byte('0'); c <= '9'; c++ {
		t[c] = true
	}
	for c := byte('a'); c <= 'z'; c++ {
		t[c] = true
	}
	for c := byte('A'); c <= 'Z'; c++ {
		t[c] = true
	}
	for _, e := range extra {
		t[e] = true
	}
	return t
}

// allowedBranchChars is a lookup table for characters permitted in git branch
// names: -, _, ., / and +.
var allowedBranchChars = charTable('-', '_', '.', '/', '+')

// ValidateBranchName ensures a branch name is safe to pass as a git ref argument.
// It rejects:
//   - empty names
//   - names starting with a hyphen (prevents git argument injection, e.g. "-f")
//   - names containing characters outside the allowed set (letters, digits,
//     -, _, ., /, +) — blocks ~ ^ : ? * [ \ space and other special characters
//   - names containing ".." (prevents git range syntax confusion and is invalid
//     per git branch naming rules)
func ValidateBranchName(name string) error {
	if len(name) == 0 {
		return fmt.Errorf("branch name is empty")
	}
	if name[0] == '-' {
		return fmt.Errorf("branch name must not start with a hyphen")
	}
	for i := 0; i < len(name); i++ {
		if !allowedBranchChars[name[i]] {
			return fmt.Errorf("branch name contains invalid character %q", name[i])
		}
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("branch name must not contain '..'")
	}
	return nil
}

// allowedRefChars is a lookup table for characters permitted in a client-supplied
// git ref: ASCII alphanumeric plus - _ . / . It is deliberately tighter than
// branch names because these refs come straight from an HTTP query parameter. SHAs,
// `HEAD`, tags and branch names pass; anything git could read as revision syntax or
// as an option (~ ^ : ? * [ ] backslash { } " ' $ ` ; space) does not.
var allowedRefChars = charTable('-', '_', '.', '/')

// ValidateGitRef ensures a client-supplied ref (e.g. the `ref` query parameter of
// /api/fs/read) is safe to pass to git inside a `ref:path` argument. It rejects an
// empty ref, a leading '-' (git would read it as an option, e.g.
// "--output=/tmp/pwn", which `git show` accepts), ".." (git range syntax),
// whitespace or NUL, and any character outside [A-Za-z0-9._/-].
//
// Callers should also pass "--end-of-options" to git commands that take a rev, so
// a ref that slips through is never parsed as an option.
func ValidateGitRef(ref string) error {
	if len(ref) == 0 {
		return fmt.Errorf("ref is empty")
	}
	if ref[0] == '-' {
		return fmt.Errorf("ref must not start with a hyphen")
	}
	if strings.ContainsAny(ref, " \t\n\r\v\f\x00") {
		return fmt.Errorf("ref must not contain whitespace")
	}
	if strings.Contains(ref, "..") {
		return fmt.Errorf("ref must not contain '..'")
	}
	for i := 0; i < len(ref); i++ {
		if !allowedRefChars[ref[i]] {
			return fmt.Errorf("ref contains invalid character %q", ref[i])
		}
	}
	return nil
}

// ValidateCommitHash requires a git object id: hexadecimal, and a length git will
// resolve. Abbreviated ids are accepted (git resolves them from 4 characters up).
//
// The value reaches git as an argument — `rev-parse --verify <hash>^{tree}`,
// `read-tree --reset -u <hash>` — so a value that is not a hash is not a late-arriving
// commit, it is a forged argument: an option-shaped one is a flag, and a rev like HEAD
// discards the task's work to restore a different commit.
func ValidateCommitHash(hash string) error {
	if hash == "" {
		return fmt.Errorf("commit hash is empty")
	}
	if len(hash) < 4 || len(hash) > 64 {
		return fmt.Errorf("commit hash %q must be 4 to 64 hexadecimal characters", hash)
	}
	for i := 0; i < len(hash); i++ {
		c := hash[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return fmt.Errorf("commit hash %q is not a git object id", hash)
		}
	}
	return nil
}

// ValidateTaskID ensures a task ID does not start with a hyphen,
// which would allow Git argument injection when the ID is used in branch names.
func ValidateTaskID(id string) error {
	if len(id) == 0 {
		return fmt.Errorf("task ID is empty")
	}
	if id[0] == '-' {
		return fmt.Errorf("task ID must not start with a hyphen")
	}
	return nil
}

// NormalizeSandboxHostIP canonicalizes the host address a port is published on. "" means
// every interface, and so do 0.0.0.0 and ::. Anything that is not an IP address is an error.
func NormalizeSandboxHostIP(ip string) (string, error) {
	trimmed := strings.TrimSpace(ip)
	if trimmed == "" {
		return "", nil
	}
	parsed := net.ParseIP(trimmed)
	if parsed == nil {
		return "", fmt.Errorf("host address %q is not an IP address", trimmed)
	}
	if v4 := parsed.To4(); v4 != nil {
		if v4.Equal(net.IPv4zero) {
			return "", nil
		}
		return v4.String(), nil
	}
	if parsed.Equal(net.IPv6zero) {
		return "", nil
	}
	return parsed.String(), nil
}

// ValidateSandboxForwardPorts checks what a project asks to publish: a container port in
// range, and a bind address that is an IP address. Nothing else is judged - the address
// reaches podman as written.
func ValidateSandboxForwardPorts(ports []PortForward) error {
	for _, pf := range ports {
		if pf.ContainerPort < 1 || pf.ContainerPort > 65535 {
			return fmt.Errorf("forward port: container port %d is out of range 1-65535", pf.ContainerPort)
		}
		if _, err := NormalizeSandboxHostIP(pf.HostIP); err != nil {
			return fmt.Errorf("forward port %d: %w", pf.ContainerPort, err)
		}
	}
	return nil
}

// NormalizeSandboxForwardPorts validates a forwarded-port list and returns it with each bind
// address canonicalized, so "0.0.0.0" is never stored where "" says the same thing.
func NormalizeSandboxForwardPorts(ports []PortForward) ([]PortForward, error) {
	if err := ValidateSandboxForwardPorts(ports); err != nil {
		return nil, err
	}
	out := make([]PortForward, len(ports))
	for i, pf := range ports {
		ip, _ := NormalizeSandboxHostIP(pf.HostIP)
		out[i] = PortForward{ContainerPort: pf.ContainerPort, HostIP: ip}
	}
	return out, nil
}

// ValidateSandboxMount checks one project Sandbox Configuration mount before it can
// reach podman.
//
// The host path is deliberately not restricted to an allowlist — "mount this host
// directory into the project's sandbox" is the feature, and a human picks it. What is
// checked are the two ways a mount silently becomes something else: a container path
// carrying ":" or ",", which rewrites the mount spec and turns "ro" into "rw", and a
// host path that is relative (podman resolves it against the server's working
// directory) or walks up.
func ValidateSandboxMount(m SandboxMount) error {
	if err := sandboxruntime.ValidateContainerPath(m.ContainerPath); err != nil {
		return err
	}
	if m.HostPath == "" {
		return fmt.Errorf("mount host path is empty")
	}
	if !filepath.IsAbs(m.HostPath) {
		return fmt.Errorf("mount host path %q is not absolute", m.HostPath)
	}
	if sandboxruntime.HasDotDotSegment(m.HostPath) {
		return fmt.Errorf("mount host path %q must not contain '..'", m.HostPath)
	}
	// The host root is refused for the same reason a container path may not be "/": the
	// operator picks which directories the sandbox sees, and "the whole filesystem,
	// read-write" is not a directory choice.
	if cleaned := filepath.Clean(m.HostPath); cleaned == string(filepath.Separator) {
		return fmt.Errorf("mount host path %q must not be the host filesystem root", m.HostPath)
	}
	if m.Mode != "" && m.Mode != "ro" && m.Mode != "rw" {
		return fmt.Errorf("mount mode %q is invalid: must be \"ro\" or \"rw\"", m.Mode)
	}
	return nil
}

// ValidateSandboxAdditions checks the two project settings that widen its container:
// the mounts above, and the capabilities re-added after `--cap-drop ALL`. The
// capability rules are sandboxruntime's, the same ones an extension manifest is
// measured against.
func ValidateSandboxAdditions(mounts []SandboxMount, capabilities []string) error {
	for i, m := range mounts {
		if err := ValidateSandboxMount(m); err != nil {
			return fmt.Errorf("mount %d: %w", i, err)
		}
	}
	if err := sandboxruntime.ValidateCapabilities(capabilities); err != nil {
		return fmt.Errorf("capabilities: %w", err)
	}
	return nil
}
