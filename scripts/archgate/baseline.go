package main

// The baseline: today's violations, frozen, so that tomorrow's are visible.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Finding is one violation of one rule. Key deliberately excludes the line number: a violation
// pinned to a line churns every time anything above it moves, and a baseline that churns gets
// regenerated wholesale, which is the moment a ratchet stops holding.
type Finding struct {
	File   string // repo-relative, forward slashes
	Line   int    // for the message only; never part of the key
	Detail string // what the violation is (an import path, a variable, a field name)
	Why    string // shown when a NEW violation appears
}

// Key is the baseline identity of a finding.
func (f Finding) Key() string { return f.File + "|" + f.Detail }

// Location is the human-facing rendering, with the line number the key omits.
func (f Finding) Location() string {
	if f.Line == 0 {
		return f.File + ": " + f.Detail
	}
	return f.File + ":" + itoa(f.Line) + ": " + f.Detail
}

// Comparison is the outcome of holding findings against a baseline.
type Comparison struct {
	New   []Finding // present now, absent from the baseline: the regression
	Stale []string  // in the baseline, no longer found: the baseline must shrink
}

// Compare diffs findings against baseline keys. Both halves are failures, and the second one is the
// half a gate usually forgets: if a rule silently stops matching, its baseline entries go stale and
// the run still prints a clean count.
func Compare(findings []Finding, baseline map[string]bool) Comparison {
	seen := make(map[string]bool, len(findings))
	var out Comparison
	for _, f := range findings {
		seen[f.Key()] = true
		if !baseline[f.Key()] {
			out.New = append(out.New, f)
		}
	}
	for key := range baseline {
		if !seen[key] {
			out.Stale = append(out.Stale, key)
		}
	}
	sort.Slice(out.New, func(i, j int) bool { return out.New[i].Key() < out.New[j].Key() })
	sort.Strings(out.Stale)
	return out
}

// BaselinePath is where a rule's frozen violations live.
func BaselinePath(rule string) string {
	return filepath.Join("scripts", "archgate", "baseline", rule+".txt")
}

// LoadBaseline reads a baseline file. Lines are "file|detail"; blank lines and # comments are
// ignored. A missing file is returned as os.ErrNotExist so the caller can distinguish "no baseline
// yet" from "empty baseline".
func LoadBaseline(path string) (map[string]bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line] = true
	}
	return out, nil
}

// WriteBaseline rewrites a rule's baseline from the current findings, deduplicated and sorted, with
// a header that says what the file is for.
func WriteBaseline(path string, findings []Finding) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	keys := make([]string, 0, len(findings))
	seen := map[string]bool{}
	for _, f := range findings {
		if !seen[f.Key()] {
			seen[f.Key()] = true
			keys = append(keys, f.Key())
		}
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("# archgate baseline: frozen violations, regenerated with -update.\n")
	b.WriteString("# A key is <file>|<detail>; no line numbers, so unrelated edits do not churn this file.\n")
	b.WriteString("# Shrink it by fixing a violation, never by adding to it.\n")
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
