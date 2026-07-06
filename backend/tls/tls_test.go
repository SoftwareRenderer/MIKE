package tls

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_DefaultEnabled(t *testing.T) {
	// TLS is enabled by default — should auto-generate certs
	_clearTLS()
	defer _restoreTLS()

	dataDir := t.TempDir()
	cfg, err := Load(dataDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config by default (TLS enabled)")
	}

	expectedCert := filepath.Join(dataDir, "tls", "kanban.crt")
	if cfg.CertFile != expectedCert {
		t.Errorf("expected cert file %s, got %s", expectedCert, cfg.CertFile)
	}
}

func TestLoad_ExplicitlyDisabled(t *testing.T) {
	_clearTLS()
	defer _restoreTLS()

	os.Setenv("KANBAN_TLS", "0")

	cfg, err := Load("/tmp/test-data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config when KANBAN_TLS=0, got %+v", cfg)
	}
}

func TestLoad_UserProvided(t *testing.T) {
	_clearTLS()
	defer _restoreTLS()

	// Create temp cert and key files
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	// Write dummy content (not valid certs, but Load just checks existence)
	if err := os.WriteFile(certPath, []byte("dummy-cert"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("dummy-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	os.Setenv("KANBAN_TLS_CERT", certPath)
	os.Setenv("KANBAN_TLS_KEY", keyPath)

	cfg, err := Load("/tmp/test-data")
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

func TestLoad_UserProvided_MissingCert(t *testing.T) {
	_clearTLS()
	defer _restoreTLS()

	os.Setenv("KANBAN_TLS_CERT", "/nonexistent/cert.pem")
	os.Setenv("KANBAN_TLS_KEY", "/nonexistent/key.pem")

	_, err := Load("/tmp/test-data")
	if err == nil {
		t.Fatal("expected error for missing cert file")
	}
}

func TestLoad_UserProvided_MissingKey(t *testing.T) {
	_clearTLS()
	defer _restoreTLS()

	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(certPath, []byte("dummy-cert"), 0o644); err != nil {
		t.Fatal(err)
	}

	os.Setenv("KANBAN_TLS_CERT", certPath)
	os.Setenv("KANBAN_TLS_KEY", "/nonexistent/key.pem")

	_, err := Load("/tmp/test-data")
	if err == nil {
		t.Fatal("expected error for missing key file")
	}
}

func TestLoad_AutoGenerate_Default(t *testing.T) {
	// Auto-generate is the default behavior (no env var needed)
	_clearTLS()
	defer _restoreTLS()

	dataDir := t.TempDir()
	cfg, err := Load(dataDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config (TLS enabled by default)")
	}

	expectedCert := filepath.Join(dataDir, "tls", "kanban.crt")
	expectedKey := filepath.Join(dataDir, "tls", "kanban.key")
	if cfg.CertFile != expectedCert {
		t.Errorf("expected cert file %s, got %s", expectedCert, cfg.CertFile)
	}
	if cfg.KeyFile != expectedKey {
		t.Errorf("expected key file %s, got %s", expectedKey, cfg.KeyFile)
	}

	// Verify files were created
	if _, err := os.Stat(cfg.CertFile); os.IsNotExist(err) {
		t.Error("cert file was not created")
	}
	if _, err := os.Stat(cfg.KeyFile); os.IsNotExist(err) {
		t.Error("key file was not created")
	}
}

func TestLoad_AutoGenerate_ReusesExisting(t *testing.T) {
	_clearTLS()
	defer _restoreTLS()

	dataDir := t.TempDir()

	// First call — generates certs
	cfg1, err := Load(dataDir)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}

	// Record mtime of generated cert
	info1, err := os.Stat(cfg1.CertFile)
	if err != nil {
		t.Fatalf("failed to stat cert: %v", err)
	}

	// Second call — should reuse existing
	cfg2, err := Load(dataDir)
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

func TestLoad_UserProvided_TakesPrecedence(t *testing.T) {
	_clearTLS()
	defer _restoreTLS()

	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	if err := os.WriteFile(certPath, []byte("dummy-cert"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("dummy-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	// User-provided certs take precedence over auto-gen default
	os.Setenv("KANBAN_TLS_CERT", certPath)
	os.Setenv("KANBAN_TLS_KEY", keyPath)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.CertFile != certPath {
		t.Errorf("expected user-provided cert path, got %s", cfg.CertFile)
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

// -- helpers to save/restore TLS env vars --

var _savedTLS = make(map[string]string)

func _clearTLS() {
	for _, env := range []string{"KANBAN_TLS", "KANBAN_TLS_CERT", "KANBAN_TLS_KEY"} {
		if v := os.Getenv(env); v != "" {
			_savedTLS[env] = v
		}
		os.Unsetenv(env)
	}
}

func _restoreTLS() {
	for _, env := range []string{"KANBAN_TLS", "KANBAN_TLS_CERT", "KANBAN_TLS_KEY"} {
		os.Unsetenv(env)
		if v, ok := _savedTLS[env]; ok {
			os.Setenv(env, v)
		}
	}
	_savedTLS = make(map[string]string)
}
