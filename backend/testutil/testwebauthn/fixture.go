// Package testwebauthn is the browser and the authenticator for a test that needs a WebAuthn ceremony and
// has neither. It mints a real ES256 key pair and produces the two response bodies the ceremonies consume:
// a registration self-attested with "none", and an assertion signed over authenticator data and the hash of
// the client data. So what a verifier under test checks is a genuine signature over genuine client data,
// not values written down by hand — which is the only way a wrong rpID, a stale challenge or a missing
// user-verification bit is caught by the test rather than by a person's browser.
package testwebauthn

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
)

// Credential is one authenticator's resident key: a credential id, the private key that signs with it,
// and the COSE key that registration stores.
type Credential struct {
	id      []byte
	key     *ecdsa.PrivateKey
	coseKey []byte
}

// NewCredential generates a fresh credential. The id is random because the server treats it as opaque.
func NewCredential() (*Credential, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate credential key: %w", err)
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, fmt.Errorf("generate credential id: %w", err)
	}
	// The uncompressed SEC1 point (0x04 || X || Y), which is what a COSE EC2 key stores as -2 and -3.
	point, err := key.PublicKey.Bytes()
	if err != nil {
		return nil, fmt.Errorf("encode public key: %w", err)
	}
	// COSE EC2 public key: kty=EC2(2), alg=ES256(-7), crv=P-256(1), then the coordinates. The pairs are
	// encoded in the order the specification lists them rather than through a map, because a map's
	// encoded key order is not the insertion order and a verifier may dispatch on the first key.
	coseKey := []byte{0xa5} // a map of five pairs
	for _, pair := range []any{
		int64(1), int64(2), int64(3), int64(-7), int64(-1), int64(1),
		int64(-2), point[1:33], int64(-3), point[33:],
	} {
		encoded, err := cbor.Marshal(pair)
		if err != nil {
			return nil, fmt.Errorf("encode COSE key: %w", err)
		}
		coseKey = append(coseKey, encoded...)
	}
	return &Credential{id: id, key: key, coseKey: coseKey}, nil
}

// ID is the credential id the ceremony registered under.
func (c *Credential) ID() []byte { return c.id }

// RegistrationBody is the JSON a browser posts to finish registering this credential for rpID at origin,
// answering the challenge the server issued. flags is what the authenticator asserts about itself.
func (c *Credential) RegistrationBody(rpID, origin, challenge string, flags protocol.AuthenticatorFlags) ([]byte, error) {
	authData, err := c.attestedAuthData(rpID, flags)
	if err != nil {
		return nil, err
	}
	clientData, err := clientData("webauthn.create", challenge, origin)
	if err != nil {
		return nil, err
	}
	// fmt "none": the same answer a platform authenticator gives when the Relying Party asked for no
	// attestation, which is what MIKE asks for.
	attestationObject, err := cbor.Marshal(map[string]any{
		"authData": authData,
		"fmt":      "none",
		"attStmt":  map[string]any{},
	})
	if err != nil {
		return nil, fmt.Errorf("encode attestation object: %w", err)
	}
	return json.Marshal(map[string]any{
		"id":    encode(c.id),
		"rawId": encode(c.id),
		"type":  "public-key",
		"response": map[string]string{
			"clientDataJSON":    encode(clientData),
			"attestationObject": encode(attestationObject),
		},
	})
}

// AssertionBody is the JSON a browser posts to sign in with this credential: the counter the
// authenticator reports, and the user handle it claims.
func (c *Credential) AssertionBody(rpID, origin, challenge string, counter uint32, userHandle []byte, flags protocol.AuthenticatorFlags) ([]byte, error) {
	authData := c.authData(rpID, flags, counter)
	clientData, err := clientData("webauthn.get", challenge, origin)
	if err != nil {
		return nil, err
	}
	// What is signed is the authenticator data followed by the hash of the client data, so a signature
	// cannot be moved between a challenge, a host or a counter value.
	clientDataHash := sha256.Sum256(clientData)
	signed := append(append([]byte{}, authData...), clientDataHash[:]...)
	digest := sha256.Sum256(signed)
	der, err := ecdsa.SignASN1(rand.Reader, c.key, digest[:])
	if err != nil {
		return nil, fmt.Errorf("sign assertion: %w", err)
	}
	body := map[string]any{
		"id":    encode(c.id),
		"rawId": encode(c.id),
		"type":  "public-key",
		"response": map[string]string{
			"clientDataJSON":    encode(clientData),
			"authenticatorData": encode(authData),
			"signature":         encode(der),
		},
	}
	if len(userHandle) > 0 {
		body["response"].(map[string]string)["userHandle"] = encode(userHandle)
	}
	return json.Marshal(body)
}

// attestedAuthData is the registration form of authenticator data: it carries the new credential's id and
// public key after the attested-data header.
func (c *Credential) attestedAuthData(rpID string, flags protocol.AuthenticatorFlags) ([]byte, error) {
	data := c.authData(rpID, flags, 0)
	data = append(data, make([]byte, 16)...) // AAGUID: all zeros, no attestation identity claimed
	data = append(data, byte(len(c.id)>>8), byte(len(c.id)))
	data = append(data, c.id...)
	return append(data, c.coseKey...), nil
}

// authData is the header every ceremony carries: the relying-party hash, the flag bit byte and the
// counter.
func (c *Credential) authData(rpID string, flags protocol.AuthenticatorFlags, counter uint32) []byte {
	hash := sha256.Sum256([]byte(rpID))
	data := make([]byte, 0, 37)
	data = append(data, hash[:]...)
	data = append(data, byte(flags))
	data = append(data, byte(counter>>24), byte(counter>>16), byte(counter>>8), byte(counter))
	return data
}

// clientData is what the browser said it was doing. The challenge is echoed exactly as the server issued
// it, which is why a response cannot be replayed against a different ceremony.
func clientData(kind, challenge, origin string) ([]byte, error) {
	return json.Marshal(struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}{Type: kind, Challenge: challenge, Origin: origin})
}

// encode is the wire form every byte field in a ceremony response travels in: base64url, unpadded.
func encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
