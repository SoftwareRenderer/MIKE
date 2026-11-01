package types

import (
	"strings"
	"testing"
	"unicode/utf8"
)

const cjkSentence = "修复容器镜像构建在嵌套 buildah 下因能力集缺失而失败的问题，并补充测量记录"

// A clipped label ending mid-rune reaches the browser as the replacement character, so the end of a
// clip is the part a person reads.
func TestClipNeverEndsHalfwayThroughACharacter(t *testing.T) {
	for _, max := range []int{1, 12, 20, 40, utf8.RuneCountInString(cjkSentence) - 1} {
		got := Clip(cjkSentence, max)
		if !utf8.ValidString(got) {
			t.Errorf("Clip(_, %d) is invalid UTF-8: %q", max, got)
		}
		if utf8.RuneCountInString(got) != max {
			t.Errorf("Clip(_, %d) returned %d runes: %q", max, utf8.RuneCountInString(got), got)
		}
		if !strings.HasPrefix(cjkSentence, got) {
			t.Errorf("Clip(_, %d) = %q, which is not a prefix of the input", max, got)
		}
	}
}

func TestClipLeavesAShortEnoughStringAlone(t *testing.T) {
	for _, s := range []string{"", "one short line", cjkSentence} {
		if got := Clip(s, utf8.RuneCountInString(s)); got != s {
			t.Errorf("Clip(len, _) = %q, want the input %q", got, s)
		}
	}
	if got := Clip(cjkSentence, 0); got != cjkSentence {
		t.Errorf("Clip(_, 0) = %q, want the input untouched: no budget is not an empty budget", got)
	}
}

// The old byte-bound cut lost the last character of a CJK title; a rune budget of the same size keeps
// it, which is the difference a person sees.
func TestClipCountsCharactersNotBytes(t *testing.T) {
	if utf8.RuneCountInString(cjkSentence) >= len(cjkSentence) {
		t.Fatalf("this sentence should hold more bytes than runes, got %d bytes", len(cjkSentence))
	}
	got := Clip(cjkSentence, 20)
	if want := string([]rune(cjkSentence)[:20]); got != want {
		t.Errorf("Clip(_, 20) = %q, want %q", got, want)
	}
}

// A truncated preview with no marker reads as text that ended where it was cut.
func TestClipMarkAnnouncesOnlyARealCut(t *testing.T) {
	if got := ClipMark(cjkSentence, 20, "…"); !strings.HasSuffix(got, "…") {
		t.Errorf("ClipMark = %q, want the marker to say something was dropped", got)
	}
	if got := ClipMark("short", 20, "…"); got != "short" {
		t.Errorf("ClipMark of a string that fits = %q, want it untouched: a marker with nothing missing is a lie", got)
	}
}
