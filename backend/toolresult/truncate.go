// Package toolresult provides pure utilities for truncating tool output.
package toolresult

import (
	"strings"
)

// Truncate truncates a tool output string to at most 10 lines and 1024 bytes, returning the
// preview, the original line and byte counts, and whether truncation was applied.
//
// The preview holds up to maxLines-1 lines plus a marker, where maxLines is the number of
// lines that fit in 1024 bytes capped at 10; when a single line exceeds that, the preview is
// the first 1024 runes plus the marker.
func Truncate(output string) (preview string, originalLines int, originalBytes int, truncated bool) {
	originalLines = len(strings.Split(output, "\n"))
	originalBytes = len(output)

	if originalLines <= 10 && originalBytes <= 1024 {
		return output, originalLines, originalBytes, false
	}

	lines := strings.Split(output, "\n")

	// Find how many lines fit within 1024 bytes (including newline separators).
	var bytesAt1KB int
	byteTotal := 0
	for i, line := range lines {
		sep := 0
		if i > 0 {
			sep = 1
		}
		byteTotal += len(line) + sep
		if byteTotal > 1024 {
			bytesAt1KB = i
			break
		}
		bytesAt1KB = i + 1
	}

	maxLines := 10
	if bytesAt1KB < maxLines {
		maxLines = bytesAt1KB
	}

	if maxLines > 1 {
		preview = strings.Join(lines[:maxLines-1], "\n") + "\n... (truncated)"
	} else {
		// Single very long line — slice on runes for UTF-8 safety.
		runes := []rune(output)
		if len(runes) > 1024 {
			preview = string(runes[:1024]) + "... (truncated)"
		} else {
			preview = output + "... (truncated)"
		}
	}

	return preview, originalLines, originalBytes, true
}
