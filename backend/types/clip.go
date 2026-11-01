package types

import "unicode/utf8"

// Clip returns the first max runes of s. It counts runes because a byte cut splits a multi-byte
// character, and the leftover tail reaches a person as the replacement character — in the title their
// own words produced, or in the text the model is shown. max <= 0 returns s unchanged.
func Clip(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// ClipMark is Clip with a marker appended when something was dropped, so a reader of a truncated
// preview is not told the text simply ended. The marker is inside the caller's budget in spirit and
// outside it in bytes: the text keeps max runes and the marker is the notice.
func ClipMark(s string, max int, mark string) string {
	clipped := Clip(s, max)
	if clipped == s {
		return s
	}
	return clipped + mark
}
