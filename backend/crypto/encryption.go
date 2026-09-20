// Package crypto encrypts the secrets kept in the database — provider API keys and the config
// values an extension manifest marks sensitive — with AES-256-GCM.
//
// The stored form is bare base64 of `nonce ‖ ciphertext ‖ tag` and names no format, so opening a value is
// the only test of what it is: ErrNotRecord is a plain value, ErrNotAuthentic a secret this build cannot
// read. The GCM additional data binds a record to what it is and whose it is.
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

// Encrypt seals value as the secret of type purpose belonging to record — "provider" and a provider id,
// "extension-config" and an extension id plus its config key. A value lifted into another row, or another
// kind of secret, will not open.
func (b *Box) Encrypt(purpose, record, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return "", fmt.Errorf("crypto: %w", err)
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypto: %w", err)
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
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return "", fmt.Errorf("crypto: %w", err)
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypto: %w", err)
	}
	// A record is a nonce and a tag at minimum; anything shorter was never one.
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

// Record joins the parts that name one secret into the record field. The separator lives here so no
// caller can spell two records that open each other.
func Record(parts ...string) string { return strings.Join(parts, fieldSep) }
