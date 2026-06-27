package exec

import (
	"regexp"
	"strings"
	"unicode"
)

var wordTokenRe = regexp.MustCompile(`[a-zA-Z]{2,}`)

// phraseDetector tracks sentences/phrases seen within a single streaming response
// to detect exact repetition in real-time.
type phraseDetector struct {
	seen    map[string]bool // normalized phrases already seen
	pending string          // incomplete sentence waiting for more text
	fired   bool            // already triggered for this response
}

// minPhraseWords is the minimum word count for a phrase to be tracked.
// Phrases shorter than this are ignored to avoid false positives on common short text.
const minPhraseWords = 8

// newPhraseDetector creates a new phraseDetector.
func newPhraseDetector() *phraseDetector {
	return &phraseDetector{
		seen: make(map[string]bool),
	}
}

// reset clears all tracked state for a new response.
func (d *phraseDetector) reset() {
	d.seen = make(map[string]bool)
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

		if d.seen[phrase] {
			d.fired = true
			return true
		}
		d.seen[phrase] = true
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

// detectTextLoop checks if the text window contains repetitive responses.
// Fires when 2+ consecutive text-only responses have high similarity.
func (d *loopDetector) detectTextLoop() bool {
	if len(d.textWindow) < 2 {
		return false
	}

	last := d.textWindow[len(d.textWindow)-1]
	lastWords := wordSet(last)

	if len(lastWords) < 5 {
		return false // too short to be meaningful
	}

	for i := 0; i < len(d.textWindow)-1; i++ {
		priorWords := wordSet(d.textWindow[i])
		if jaccardSimilarity(lastWords, priorWords) > 0.6 {
			return true
		}
	}

	return false
}

// wordSet tokenizes text into a set of meaningful words (lowercased, stop words removed).
func wordSet(text string) map[string]bool {
	matches := wordTokenRe.FindAllString(text, -1)

	words := make(map[string]bool)
	for _, m := range matches {
		w := strings.ToLower(m)
		if !isStopWord(w) {
			words[w] = true
		}
	}
	return words
}

// jaccardSimilarity computes the Jaccard similarity between two word sets.
func jaccardSimilarity(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}

	intersection := 0
	for w := range a {
		if b[w] {
			intersection++
		}
	}

	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// stopWords is a set of common English stop words to ignore in similarity checks.
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "are": true, "was": true, "were": true,
	"be": true, "been": true, "being": true, "have": true, "has": true, "had": true,
	"do": true, "does": true, "did": true, "will": true, "would": true, "could": true,
	"should": true, "may": true, "might": true, "shall": true, "can": true,
	"to": true, "of": true, "in": true, "for": true, "on": true, "with": true,
	"at": true, "by": true, "from": true, "as": true, "into": true, "through": true,
	"during": true, "before": true, "after": true, "above": true, "below": true,
	"between": true, "out": true, "off": true, "over": true, "under": true,
	"and": true, "but": true, "or": true, "nor": true, "not": true, "so": true,
	"yet": true, "both": true, "either": true, "neither": true,
	"each": true, "every": true, "all": true, "any": true, "few": true, "more": true,
	"most": true, "other": true, "some": true, "such": true, "no": true, "only": true,
	"own": true, "same": true, "than": true, "too": true, "very": true, "just": true,
	"because": true, "if": true, "when": true, "where": true, "how": true, "what": true,
	"which": true, "who": true, "whom": true, "this": true, "that": true, "these": true,
	"those": true, "i": true, "me": true, "my": true, "myself": true, "we": true,
	"our": true, "you": true, "your": true, "he": true, "him": true, "his": true,
	"it": true, "its": true, "they": true, "them": true, "their": true,
	"about": true, "up": true, "then": true, "there": true, "here": true,
	"also": true, "back": true, "like": true, "new": true, "way": true, "use": true,
	"uses": true, "used": true, "using": true, "try": true, "trying": true,
	"need": true, "needs": true, "think": true, "know": true, "see": true,
	"get": true, "got": true, "going": true, "make": true, "made": true,
	"want": true, "wanting": true, "let": true, "say": true, "said": true,
	"says": true, "well": true, "still": true, "even": true, "much": true,
	"many": true, "since": true, "until": true, "while": true,
}

func isStopWord(w string) bool {
	return stopWords[w]
}

// isSentenceEnd checks if a rune is sentence-ending punctuation.
func isSentenceEnd(r rune) bool {
	return r == '.' || r == '!' || r == '?'
}

// isWhitespace checks if a rune is whitespace.
func isWhitespace(r rune) bool {
	return unicode.IsSpace(r)
}
