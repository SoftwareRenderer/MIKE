package auth

import (
	"strings"
	"testing"
)

// A QR asked to hold more than the pairing link may decode to something the reader did not ask for.
func TestPairingQRRefusesContentBeyondItsLimit(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"", "https://mike.lan:3000/#pair=" + strings.Repeat("x", pairingQRMaxBytes)} {
		if _, err := PairingQRPNG(content); err == nil {
			t.Fatalf("the picture accepted %d bytes of content", len(content))
		}
	}
}

// The code rides in the fragment because a query string reaches a proxy's access log and a Referer header.
func TestPairingURLCarriesTheCodeInTheFragment(t *testing.T) {
	t.Parallel()
	url := PairingURL("https://mike.lan:3000", "ABCD2345")
	if want := "https://mike.lan:3000/#pair=ABCD2345"; url != want {
		t.Fatalf("PairingURL = %q, want %q", url, want)
	}
	if strings.Contains(strings.SplitN(url, "#", 2)[0], "ABCD") {
		t.Fatalf("the code reached the part of the URL a server logs: %s", url)
	}
}
