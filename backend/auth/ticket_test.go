package auth

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

func testSession(ttl time.Duration) *webauthn.SessionData {
	return &webauthn.SessionData{
		Challenge:            "abcdefghij0123456789",
		RelyingPartyID:       "localhost",
		Origin:               "http://localhost:3000",
		UserID:               []byte("mike-operator"),
		AllowedCredentialIDs: [][]byte{{1, 2, 3}, {4, 5}},
		Expires:              time.Now().Add(ttl),
	}
}

func newTestTickets(t *testing.T) *Tickets {
	t.Helper()
	tickets, err := NewTickets("a-secret-long-enough-to-be-the-encryption-key")
	if err != nil {
		t.Fatalf("build tickets: %v", err)
	}
	return tickets
}

func TestTicketRoundTrip(t *testing.T) {
	tickets := newTestTickets(t)
	session := testSession(time.Minute)

	ticket, err := tickets.Seal(ticketPurposeRegister, session, "")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, _, err := tickets.Open(ticketPurposeRegister, ticket)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got.Challenge != session.Challenge || got.Origin != session.Origin || got.RelyingPartyID != session.RelyingPartyID {
		t.Errorf("round trip changed the ceremony state: %+v", got)
	}
	if string(got.UserID) != "mike-operator" {
		t.Errorf("user handle = %q", got.UserID)
	}
	if len(got.AllowedCredentialIDs) != 2 {
		t.Errorf("allowed credentials = %v; want the two issued", got.AllowedCredentialIDs)
	}
}

// The two ceremonies are not interchangeable: the purpose is inside the seal, not inside the payload, so
// an unfinished registration cannot be presented to the sign-in endpoint (or the other way round).
func TestTicketIsBoundToItsCeremony(t *testing.T) {
	tickets := newTestTickets(t)
	ticket, err := tickets.Seal(ticketPurposeRegister, testSession(time.Minute), "")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, _, err := tickets.Open(ticketPurposeAssert, ticket); !errors.Is(err, ErrTicket) {
		t.Errorf("a registration ticket opened the sign-in endpoint: %v", err)
	}
}

func TestTicketRefuses(t *testing.T) {
	tickets := newTestTickets(t)
	good, err := tickets.Seal(ticketPurposeAssert, testSession(time.Minute), "")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	otherInstall, err := NewTickets("another-install-encryption-key")
	if err != nil {
		t.Fatalf("build other tickets: %v", err)
	}

	expired, err := tickets.Seal(ticketPurposeAssert, testSession(-time.Minute), "")
	if err != nil {
		t.Fatalf("seal expired: %v", err)
	}

	macAt := strings.LastIndex(good, ".")
	head, mac := good[:macAt], good[macAt+1:]

	for _, tc := range []struct {
		name   string
		ticket string
	}{
		{"no signature", head},
		{"no payload", "." + mac},
		{"a foreign signature", strings.Repeat("A", len(head)) + "." + mac},
		{"a tampered payload", base64.RawURLEncoding.EncodeToString([]byte(`{"Challenge":"tampered"}`)) + "." + mac},
		{"a truncated signature", good[:len(good)-4]},
		{"garbage", "not-a-ticket"},
		{"empty", ""},
		{"from another install", mustSeal(t, otherInstall, testSession(time.Minute))},
		{"expired", expired},
	} {
		if _, _, err := tickets.Open(ticketPurposeAssert, tc.ticket); !errors.Is(err, ErrTicket) {
			t.Errorf("%s: got %v; want ErrTicket", tc.name, err)
		}
	}
}

// The pairing claim a registration is paid with rides inside the seal, so a ticket cannot be pointed at
// another code's claim.
func TestTicketCarriesTheStateSealedWithIt(t *testing.T) {
	tickets := newTestTickets(t)
	mine, err := tickets.Seal(ticketPurposeRegister, testSession(time.Minute), "spend-key-mine")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	theirs, err := tickets.Seal(ticketPurposeRegister, testSession(time.Minute), "spend-key-theirs")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	_, extra, err := tickets.Open(ticketPurposeRegister, mine)
	if err != nil || extra != "spend-key-mine" {
		t.Fatalf("the sealed state came back as %q (%v)", extra, err)
	}

	// Their body under my seal: the MAC covers the claim, so it cannot be swapped between tickets.
	mineMacAt, theirsMacAt := strings.LastIndex(mine, "."), strings.LastIndex(theirs, ".")
	swapped := theirs[:theirsMacAt] + mine[mineMacAt:]
	if _, _, err := tickets.Open(ticketPurposeRegister, swapped); !errors.Is(err, ErrTicket) {
		t.Errorf("another ceremony's claim opened under this ticket's seal: %v", err)
	}
	if _, _, err := tickets.Open(ticketPurposeAssert, mine); !errors.Is(err, ErrTicket) {
		t.Errorf("a sealed claim opened the sign-in endpoint: %v", err)
	}
}

func mustSeal(t *testing.T, tickets *Tickets, session *webauthn.SessionData) string {
	t.Helper()
	ticket, err := tickets.Seal(ticketPurposeAssert, session, "")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return ticket
}

func TestNewTicketsRefusesNoSecret(t *testing.T) {
	if _, err := NewTickets(""); err == nil {
		t.Fatal("tickets were built with no secret to seal with")
	}
}
