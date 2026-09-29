package fsutil

import (
	"strings"
	"testing"
)

// ValidateRelPath decides whether a client-supplied relative path may be joined
// onto a directory the caller has already contained. These are the shapes that
// must never reach that join.
func TestValidateRelPathRejects(t *testing.T) {
	rejected := map[string]string{
		"empty":              "",
		"absolute":           "/etc/passwd",
		"parent segment":     "../escape",
		"nested parent":      "a/b/../../../escape",
		"dot segment":        "./a",
		"dotdot at end":      "a/..",
		"empty segment":      "a//b",
		"trailing slash":     "a/",
		"leading slash":      "/a/b",
		"backslash":          `a\b`,
		"backslash escape":   `a\..\b`,
		"nul":                "a\x00b",
		"newline":            "a\nb",
		"tab":                "a\tb",
		"del":                "a\x7fb",
		"reserved temp name": AtomicTempPrefix + "x",
		"nested temp name":   "tmp/" + AtomicTempPrefix + "x",
		"over-long path":     strings.Repeat("a", MaxRelPathLen+1),
		"over-long segment":  strings.Repeat("a", MaxRelSegmentLen+1),
	}
	for name, rel := range rejected {
		if err := ValidateRelPath(rel); err == nil {
			t.Errorf("%s: %q was accepted", name, rel)
		}
	}
}

// A dropped file keeps its own name and its own folder — Unicode, spaces, dots and
// leading dots included. Only the shapes above are refused.
func TestValidateRelPathAccepts(t *testing.T) {
	accepted := []string{
		"notes.txt",
		"my report (final).md",
		"assets/img/logo@2x.png",
		"中文/说明.md",
		".gitignore",
		".env.local",
		"a/b/c/d/e/f/g.go",
		"..hidden",
		"dot..name",
		"-dash-start.sh",
		strings.Repeat("a", MaxRelSegmentLen),
	}
	for _, rel := range accepted {
		if err := ValidateRelPath(rel); err != nil {
			t.Errorf("%q was refused: %v", rel, err)
		}
	}
}
