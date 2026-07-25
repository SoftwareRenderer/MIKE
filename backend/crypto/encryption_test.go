package crypto

import (
	"testing"
)

// setEncryptionKey sets a valid 32-byte hex key for testing and returns a cleanup function.
func setEncryptionKey(t *testing.T) {
	t.Helper()
	// 32 bytes = 64 hex chars
	t.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
}

func clearEncryptionKey(t *testing.T) {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", "")
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	setEncryptionKey(t)

	plaintext := "sk-test-api-key-12345"
	encrypted, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	if encrypted == plaintext {
		t.Fatal("encrypted value should differ from plaintext when key is set")
	}

	decrypted, err := Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptEmptyString(t *testing.T) {
	setEncryptionKey(t)

	result, err := Encrypt("")
	if err != nil {
		t.Fatalf("Encrypt(\"\") failed: %v", err)
	}
	if result != "" {
		t.Errorf("Encrypt(\"\") = %q, want \"\"", result)
	}
}

func TestDecryptEmptyString(t *testing.T) {
	setEncryptionKey(t)

	result, err := Decrypt("")
	if err != nil {
		t.Fatalf("Decrypt(\"\") failed: %v", err)
	}
	if result != "" {
		t.Errorf("Decrypt(\"\") = %q, want \"\"", result)
	}
}

func TestEncryptDecryptNoKey(t *testing.T) {
	clearEncryptionKey(t)

	plaintext := "sk-test-api-key-12345"
	_, err := Encrypt(plaintext)
	if err == nil {
		t.Fatal("Encrypt should fail without key")
	}

	_, err = Decrypt(plaintext)
	if err == nil {
		t.Fatal("Decrypt should fail without key")
	}
}

func TestIsEnabled(t *testing.T) {
	tests := []struct {
		name   string
		envVal string
		want   bool
	}{
		{"valid key", "some-secret-key", true},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", tt.envVal)
			if got := IsEnabled(); got != tt.want {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTamperedCiphertext(t *testing.T) {
	setEncryptionKey(t)

	plaintext := "sk-test-api-key-12345"
	encrypted, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Tamper with the ciphertext
	data := []byte(encrypted)
	if len(data) > 0 {
		data[0] ^= 0x01 // flip a bit
	}

	_, err = Decrypt(string(data))
	if err == nil {
		t.Fatal("Decrypt should fail on tampered ciphertext")
	}
}

func TestDecryptInvalidBase64(t *testing.T) {
	setEncryptionKey(t)

	_, err := Decrypt("not-valid-base64!!!")
	if err == nil {
		t.Fatal("Decrypt should fail on invalid base64")
	}
}

func TestValidateKey(t *testing.T) {
	tests := []struct {
		name    string
		envVal  string
		wantErr bool
	}{
		{"valid key", "some-secret-key", false},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", tt.envVal)
			err := ValidateKey()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateKey() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEncryptDifferentValuesProduceDifferentCiphertext(t *testing.T) {
	setEncryptionKey(t)

	// Encrypt the same value twice — GCM uses random nonces, so results should differ
	e1, err := Encrypt("same-value")
	if err != nil {
		t.Fatalf("Encrypt 1 failed: %v", err)
	}
	e2, err := Encrypt("same-value")
	if err != nil {
		t.Fatalf("Encrypt 2 failed: %v", err)
	}
	if e1 == e2 {
		t.Fatal("two encryptions of the same value should produce different ciphertext (random nonce)")
	}

	// But both should decrypt to the same value
	d1, _ := Decrypt(e1)
	d2, _ := Decrypt(e2)
	if d1 != d2 {
		t.Errorf("decrypted values differ: %q vs %q", d1, d2)
	}
}
