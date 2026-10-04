package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"

	"mike/backend/store"
	"mike/backend/testutil/testwebauthn"
)

// newTestPasskeys is a ceremony pair over an empty database: nothing is registered until a test
// registers it.
func newTestPasskeys(t *testing.T) (*Passkeys, *store.Store, *Tickets) {
	t.Helper()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	tickets, err := NewTickets("test-encryption-key")
	if err != nil {
		t.Fatalf("build tickets: %v", err)
	}
	return NewPasskeys(s, tickets), s, tickets
}

var localHost = RelyingHost{RPID: "localhost", Origin: "http://localhost:3000"}

// register runs both halves of a registration and returns the credential the browser minted.
func register(t *testing.T, p *Passkeys, tickets *Tickets, host RelyingHost, name string, flags protocol.AuthenticatorFlags) *testwebauthn.Credential {
	t.Helper()
	options, ticket, err := p.BeginRegistration(context.Background(), host)
	if err != nil {
		t.Fatalf("begin registration: %v", err)
	}
	session, err := tickets.Open(ticketPurposeRegister, ticket)
	if err != nil {
		t.Fatalf("the issued ticket does not open: %v", err)
	}
	if len(options.Response.CredentialExcludeList) != 0 {
		t.Error("a registration did not exclude the credentials already registered")
	}
	credential, err := testwebauthn.NewCredential()
	if err != nil {
		t.Fatalf("mint credential: %v", err)
	}
	body, err := credential.RegistrationBody(host.RPID, host.Origin, session.Challenge, flags)
	if err != nil {
		t.Fatalf("build registration response: %v", err)
	}
	registration, err := protocol.ParseCredentialCreationResponseBytes(body)
	if err != nil {
		t.Fatalf("parse the registration response: %v", err)
	}
	if _, err := p.FinishRegistration(context.Background(), host, ticket, registration, name); err != nil {
		t.Fatalf("finish registration: %v", err)
	}
	return credential
}

// assertCredential runs both halves of a sign-in.
func assertCredential(t *testing.T, p *Passkeys, tickets *Tickets, host RelyingHost, credential *testwebauthn.Credential, counter uint32, flags protocol.AuthenticatorFlags) error {
	t.Helper()
	options, ticket, err := p.BeginAssertion(context.Background(), host)
	if err != nil {
		return err
	}
	if len(options.Response.AllowedCredentials) != 1 {
		t.Errorf("the sign-in offered %d credentials, want the one this host has", len(options.Response.AllowedCredentials))
	}
	session, err := tickets.Open(ticketPurposeAssert, ticket)
	if err != nil {
		return err
	}
	body, err := credential.AssertionBody(host.RPID, host.Origin, session.Challenge, counter, operatorHandle, flags)
	if err != nil {
		return err
	}
	assertion, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		return err
	}
	_, err = p.FinishAssertion(context.Background(), host, ticket, assertion)
	return err
}

func TestNewRelyingHost(t *testing.T) {
	for _, tc := range []struct {
		name       string
		authority  string
		secure     bool
		wantRPID   string
		wantOrigin string
	}{
		{"loopback with a port", "localhost:3000", false, "localhost", "http://localhost:3000"},
		{"loopback over TLS", "localhost:3000", true, "localhost", "https://localhost:3000"},
		{"the default TLS port is not spelled", "kanban.example.com:443", true, "kanban.example.com", "https://kanban.example.com"},
		{"the default plain port is not spelled", "localhost:80", false, "localhost", "http://localhost"},
		{"a host without a port", "kanban.example.com", true, "kanban.example.com", "https://kanban.example.com"},
		{"case is folded", "Kanban.Example.COM:8443", true, "kanban.example.com", "https://kanban.example.com:8443"},
		{"an mDNS name", "mike.local:3000", true, "mike.local", "https://mike.local:3000"},
	} {
		got, err := NewRelyingHost(tc.authority, tc.secure)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got.RPID != tc.wantRPID || got.Origin != tc.wantOrigin {
			t.Errorf("%s: got rpID %q origin %q; want %q and %q", tc.name, got.RPID, got.Origin, tc.wantRPID, tc.wantOrigin)
		}
	}
	if _, err := NewRelyingHost(":3000", false); err == nil {
		t.Error("an empty host was accepted as a relying party")
	}
	// An address cannot be a relying-party id, and the refusal has to say that rather than let the
	// browser fail on a prompt the person already answered.
	for _, authority := range []string{"192.168.1.10:3000", "[::1]:3000"} {
		if _, err := NewRelyingHost(authority, true); !errors.Is(err, ErrHostCannotHostPasskeys) {
			t.Errorf("%s: got %v; want ErrHostCannotHostPasskeys", authority, err)
		}
	}
}

func TestRegistrationRegistersAndSignInAccepts(t *testing.T) {
	p, s, tickets := newTestPasskeys(t)
	ctx := context.Background()

	if has, err := p.HasPasskeys(ctx); err != nil || has {
		t.Fatalf("HasPasskeys on an empty install = %v, %v; want false, nil", has, err)
	}
	if _, _, err := p.BeginAssertion(ctx, localHost); !errors.Is(err, ErrNoPasskeyForHost) {
		t.Fatalf("sign-in on an unpaired install = %v; want ErrNoPasskeyForHost", err)
	}

	credential := register(t, p, tickets, localHost, "test laptop",
		protocol.FlagUserPresent|protocol.FlagUserVerified|protocol.FlagAttestedCredentialData)

	if has, err := p.HasPasskeys(ctx); err != nil || !has {
		t.Fatalf("HasPasskeys after registration = %v, %v; want true, nil", has, err)
	}
	rows, err := s.ListPasskeys(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %v, %v; want one", rows, err)
	}
	if rows[0].Name != "test laptop" || rows[0].RPID != "localhost" {
		t.Errorf("row = %+v; want the label and the host the ceremony ran on", rows[0])
	}
	if want := EncodeCredentialID(credential.ID()); rows[0].CredentialID != want {
		t.Errorf("credential id = %q, want %q", rows[0].CredentialID, want)
	}

	flags := protocol.FlagUserPresent | protocol.FlagUserVerified
	if err := assertCredential(t, p, tickets, localHost, credential, 1, flags); err != nil {
		t.Fatalf("sign-in with the registered credential: %v", err)
	}
	rows, _ = s.ListPasskeys(ctx)
	if rows[0].LastUsedAt.Before(rows[0].CreatedAt.Add(-time.Minute)) {
		t.Errorf("last_used_at = %v was not moved by a verified sign-in", rows[0].LastUsedAt)
	}
}

// The ceremony is bound to the host it began on. A signature made for another origin — a page on a
// different port, a LAN address someone else answers — is not proof of anything here.
func TestAssertionRefusesAnotherOrigin(t *testing.T) {
	p, _, tickets := newTestPasskeys(t)
	flags := protocol.FlagUserPresent | protocol.FlagUserVerified | protocol.FlagAttestedCredentialData
	credential := register(t, p, tickets, localHost, "laptop", flags)

	options, ticket, err := p.BeginAssertion(context.Background(), localHost)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	_ = options
	session, err := tickets.Open(ticketPurposeAssert, ticket)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	body, err := credential.AssertionBody(localHost.RPID, "http://localhost:9999", session.Challenge, 1, operatorHandle,
		protocol.FlagUserPresent|protocol.FlagUserVerified)
	if err != nil {
		t.Fatalf("build response: %v", err)
	}
	assertion, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		t.Fatalf("parse the assertion: %v", err)
	}
	if _, err := p.FinishAssertion(context.Background(), localHost, ticket, assertion); !errors.Is(err, ErrCeremonyRefused) {
		t.Fatalf("an assertion from another origin = %v; want ErrCeremonyRefused", err)
	}
}

// User verification required is what lets a finished registration stand as the sign-in it is followed by,
// so an assertion that only proves presence is refused rather than downgraded.
func TestAssertionRefusesWithoutUserVerification(t *testing.T) {
	p, _, tickets := newTestPasskeys(t)
	credential := register(t, p, tickets, localHost, "laptop",
		protocol.FlagUserPresent|protocol.FlagUserVerified|protocol.FlagAttestedCredentialData)

	if err := assertCredential(t, p, tickets, localHost, credential, 1, protocol.FlagUserPresent); !errors.Is(err, ErrCeremonyRefused) {
		t.Fatalf("an assertion without user verification = %v; want ErrCeremonyRefused", err)
	}
}

// A counter that goes backwards on a credential that cannot be synchronized means the key was copied.
func TestAssertionRefusesABackwardsCounterOnAHardwareCredential(t *testing.T) {
	p, _, tickets := newTestPasskeys(t)
	credential := register(t, p, tickets, localHost, "security key",
		protocol.FlagUserPresent|protocol.FlagUserVerified|protocol.FlagAttestedCredentialData)
	flags := protocol.FlagUserPresent | protocol.FlagUserVerified

	if err := assertCredential(t, p, tickets, localHost, credential, 9, flags); err != nil {
		t.Fatalf("first sign-in: %v", err)
	}
	if err := assertCredential(t, p, tickets, localHost, credential, 4, flags); !errors.Is(err, ErrCeremonyRefused) {
		t.Fatalf("a sign-in whose counter went backwards = %v; want ErrCeremonyRefused", err)
	}
}

// A synchronized passkey has no counter that means anything across its devices, so the same signal is
// reported and accepted rather than locking a person out of their own phone.
func TestAssertionAcceptsABackwardsCounterOnASynchronizedCredential(t *testing.T) {
	p, _, tickets := newTestPasskeys(t)
	credential := register(t, p, tickets, localHost, "phone",
		protocol.FlagUserPresent|protocol.FlagUserVerified|protocol.FlagBackupEligible|protocol.FlagAttestedCredentialData)
	flags := protocol.FlagUserPresent | protocol.FlagUserVerified | protocol.FlagBackupEligible

	if err := assertCredential(t, p, tickets, localHost, credential, 9, flags); err != nil {
		t.Fatalf("first sign-in: %v", err)
	}
	if err := assertCredential(t, p, tickets, localHost, credential, 4, flags); err != nil {
		t.Fatalf("a synchronized credential's stale counter was refused: %v", err)
	}
}

// A credential minted for one host is not offered, and not accepted, at another.
func TestCredentialsAreScopedToTheHostTheyWereMintedFor(t *testing.T) {
	p, _, tickets := newTestPasskeys(t)
	lan := RelyingHost{RPID: "mike.local", Origin: "https://mike.local:3000"}
	flags := protocol.FlagUserPresent | protocol.FlagUserVerified | protocol.FlagAttestedCredentialData
	register(t, p, tickets, lan, "desktop", flags)

	if _, _, err := p.BeginAssertion(context.Background(), localHost); !errors.Is(err, ErrNoPasskeyForHost) {
		t.Fatalf("localhost was offered a credential minted for %s: %v", lan.RPID, err)
	}
}

// A response that answers a challenge nobody issued is refused, which is the same check that stops one
// ceremony's response being used against another.
func TestAssertionRefusesAForeignChallenge(t *testing.T) {
	p, _, tickets := newTestPasskeys(t)
	credential := register(t, p, tickets, localHost, "laptop",
		protocol.FlagUserPresent|protocol.FlagUserVerified|protocol.FlagAttestedCredentialData)

	_, ticket, err := p.BeginAssertion(context.Background(), localHost)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	body, err := credential.AssertionBody(localHost.RPID, localHost.Origin, "not-the-challenge", 1, operatorHandle,
		protocol.FlagUserPresent|protocol.FlagUserVerified)
	if err != nil {
		t.Fatalf("build response: %v", err)
	}
	assertion, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		t.Fatalf("parse the assertion: %v", err)
	}
	if _, err := p.FinishAssertion(context.Background(), localHost, ticket, assertion); !errors.Is(err, ErrCeremonyRefused) {
		t.Fatalf("an assertion answering someone else's challenge = %v; want ErrCeremonyRefused", err)
	}
}
