package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/base64"
	"fmt"
	"io"
	"os"
)

// Encrypt encrypts a plaintext value using AES-256-GCM.
// The key is read from the ENCRYPTION_KEY environment variable (hex-encoded 32-byte key).
// If ENCRYPTION_KEY is not set, the value is returned as-is (plain text fallback).
// Returns a base64-encoded string of the encrypted data (nonce + ciphertext + auth tag).
func Encrypt(value string) (string, error) {
	if value == "" {
		return "", nil
	}

	key := getEncryptionKey()
	if key == nil {
		// No encryption key configured — store as plain text
		return value, nil
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
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

// Decrypt decrypts a base64-encoded AES-256-GCM ciphertext.
// The key is read from the ENCRYPTION_KEY environment variable.
// If ENCRYPTION_KEY is not set, the value is returned as-is (plain text fallback).
func Decrypt(encrypted string) (string, error) {
	if encrypted == "" {
		return "", nil
	}

	key := getEncryptionKey()
	if key == nil {
		// No encryption key configured — return as plain text
		return encrypted, nil
	}

	data, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return "", fmt.Errorf("crypto: failed to decode base64: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("crypto: failed to create cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypto: failed to create GCM: %w", err)
	}

	nonceSize := aesGCM.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("crypto: ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	decrypted, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("crypto: failed to decrypt: %w", err)
	}

	return string(decrypted), nil
}

// IsEnabled returns true if the ENCRYPTION_KEY environment variable is set and valid.
func IsEnabled() bool {
	return getEncryptionKey() != nil
}

// getEncryptionKey reads the ENCRYPTION_KEY env var and returns the raw bytes.
// Returns nil if the env var is not set or has an invalid format.
func getEncryptionKey() []byte {
	raw := os.Getenv("ENCRYPTION_KEY")
	if raw == "" {
		return nil
	}
	key, err := hex.DecodeString(raw)
	if err != nil || len(key) != 32 {
		// Invalid key — log a warning but don't fail
		// (handled at the config save level)
		return nil
	}
	return key
}

// ValidateKey checks if the ENCRYPTION_KEY env var is set and has the correct format.
// Returns an error describing the problem, or nil if valid.
func ValidateKey() error {
	raw := os.Getenv("ENCRYPTION_KEY")
	if raw == "" {
		return fmt.Errorf("ENCRYPTION_KEY environment variable is not set")
	}
	key, err := hex.DecodeString(raw)
	if err != nil {
		return fmt.Errorf("ENCRYPTION_KEY is not valid hex: %w", err)
	}
	if len(key) != 32 {
		return fmt.Errorf("ENCRYPTION_KEY must be 32 bytes (hex-encoded 256-bit key), got %d bytes", len(key))
	}
	return nil
}
