package auth

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMintedCodeIsTypable(t *testing.T) {
	codes := NewPairingCodes()
	got, err := codes.Mint(DevicePairingTTL)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if len(got.Code) != pairingCodeLength {
		t.Errorf("code %q is %d characters; want %d", got.Code, len(got.Code), pairingCodeLength)
	}
	if strings.ContainsAny(got.Code, "01OILUV") {
		t.Errorf("code %q uses a character that is hard to read off a terminal", got.Code)
	}
	if want := got.Code[:4] + "-" + got.Code[4:]; FormatPairingCode(got.Code) != want {
		t.Errorf("FormatPairingCode(%q) = %q; want %q", got.Code, FormatPairingCode(got.Code), want)
	}
	if got.ExpiresAt.Before(time.Now().Add(DevicePairingTTL - time.Minute)) {
		t.Errorf("expiry %v is sooner than the TTL the mint was asked for", got.ExpiresAt)
	}
}

// Nothing counts attempts against a code, so the size of the space is the whole answer to a guesser. The
// budget is the route's default allowance spent over the whole boot TTL.
func TestPairingCodeSpaceOutrunsTheGuessingBudget(t *testing.T) {
	t.Parallel()
	const attemptsPerMinutePerAddress = 5
	budget := uint64(attemptsPerMinutePerAddress) * uint64(BootPairingTTL/time.Minute)

	space := uint64(1)
	for i := 0; i < pairingCodeLength; i++ {
		space *= uint64(len(pairingAlphabet))
	}
	if odds := space / budget; odds < 1_000_000_000 {
		t.Fatalf("%d guesses in the boot window are only %.0f-to-one against a code of %d characters from %d "+
			"symbols", budget, float64(odds), pairingCodeLength, len(pairingAlphabet))
	}
}

// A symbol written twice keeps the printed length and shortens the space, and nothing else notices.
func TestPairingAlphabetHasNoRepeatedSymbol(t *testing.T) {
	t.Parallel()
	seen := map[rune]bool{}
	for _, r := range pairingAlphabet {
		if seen[r] {
			t.Fatalf("pairingAlphabet holds %q twice", r)
		}
		seen[r] = true
	}
}

// A code that has only begun a ceremony is still live: the spend belongs to the registration, so a browser
// with no prompt to show can pair again with the code it already has.
func TestCodeIsLiveUntilItsRegistrationLands(t *testing.T) {
	codes := NewPairingCodes()
	minted, err := codes.Mint(DevicePairingTTL)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	key, err := codes.Claim(minted.Code)
	if err != nil {
		t.Fatalf("a live code was refused: %v", err)
	}
	if n := codes.Live(); n != 1 {
		t.Fatalf("%d codes live after a begin, want 1", n)
	}
	again, err := codes.Claim(minted.Code)
	if err != nil || again != key {
		t.Fatalf("a second begin claimed %q (%v); want the same key %q", again, err, key)
	}

	if _, err := codes.spend(key); err != nil {
		t.Fatalf("spend: %v", err)
	}
	if _, err := codes.spend(key); !errors.Is(err, ErrInvalidPairingCode) {
		t.Errorf("a spent code paid for a second registration: %v", err)
	}
	if n := codes.Live(); n != 0 {
		t.Errorf("%d codes still live after the only one was spent", n)
	}
}

// A registration whose own write failed has not associated a passkey, so the code that paid for it comes
// back rather than dying with the attempt.
func TestSpentCodeComesBackWhenTheRegistrationFails(t *testing.T) {
	codes := NewPairingCodes()
	minted, err := codes.Mint(DevicePairingTTL)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	key, err := codes.Claim(minted.Code)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	claim, err := codes.spend(key)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	codes.undo(claim)
	if _, err := codes.spend(key); err != nil {
		t.Errorf("a returned code could not pay for the retry: %v", err)
	}
}

// Revoking is the operator's answer to "someone might be about to use that code", so it outranks a claim
// still in flight: a code the slow spender failed to pay with stays gone.
func TestRevokeOutranksAClaimStillInFlight(t *testing.T) {
	codes := NewPairingCodes()
	minted, err := codes.Mint(DevicePairingTTL)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	key, err := codes.Claim(minted.Code)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	claim, err := codes.spend(key)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}

	codes.RevokeAll()
	codes.undo(claim)

	if n := codes.Live(); n != 0 {
		t.Fatalf("%d codes live after a revoke; a slow spender resurrected one", n)
	}
	if _, err := codes.spend(key); !errors.Is(err, ErrInvalidPairingCode) {
		t.Errorf("a revoked code paid for a registration: %v", err)
	}
}

// Two browsers may reach the write with tickets from one code at the same moment, and the code is one
// delete of one entry: exactly one of them pays.
func TestOneCodePaysOnceWhenRegistrationsRace(t *testing.T) {
	codes := NewPairingCodes()
	minted, err := codes.Mint(DevicePairingTTL)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	key, err := codes.Claim(minted.Code)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	const attempts = 8
	paid := make(chan bool, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := codes.spend(key)
			paid <- err == nil
		}()
	}
	wg.Wait()
	close(paid)

	var winners int
	for ok := range paid {
		if ok {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("%d registrations paid with one code; want 1", winners)
	}
	if n := codes.Live(); n != 0 {
		t.Errorf("%d codes live after the only one was spent", n)
	}
}

// A code is read off one screen and typed into another, so the shape it is typed in is not the answer to
// whether it is the right code.
func TestClaimIgnoresHowTheCodeWasTyped(t *testing.T) {
	codes := NewPairingCodes()
	minted, err := codes.Mint(DevicePairingTTL)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	lowered := strings.ToLower(minted.Code)
	if _, err := codes.Claim(" " + lowered[:4] + "-" + lowered[4:] + " "); err != nil {
		t.Errorf("a lower-cased code with a dash and spaces was refused: %v", err)
	}
}

func TestClaimRefuses(t *testing.T) {
	codes := NewPairingCodes()
	minted, err := codes.Mint(DevicePairingTTL)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	for _, presented := range []string{
		"",
		"ZZZZZZZZ",
		minted.Code[:7] + otherLetter(minted.Code[7:]),
		minted.Code + "X",
	} {
		if _, err := codes.Claim(presented); !errors.Is(err, ErrInvalidPairingCode) {
			t.Errorf("Claim(%q) = %v; want ErrInvalidPairingCode", presented, err)
		}
	}
}

func TestCodeExpires(t *testing.T) {
	codes := NewPairingCodes()
	now := time.Now()
	codes.now = func() time.Time { return now }

	minted, err := codes.Mint(time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	key, err := codes.Claim(minted.Code)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := codes.Claim(minted.Code); !errors.Is(err, ErrInvalidPairingCode) {
		t.Errorf("an expired code was claimed: %v", err)
	}
	// A ceremony already under way cannot pay with a code that ran out mid-ceremony.
	if _, err := codes.spend(key); !errors.Is(err, ErrInvalidPairingCode) {
		t.Errorf("an expired code paid for a registration: %v", err)
	}
	if n := codes.Live(); n != 0 {
		t.Errorf("%d codes live; an expired one is not live", n)
	}
}

// A code is minted from an authenticated browser, so the number outstanding is a thing an attacker with
// one stolen session cannot grow without limit.
func TestLiveCodesAreCapped(t *testing.T) {
	codes := NewPairingCodes()
	var minted []PairingCode
	for i := 0; i < pairingMaxLive; i++ {
		code, err := codes.Mint(DevicePairingTTL)
		if err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
		minted = append(minted, code)
	}
	if _, err := codes.Mint(DevicePairingTTL); !errors.Is(err, ErrTooManyPairingCodes) {
		t.Fatalf("a fourth live code was minted: %v", err)
	}
	key, err := codes.Claim(minted[0].Code)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := codes.spend(key); err != nil {
		t.Fatalf("spend: %v", err)
	}
	if _, err := codes.Mint(DevicePairingTTL); err != nil {
		t.Errorf("no code could be minted after one was spent: %v", err)
	}
}

func TestRevokeAllDropsEveryCode(t *testing.T) {
	codes := NewPairingCodes()
	minted, err := codes.Mint(DevicePairingTTL)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	codes.RevokeAll()
	if n := codes.Live(); n != 0 {
		t.Errorf("%d codes live after RevokeAll", n)
	}
	if _, err := codes.Claim(minted.Code); !errors.Is(err, ErrInvalidPairingCode) {
		t.Errorf("a revoked code was accepted: %v", err)
	}
}

func TestCodesDoNotRepeat(t *testing.T) {
	codes := NewPairingCodes()
	seen := map[string]bool{}
	for range 200 {
		// The cap is on *live* codes, so each one is spent to make room for the next.
		minted, err := codes.Mint(DevicePairingTTL)
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		if seen[minted.Code] {
			t.Fatalf("code %q was minted twice", minted.Code)
		}
		seen[minted.Code] = true
		key, err := codes.Claim(minted.Code)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if _, err := codes.spend(key); err != nil {
			t.Fatalf("spend: %v", err)
		}
	}
}

// otherLetter returns a character that is not the one handed in, so a near-miss code differs in exactly
// one place and stays in the alphabet.
func otherLetter(one string) string {
	for _, candidate := range pairingAlphabet {
		if string(candidate) != one {
			return string(candidate)
		}
	}
	return ""
}
