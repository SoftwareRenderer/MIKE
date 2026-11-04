package sandboxruntime

import "strings"

// ShellQuote wraps one argument so that `/bin/sh -c` reads it as one word, whatever it contains.
// Command lines here are built by concatenation, so quote the argument, never the whole line.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
