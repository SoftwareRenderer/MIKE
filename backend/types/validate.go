package types

import (
	"fmt"
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

// allowedRefChars is a lookup table for characters permitted in a client-
// supplied git ref. Only ASCII alphanumeric, hyphen, underscore, dot and slash
// are allowed — a deliberately tighter set than branch names, because refs of
// this kind come straight from an HTTP query parameter. Hex SHAs (0-9a-f),
// `HEAD`, tags and branch names all pass; everything git could read as
// revision syntax or as an option (~ ^ : ? * [ ] backslash { } " ' $ ` ; space)
// does not.
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

// ValidateGitRef ensures a client-supplied ref (e.g. the `ref` query parameter
// of /api/fs/read) is safe to pass to git as part of a `ref:path` argument.
// It rejects:
//   - empty refs
//   - refs starting with a hyphen (prevents git argument injection, e.g.
//     "--output=/tmp/pwn" which `git show` happily accepts as an option)
//   - refs containing ".." (prevents git range syntax confusion)
//   - refs containing whitespace or NUL (never valid in a ref, and the usual
//     carrier of shell/argument tricks)
//   - refs with any character outside [A-Za-z0-9._/-] — blocks the revision
//     operators (~ ^ :), globs (* ? [ ]), quoting and expansion ({ } " ' $ ` ;)
//
// Callers should additionally pass "--end-of-options" to git commands that take
// a rev, so a ref that slips through is never parsed as an option.
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
