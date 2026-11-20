package fsutil

import (
	"bytes"
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrNotText is a route's refusal to answer bytes as text. One sentence, because the fact is the same
// whoever the bytes were read out of — a working tree, a commit, one member of an archive.
var ErrNotText = errors.New("not a text file")

// IsText reports whether bytes can be answered as text: no NUL, and valid UTF-8 throughout. Both halves are
// needed — bytes can be NUL-free and undecodable — and the NUL test is what turns an image away without
// decoding it.
//
// Refusing rather than repairing is what protects the file: /api/fs/read feeds the editor's buffer, and a
// save would write repaired bytes back.
func IsText(b []byte) bool { return bytes.IndexByte(b, 0) < 0 && utf8.Valid(b) }

// ToText repairs bytes the JSON encoder refuses: every undecodable run becomes one U+FFFD. It is for what a
// person only reads — a diff row, a patch, a status line. A value that may be written back takes IsText's
// refusal instead.
//
// The repair is one-way, so a character count is no longer a byte count. A NUL survives it, so a caller may
// ask IsText before or after.
func ToText(s string) string { return strings.ToValidUTF8(s, "\ufffd") }
