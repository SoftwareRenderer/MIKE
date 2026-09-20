package crypto

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// The KDF is expensive on purpose, so one box is shared; a test that needs its own says why.
func testBox(t *testing.T) *Box {
	t.Helper()
	box, err := New("test-passphrase", []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return box
}

const (
	purpose = "provider"
	record  = "7"
)

func TestRoundTrip(t *testing.T) {
	box := testBox(t)
	sealed, err := box.Encrypt(purpose, record, "sk-secret-value")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := base64.StdEncoding.DecodeString(sealed); err != nil {
		t.Errorf("stored form is not bare base64 — no marker, no prefix: %q (%v)", sealed, err)
	}
	if strings.Contains(sealed, "sk-secret") {
		t.Error("the plaintext appears in the stored form")
	}
	if !box.Opens(purpose, record, sealed) {
		t.Error("Opens disagrees with Decrypt")
	}
	got, err := box.Decrypt(purpose, record, sealed)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != "sk-secret-value" {
		t.Errorf("Decrypt = %q, want the original", got)
	}
}

func TestTwoSealsOfOneValueDiffer(t *testing.T) {
	// A random nonce per value is what stops two identical secrets producing an identical row.
	box := testBox(t)
	a, _ := box.Encrypt(purpose, record, "same")
	b, _ := box.Encrypt(purpose, record, "same")
	if a == b {
		t.Error("two seals of one value are byte-identical: no per-value nonce")
	}
}

func TestEmptyValueIsNoSecret(t *testing.T) {
	box := testBox(t)
	sealed, err := box.Encrypt(purpose, record, "")
	if err != nil || sealed != "" {
		t.Errorf("Encrypt(\"\") = %q, %v; want empty, no error", sealed, err)
	}
	got, err := box.Decrypt(purpose, record, "")
	if err != nil || got != "" {
		t.Errorf("Decrypt(\"\") = %q, %v; want empty, no error", got, err)
	}
}

func TestRecordIdentityIsBoundIn(t *testing.T) {
	box := testBox(t)
	sealed, err := box.Encrypt(purpose, record, "sk-secret-value")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	for _, tc := range []struct{ name, purpose, record string }{
		{"another provider", purpose, "8"},
		{"another kind of secret", "extension-config", record},
	} {
		if _, err := box.Decrypt(tc.purpose, tc.record, sealed); !errors.Is(err, ErrNotAuthentic) {
			t.Errorf("%s: Decrypt err = %v, want ErrNotAuthentic", tc.name, err)
		}
	}
}

// The separator is the only thing telling these two spellings of one identity apart.
func TestIdentityFieldsDoNotCollide(t *testing.T) {
	box := testBox(t)
	sealed, err := box.Encrypt("ab", "c", "x")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := box.Decrypt("a", "bc", sealed); !errors.Is(err, ErrNotAuthentic) {
		t.Errorf(`("a","bc") opened a record sealed for ("ab","c"): err = %v`, err)
	}
}

func TestTamperedRecordRefuses(t *testing.T) {
	box := testBox(t)
	sealed, _ := box.Encrypt(purpose, record, "sk-secret-value")
	corrupted := sealed[:len(sealed)-2] + "AA"
	if _, err := box.Decrypt(purpose, record, corrupted); !errors.Is(err, ErrNotAuthentic) {
		t.Errorf("tampered ciphertext: err = %v, want ErrNotAuthentic", err)
	}
}

func TestNotARecord(t *testing.T) {
	box := testBox(t)
	// Nothing names the format, so shape decides: outside base64, or shorter than a nonce and a tag,
	// was never a record.
	for _, v := range []string{
		"sk-plain-old-key", "not base64 at all!", "AAA=", base64.StdEncoding.EncodeToString([]byte("short")),
	} {
		if _, err := box.Decrypt(purpose, record, v); !errors.Is(err, ErrNotRecord) {
			t.Errorf("Decrypt(%q) err = %v, want ErrNotRecord", v, err)
		}
	}
	if box.Opens(purpose, record, "sk-plain-old-key") {
		t.Error("Opens answered true for plain text")
	}
	sealed, _ := box.Encrypt(purpose, record, "sk-secret-value")
	if box.Opens(purpose, "8", sealed) || box.Opens("extension-config", record, sealed) {
		t.Error("Opens answered true for a record sealed under another identity")
	}
}

// A plain secret long enough to look like a record is one that does not authenticate: it answers as an
// unreadable secret, never as itself.
func TestPlainValueThatLooksLikeARecordRefuses(t *testing.T) {
	box := testBox(t)
	looksLikeOne := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 40)))
	if _, err := box.Decrypt(purpose, record, looksLikeOne); !errors.Is(err, ErrNotAuthentic) {
		t.Errorf("Decrypt err = %v, want ErrNotAuthentic", err)
	}
}

func TestAnotherSaltIsAnotherKey(t *testing.T) {
	a, err := New("same-passphrase", []byte("salt-one-abcdefg"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	b, err := New("same-passphrase", []byte("salt-two-abcdefg"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sealed, _ := a.Encrypt(purpose, record, "x")
	if _, err := b.Decrypt(purpose, record, sealed); !errors.Is(err, ErrNotAuthentic) {
		t.Errorf("a different installation salt opened the record: err = %v", err)
	}
}

// One derivation per process is what the Box exists to buy.
func TestDerivationHappensOncePerBox(t *testing.T) {
	before := Derivations()
	box := testBox(t)
	for i := 0; i < 200; i++ {
		sealed, err := box.Encrypt(purpose, record, "sk-secret-value")
		if err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
		if _, err := box.Decrypt(purpose, record, sealed); err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
	}
	if got := Derivations() - before; got != 1 {
		t.Errorf("%d derivations for one New and 200 seals, want exactly 1", got)
	}
}

func TestNewRefusesUselessInput(t *testing.T) {
	if _, err := New("", []byte("0123456789abcdef")); err == nil {
		t.Error("an empty passphrase derived a key")
	}
	if _, err := New("passphrase", []byte("short")); err == nil {
		t.Error("a short salt derived a key")
	}
}
