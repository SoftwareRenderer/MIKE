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

// The code rides in the fragment because a query string reaches a proxy's access log and a Referer
// header, and a name given while minting rides the same fragment, escaped. Nothing a server logs may
// carry the code, which is a credential until the ceremony spends it.
func TestPairingURLCarriesTheCodeInTheFragment(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		label string
		want  string
	}{
		{name: "unnamed", label: "", want: "https://mike.lan:3000/#pair=ABCD2345"},
		{name: "named", label: "Kitchen #2 & table", want: "https://mike.lan:3000/#pair=ABCD2345&name=Kitchen+%232+%26+table"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url := PairingURL("https://mike.lan:3000", "ABCD2345", tc.label)
			if url != tc.want {
				t.Fatalf("PairingURL = %q, want %q", url, tc.want)
			}
			if strings.Contains(strings.SplitN(url, "#", 2)[0], "ABCD") {
				t.Fatalf("the code reached the part of the URL a server logs: %s", url)
			}
		})
	}
}
