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

func rowCount(banner, want string) int {
	rows := 0
	for _, line := range strings.Split(strings.TrimSuffix(banner, "\n"), "\n") {
		if strings.TrimSpace(strings.Trim(line, "│")) == want {
			rows++
		}
	}
	return rows
}

func TestPairingBannerShowsTheCodeOnItsOwnRow(t *testing.T) {
	t.Parallel()
	banner := PairingBanner("266A-SG2B", []string{"http://localhost:3000/#pair=266ASG2B"}, bannerWhen)

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

func TestPairingBannerPrintsOneRowPerAuthorizedHost(t *testing.T) {
	t.Parallel()
	urls := []string{
		PairingURL("https://mike.lan:3000", "ABCD2345", ""),
		PairingURL("https://mike.example.com:3000", "ABCD2345", ""),
	}
	banner := PairingBanner("ABCD-2345", urls, bannerWhen)

	for _, want := range urls {
		if rows := rowCount(banner, want); rows != 1 {
			t.Errorf("%q appears on %d rows, want 1:\n%s", want, rows, banner)
		}
	}
	if found := pairingCodeRow.FindAllStringSubmatch(banner, -1); len(found) != 1 {
		t.Fatalf("%d rows carry a pairing code, want 1:\n%s", len(found), banner)
	}
}

func TestPairingBannerHostNoteFitsSixtyColumns(t *testing.T) {
	t.Parallel()
	if n := utf8.RuneCountInString(bannerHostNote); n+2*(bannerPad+1) > 60 {
		t.Errorf("the host note is %d columns, which draws a framed row of %d over the terminal of 60",
			n, n+2*(bannerPad+1))
	}
	longHost := strings.Repeat("a", 60) + ".example.internal"
	for _, urls := range [][]string{
		{PairingURL("https://mike.lan:3000", "ABCD2345", ""), PairingURL("https://mike.example.com:3000", "ABCD2345", "")},
		{PairingURL("https://"+longHost, "ABCD2345", ""), PairingURL("https://mike.lan:3000", "ABCD2345", "")},
	} {
		banner := PairingBanner("ABCD-2345", urls, bannerWhen)
		if rows := rowCount(banner, bannerHostNote); rows != 1 {
			t.Fatalf("%d rows carry the host note, want 1:\n%s", rows, banner)
		}
	}
}

func TestPairingBannerHostNoteFollowsTheAddressCount(t *testing.T) {
	t.Parallel()
	one := PairingBanner("ABCD-2345", []string{PairingURL("https://mike.lan:3000", "ABCD2345", "")}, bannerWhen)
	if rows := rowCount(one, bannerHostNote); rows != 0 {
		t.Errorf("%d rows carry the host note for one address, want 0:\n%s", rows, one)
	}
	several := PairingBanner("ABCD-2345", []string{
		PairingURL("https://mike.lan:3000", "ABCD2345", ""),
		PairingURL("https://mike.example.com:3000", "ABCD2345", ""),
	}, bannerWhen)
	if rows := rowCount(several, bannerHostNote); rows != 1 {
		t.Errorf("%d rows carry the host note for two addresses, want 1:\n%s", rows, several)
	}
}

func TestPairingBannerFrameIsEven(t *testing.T) {
	t.Parallel()
	banner := PairingBanner("ABCD-2345", []string{"http://localhost:3000/#pair=ABCD2345", "https://mike.lan:3000/#pair=ABCD2345"}, bannerWhen)
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
	banner := PairingBanner("ABCD-2345", []string{PairingURL("https://"+host, "ABCD2345", "")}, bannerWhen)

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
