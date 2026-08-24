package tls

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWithCert_DefaultAutoGenerate(t *testing.T) {
	dataDir := t.TempDir()
	cfg, err := LoadWithCert(dataDir, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	expectedCert := filepath.Join(dataDir, "tls", "kanban.crt")
	if cfg.CertFile != expectedCert {
		t.Errorf("expected cert file %s, got %s", expectedCert, cfg.CertFile)
	}
}

func TestLoadWithCert_UserProvided(t *testing.T) {
	// Create temp cert and key files
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	// Write dummy content (not valid certs, but LoadWithCert just checks existence)
	if err := os.WriteFile(certPath, []byte("dummy-cert"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("dummy-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadWithCert(t.TempDir(), certPath, keyPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.CertFile != certPath {
		t.Errorf("expected cert file %s, got %s", certPath, cfg.CertFile)
	}
	if cfg.KeyFile != keyPath {
		t.Errorf("expected key file %s, got %s", keyPath, cfg.KeyFile)
	}
}

func TestLoadWithCert_UserProvided_MissingCert(t *testing.T) {
	_, err := LoadWithCert(t.TempDir(), "/nonexistent/cert.pem", "/nonexistent/key.pem")
	if err == nil {
		t.Fatal("expected error for missing cert file")
	}
}

func TestLoadWithCert_UserProvided_MissingKey(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(certPath, []byte("dummy-cert"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadWithCert(t.TempDir(), certPath, "/nonexistent/key.pem")
	if err == nil {
		t.Fatal("expected error for missing key file")
	}
}

func TestLoadWithCert_AutoGenerate_ReusesExisting(t *testing.T) {
	dataDir := t.TempDir()

	// First call — generates certs
	cfg1, err := LoadWithCert(dataDir, "", "")
	if err != nil {
		t.Fatalf("first load: %v", err)
	}

	// Record mtime of generated cert
	info1, err := os.Stat(cfg1.CertFile)
	if err != nil {
		t.Fatalf("failed to stat cert: %v", err)
	}

	// Second call — should reuse existing
	cfg2, err := LoadWithCert(dataDir, "", "")
	if err != nil {
		t.Fatalf("second load: %v", err)
	}

	info2, err := os.Stat(cfg2.CertFile)
	if err != nil {
		t.Fatalf("failed to stat cert after reuse: %v", err)
	}

	// Mtime should be the same (not regenerated)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Error("cert was regenerated on second load")
	}
}

func TestLoadWithCert_UserProvided_TakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	if err := os.WriteFile(certPath, []byte("dummy-cert"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("dummy-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	// User-provided paths take precedence over auto-gen
	cfg, err := LoadWithCert(dir, certPath, keyPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.CertFile != certPath {
		t.Errorf("expected user-provided cert path, got %s", cfg.CertFile)
	}
}

func TestLoadDisabled(t *testing.T) {
	cfg := LoadDisabled()
	if cfg != nil {
		t.Fatalf("expected nil config, got %+v", cfg)
	}
}

func TestGenerateSelfSignedCert(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	err := generateSelfSignedCert(certPath, keyPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify files exist and have content
	certData, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("failed to read cert: %v", err)
	}
	if len(certData) == 0 {
		t.Error("cert file is empty")
	}

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("failed to read key: %v", err)
	}
	if len(keyData) == 0 {
		t.Error("key file is empty")
	}

	// Verify key file permissions (should be 0600)
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("failed to stat key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected key file permissions 0600, got %o", info.Mode().Perm())
	}
}
