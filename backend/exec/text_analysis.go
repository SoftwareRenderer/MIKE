package exec

import (
	"strings"
	"unicode"
)

// blockDetector tracks raw text blocks within a single streaming response
// to detect exact repetitions of any block (not just sentences) in real-time.
type blockDetector struct {
	buffer string
	maxLen int
}

func newBlockDetector() *blockDetector {
	return &blockDetector{
		maxLen: 4096,
	}
}

func (d *blockDetector) reset() {
	d.buffer = ""
}

func (d *blockDetector) check(text string) bool {
	d.buffer += text
	if len(d.buffer) > d.maxLen {
		d.buffer = d.buffer[len(d.buffer)-d.maxLen:]
	}

	if len(d.buffer) < 30 {
		return false
	}

	// Check for repetitions of blocks from 10 to 100 characters.
	// A loop is detected if the same block repeats 3 times consecutively.
	for size := 10; size <= len(d.buffer)/3; size++ {
		block := d.buffer[len(d.buffer)-size:]
		if len(d.buffer) >= 3*size &&
			d.buffer[len(d.buffer)-2*size:len(d.buffer)-size] == block &&
			d.buffer[len(d.buffer)-3*size:len(d.buffer)-2*size] == block {
			return true
		}
	}
	return false
}

// phraseDetector tracks sentences/phrases seen within a single streaming response

// to detect exact repetition in real-time.
type phraseDetector struct {
	seen    map[string]int // phrase -> occurrence count
	pending string        // incomplete sentence waiting for more text
	fired   bool          // already triggered for this response
}

// minPhraseWords is the minimum word count for a phrase to be tracked.
// Phrases shorter than this are ignored to avoid false positives on common short text.
const minPhraseWords = 5

// newPhraseDetector creates a new phraseDetector.
func newPhraseDetector() *phraseDetector {
	return &phraseDetector{
		seen: make(map[string]int),
	}
}

// maxPhraseRepeats is the number of times a phrase must repeat before firing.
const maxPhraseRepeats = 10

// reset clears all tracked state for a new response.
func (d *phraseDetector) reset() {
	d.seen = make(map[string]int)
	d.pending = ""
	d.fired = false
}

// check feeds new text into the detector and returns true if exact phrase
// repetition is detected. Once fired, subsequent calls return false until reset.
//
// It appends new text to any pending (incomplete) text, extracts complete
// sentences, and checks each against previously seen phrases. Incomplete
// text at the end is kept as pending for the next call.
func (d *phraseDetector) check(newText string) bool {
	if d.fired {
		return false
	}

	// Combine pending text with new text using Builder to avoid allocation
	// when pending is empty (common case for complete sentences).
	var b strings.Builder
	if d.pending != "" {
		b.WriteString(d.pending)
	}
	b.WriteString(newText)
	text := b.String()
	d.pending = ""

	// Split on sentence boundaries
	segments := splitSentences(text)

	// Process all segments except the last (which may be incomplete)
	completeCount := len(segments)
	if completeCount > 0 {
		last := segments[completeCount-1]
		// Check if the last segment looks complete (ends with sentence punctuation)
		lastTrimmed := strings.TrimSpace(last)
		if len(lastTrimmed) == 0 || !isSentenceEnd(rune(lastTrimmed[len(lastTrimmed)-1])) {
			// Incomplete — keep as pending
			d.pending = last
			completeCount--
		}
	}

	for i := 0; i < completeCount; i++ {
		phrase := normalizePhrase(segments[i])
		if phrase == "" || wordCount(phrase) < minPhraseWords {
			continue
		}

		d.seen[phrase]++
		if d.seen[phrase] >= maxPhraseRepeats {
			d.fired = true
			return true
		}
	}

	// Bound the pending buffer to prevent unbounded memory growth
	// from extremely long text without sentence boundaries.
	if len(d.pending) > maxPendingSize {
		d.pending = ""
	}

	return false
}

// splitSentences splits text into segments at sentence boundaries.
// A sentence boundary is: .!? followed by whitespace/newline, .!? followed by uppercase,
// or a double newline.
// The last segment (incomplete sentence) is included so it can accumulate more text.
// maxPendingSize limits the phraseDetector.pending buffer to prevent
// unbounded memory growth from extremely long text without sentence boundaries.
const maxPendingSize = 16 * 1024

func splitSentences(text string) []string {
	runes := []rune(text)
	n := len(runes)
	var segments []string
	var current strings.Builder
	i := 0

	for i < n {
		r := runes[i]
		current.WriteRune(r)

		if isSentenceEnd(r) && i+1 < n {
			next := runes[i+1]
			// Boundary: punctuation followed by whitespace or uppercase
			if isWhitespace(next) || (next >= 'A' && next <= 'Z') {
				// Consume trailing whitespace as delimiter
				i++
				for i < n && isWhitespace(runes[i]) {
					i++
				}
				segments = append(segments, current.String())
				current.Reset()
				continue
			}
		}
		// Also handle double newline as boundary
		if r == '\n' && i+1 < n && runes[i+1] == '\n' {
			i++ // consume second newline
			segments = append(segments, current.String())
			current.Reset()
			continue
		}
		i++
	}

	// Add remaining text (incomplete sentence)
	if current.Len() > 0 {
		segments = append(segments, current.String())
	}

	if len(segments) == 0 {
		segments = []string{text}
	}

	return segments
}

// normalizePhrase lowercases, trims whitespace and trailing punctuation, and collapses internal whitespace.
func normalizePhrase(s string) string {
	// Lowercase
	s = strings.ToLower(s)
	// Collapse whitespace and trim in one pass (Fields splits on all whitespace, Join uses single space)
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	// Trim trailing punctuation from the last word
	last := len(fields) - 1
	fields[last] = strings.TrimRight(fields[last], ".,!?;:")
	if fields[last] == "" && len(fields) == 1 {
		return ""
	}
	if fields[last] == "" {
		fields = fields[:last]
	}
	return strings.Join(fields, " ")
}

// wordCount counts the number of words in a phrase.
func wordCount(s string) int {
	return len(strings.Fields(s))
}

// isSentenceEnd checks if a rune is sentence-ending punctuation.
func isSentenceEnd(r rune) bool {
	return r == '.' || r == '!' || r == '?'
}

// isWhitespace checks if a rune is whitespace.
func isWhitespace(r rune) bool {
	return unicode.IsSpace(r)
}
