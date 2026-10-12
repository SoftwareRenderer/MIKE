package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// A pairing code allows a browser to register a passkey. It does not authenticate the user or
// create a session; the registration still requires a device signature. This makes printed codes
// safe to leave on a terminal for a limited time.
//
// Codes live in memory. A registration spends one code. Abandoned ceremonies leave codes live until
// they expire. Restarts drop all codes so they do not outlive the ceremony.

const (
	// BootPairingTTL is how long the code printed at startup stays live. It is longer than the code a
	// signed-in device mints because the boot code is printed before the person has read it.
	BootPairingTTL = 15 * time.Minute

	// DevicePairingTTL is how long a code minted from Settings stays live — long enough to type the code
	// into a second device, short enough that a code left on screen stops being an invitation.
	DevicePairingTTL = 5 * time.Minute

	// pairingCodeLength gives a space of 29^8 — 2^38.9, since pairingAlphabet holds 29 symbols rather than 32.
	// Guessing is bounded by the login rate limiter, not by a per-code counter: a wrong code has no entry to
	// count against.
	pairingCodeLength = 8

	// pairingMaxLive caps how many codes are outstanding at once, so a minted code cannot be hoarded.
	pairingMaxLive = 3

	// pairingAlphabet has no 0/O, 1/I/L, or U/V: these codes are read off a terminal or a phone screen
	// and typed on another keyboard, and the characters that cost a mistyped attempt are exactly the
	// ones a font draws the same way.
	pairingAlphabet = "23456789ABCDEFGHJKMNPQRSTWXYZ"
)

// ErrInvalidPairingCode is the one refusal for a code that is unknown, expired, already spent or out of
// attempts. Distinguishing them would tell a guesser how close they got.
var ErrInvalidPairingCode = errors.New("pairing code is not valid")

// ErrTooManyPairingCodes refuses a mint while the cap is already full.
var ErrTooManyPairingCodes = errors.New("too many pairing codes are already waiting")

// PairingCode is a minted code with the expiry its presenter should be shown.
type PairingCode struct {
	Code      string
	ExpiresAt time.Time
}

// pairingCode is the memory behind one live code.
type pairingCode struct {
	expiresAt time.Time
}

// PairingCodes holds the outstanding codes. Its mutex is a leaf: nothing is called while it is held.
type PairingCodes struct {
	mu    sync.Mutex
	codes map[string]*pairingCode // keyed by hex(sha256(code)), so a presented code is one lookup
	// epoch counts revokes. A code restored via undo() is refused if the epoch has moved,
	// ensuring revoked codes stay dropped.
	epoch uint64
	// now is the clock, replaced only by a test that needs to watch a code expire.
	now func() time.Time
}

// pairingClaim is a spent code put aside until the registration it pays for is written.
type pairingClaim struct {
	key       string
	expiresAt time.Time
	epoch     uint64
}

// NewPairingCodes returns an empty code set.
func NewPairingCodes() *PairingCodes {
	return &PairingCodes{codes: map[string]*pairingCode{}, now: time.Now}
}

// Mint issues a code valid for ttl. The plaintext is returned once and never kept: the map holds its
// digest, which is what a presented code is compared by.
func (c *PairingCodes) Mint(ttl time.Duration) (PairingCode, error) {
	code, err := randomCode()
	if err != nil {
		return PairingCode{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropExpired()
	if len(c.codes) >= pairingMaxLive {
		return PairingCode{}, ErrTooManyPairingCodes
	}
	expires := c.now().Add(ttl)
	c.codes[codeKey(code)] = &pairingCode{expiresAt: expires}
	return PairingCode{Code: code, ExpiresAt: expires}, nil
}

// Claim checks a presented code and returns the key its entry is spent by. Nothing is deleted: a code buys
// one registration and is spent when that registration is written, so a browser that cannot run the
// ceremony is left holding a code that still works.
func (c *PairingCodes) Claim(presented string) (string, error) {
	code := NormalizePairingCode(presented)
	if len(code) != pairingCodeLength {
		return "", ErrInvalidPairingCode
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropExpired()
	key := codeKey(code)
	entry, ok := c.codes[key]
	if !ok || !c.now().Before(entry.expiresAt) {
		return "", ErrInvalidPairingCode
	}
	return key, nil
}

// spend takes a claimed entry out of the set and returns what `undo` needs to put it back. A key whose
// entry is gone or expired is the refusal a wrong code gets.
func (c *PairingCodes) spend(key string) (pairingClaim, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.codes[key]
	if !ok || !c.now().Before(entry.expiresAt) {
		return pairingClaim{}, ErrInvalidPairingCode
	}
	delete(c.codes, key)
	return pairingClaim{key: key, expiresAt: entry.expiresAt, epoch: c.epoch}, nil
}

// undo returns a spent code to the set because the registration it paid for was never written. A revoke
// in between outranks the claim: the codes this set was told to drop stay dropped.
func (c *PairingCodes) undo(claim pairingClaim) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim.epoch != c.epoch {
		return
	}
	if _, live := c.codes[claim.key]; !live {
		c.codes[claim.key] = &pairingCode{expiresAt: claim.expiresAt}
	}
}

// Live reports how many codes are still outstanding, after dropping the expired ones.
func (c *PairingCodes) Live() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropExpired()
	return len(c.codes)
}

// RevokeAll drops every outstanding code. Registered passkeys are untouched: this is the "someone else
// might be about to use that code" button, not the emergency exit.
func (c *PairingCodes) RevokeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.codes)
	c.epoch++
}

// dropExpired removes codes whose window has passed. Callers hold c.mu.
func (c *PairingCodes) dropExpired() {
	now := c.now()
	for key, entry := range c.codes {
		if !now.Before(entry.expiresAt) {
			delete(c.codes, key)
		}
	}
}

// NormalizePairingCode is what a typed code becomes: upper case, with the spaces and dashes a person
// adds to keep track of eight characters removed. One function owns it, so the code printed at startup,
// the one in the pairing URL and the one in the form are compared in exactly one shape.
func NormalizePairingCode(presented string) string {
	var b strings.Builder
	b.Grow(pairingCodeLength)
	for _, r := range presented {
		switch {
		case r == ' ' || r == '-' || r == '\t':
			continue
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// FormatPairingCode is how a code is shown: two groups of four, which is what a person reads aloud and
// types without losing their place.
func FormatPairingCode(code string) string {
	if len(code) != pairingCodeLength {
		return code
	}
	return code[:4] + "-" + code[4:]
}

// randomCode draws from crypto/rand. It redraws bytes to avoid modulo bias, ensuring
// each alphabet symbol is equally likely.
func randomCode() (string, error) {
	limit := byte(256 - 256%len(pairingAlphabet))
	b := make([]byte, pairingCodeLength)
	for i := range b {
		for {
			var one [1]byte
			if _, err := rand.Read(one[:]); err != nil {
				return "", fmt.Errorf("draw pairing code: %w", err)
			}
			if one[0] >= limit {
				continue
			}
			b[i] = pairingAlphabet[one[0]%byte(len(pairingAlphabet))]
			break
		}
	}
	return string(b), nil
}

// codeKey is the map key for a code. The digest is not a secret here — the codes are high-entropy and
// short-lived — it is what lets a presented code be looked up instead of compared against a list.
func codeKey(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}
