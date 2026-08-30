package toolresult

import (
	"strings"
	"testing"
)

// marker is what the client looks for to know the output was cut short.
const marker = "... (truncated)"

// TestTruncate is the whole of Truncate's contract in one place: it reports the
// ORIGINAL line/byte counts back and rewrites the preview once either threshold
// (more than 10 lines, more than 1024 bytes) is crossed. Each row is one point of
// that function; the rows around 10 lines and around 1024 bytes exist in pairs so a
// boundary shift in either direction shows up as exactly one failing row.
func TestTruncate(t *testing.T) {
	emoji := "\U0001F600"      // 😀 — 4 bytes per rune
	japaneseLine := "日本語テスト\n" // 6 runes × 3 bytes + newline = 19 bytes

	cases := []struct {
		name  string
		input string
		// wantLines/wantBytes are the ORIGINAL counts (not preview counts) and
		// wantTrunc is whether either threshold was crossed.
		wantLines int
		wantBytes int
		wantTrunc bool
		// check covers the assertions that are not about the marker: exact preview,
		// preview length, newline count, rune-safety. Nil for the plain rows.
		check func(t *testing.T, preview string)
	}{
		{
			name:      "short output",
			input:     "total 100\ndrwxr-xr-x  5 user user  4096 Jan 1 .\n",
			wantLines: 3,
			wantBytes: len("total 100\ndrwxr-xr-x  5 user user  4096 Jan 1 .\n"),
		},
		{
			name:      "empty output still counts as one line (strings.Split semantics)",
			input:     "",
			wantLines: 1,
			wantBytes: 0,
		},
		{
			name:      "exactly 10 lines under 1KB",
			input:     strings.Repeat("line\n", 9) + "line", // 9 newlines → 10 elements, 49 bytes
			wantLines: 10,
			wantBytes: 49,
		},
		{
			name:      "just over 10 lines",
			input:     strings.Repeat("line\n", 11), // 12 elements: 11 lines + trailing empty
			wantLines: 12,
			wantBytes: 55,
			wantTrunc: true,
			// maxLines = min(10, 12) = 10 → 9 lines joined + marker
			check: exactPreview(strings.Repeat("line\n", 9) + marker),
		},
		{
			name:      "more than 10 lines",
			input:     strings.Repeat("line of output text\n", 20),
			wantLines: 21, // trailing empty element after the final newline
			wantBytes: len(strings.Repeat("line of output text\n", 20)),
			wantTrunc: true,
			check:     newlinesUnder(20),
		},
		{
			name:      "exactly 1024 bytes in 2 lines",
			input:     strings.Repeat("x", 1023) + "\n",
			wantLines: 2,
			wantBytes: 1024,
		},
		{
			name:      "just over 1024 bytes in 2 lines",
			input:     strings.Repeat("x", 1024) + "\n",
			wantLines: 2,
			wantBytes: 1025,
			wantTrunc: true,
		},
		{
			name:      "over 1KB with few lines",
			input:     strings.Repeat("x", 600) + "\n" + strings.Repeat("x", 600),
			wantLines: 2,
			wantBytes: 1201,
			wantTrunc: true,
		},
		{
			name:      "exactly 10 lines and exactly 1024 bytes",
			input:     strings.Repeat("x\n", 9) + strings.Repeat("x", 1006), // 18 + 1006
			wantLines: 10,
			wantBytes: 1024,
		},
		{
			name:      "both over 10 lines and over 1KB",
			input:     strings.Repeat(strings.Repeat("x", 100)+"\n", 20),
			wantLines: 21,
			wantBytes: len(strings.Repeat(strings.Repeat("x", 100)+"\n", 20)),
			wantTrunc: true,
		},
		{
			name:      "single very long line (maxLines collapses to 1)",
			input:     strings.Repeat("x", 2000),
			wantLines: 1,
			wantBytes: 2000,
			wantTrunc: true,
			check: func(t *testing.T, preview string) {
				endSuffix(t, preview)
				// Rune-slicing path: 1024 runes of content + the marker.
				if want := 1024 + len(marker); len(preview) != want {
					t.Errorf("preview length = %d, want %d", len(preview), want)
				}
			},
		},
		{
			name:      "multi-byte UTF-8 under 1024 runes",
			input:     strings.Repeat(emoji, 300), // 1200 bytes but only 300 runes
			wantLines: 1,
			wantBytes: 1200,
			wantTrunc: true,
			check: func(t *testing.T, preview string) {
				endSuffix(t, preview)
				// Fewer than 1024 runes, so nothing is cut — the marker is all that's added.
				body := strings.TrimSuffix(preview, marker)
				if n := len([]rune(body)); n != 300 {
					t.Errorf("preview runes = %d, want 300", n)
				}
				noReplacementChars(t, body)
			},
		},
		{
			name:      "multi-byte UTF-8 over 1024 runes",
			input:     strings.Repeat(emoji, 2000), // 8000 bytes, 2000 runes
			wantLines: 1,
			wantBytes: 8000,
			wantTrunc: true,
			check: func(t *testing.T, preview string) {
				body := strings.TrimSuffix(preview, marker)
				runes := []rune(body)
				if len(runes) != 1024 {
					t.Fatalf("preview runes = %d, want 1024", len(runes))
				}
				for i, r := range runes {
					if r != []rune(emoji)[0] {
						t.Fatalf("rune %d = %q, want %s — rune slicing cut a rune in half", i, string(r), emoji)
					}
				}
			},
		},
		{
			name:      "multi-byte UTF-8 across many lines",
			input:     strings.Repeat(japaneseLine, 15), // 15 × 19 = 285 bytes
			wantLines: 16,                               // 15 lines + trailing empty
			wantBytes: 285,
			wantTrunc: true,
			check: func(t *testing.T, preview string) {
				// maxLines = min(10, 16) = 10 → 9 elements joined (8 newlines) + "\n" + marker
				if n := strings.Count(preview, "\n"); n != 9 {
					t.Errorf("preview newlines = %d, want 9", n)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			preview, lines, bytes, trunc := Truncate(tc.input)

			if trunc != tc.wantTrunc {
				t.Errorf("truncated = %v, want %v", trunc, tc.wantTrunc)
			}
			if lines != tc.wantLines {
				t.Errorf("reported lines = %d, want %d", lines, tc.wantLines)
			}
			if bytes != tc.wantBytes {
				t.Errorf("reported bytes = %d, want %d", bytes, tc.wantBytes)
			}

			if !tc.wantTrunc {
				// Under both thresholds the output must come back byte-identical.
				if preview != tc.input {
					t.Errorf("preview must be untouched when not truncated, got %q", preview)
				}
				return
			}
			if !strings.Contains(preview, marker) {
				t.Errorf("preview should carry the truncation marker, got: %q", preview)
			}
			if tc.check != nil {
				tc.check(t, preview)
			}
		})
	}
}

// exactPreview asserts the preview is exactly this string.
func exactPreview(want string) func(t *testing.T, preview string) {
	return func(t *testing.T, preview string) {
		t.Helper()
		if preview != want {
			t.Errorf("preview mismatch.\nexpected: %q\n     got: %q", want, preview)
		}
	}
}

// endSuffix asserts the marker terminates the preview (not merely appears in it).
func endSuffix(t *testing.T, preview string) {
	t.Helper()
	if !strings.HasSuffix(preview, marker) {
		t.Errorf("preview should end with the truncation marker, got: %q", preview)
	}
}

// newlinesUnder bounds how much of the original survived in the preview.
func newlinesUnder(max int) func(t *testing.T, preview string) {
	return func(t *testing.T, preview string) {
		t.Helper()
		if n := strings.Count(preview, "\n"); n >= max {
			t.Errorf("preview should hold fewer than %d newlines, got %d", max, n)
		}
	}
}

// noReplacementChars catches a byte-slice that split a multi-byte rune.
func noReplacementChars(t *testing.T, s string) {
	t.Helper()
	for i, r := range s {
		if r == 0xFFFD {
			t.Errorf("rune %d is U+FFFD — UTF-8 slicing was unsafe", i)
			return
		}
	}
}
