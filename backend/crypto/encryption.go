// Package crypto encrypts provider API keys and sensitive extension config
// with AES-256-GCM.
//
// Stored values are base64(nonce ‖ ciphertext ‖ tag). Opening a value is
// the only test of its format: ErrNotRecord for plain values, ErrNotAuthentic
// for secrets this build cannot read. Additional data binds a record
// to its purpose and owner.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	"golang.org/x/crypto/argon2"
)

const (
	// SaltSize is the length of the per-installation KDF salt, generated once and stored in the
	// database so the derivation is repeatable across restarts.
	SaltSize = 16

	// The KDF's cost, paid once per process. It is memory-hard because the input is a passphrase an
	// operator may well have typed short.
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB, so 64 MiB
	argonThreads = 4
	keyLen       = 32

	// fieldSep separates the additional-data fields, so two spellings of the same string cannot name
	// the same record.
	fieldSep = "\x00"
)

var (
	// ErrNotRecord reports a stored value that cannot be one of this package's records at all — plain
	// text, or a row this build does not read.
	ErrNotRecord = errors.New("crypto: stored value is not an encrypted record")
	// ErrNotAuthentic reports a value shaped like a record that did not authenticate: the passphrase
	// changed, the bytes were tampered with, or the value moved from another record.
	ErrNotAuthentic = errors.New("crypto: stored value does not authenticate")

	// derivations is how the one-derivation-per-process test counts without timing anything.
	derivations atomic.Int64
)

// Box holds the derived data key. It is immutable, so it is safe to share.
type Box struct {
	key []byte
}

// New derives a Box's data key from the passphrase and the installation salt.
func New(passphrase string, salt []byte) (*Box, error) {
	if passphrase == "" {
		return nil, errors.New("crypto: no passphrase to derive from")
	}
	if len(salt) < SaltSize {
		return nil, fmt.Errorf("crypto: salt must be at least %d bytes, got %d", SaltSize, len(salt))
	}
	derivations.Add(1)
	key := argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, keyLen)
	return &Box{key: key}, nil
}

// Derivations reports how many data keys this process has derived.
func Derivations() int64 { return derivations.Load() }

// Opens reports whether value is a record for exactly this purpose and record.
func (b *Box) Opens(purpose, record, value string) bool {
	_, err := b.Decrypt(purpose, record, value)
	return err == nil
}

// gcm returns the AEAD for this box's key.
func (b *Box) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, fmt.Errorf("crypto: %w", err)
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: %w", err)
	}
	return aesGCM, nil
}

// Encrypt seals value as a secret of type purpose belonging to record.
// Examples: ("provider", providerID) or ("extension-config", extensionID+key).
// A value moved to another record or purpose will not open.
func (b *Box) Encrypt(purpose, record, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	aesGCM, err := b.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("crypto: %w", err)
	}
	combined := aesGCM.Seal(nonce, nonce, []byte(value), aad(purpose, record))
	return base64.StdEncoding.EncodeToString(combined), nil
}

// Decrypt opens a record produced by Encrypt for the same purpose and record. An empty value is an
// empty secret, not a failure; anything else must be a record and must authenticate.
func (b *Box) Decrypt(purpose, record, sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	data, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", ErrNotRecord
	}
	aesGCM, err := b.gcm()
	if err != nil {
		return "", err
	}
	// nonce and tag are required; anything shorter is not a record.
	if len(data) < aesGCM.NonceSize()+aesGCM.Overhead() {
		return "", ErrNotRecord
	}
	nonce, ciphertext := data[:aesGCM.NonceSize()], data[aesGCM.NonceSize():]
	plain, err := aesGCM.Open(nil, nonce, ciphertext, aad(purpose, record))
	if err != nil {
		return "", ErrNotAuthentic
	}
	return string(plain), nil
}

func aad(purpose, record string) []byte {
	return []byte(purpose + fieldSep + record)
}

// Record joins parts into a single record string. The separator is kept here to prevent collisions.
func Record(parts ...string) string { return strings.Join(parts, fieldSep) }
