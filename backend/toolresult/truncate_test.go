package toolresult

import (
	"strings"
	"testing"
)

func TestTruncateShortOutput(t *testing.T) {
	output := "total 100\ndrwxr-xr-x  5 user user  4096 Jan 1 .\n"
	preview, lines, bytes, truncated := Truncate(output)

	if truncated {
		t.Error("short output should not be truncated")
	}
	if preview != output {
		t.Errorf("preview should equal original for short output, got: %q", preview)
	}
	if lines != 3 {
		t.Errorf("expected 3 lines, got %d", lines)
	}
	if bytes != len(output) {
		t.Errorf("expected %d bytes, got %d", len(output), bytes)
	}
}

func TestTruncateEmpty(t *testing.T) {
	preview, lines, bytes, truncated := Truncate("")

	if truncated {
		t.Error("empty output should not be truncated")
	}
	if preview != "" {
		t.Errorf("expected empty preview, got: %q", preview)
	}
	if lines != 1 {
		t.Errorf("strings.Split on empty string yields 1 element, expected 1 line, got %d", lines)
	}
	if bytes != 0 {
		t.Errorf("expected 0 bytes, got %d", bytes)
	}
}

func TestTruncateExactly10LinesUnder1KB(t *testing.T) {
	// Exactly 10 elements from strings.Split, well under 1KB — should NOT truncate
	// 9 newlines → 10 elements from split
	output := strings.Repeat("line\n", 9) + "line" // 10 elements, 49 bytes
	preview, lines, bytes, truncated := Truncate(output)

	if truncated {
		t.Error("exactly 10 lines under 1KB should not be truncated")
	}
	if preview != output {
		t.Errorf("preview should equal original, got: %q", preview)
	}
	if lines != 10 {
		t.Errorf("expected 10 lines, got %d", lines)
	}
	if bytes != 49 {
		t.Errorf("expected 49 bytes, got %d", bytes)
	}
}

func TestTruncateExactly1024BytesFewLines(t *testing.T) {
	// Few lines but exactly 1024 bytes — should NOT truncate (≤1024)
	// 2 lines: first line 1023 bytes + "\n" + empty = 1024 bytes total
	line := strings.Repeat("x", 1023)
	output := line + "\n"
	preview, lines, bytes, truncated := Truncate(output)

	if truncated {
		t.Error("exactly 1024 bytes with 2 lines should not be truncated")
	}
	if preview != output {
		t.Errorf("preview should equal original, got length %d", len(preview))
	}
	if lines != 2 {
		t.Errorf("expected 2 lines, got %d", lines)
	}
	if bytes != 1024 {
		t.Errorf("expected 1024 bytes, got %d", bytes)
	}
}

func TestTruncateExactly10LinesExactly1024Bytes(t *testing.T) {
	// Exactly 10 elements from split AND exactly 1024 bytes — boundary: should NOT truncate
	// 9 newlines → 10 elements. 9 lines of "x" (1 byte each) + newlines = 18 bytes
	// Last line needs 1024 - 18 = 1006 bytes
	lastLine := strings.Repeat("x", 1006)
	output := strings.Repeat("x\n", 9) + lastLine
	preview, lines, bytes, truncated := Truncate(output)

	if truncated {
		t.Error("exactly 10 lines and 1024 bytes should not be truncated")
	}
	if preview != output {
		t.Errorf("preview should equal original, got length %d", len(preview))
	}
	if lines != 10 {
		t.Errorf("expected 10 lines, got %d", lines)
	}
	if bytes != 1024 {
		t.Errorf("expected 1024 bytes, got %d", bytes)
	}
}

func TestTruncateMoreThan10Lines(t *testing.T) {
	// 20 lines of "line of output text\n" — same as existing handler test
	// strings.Split produces 21 elements (trailing empty after final \n)
	output := strings.Repeat("line of output text\n", 20)
	preview, lines, bytes, truncated := Truncate(output)

	if !truncated {
		t.Fatal("expected truncation for >10 lines")
	}
	if lines != 21 {
		t.Errorf("expected 21 lines (trailing empty), got %d", lines)
	}
	if bytes != len(output) {
		t.Errorf("expected %d bytes, got %d", len(output), bytes)
	}
	if !strings.Contains(preview, "... (truncated)") {
		t.Errorf("preview should contain truncation marker, got: %q", preview)
	}
	// Should have fewer lines than original
	previewLines := strings.Count(preview, "\n")
	if previewLines >= 20 {
		t.Errorf("expected fewer than 20 newlines in preview, got %d", previewLines)
	}
}

func TestTruncateOver1KBWithFewLines(t *testing.T) {
	// 2 lines but >1024 bytes — should truncate
	line := strings.Repeat("x", 600)
	output := line + "\n" + line // 600 + 1 + 600 = 1201 bytes, 2 elements from split
	preview, lines, bytes, truncated := Truncate(output)

	if !truncated {
		t.Fatal("expected truncation for >1024 bytes")
	}
	if lines != 2 {
		t.Errorf("expected 2 lines, got %d", lines)
	}
	if bytes != 1201 {
		t.Errorf("expected 1201 bytes, got %d", bytes)
	}
	if !strings.Contains(preview, "... (truncated)") {
		t.Errorf("preview should contain truncation marker, got: %q", preview)
	}
}

func TestTruncateBothOver10LinesAndOver1KB(t *testing.T) {
	// Many long lines — both conditions exceeded
	// 20 lines of 100 x's each + trailing newline → 21 elements from split
	output := strings.Repeat(strings.Repeat("x", 100)+"\n", 20)
	preview, lines, bytes, truncated := Truncate(output)

	if !truncated {
		t.Fatal("expected truncation for both >10 lines and >1KB")
	}
	if lines != 21 {
		t.Errorf("expected 21 lines (trailing empty), got %d", lines)
	}
	if bytes != len(output) {
		t.Errorf("expected %d bytes, got %d", len(output), bytes)
	}
	if !strings.Contains(preview, "... (truncated)") {
		t.Errorf("preview should contain truncation marker, got: %q", preview)
	}
}

func TestTruncateSingleVeryLongLine(t *testing.T) {
	// Single line > 1024 bytes — maxLines will be 0, uses rune slicing
	output := strings.Repeat("x", 2000)
	preview, lines, bytes, truncated := Truncate(output)

	if !truncated {
		t.Fatal("expected truncation for single line > 1024 bytes")
	}
	if lines != 1 {
		t.Errorf("expected 1 line, got %d", lines)
	}
	if bytes != 2000 {
		t.Errorf("expected 2000 bytes, got %d", bytes)
	}
	if !strings.HasSuffix(preview, "... (truncated)") {
		t.Errorf("preview should end with truncation marker, got: %q", preview)
	}
	// Preview should be 1024 chars + truncation suffix
	expectedLen := 1024 + len("... (truncated)")
	if len(preview) != expectedLen {
		t.Errorf("expected preview length %d, got %d", expectedLen, len(preview))
	}
}

func TestTruncateMultiByteUTF8(t *testing.T) {
	// Multi-byte UTF-8 characters — ensure rune-safe slicing
	// Each "😀" is 4 bytes. 300 emojis = 1200 bytes, 1 line.
	emoji := "😀"
	output := strings.Repeat(emoji, 300)
	preview, lines, bytes, truncated := Truncate(output)

	if !truncated {
		t.Fatal("expected truncation for >1024 bytes")
	}
	if lines != 1 {
		t.Errorf("expected 1 line, got %d", lines)
	}
	if bytes != 1200 {
		t.Errorf("expected 1200 bytes, got %d", bytes)
	}
	if !strings.HasSuffix(preview, "... (truncated)") {
		t.Errorf("preview should end with truncation marker, got: %q", preview)
	}
	// Since the output is only 300 runes (< 1024), the full output is returned + suffix
	// (the rune-slicing path doesn't cut when there are fewer than 1024 runes)
	withoutSuffix := strings.TrimSuffix(preview, "... (truncated)")
	if len([]rune(withoutSuffix)) != 300 {
		t.Errorf("expected 300 runes in preview (without suffix), got %d", len([]rune(withoutSuffix)))
	}
	// No replacement characters — UTF-8 slicing is safe
	for i, r := range withoutSuffix {
		if r == 0xFFFD {
			t.Errorf("preview contains replacement character at position %d — UTF-8 slicing was unsafe", i)
			break
		}
	}
}

func TestTruncateMultiByteUTF8LongLine(t *testing.T) {
	// Multi-byte UTF-8 that exceeds 1024 runes — verify rune-safe truncation
	emoji := "😀"
	output := strings.Repeat(emoji, 2000) // 8000 bytes, 2000 runes, 1 line
	preview, lines, bytes, truncated := Truncate(output)

	if !truncated {
		t.Fatal("expected truncation for >1024 bytes")
	}
	if lines != 1 {
		t.Errorf("expected 1 line, got %d", lines)
	}
	if bytes != 8000 {
		t.Errorf("expected 8000 bytes, got %d", bytes)
	}
	withoutSuffix := strings.TrimSuffix(preview, "... (truncated)")
	if len([]rune(withoutSuffix)) != 1024 {
		t.Errorf("expected 1024 runes in preview (without suffix), got %d", len([]rune(withoutSuffix)))
	}
	// No replacement characters
	for i, r := range withoutSuffix {
		if r != '😀' {
			t.Errorf("unexpected rune %q at position %d", string(r), i)
			break
		}
	}
}

func TestTruncateMultiByteUTF8MultipleLines(t *testing.T) {
	// Multi-byte UTF-8 across multiple lines
	// "日本語テスト\n" = 6 Japanese chars × 3 bytes + 1 newline = 19 bytes each
	line := "日本語テスト\n"
	output := strings.Repeat(line, 15) // 15 × 19 = 285 bytes
	// Split produces 16 elements (15 lines + 1 trailing empty)
	preview, lines, bytes, truncated := Truncate(output)

	if !truncated {
		t.Fatal("expected truncation for >10 lines")
	}
	if lines != 16 {
		t.Errorf("expected 16 lines (trailing empty), got %d", lines)
	}
	if bytes != 285 {
		t.Errorf("expected 285 bytes, got %d", bytes)
	}
	if !strings.Contains(preview, "... (truncated)") {
		t.Errorf("preview should contain truncation marker, got: %q", preview)
	}
	// maxLines = min(10, 16) = 10, so maxLines-1 = 9 lines joined + truncation
	// 9 elements joined by "\n" = 8 newlines, plus "\n... (truncated)" = 1 more = 9 newlines
	previewNewlines := strings.Count(preview, "\n")
	if previewNewlines != 9 {
		t.Errorf("expected 9 newlines in preview, got %d", previewNewlines)
	}
}

func TestTruncateJustOver10Lines(t *testing.T) {
	// 11 "line\n" → split gives 12 elements (11 lines + 1 trailing empty)
	output := strings.Repeat("line\n", 11)
	preview, lines, _, truncated := Truncate(output)

	if !truncated {
		t.Fatal("expected truncation for 12 lines")
	}
	if lines != 12 {
		t.Errorf("expected 12 lines (trailing empty), got %d", lines)
	}
	if !strings.Contains(preview, "... (truncated)") {
		t.Errorf("preview should contain truncation marker, got: %q", preview)
	}
	// maxLines = min(10, 12) = 10, so maxLines-1 = 9 lines joined + truncation
	expected := strings.Repeat("line\n", 9) + "... (truncated)"
	if preview != expected {
		t.Errorf("preview mismatch.\nexpected: %q\n  got: %q", expected, preview)
	}
}

func TestTruncateJustOver1024Bytes(t *testing.T) {
	// 2 lines, 1025 bytes — just over byte threshold
	line := strings.Repeat("x", 1024)
	output := line + "\n" // 1025 bytes, 2 elements from split
	preview, lines, bytes, truncated := Truncate(output)

	if !truncated {
		t.Fatal("expected truncation for 1025 bytes")
	}
	if lines != 2 {
		t.Errorf("expected 2 lines, got %d", lines)
	}
	if bytes != 1025 {
		t.Errorf("expected 1025 bytes, got %d", bytes)
	}
	if !strings.Contains(preview, "... (truncated)") {
		t.Errorf("preview should contain truncation marker, got: %q", preview)
	}
}
