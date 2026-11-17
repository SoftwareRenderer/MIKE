package extensions

// A changelog is published text, and it is best-effort: nothing in the update path may fail because a
// line was unparseable or the file is absent. So the two decisions are that usable lines survive in
// the order the file holds them (oldest → newest) whatever the noise around them, and that the slice
// shown is anchored on the installed commit's hash — the first match, so a duplicated hash cannot
// anchor the reader on the newer copy and hide the entries in between.

import (
	"strings"
	"testing"
)

func TestParseChangelog(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []ChangelogEntry
	}{
		{
			name: "file order (oldest → newest) is preserved",
			data: `{"hash":"aaa","version":"1.0.0","description":"Initial release"}` + "\n" +
				`{"hash":"bbb","version":"1.1.0","description":"Added feature"}`,
			want: []ChangelogEntry{
				{Hash: "aaa", Version: "1.0.0", Description: "Initial release"},
				{Hash: "bbb", Version: "1.1.0", Description: "Added feature"},
			},
		},
		{
			name: "blank and whitespace-only lines are skipped",
			data: "\n{\"hash\":\"aaa\",\"version\":\"1.0.0\",\"description\":\"x\"}\n\n   \n{\"hash\":\"bbb\",\"version\":\"1.1.0\",\"description\":\"y\"}\n",
			want: []ChangelogEntry{
				{Hash: "aaa", Version: "1.0.0", Description: "x"},
				{Hash: "bbb", Version: "1.1.0", Description: "y"},
			},
		},
		{
			name: "a CRLF file parses",
			data: "{\"hash\":\"aaa\",\"version\":\"1.0.0\",\"description\":\"x\"}\r\n{\"hash\":\"bbb\",\"version\":\"1.1.0\",\"description\":\"y\"}\r\n",
			want: []ChangelogEntry{
				{Hash: "aaa", Version: "1.0.0", Description: "x"},
				{Hash: "bbb", Version: "1.1.0", Description: "y"},
			},
		},
		{
			name: "a malformed line never takes the good ones with it",
			data: "not json\n{\"hash\":\"aaa\",\"version\":\"1.0.0\",\"description\":\"x\"}\n{\"hash\":\n[1,2,3]",
			want: []ChangelogEntry{
				{Hash: "aaa", Version: "1.0.0", Description: "x"},
			},
		},
		{
			name: "an optional field stays empty rather than failing the line",
			data: "{\"hash\":\"aaa\",\"version\":\"1.0.0\"}",
			want: []ChangelogEntry{{Hash: "aaa", Version: "1.0.0"}},
		},
		{name: "no data", data: "", want: nil},
		{name: "only whitespace", data: "   \n\t  \n", want: nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseChangelog(tc.data)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (%+v)", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				if got[i] != w {
					t.Errorf("entries[%d] = %+v, want %+v", i, got[i], w)
				}
			}
		})
	}
}

func TestChangelogSince(t *testing.T) {
	three := []ChangelogEntry{
		{Hash: "aaa", Version: "1.0.0", Description: "one"},
		{Hash: "bbb", Version: "1.1.0", Description: "two"},
		{Hash: "ccc", Version: "2.0.0", Description: "three"},
	}
	tests := []struct {
		name    string
		entries []ChangelogEntry
		since   string
		want    []ChangelogEntry
	}{
		{"everything after the match", three, "bbb",
			[]ChangelogEntry{{Hash: "ccc", Version: "2.0.0", Description: "three"}}},
		{"a match at the start reports the rest", three, "aaa", []ChangelogEntry{
			{Hash: "bbb", Version: "1.1.0", Description: "two"},
			{Hash: "ccc", Version: "2.0.0", Description: "three"},
		}},
		{"a match at the end reports nothing", three, "ccc", nil},
		{"the hash is compared case-insensitively", three, "BBB",
			[]ChangelogEntry{{Hash: "ccc", Version: "2.0.0", Description: "three"}}},
		{"no match shows nothing", three, "zzz", nil},
		{"no installed hash shows nothing", three, "", nil},
		{
			// A duplicated hash anchors on the FIRST (oldest) occurrence, or the entries between
			// the two copies stay hidden from the person who is on the older one.
			name:    "the first match wins",
			entries: []ChangelogEntry{{Hash: "aaa"}, {Hash: "aaa"}, {Hash: "bbb"}},
			since:   "aaa",
			want:    []ChangelogEntry{{Hash: "aaa"}, {Hash: "bbb"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ChangelogSince(tc.entries, tc.since)
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			for i, want := range tc.want {
				if got[i] != want {
					t.Errorf("got[%d] = %+v, want %+v", i, got[i], want)
				}
			}
		})
	}
}

// A realistic file read end to end: the published text through the parser, then filtered by the
// installed commit — the pair `GET /api/extensions/check-updates` runs on every row.
func TestChangelogRoundTripOfAPublishedFile(t *testing.T) {
	hashes := []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)}
	versions := []string{"1.0.0", "1.1.0", "2.0.0"}
	descs := []string{"initial", "v1.1", "v2.0"}
	lines := make([]string, 0, len(hashes))
	for i, h := range hashes {
		lines = append(lines, `{"hash":"`+h+`","version":"`+versions[i]+`","description":"`+descs[i]+`"}`)
	}

	got := ChangelogSince(ParseChangelog(strings.Join(lines, "\n")), hashes[0])
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (%+v)", len(got), got)
	}
	if got[0].Description != "v1.1" || got[1].Description != "v2.0" {
		t.Errorf("got = %+v, want v1.1 then v2.0", got)
	}
}
