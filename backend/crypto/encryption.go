// Package crypto encrypts secrets kept in the database (provider API keys) with
// AES-256-GCM, keyed from MIKE_ENCRYPTION_KEY. Refusing to run without a key is the
// server's decision, not this package's.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

// Version prefix for the encryption format.
// Format: "v1:" + base64(salt + nonce + ciphertext + tag)
// Uses PBKDF2-HMAC-SHA256 (100k iterations) for key derivation with a random salt.
const v1Prefix = "v1:"

// PBKDF2 iteration count. Balances security against performance:
// 100,000 iterations takes ~50ms on typical hardware, which is acceptable
// for the frequency of decryption (once per task start, not per-token).
const pbkdf2Iterations = 100_000

// saltSize is the length of the random salt in bytes for PBKDF2.
const saltSize = 16

// Encrypt encrypts a plaintext value using AES-256-GCM.
// API keys are encrypted before being stored in the database.
//
// The format is: "v1:" + base64(salt + nonce + ciphertext + tag)
// Key derivation uses PBKDF2-HMAC-SHA256 with a random salt and 100k iterations.
func Encrypt(value string) (string, error) {
	if value == "" {
		return "", nil
	}

	salt, err := generateSalt()
	if err != nil {
		return "", fmt.Errorf("crypto: failed to generate salt: %w", err)
	}

	key := deriveKeyPBKDF2(salt)
	if key == nil {
		return "", fmt.Errorf("crypto: MIKE_ENCRYPTION_KEY environment variable is not set")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("crypto: failed to create cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypto: failed to create GCM: %w", err)
	}

	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("crypto: failed to generate nonce: %w", err)
	}

	// Seal appends the ciphertext + auth tag to the nonce
	encrypted := aesGCM.Seal(nonce, nonce, []byte(value), nil)

	// Format: "v1:" + base64(salt + encrypted)
	combined := append(salt, encrypted...)
	return v1Prefix + base64.StdEncoding.EncodeToString(combined), nil
}

// Decrypt decrypts an encrypted value produced by Encrypt.
// Supports the v1 (PBKDF2) format exclusively.
func Decrypt(encrypted string) (string, error) {
	if encrypted == "" {
		return "", nil
	}

	if !strings.HasPrefix(encrypted, v1Prefix) {
		return "", fmt.Errorf("crypto: unrecognized encryption format (expected %s prefix)", v1Prefix)
	}

	return decryptV1(encrypted[len(v1Prefix):])
}

// decryptV1 decrypts a v1-formatted value (PBKDF2-derived key with salt).
func decryptV1(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("crypto: failed to decode base64: %w", err)
	}

	if len(data) < saltSize {
		return "", fmt.Errorf("crypto: ciphertext too short")
	}

	salt := data[:saltSize]
	encrypted := data[saltSize:]

	derivedKey := deriveKeyPBKDF2(salt)

	block, err := aes.NewCipher(derivedKey)
	if err != nil {
		return "", fmt.Errorf("crypto: failed to create cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypto: failed to create GCM: %w", err)
	}

	nonceSize := aesGCM.NonceSize()
	if len(encrypted) < nonceSize {
		return "", fmt.Errorf("crypto: ciphertext too short")
	}

	nonce, ciphertext := encrypted[:nonceSize], encrypted[nonceSize:]
	decrypted, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("crypto: failed to decrypt: %w", err)
	}

	return string(decrypted), nil
}

// IsEnabled returns true if the MIKE_ENCRYPTION_KEY environment variable is set.
func IsEnabled() bool {
	return os.Getenv("MIKE_ENCRYPTION_KEY") != ""
}

// deriveKeyPBKDF2 derives a 32-byte AES key from the MIKE_ENCRYPTION_KEY env var
// using PBKDF2-HMAC-SHA256 with the given salt.
// Returns nil if the env var is not set.
func deriveKeyPBKDF2(salt []byte) []byte {
	raw := os.Getenv("MIKE_ENCRYPTION_KEY")
	if raw == "" {
		return nil
	}
	return pbkdf2.Key([]byte(raw), salt, pbkdf2Iterations, 32, sha256.New)
}

// generateSalt generates a cryptographically random salt.
func generateSalt() ([]byte, error) {
	salt := make([]byte, saltSize)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	return salt, nil
}
