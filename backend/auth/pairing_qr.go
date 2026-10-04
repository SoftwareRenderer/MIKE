package auth

import (
	"encoding/base64"
	"fmt"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// PairingURL is the link a pairing code is presented as. The code rides in the fragment rather than the
// query: a query string reaches a proxy's access log and a Referer header, and this code is a credential
// until the ceremony spends it.
func PairingURL(origin, code string) string {
	return origin + "/#pair=" + code
}

const (
	// pairingQRPixels is the PNG's side length: readable on a settings panel and on a second laptop's
	// screen without being a poster.
	pairingQRPixels = 256
	// pairingQRMaxBytes bounds the encoded content, so a QR is never asked to hold an arbitrary string —
	// the failure that matters is a picture that decodes to something else.
	pairingQRMaxBytes = 512
	// pairingQRMarginModules is the quiet zone drawn around the code. The specification wants four
	// modules of light; a terminal's own margin is not a colour the renderer controls, so they are drawn.
	pairingQRMarginModules = 4

	// The two SGR sequences a module is drawn with. Colour is what makes the picture the same code on a
	// dark terminal as on a light one: a space is the terminal's background, and that background is
	// whichever colour the reader's theme chose.
	qrForeground = "\x1b[30m" // modules: black
	qrBackground = "\x1b[47m" // and the field they sit on: white
	qrReset      = "\x1b[0m"
)

// PairingQRPNG is the content as a `data:image/png` URL, so the browser that minted a code can show it to
// a phone without a second request that would carry the code again.
func PairingQRPNG(content string) (string, error) {
	code, err := pairingQR(content)
	if err != nil {
		return "", err
	}
	png, err := code.PNG(pairingQRPixels)
	if err != nil {
		return "", fmt.Errorf("render pairing QR: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

// PairingQRTerminal is the content as lines of block-drawing text, so the operator's own terminal is
// enough to pair the first device — nobody has to read eight characters across a room.
func PairingQRTerminal(content string) (string, error) {
	code, err := pairingQR(content)
	if err != nil {
		return "", err
	}
	return renderTerminalQR(code.Bitmap()), nil
}

// renderTerminalQR draws a module grid two rows per line: each text line carries a top and a bottom
// module, so a version-5 code fits without scrolling past the boot log that says what it is for.
func renderTerminalQR(bitmap [][]bool) string {
	var b strings.Builder
	line := func(top, bottom []bool) {
		b.WriteString(qrBackground + qrForeground)
		for i := 0; i < pairingQRMarginModules; i++ {
			b.WriteString("  ")
		}
		for x := 0; x < len(top); x++ {
			upper, lower := top[x], false
			if bottom != nil {
				lower = bottom[x]
			}
			switch {
			case upper && lower:
				b.WriteRune('█')
			case upper:
				b.WriteRune('▀')
			case lower:
				b.WriteRune('▄')
			default:
				b.WriteRune(' ')
			}
			b.WriteRune(' ')
		}
		for i := 0; i < pairingQRMarginModules; i++ {
			b.WriteString("  ")
		}
		b.WriteString(qrReset + "\n")
	}
	field := strings.Repeat(" ", 2*(len(bitmap[0])+2*pairingQRMarginModules))
	for i := 0; i < pairingQRMarginModules; i++ {
		b.WriteString(qrBackground + field + qrReset + "\n")
	}
	for y := 0; y < len(bitmap); y += 2 {
		if y+1 < len(bitmap) {
			line(bitmap[y], bitmap[y+1])
		} else {
			line(bitmap[y], nil)
		}
	}
	for i := 0; i < pairingQRMarginModules; i++ {
		b.WriteString(qrBackground + field + qrReset + "\n")
	}
	return b.String()
}

// pairingQR builds the code both renderings share. Medium error correction is the balance between a small
// picture and one that survives a phone camera held at an angle.
func pairingQR(content string) (*qrcode.QRCode, error) {
	if len(content) == 0 || len(content) > pairingQRMaxBytes {
		return nil, fmt.Errorf("a pairing QR holds %d bytes of content, want 1-%d", len(content), pairingQRMaxBytes)
	}
	code, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return nil, fmt.Errorf("encode pairing QR: %w", err)
	}
	return code, nil
}
