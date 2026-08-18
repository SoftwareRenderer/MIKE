package extensions

import (
	"encoding/json"
	"strings"
)

// ChangelogEntry is a single line of an extension's CHANGELOG.jsonl file.
type ChangelogEntry struct {
	Hash        string `json:"hash"`        // full commit hash the change shipped in
	Version     string `json:"version"`     // extension version at that commit
	Description string `json:"description"` // human-readable description of the change
}

// ParseChangelog parses a JSONL changelog: one JSON object per line, ordered
// oldest → newest. Blank lines and lines that fail to parse are skipped so a
// single malformed line never discards the whole file.
func ParseChangelog(data string) []ChangelogEntry {
	var entries []ChangelogEntry
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e ChangelogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries
}

// ChangelogSince returns the entries strictly newer than the entry whose hash
// equals sinceHash (case-insensitive) — i.e. everything after it in the
// oldest → newest ordering. It anchors on the FIRST matching occurrence.
// Returns empty when sinceHash is empty or no entry matches it (per spec,
// an unknown installed commit shows no changes).
func ChangelogSince(entries []ChangelogEntry, sinceHash string) []ChangelogEntry {
	if sinceHash == "" {
		return nil
	}
	for i, e := range entries {
		if strings.EqualFold(e.Hash, sinceHash) {
			return entries[i+1:]
		}
	}
	return nil
}
