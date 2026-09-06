package types

import (
	"fmt"
	"net"
	"strings"
)

// allowedBranchChars is a lookup table for characters permitted in git branch
// names. Only ASCII alphanumeric, hyphen, underscore, dot, slash, and plus are
// allowed.
var allowedBranchChars = func() [256]bool {
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
	t['-'] = true
	t['_'] = true
	t['.'] = true
	t['/'] = true
	t['+'] = true
	return t
}()

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
var allowedRefChars = func() [256]bool {
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
	t['-'] = true
	t['_'] = true
	t['.'] = true
	t['/'] = true
	return t
}()

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
