package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"golang.org/x/crypto/hkdf"
)

// A WebAuthn ceremony is two requests with a signature in between, and nothing authenticates anyone
// during the gap. The state that binds the challenge this server chose to the response the browser
// brings back is sealed and handed to the browser rather than kept in a table: an abandoned ceremony is
// the normal outcome (someone closes the tab), so a table of challenges would be state held for a client
// that is already gone.
//
// The seal is an HMAC-SHA256 over the ceremony's purpose and the payload, keyed by a value derived from
// this install's secret. So a ticket cannot cross between the two ceremony endpoints — the purpose is
// inside the MAC, not inside the payload — and a ticket from a copied database fails at another install.
//
// The payload is the library's own msgpack encoding of SessionData, which is the codec it generates for
// persisting exactly that value; the JSON form is for the wire and drops what only the verifier needs.

// TicketTTL is how long a begun ceremony stays openable. It bounds the window in which a stolen response
// body could be replayed against a challenge nobody is waiting on, and it is longer than the ceremony's
// own timeout so the library's answer is the one a person ever sees.
const TicketTTL = 2 * time.Minute

// The two purposes a ticket is sealed for. They are not interchangeable, so an unfinished registration
// ticket cannot be presented to the sign-in endpoint.
const (
	ticketPurposeRegister = "register"
	ticketPurposeAssert   = "assert"
)

// ticketMaxPayloadBytes bounds the sealed blob before it is decoded. SessionData is a challenge, a user
// handle and a credential list; anything that outgrows this came from somewhere else.
const ticketMaxPayloadBytes = 16 << 10

// ErrTicket is the one refusal for every way a ticket is unusable: forged, tampered, the wrong
// ceremony's, from another install, or expired. They are one answer because telling them apart is a
// signal about the ticket that nobody needs.
var ErrTicket = errors.New("ceremony ticket is invalid or has expired")

// Tickets seals and opens ceremony state. The key is written once, so a Tickets is safe to share.
type Tickets struct {
	key []byte
}

// NewTickets derives the seal key from the resolved secret (MIKE_ENCRYPTION_KEY) with HKDF, so a ticket
// is one use of that secret rather than the secret itself.
func NewTickets(secret string) (*Tickets, error) {
	if secret == "" {
		return nil, errors.New("ceremony tickets have no secret to be sealed with")
	}
	key := make([]byte, sha256.Size)
	if _, err := hkdf.New(sha256.New, []byte(secret), nil, []byte("mike/auth/ceremony-ticket")).Read(key); err != nil {
		return nil, fmt.Errorf("derive ceremony ticket key: %w", err)
	}
	return &Tickets{key: key}, nil
}

// Seal encodes the ceremony state and appends its MAC. The result is what the browser holds between the
// two halves of the ceremony.
func (t *Tickets) Seal(purpose string, session *webauthn.SessionData) (string, error) {
	payload, err := session.MarshalMsg(nil)
	if err != nil {
		return "", fmt.Errorf("encode ceremony state: %w", err)
	}
	if len(payload) > ticketMaxPayloadBytes {
		return "", fmt.Errorf("ceremony state is %d bytes, over the %d byte bound", len(payload), ticketMaxPayloadBytes)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + t.mac(purpose, body), nil
}

// Open returns the ceremony state a previous Begin call issued for this purpose on this install.
//
// The expiry check is here rather than left to the library: a ticket is held by a browser that can hold
// it for as long as it likes, and the seal says who issued it, not when it stopped being current.
func (t *Tickets) Open(purpose, ticket string) (*webauthn.SessionData, error) {
	body, mac, ok := strings.Cut(ticket, ".")
	if !ok || body == "" || len(body) > base64.RawURLEncoding.EncodedLen(ticketMaxPayloadBytes) {
		return nil, ErrTicket
	}
	if !hmac.Equal([]byte(mac), []byte(t.mac(purpose, body))) {
		return nil, ErrTicket
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || len(payload) > ticketMaxPayloadBytes {
		return nil, ErrTicket
	}
	session := &webauthn.SessionData{}
	if _, err := session.UnmarshalMsg(payload); err != nil {
		return nil, ErrTicket
	}
	if time.Now().After(session.Expires) {
		return nil, ErrTicket
	}
	return session, nil
}

// mac is the seal over one purpose and one payload. The separator keeps a purpose that ends in the
// payload's first byte from reading as another (purpose, payload) pair.
func (t *Tickets) mac(purpose, body string) string {
	mac := hmac.New(sha256.New, t.key)
	mac.Write([]byte(purpose))
	mac.Write([]byte{0})
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
