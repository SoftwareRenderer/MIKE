package types

import (
	"strings"
	"testing"
	"unicode/utf8"
)

const cjkSentence = "修复容器镜像构建在嵌套 buildah 下因能力集缺失而失败的问题，并补充测量记录"

// A clipped label ending mid-rune reaches the browser as the replacement character, so the end of a
// clip is the part a person reads. The sentence has to cost more bytes than characters, or a
// byte-boundary cut and a rune-boundary one are the same cut and nothing here can tell them apart.
func TestClipNeverEndsHalfwayThroughACharacter(t *testing.T) {
	if utf8.RuneCountInString(cjkSentence) >= len(cjkSentence) {
		t.Fatalf("the sample holds %d runes in %d bytes, want more bytes than runes", utf8.RuneCountInString(cjkSentence), len(cjkSentence))
	}
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

// A truncated preview with no marker reads as text that ended where it was cut.
func TestClipMarkAnnouncesOnlyARealCut(t *testing.T) {
	if got := ClipMark(cjkSentence, 20, "…"); !strings.HasSuffix(got, "…") {
		t.Errorf("ClipMark = %q, want the marker to say something was dropped", got)
	}
	if got := ClipMark("short", 20, "…"); got != "short" {
		t.Errorf("ClipMark of a string that fits = %q, want it untouched: a marker with nothing missing is a lie", got)
	}
}
