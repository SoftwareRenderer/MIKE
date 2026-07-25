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
		return "", fmt.Errorf("crypto: encryption key not configured")
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
		return "", fmt.Errorf("crypto: encryption key not configured")
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

// getEncryptionKey reads the ENCRYPTION_KEY env var and returns a 32-byte key.
// It uses SHA-256 to derive a consistent 32-byte key from any non-empty input string.
// Returns nil if the env var is not set.
func getEncryptionKey() []byte {
	raw := os.Getenv("ENCRYPTION_KEY")
	if raw == "" {
		return nil
	}
	hash := sha256.Sum256([]byte(raw))
	return hash[:]
}

// ValidateKey checks if the ENCRYPTION_KEY env var is set.
// Returns an error if the key is empty, or nil if valid.
func ValidateKey() error {
	raw := os.Getenv("ENCRYPTION_KEY")
	if raw == "" {
		return fmt.Errorf("ENCRYPTION_KEY environment variable is not set")
	}
	return nil
}
