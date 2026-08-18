package extensions

import (
	"strings"
	"testing"
)

func TestParseChangelog_Valid(t *testing.T) {
	data := `{"hash":"aaa","version":"1.0.0","description":"Initial release"}
{"hash":"bbb","version":"1.1.0","description":"Added feature"}`
	entries := ParseChangelog(data)
	if len(entries) != 2 {
		t.Fatalf("len = %d, want 2", len(entries))
	}
	// File order (oldest → newest) must be preserved.
	want := []struct {
		hash, version, desc string
	}{
		{"aaa", "1.0.0", "Initial release"},
		{"bbb", "1.1.0", "Added feature"},
	}
	for i, w := range want {
		if entries[i].Hash != w.hash || entries[i].Version != w.version || entries[i].Description != w.desc {
			t.Errorf("entries[%d] = %+v, want %+v", i, entries[i], w)
		}
	}
}

func TestParseChangelog_SkipsBlankLines(t *testing.T) {
	data := "\n{\"hash\":\"aaa\",\"version\":\"1.0.0\",\"description\":\"x\"}\n\n   \n{\"hash\":\"bbb\",\"version\":\"1.1.0\",\"description\":\"y\"}\n"
	entries := ParseChangelog(data)
	if len(entries) != 2 {
		t.Fatalf("len = %d, want 2", len(entries))
	}
	if entries[0].Hash != "aaa" || entries[1].Hash != "bbb" {
		t.Errorf("entries = %+v", entries)
	}
}

func TestParseChangelog_HandlesCRLF(t *testing.T) {
	data := "{\"hash\":\"aaa\",\"version\":\"1.0.0\",\"description\":\"x\"}\r\n{\"hash\":\"bbb\",\"version\":\"1.1.0\",\"description\":\"y\"}\r\n"
	entries := ParseChangelog(data)
	if len(entries) != 2 {
		t.Fatalf("len = %d, want 2", len(entries))
	}
}

func TestParseChangelog_SkipsMalformedLines(t *testing.T) {
	data := "not json\n{\"hash\":\"aaa\",\"version\":\"1.0.0\",\"description\":\"x\"}\n{\"hash\":\n[1,2,3]"
	entries := ParseChangelog(data)
	if len(entries) != 1 {
		t.Fatalf("len = %d, want 1", len(entries))
	}
	if entries[0].Hash != "aaa" {
		t.Errorf("entries[0] = %+v", entries[0])
	}
}

func TestParseChangelog_Empty(t *testing.T) {
	if got := ParseChangelog(""); got != nil {
		t.Errorf("ParseChangelog(\"\") = %+v, want nil", got)
	}
	if got := ParseChangelog("   \n\t  \n"); got != nil {
		t.Errorf("ParseChangelog(whitespace) = %+v, want nil", got)
	}
}

func TestParseChangelog_PartialFields(t *testing.T) {
	// Lines may omit optional fields; parsing must not fail.
	data := "{\"hash\":\"aaa\",\"version\":\"1.0.0\"}"
	entries := ParseChangelog(data)
	if len(entries) != 1 {
		t.Fatalf("len = %d, want 1", len(entries))
	}
	if entries[0].Description != "" {
		t.Errorf("Description = %q, want empty", entries[0].Description)
	}
}

func TestChangelogSince_AfterMatch(t *testing.T) {
	entries := []ChangelogEntry{
		{Hash: "aaa", Version: "1.0.0", Description: "one"},
		{Hash: "bbb", Version: "1.1.0", Description: "two"},
		{Hash: "ccc", Version: "2.0.0", Description: "three"},
	}
	got := ChangelogSince(entries, "bbb")
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Hash != "ccc" || got[0].Description != "three" {
		t.Errorf("got[0] = %+v, want ccc/three", got[0])
	}
}

func TestChangelogSince_MatchAtStart(t *testing.T) {
	entries := []ChangelogEntry{
		{Hash: "aaa"}, {Hash: "bbb"}, {Hash: "ccc"},
	}
	got := ChangelogSince(entries, "aaa")
	if len(got) != 2 || got[0].Hash != "bbb" || got[1].Hash != "ccc" {
		t.Errorf("got = %+v, want [bbb ccc]", got)
	}
}

func TestChangelogSince_MatchAtEnd(t *testing.T) {
	entries := []ChangelogEntry{{Hash: "aaa"}, {Hash: "bbb"}}
	if got := ChangelogSince(entries, "bbb"); len(got) != 0 {
		t.Errorf("got = %+v, want empty", got)
	}
}

func TestChangelogSince_CaseInsensitive(t *testing.T) {
	entries := []ChangelogEntry{
		{Hash: "AAAA"},
		{Hash: "bbb", Version: "1.1.0", Description: "newer"},
	}
	got := ChangelogSince(entries, "aaaa")
	if len(got) != 1 || got[0].Hash != "bbb" {
		t.Errorf("got = %+v, want [bbb]", got)
	}
}

func TestChangelogSince_NoMatch(t *testing.T) {
	entries := []ChangelogEntry{{Hash: "aaa"}, {Hash: "bbb"}}
	// No matching hash → show nothing (per spec).
	if got := ChangelogSince(entries, "zzz"); len(got) != 0 {
		t.Errorf("got = %+v, want empty", got)
	}
}

func TestChangelogSince_EmptySinceHash(t *testing.T) {
	entries := []ChangelogEntry{{Hash: "aaa"}}
	if got := ChangelogSince(entries, ""); len(got) != 0 {
		t.Errorf("got = %+v, want empty", got)
	}
}

func TestChangelogSince_FirstMatchWins(t *testing.T) {
	// Duplicate hashes: anchor on the FIRST (oldest) occurrence.
	entries := []ChangelogEntry{
		{Hash: "aaa"},
		{Hash: "aaa"},
		{Hash: "bbb"},
	}
	got := ChangelogSince(entries, "aaa")
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (entries after the first aaa)", len(got))
	}
	if got[0].Hash != "aaa" || got[1].Hash != "bbb" {
		t.Errorf("got = %+v, want [aaa bbb]", got)
	}
}

func TestParseChangelog_ChangelogSince_RoundTrip(t *testing.T) {
	// End-to-end: parse a realistic file, then filter by installed hash.
	lines := []string{}
	hashes := []string{
		strings.Repeat("a", 64),
		strings.Repeat("b", 64),
		strings.Repeat("c", 64),
	}
	versions := []string{"1.0.0", "1.1.0", "2.0.0"}
	descs := []string{"initial", "v1.1", "v2.0"}
	for i, h := range hashes {
		lines = append(lines, `{"hash":"`+h+`","version":"`+versions[i]+`","description":"`+descs[i]+`"}`)
	}
	data := strings.Join(lines, "\n")
	entries := ParseChangelog(data)
	got := ChangelogSince(entries, strings.Repeat("a", 64))
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Description != "v1.1" || got[1].Description != "v2.0" {
		t.Errorf("got = %+v", got)
	}
}
