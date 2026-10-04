package auth

import (
	"strings"
	"time"
	"unicode/utf8"
)

// The boot printout is drawn with spaces and rule characters rather than as a picture, because a picture
// depends on the reader's cell shape and text does not.

const (
	bannerPad     = 3
	bannerIndent  = 4
	bannerCodeGap = 6
	// bannerMaxColumns is the widest box that still fits a normal terminal. A wrapped row looks broken and
	// the code reads fine without a frame, so an over-long row drops the frame rather than the budget.
	bannerMaxColumns = 80
)

// PairingBanner is the pairing code as the boot log shows it. `code` is the dashed form (`FormatPairingCode`).
//
// The code row is a contract across languages: the browser lane reads the code back out of the server's log
// to pair its test device, so it must be the label, then the dashed code, then nothing — see PAIRING_CODE_ROW
// in `frontend/tests/helpers-auth.mjs`.
func PairingBanner(code, url string, expiresAt time.Time) string {
	indent := strings.Repeat(" ", bannerIndent)
	return box([]string{
		"",
		"No passkey is registered yet — pair the first device.",
		"",
		indent + "PAIRING CODE" + strings.Repeat(" ", bannerCodeGap) + code,
		indent + url,
		"",
		"The code works once and expires at " + expiresAt.UTC().Format("2006-01-02 15:04:05") + " UTC.",
		"Restart MIKE to print a new one, or mint one in Settings.",
		"",
	})
}

// Widths are counted in runes: a row carries an em dash, and counting its bytes would put every border two
// columns from where it draws.
func box(rows []string) string {
	width := 0
	for _, row := range rows {
		if n := utf8.RuneCountInString(row); n > width {
			width = n
		}
	}
	var b strings.Builder
	if width+2*(bannerPad+1) > bannerMaxColumns {
		for _, row := range rows {
			b.WriteString(strings.Repeat(" ", bannerPad) + row + "\n")
		}
		return b.String()
	}
	inner := width + 2*bannerPad
	rule := strings.Repeat("─", inner)
	b.WriteString("┌" + rule + "┐\n")
	for _, row := range rows {
		b.WriteString("│" + strings.Repeat(" ", bannerPad) + row +
			strings.Repeat(" ", width-utf8.RuneCountInString(row)+bannerPad) + "│\n")
	}
	b.WriteString("└" + rule + "┘\n")
	return b.String()
}
