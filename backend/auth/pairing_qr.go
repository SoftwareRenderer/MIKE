package auth

import (
	"encoding/base64"
	"fmt"

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

// pairingQR builds the picture the settings panel shows. Medium error correction is the balance between a
// small picture and one that survives a phone camera held at an angle.
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
