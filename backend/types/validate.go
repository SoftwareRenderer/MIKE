package types

import (
	"fmt"
	"strings"
)

// allowedBranchChars is a lookup table for characters permitted in git branch
// names. Only ASCII alphanumeric, hyphen, underscore, dot, slash, and plus are
// allowed. This is a whitelist to prevent git argument injection and ensure
// refs are valid per git's own naming rules.
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
