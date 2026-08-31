// Package shlex renders argument lists as shell command lines.
//
// Two host-side callers need this: the task-sandbox probe runner (which hands
// argv to `podman exec … sh -c`) and the language-server environment check
// (which hands a command to the sandbox bridge). Both run something a manifest
// declared, so no word of it may be live shell syntax.
package shlex

import "strings"

// Quote single-quotes one word so the shell treats it as a literal. A quote
// inside the word closes the quoting, adds an escaped quote, and reopens it —
// the standard trick, and the reason no argument can break out of its own word.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Join renders argv as a command line: every word quoted, space-separated.
func Join(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = Quote(a)
	}
	return strings.Join(quoted, " ")
}
