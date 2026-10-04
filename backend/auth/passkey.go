package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"mike/backend/store"
	"mike/pkg/logging"
)

// The ceremonies MIKE runs: a registration, which a pairing code authorizes and which produces a
// credential, and an assertion, which every later sign-in is. Both are bound to the host the request
// arrived at, and both carry their state in a sealed ticket rather than a table (see ticket.go).

const (
	// rpDisplayName is what the browser's passkey prompt shows.
	rpDisplayName = "MIKE"

	// maxPasskeyRecordBytes bounds what one credential may occupy in the database. The record is the
	// verifier's own bytes — key, flags, and the raw attestation object kept for re-verification — and a
	// real one is a couple of kilobytes.
	maxPasskeyRecordBytes = 16 << 10
)

var (
	// ErrCeremonyRefused wraps every way a ceremony can fail to verify: the wrong key, an expired
	// challenge, a response from another origin, a credential nobody registered. It is a client fault, so
	// the answer is 401 and the cause belongs in the log.
	ErrCeremonyRefused = errors.New("passkey ceremony refused")

	// ErrNoPasskeyForHost says this address has no credential to offer, so a sign-in here cannot start.
	// It is not a secret — the same answer is implied by the login form offering a pairing code — and it
	// is what tells the browser to stop prompting and ask for a code instead.
	ErrNoPasskeyForHost = errors.New("no passkey is registered for this host")

	// ErrHostCannotHostPasskeys refuses a ceremony at a bare IP address. A credential is stamped with a
	// hostname, and a verifier will not accept an address as a relying-party id, so MIKE reached at
	// https://192.168.1.10:3000 has no passkey to offer. The operator names the host instead — a
	// hostname in MIKE_ALLOWED_HOSTS, which is what the Host pinning already asks for.
	ErrHostCannotHostPasskeys = errors.New("this address cannot use passkeys; open MIKE by its hostname")
)

// RelyingHost is the pair a ceremony is bound to: the relying-party id a credential is minted for, and
// the one origin the response must come from. Both come from the request, because one install answers at
// localhost, at a LAN address and behind a pinned proxy name, and WebAuthn ties a credential to the first
// and a signature to the second. The Host header is already confined by the pinning middleware, so this
// chooses among hosts the operator pinned rather than among hosts a visitor picks.
type RelyingHost struct {
	// RPID is the host the credential is stamped with — a hostname, never an address.
	RPID string
	// Origin is the full origin the browser reports, scheme and non-default port included.
	Origin string
}

// NewRelyingHost derives the pair from a request authority and whether TLS terminated here. A host that
// cannot be a relying party — empty, an address rather than a name, or one the verifier's own validation
// refuses — is an error the caller answers rather than a ceremony that fails in the browser.
func NewRelyingHost(authority string, secure bool) (RelyingHost, error) {
	host, port := authority, ""
	if h, p, err := net.SplitHostPort(authority); err == nil {
		host, port = h, p
	}
	host = strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "["), "]")
	if host == "" {
		return RelyingHost{}, fmt.Errorf("no host to bind the ceremony to in %q", authority)
	}
	if net.ParseIP(host) != nil {
		return RelyingHost{}, fmt.Errorf("%w: %s is an address, not a hostname", ErrHostCannotHostPasskeys, host)
	}
	scheme := "http"
	if secure {
		scheme = "https"
	}
	origin := scheme + "://" + host
	if port != "" && !isDefaultPort(scheme, port) {
		origin += ":" + port
	}
	bound := RelyingHost{RPID: host, Origin: origin}
	// The verifier validates both spellings, so the refusal happens here rather than halfway through a
	// prompt the person has already answered.
	if _, err := bound.verifier(); err != nil {
		return RelyingHost{}, err
	}
	return bound, nil
}

func isDefaultPort(scheme, port string) bool {
	return scheme == "https" && port == "443" || scheme == "http" && port == "80"
}

// verifier builds the verifier for this host. It is built per request rather than held: the relying-party
// id and origin are what the request says, and one config shared between localhost and a proxy domain
// would accept a signature made for one as proof of the other.
func (h RelyingHost) verifier() (*webauthn.WebAuthn, error) {
	w, err := webauthn.New(&webauthn.Config{
		RPDisplayName: rpDisplayName,
		RPID:          h.RPID,
		RPOrigins:     []string{h.Origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			// A resident key is what lets the browser offer the credential without being told its
			// id, and a discoverable credential is what a synced passkey is: pairing one device
			// covers the others that share the person's passkey provider.
			ResidentKey: protocol.ResidentKeyRequirementRequired,
			// Verification required is what lets a finished registration stand as the sign-in it
			// is followed by. A roaming security key therefore needs a PIN or biometric enrolled;
			// touch alone is presence, and presence alone does not open a session here.
			UserVerification: protocol.VerificationRequired,
		},
		// "none" because no decision here depends on who made the authenticator: there is no metadata
		// service behind an appliance's own passkey policy, and none is what lets a platform
		// authenticator answer without shipping the person's attestation data to this server.
		AttestationPreference: protocol.PreferNoAttestation,
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: TicketTTL, TimeoutUVD: TicketTTL},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: TicketTTL, TimeoutUVD: TicketTTL},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("host %q cannot host passkeys: %w", h.RPID, err)
	}
	return w, nil
}

// operatorHandle is the user handle every credential on this install carries. One install has one
// operator, so the handle is a constant rather than a row: it is not a secret (it is inside every
// registration ceremony), and nothing keys off it except the equality check the ceremony itself makes.
var operatorHandle = []byte("mike-operator")

// operator is the webauthn.User adapter over the rows this host has credentials for.
type operator struct {
	credentials []webauthn.Credential
}

func (operator) WebAuthnID() []byte   { return operatorHandle }
func (operator) WebAuthnName() string { return "MIKE operator" }
func (operator) WebAuthnDisplayName() string {
	return "MIKE operator"
}

func (o operator) WebAuthnCredentials() []webauthn.Credential { return o.credentials }

// Passkeys runs the ceremonies against this install's credentials.
type Passkeys struct {
	store   *store.Store
	tickets *Tickets
}

// NewPasskeys wires the ceremonies to the credential table and the ticket seal.
func NewPasskeys(s *store.Store, tickets *Tickets) *Passkeys {
	return &Passkeys{store: s, tickets: tickets}
}

// HasPasskeys reports whether this install has any credential — the unpaired/paired distinction the
// login form and the boot pairing decision both read.
func (p *Passkeys) HasPasskeys(ctx context.Context) (bool, error) {
	n, err := p.store.PasskeyCount(ctx)
	return n > 0, err
}

// BeginRegistration starts the ceremony a pairing code has just spent. The response is the browser's
// creation options; the ticket is what the finish request must carry back.
//
// The already-registered credentials are excluded: a device that has a passkey here gets "you already
// have one" from its own browser instead of a second row for the same key.
func (p *Passkeys) BeginRegistration(ctx context.Context, host RelyingHost) (*protocol.CredentialCreation, string, error) {
	credentials, err := p.credentials(ctx, host.RPID)
	if err != nil {
		return nil, "", err
	}
	w, err := host.verifier()
	if err != nil {
		return nil, "", err
	}
	options, session, err := w.BeginRegistration(
		operator{credentials: credentials},
		webauthn.WithExclusions(credentialDescriptors(credentials)),
	)
	if err != nil {
		return nil, "", fmt.Errorf("begin passkey registration: %w", err)
	}
	ticket, err := p.tickets.Seal(ticketPurposeRegister, session)
	if err != nil {
		return nil, "", err
	}
	return options, ticket, nil
}

// FinishRegistration verifies the browser's registration response and registers the credential for this
// host. The response is the parsed credential the route read off the wire: the verifier works on that
// form, so the HTTP boundary stays in backend/server and this package answers with typed results.
//
// A completed registration is itself proof of key possession with user verification, which is why it
// opens the session on the browser that made it — the pairing code bought the ceremony, not the session.
func (p *Passkeys) FinishRegistration(ctx context.Context, host RelyingHost, ticket string, response *protocol.ParsedCredentialCreationData, name string) (string, error) {
	session, err := p.tickets.Open(ticketPurposeRegister, ticket)
	if err != nil {
		return "", err
	}
	credentials, err := p.credentials(ctx, host.RPID)
	if err != nil {
		return "", err
	}
	w, err := host.verifier()
	if err != nil {
		return "", err
	}
	credential, err := w.CreateCredential(operator{credentials: credentials}, *session, response)
	if err != nil {
		return "", refused(err)
	}
	record, err := marshalCredential(credential)
	if err != nil {
		return "", err
	}
	id := EncodeCredentialID(credential.ID)
	if err := p.store.CreatePasskey(ctx, store.NewPasskey{
		CredentialID: id,
		RPID:         host.RPID,
		Name:         name,
		Record:       record,
	}); err != nil {
		return "", fmt.Errorf("register passkey: %w", err)
	}
	logging.For(logging.Auth).Infof("registered a passkey for %s (credential %s)", host.RPID, id)
	return id, nil
}

// BeginAssertion starts a sign-in. Only this host's credentials are offered, so the browser is not
// prompted for a key whose signature could not be accepted here.
func (p *Passkeys) BeginAssertion(ctx context.Context, host RelyingHost) (*protocol.CredentialAssertion, string, error) {
	credentials, err := p.credentials(ctx, host.RPID)
	if err != nil {
		return nil, "", err
	}
	if len(credentials) == 0 {
		return nil, "", ErrNoPasskeyForHost
	}
	w, err := host.verifier()
	if err != nil {
		return nil, "", err
	}
	options, session, err := w.BeginLogin(
		operator{credentials: credentials},
		webauthn.WithAllowedCredentials(credentialDescriptors(credentials)),
	)
	if err != nil {
		return nil, "", fmt.Errorf("begin passkey sign-in: %w", err)
	}
	ticket, err := p.tickets.Seal(ticketPurposeAssert, session)
	if err != nil {
		return nil, "", err
	}
	return options, ticket, nil
}

// FinishAssertion verifies a signature and returns the credential that made it, given the parsed
// credential the route read off the wire.
//
// The credential is written back because both the signature counter and the latched user-verification bit
// moved: the record on disk is the verifier's state, not a copy of what registration produced.
func (p *Passkeys) FinishAssertion(ctx context.Context, host RelyingHost, ticket string, response *protocol.ParsedCredentialAssertionData) (string, error) {
	session, err := p.tickets.Open(ticketPurposeAssert, ticket)
	if err != nil {
		return "", err
	}
	credentials, err := p.credentials(ctx, host.RPID)
	if err != nil {
		return "", err
	}
	w, err := host.verifier()
	if err != nil {
		return "", err
	}
	credential, err := w.ValidateLogin(operator{credentials: credentials}, *session, response)
	if err != nil {
		return "", refused(err)
	}
	if credential.Authenticator.CloneWarning {
		// The signature verified, so this is the person's key — the question is whether it is a copy.
		// A credential that cannot be synchronized has one counter and it must never go backwards. A
		// synchronized one has no counter that means anything across devices, so it is reported and
		// accepted rather than refusing a person who has just unlocked their phone.
		if !credential.Flags.BackupEligible {
			logging.For(logging.Auth).Errorf(
				"refused a passkey for %s whose signature counter went backwards (credential %s): a key that "+
					"cannot be synchronized must count up",
				host.RPID, EncodeCredentialID(credential.ID))
			return "", fmt.Errorf("%w: the passkey's signature counter went backwards", ErrCeremonyRefused)
		}
		logging.For(logging.Auth).Warnf(
			"synchronized passkey for %s reported a stale signature counter (credential %s)",
			host.RPID, EncodeCredentialID(credential.ID))
	}
	record, err := marshalCredential(credential)
	if err != nil {
		return "", err
	}
	id := EncodeCredentialID(credential.ID)
	if err := p.store.UsePasskey(ctx, id, record); err != nil {
		return "", fmt.Errorf("record passkey use: %w", err)
	}
	return id, nil
}

// credentials loads this host's credentials in the shape the verifier works in.
func (p *Passkeys) credentials(ctx context.Context, rpID string) ([]webauthn.Credential, error) {
	records, err := p.store.ListPasskeyRecords(ctx, rpID)
	if err != nil {
		return nil, err
	}
	credentials := make([]webauthn.Credential, 0, len(records))
	for _, rec := range records {
		var credential webauthn.Credential
		if err := json.Unmarshal(rec.Record, &credential); err != nil {
			return nil, fmt.Errorf("stored passkey %s is not a credential record: %w", rec.CredentialID, err)
		}
		credentials = append(credentials, credential)
	}
	return credentials, nil
}

// credentialDescriptors is the allow/exclude list form of a credential set.
func credentialDescriptors(credentials []webauthn.Credential) []protocol.CredentialDescriptor {
	descriptors := make([]protocol.CredentialDescriptor, 0, len(credentials))
	for _, c := range credentials {
		descriptors = append(descriptors, protocol.CredentialDescriptor{
			Type:         protocol.PublicKeyCredentialType,
			CredentialID: c.ID,
		})
	}
	return descriptors
}

// marshalCredential is the stored form of a credential: the verifier's own record, so a library that
// needs another field at verification time keeps it without this table changing.
func marshalCredential(credential *webauthn.Credential) ([]byte, error) {
	record, err := json.Marshal(credential)
	if err != nil {
		return nil, fmt.Errorf("encode passkey record: %w", err)
	}
	if len(record) > maxPasskeyRecordBytes {
		return nil, fmt.Errorf("passkey record is %d bytes, over the %d byte bound", len(record), maxPasskeyRecordBytes)
	}
	return record, nil
}

// refused is a ceremony the verifier said no to. The library's own words are kept out of the chain: they
// describe the response, and the response belongs to whoever sent it.
func refused(err error) error {
	return fmt.Errorf("%w: %v", ErrCeremonyRefused, err)
}

// EncodeCredentialID is the wire and column form of a credential id: base64url without padding, which is
// what the browser sends and what a path parameter can carry. One function owns it, so the id in a row,
// in a listing and in a DELETE path are the same string.
func EncodeCredentialID(id []byte) string {
	return base64.RawURLEncoding.EncodeToString(id)
}
