package auth

import (
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// One rule written twice: the browser lane mirrors this pattern (`PAIRING_CODE_ROW` in
// frontend/tests/helpers-auth.mjs) to read the code out of the server's log.
var pairingCodeRow = regexp.MustCompile(`(?m)PAIRING CODE\s+([0-9A-Z]{4}-[0-9A-Z]{4})[ │]*$`)

var bannerWhen = time.Date(2026, 10, 4, 15, 9, 2, 0, time.UTC)

func TestPairingBannerShowsTheCodeOnItsOwnRow(t *testing.T) {
	t.Parallel()
	banner := PairingBanner("266A-SG2B", "http://localhost:3000/#pair=266ASG2B", bannerWhen)

	found := pairingCodeRow.FindAllStringSubmatch(banner, -1)
	if len(found) != 1 {
		t.Fatalf("%d rows carry a pairing code, want 1:\n%s", len(found), banner)
	}
	if code := found[0][1]; code != "266A-SG2B" {
		t.Fatalf("the code row reads %q, want 266A-SG2B:\n%s", code, banner)
	}
	for _, want := range []string{"http://localhost:3000/#pair=266ASG2B", "2026-10-04 15:09:02", "works once"} {
		if !strings.Contains(banner, want) {
			t.Errorf("the banner does not say %q:\n%s", want, banner)
		}
	}
	if strings.Contains(banner, bannerWhen.Format(time.RFC3339Nano)) {
		t.Errorf("the expiry is stamped with an offset; the banner says UTC:\n%s", banner)
	}
}

func TestPairingBannerFrameIsEven(t *testing.T) {
	t.Parallel()
	banner := PairingBanner("ABCD-2345", "http://localhost:3000/#pair=ABCD2345", bannerWhen)
	lines := strings.Split(strings.TrimSuffix(banner, "\n"), "\n")
	width := utf8.RuneCountInString(lines[0])
	if width > bannerMaxColumns {
		t.Fatalf("the banner is %d columns wide, which wraps a terminal of %d", width, bannerMaxColumns)
	}
	for i, line := range lines {
		if n := utf8.RuneCountInString(line); n != width {
			t.Fatalf("line %d is %d columns wide, want %d:\n%s", i, n, width, banner)
		}
	}
	if first := lines[0]; !strings.HasPrefix(first, "┌") || !strings.HasSuffix(first, "┐") {
		t.Fatalf("the banner is not framed:\n%s", banner)
	}
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "└") || !strings.HasSuffix(last, "┘") {
		t.Fatalf("the banner has no foot rule:\n%s", banner)
	}
}

// A pinned hostname can outgrow a terminal, and the code is readable without the frame.
func TestPairingBannerDropsTheFrameRatherThanWrapping(t *testing.T) {
	t.Parallel()
	host := strings.Repeat("a", 60) + ".example.internal"
	banner := PairingBanner("ABCD-2345", PairingURL("https://"+host, "ABCD2345", ""), bannerWhen)

	if strings.Contains(banner, "│") || strings.Contains(banner, "┌") {
		t.Fatalf("a %d-column host was framed anyway:\n%s", len(host), banner)
	}
	for i, line := range strings.Split(strings.TrimSuffix(banner, "\n"), "\n") {
		if line != "" && !strings.HasPrefix(line, strings.Repeat(" ", bannerPad)) {
			t.Fatalf("line %d lost its padding:\n%s", i, banner)
		}
	}
	if !pairingCodeRow.MatchString(banner) {
		t.Fatalf("the code row changed shape when the frame went away:\n%s", banner)
	}
}
