package tls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"mike/pkg/logging"
)

// --- helpers ---------------------------------------------------------------

func mustParseCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	cert, err := parseCertificatePEM(pemBytes)
	if err != nil {
		t.Fatalf("failed to parse %s: %v", path, err)
	}
	return cert
}

// leafSANsMatch is the file-based entry point to sanNamesMatch for tests that
// drive the re-issue decision from a certificate on disk. Production only ever
// compares a leaf it has already loaded, so this wrapper — which reports false
// for a missing or unparseable certificate instead of failing the test — lives
// here rather than in tls.go.
func leafSANsMatch(certPath string, dnsNames []string, ips []net.IP) bool {
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		return false
	}
	leaf, err := parseCertificatePEM(pemBytes)
	if err != nil {
		return false
	}
	return sanNamesMatch(leaf, dnsNames, ips)
}

func hasString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func hasIP(xs []net.IP, want string) bool {
	for _, x := range xs {
		if x.String() == want {
			return true
		}
	}
	return false
}

// --- LoadWithCert ----------------------------------------------------------

func TestLoadWithCert_DefaultAutoGenerate(t *testing.T) {
	dataDir := t.TempDir()
	cfg, err := LoadWithCert(dataDir, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	expectedCert := filepath.Join(dataDir, "tls", "mike.crt")
	if cfg.CertFile != expectedCert {
		t.Errorf("expected cert file %s, got %s", expectedCert, cfg.CertFile)
	}
	expectedKey := filepath.Join(dataDir, "tls", "mike.key")
	if cfg.KeyFile != expectedKey {
		t.Errorf("expected key file %s, got %s", expectedKey, cfg.KeyFile)
	}
	expectedCA := filepath.Join(dataDir, "tls", "mike-ca.crt")
	if cfg.CAFile != expectedCA {
		t.Errorf("expected CA file %s, got %s", expectedCA, cfg.CAFile)
	}

	for _, path := range []string{cfg.CertFile, cfg.KeyFile, cfg.CAFile, filepath.Join(dataDir, "tls", "mike-ca.key")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s to exist: %v", path, err)
		}
	}
}

func TestLoadWithCert_UserProvided(t *testing.T) {
	dir := t.TempDir()
	// A real pair: LoadWithCert has to be able to serve what it accepts.
	auto, err := LoadWithCert(t.TempDir(), "", "")
	if err != nil {
		t.Fatalf("generate a pair to serve: %v", err)
	}
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	for _, pair := range [][2]string{{auto.CertFile, certPath}, {auto.KeyFile, keyPath}} {
		content, err := os.ReadFile(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pair[1], content, 0o600); err != nil {
			t.Fatal(err)
		}
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
	// A user-supplied certificate has no CA of ours attached.
	if cfg.CAFile != "" {
		t.Errorf("expected empty CAFile for user-provided cert, got %s", cfg.CAFile)
	}
}

// A certificate and key that were never issued together pass os.Stat and only fail mid-handshake.
func TestLoadWithCert_UserProvided_MismatchedPair(t *testing.T) {
	one, err := LoadWithCert(t.TempDir(), "", "")
	if err != nil {
		t.Fatalf("first pair: %v", err)
	}
	two, err := LoadWithCert(t.TempDir(), "", "")
	if err != nil {
		t.Fatalf("second pair: %v", err)
	}

	_, err = LoadWithCert(t.TempDir(), one.CertFile, two.KeyFile)
	if err == nil {
		t.Fatal("expected an error for a certificate and key that do not belong together")
	}
	for _, want := range []string{"--tls-cert", "--tls-key", EnvTLSCert, EnvTLSKey} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name the spelling the operator set (%s), got: %v", want, err)
		}
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

	cfg1, err := LoadWithCert(dataDir, "", "")
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	info1, err := os.Stat(cfg1.CertFile)
	if err != nil {
		t.Fatalf("failed to stat cert: %v", err)
	}

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

func TestLoadWithCert_ReusesCAAndLeafAcrossCalls(t *testing.T) {
	dataDir := t.TempDir()

	if _, err := LoadWithCert(dataDir, "", ""); err != nil {
		t.Fatalf("first load: %v", err)
	}
	caBefore := mustParseCert(t, filepath.Join(dataDir, "tls", caCertName))
	leafBefore := mustParseCert(t, filepath.Join(dataDir, "tls", leafCertName))

	if _, err := LoadWithCert(dataDir, "", ""); err != nil {
		t.Fatalf("second load: %v", err)
	}
	caAfter := mustParseCert(t, filepath.Join(dataDir, "tls", caCertName))
	leafAfter := mustParseCert(t, filepath.Join(dataDir, "tls", leafCertName))

	if caBefore.SerialNumber.Cmp(caAfter.SerialNumber) != 0 {
		t.Error("CA was replaced on second load — devices that imported it would stop trusting the server")
	}
	if leafBefore.SerialNumber.Cmp(leafAfter.SerialNumber) != 0 {
		t.Error("leaf was re-issued on second load although the host names did not change")
	}
}

func TestLoadWithCert_RegeneratesLeafWhenNamesChange(t *testing.T) {
	dataDir := t.TempDir()

	if _, err := LoadWithCert(dataDir, "", ""); err != nil {
		t.Fatalf("first load: %v", err)
	}

	tlsDir := filepath.Join(dataDir, "tls")
	ca, err := ensureCA(tlsDir)
	if err != nil {
		t.Fatalf("ensureCA: %v", err)
	}
	caSerial := ca.cert.SerialNumber

	// Simulate a leaf issued for an outdated name set (e.g. the host lost its LAN IP).
	certPath := filepath.Join(tlsDir, leafCertName)
	keyPath := filepath.Join(tlsDir, leafKeyName)
	if err := generateLeaf(certPath, keyPath, ca, []string{"stale-host"}, nil); err != nil {
		t.Fatalf("generateLeaf: %v", err)
	}
	leafBefore := mustParseCert(t, certPath)

	cfg, err := LoadWithCert(dataDir, "", "")
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if cfg.CertFile != certPath {
		t.Errorf("unexpected cert path %s", cfg.CertFile)
	}

	leafAfter := mustParseCert(t, certPath)
	if leafAfter.SerialNumber.Cmp(leafBefore.SerialNumber) == 0 {
		t.Fatal("leaf was not re-issued after its names changed")
	}
	if hasString(leafAfter.DNSNames, "stale-host") {
		t.Error("re-issued leaf still carries the stale name")
	}
	if !hasString(leafAfter.DNSNames, "localhost") {
		t.Errorf("re-issued leaf is missing localhost; got %v", leafAfter.DNSNames)
	}

	// The CA must survive the re-issue untouched.
	caAfter := mustParseCert(t, filepath.Join(tlsDir, caCertName))
	if caAfter.SerialNumber.Cmp(caSerial) != 0 {
		t.Error("CA was replaced while re-issuing the leaf")
	}
	if err := leafAfter.CheckSignatureFrom(caAfter); err != nil {
		t.Errorf("re-issued leaf is not signed by the CA: %v", err)
	}
}

// signTestCA writes a self-signed CA pair with the given dates, which is how an expired root reaches
// the loader.
func signTestCA(t *testing.T, tlsDir string, notBefore, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(0x1111),
		Subject:               pkix.Name{Organization: []string{certOrganization}, CommonName: caCommonName},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tlsDir, caCertName), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeKeyPEM(filepath.Join(tlsDir, caKeyName), key); err != nil {
		t.Fatal(err)
	}
}

// writeTestLeaf replaces the served leaf with one signed by the same CA, carrying today's names and
// the given dates, so only its dates can make the server replace it.
func writeTestLeaf(t *testing.T, dataDir string, notBefore, notAfter time.Time) {
	t.Helper()
	tlsDir := filepath.Join(dataDir, "tls")
	ca, err := loadCA(filepath.Join(tlsDir, caCertName), filepath.Join(tlsDir, caKeyName))
	if err != nil {
		t.Fatalf("load the CA to sign with: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dnsNames, ips := collectLeafNames()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(testLeafSerial),
		Subject:               pkix.Name{Organization: []string{certOrganization}, CommonName: leafCommonName},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tlsDir, leafCertName), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeKeyPEM(filepath.Join(tlsDir, leafKeyName), key); err != nil {
		t.Fatal(err)
	}
}

// testLeafSerial marks a leaf a test wrote, so "was it replaced?" is a serial comparison.
const testLeafSerial = 4242

// A leaf costs nothing to replace and no device remembers it, so expiry is settled quietly: the CA
// behind it comes through untouched and nothing is logged.
func TestLoadWithCert_ReplacesAnExpiringLeaf(t *testing.T) {
	cases := []struct {
		name     string
		notAfter time.Time
		replaced bool
	}{
		{"expired", time.Now().Add(-time.Hour), true},
		{"inside_the_renewal_window", time.Now().Add(leafRenewalWindow / 3), true},
		{"beyond_the_renewal_window", time.Now().Add(leafRenewalWindow * 2), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			if _, err := LoadWithCert(dataDir, "", ""); err != nil {
				t.Fatal(err)
			}
			caBefore := mustParseCert(t, filepath.Join(dataDir, "tls", caCertName))

			writeTestLeaf(t, dataDir, time.Now().Add(-2*time.Hour), tc.notAfter)

			buf, restore := logging.TestSink()
			defer restore()
			cfg, err := LoadWithCert(dataDir, "", "")
			if err != nil {
				t.Fatalf("reload: %v", err)
			}

			leaf := mustParseCert(t, cfg.CertFile)
			replaced := leaf.SerialNumber.Int64() != testLeafSerial
			if replaced != tc.replaced {
				t.Errorf("a leaf ending %s: replaced=%v, want %v", tc.notAfter.Format(time.RFC3339), replaced, tc.replaced)
			}
			if err := leaf.VerifyHostname("localhost"); err != nil {
				t.Errorf("the served leaf no longer covers localhost: %v", err)
			}
			if caAfter := mustParseCert(t, filepath.Join(dataDir, "tls", caCertName)); caAfter.SerialNumber.Cmp(caBefore.SerialNumber) != 0 {
				t.Error("the CA was replaced while re-issuing its leaf")
			}
			if _, logged := rotationLine(buf.String()); logged {
				t.Errorf("replacing a leaf announced a root rotation: %s", buf.String())
			}
		})
	}
}

// Every road to a replaced root is announced, because no device can see it coming.
func TestEnsureCA_AnnouncesAReplacedRoot(t *testing.T) {
	newRoot := func(t *testing.T, dataDir string) *x509.Certificate {
		t.Helper()
		cfg, err := LoadWithCert(dataDir, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return mustParseCert(t, cfg.CAFile)
	}

	t.Run("unparseable_certificate", func(t *testing.T) {
		dataDir := t.TempDir()
		before := newRoot(t, dataDir)
		if err := os.WriteFile(filepath.Join(dataDir, "tls", caCertName), []byte("not a certificate"), 0o644); err != nil {
			t.Fatal(err)
		}
		checkReplacement(t, dataDir, before)
	})

	t.Run("certificate_without_its_key", func(t *testing.T) {
		dataDir := t.TempDir()
		before := newRoot(t, dataDir)
		if err := os.Remove(filepath.Join(dataDir, "tls", caKeyName)); err != nil {
			t.Fatal(err)
		}
		checkReplacement(t, dataDir, before)
	})

	t.Run("expired_root", func(t *testing.T) {
		dataDir := t.TempDir()
		tlsDir := filepath.Join(dataDir, "tls")
		if err := os.MkdirAll(tlsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		signTestCA(t, tlsDir, time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
		before := mustParseCert(t, filepath.Join(tlsDir, caCertName))
		checkReplacement(t, dataDir, before)
	})

	t.Run("certificate_that_is_not_a_CA", func(t *testing.T) {
		dataDir := t.TempDir()
		tlsDir := filepath.Join(dataDir, "tls")
		if err := os.MkdirAll(tlsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		// A server leaf and its own key: a matching pair that may not sign anything.
		other, err := LoadWithCert(t.TempDir(), "", "")
		if err != nil {
			t.Fatal(err)
		}
		for src, dst := range map[string]string{
			other.CertFile: filepath.Join(tlsDir, caCertName),
			other.KeyFile:  filepath.Join(tlsDir, caKeyName),
		} {
			content, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dst, content, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		before := mustParseCert(t, filepath.Join(tlsDir, caCertName))
		checkReplacement(t, dataDir, before)
	})

	t.Run("a_first_install_says_nothing_about_a_root", func(t *testing.T) {
		buf, restore := logging.TestSink()
		defer restore()
		if _, err := LoadWithCert(t.TempDir(), "", ""); err != nil {
			t.Fatal(err)
		}
		if _, logged := rotationLine(buf.String()); logged {
			t.Errorf("a fresh install announced replacing a root it never had: %s", buf.String())
		}
	})
}

// checkReplacement reloads dataDir expecting a new root, reported once at Error and naming the
// certificate to re-import.
func checkReplacement(t *testing.T, dataDir string, before *x509.Certificate) {
	t.Helper()
	buf, restore := logging.TestSink()
	defer restore()

	cfg, err := LoadWithCert(dataDir, "", "")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	after := mustParseCert(t, cfg.CAFile)
	if after.SerialNumber.Cmp(before.SerialNumber) == 0 {
		t.Fatal("the unusable root was kept")
	}
	if !after.IsCA {
		t.Error("the replacement is not a CA certificate")
	}
	if leaf := mustParseCert(t, cfg.CertFile); leaf.CheckSignatureFrom(after) != nil {
		t.Error("the served leaf is not signed by the replacement root")
	}
	line, logged := rotationLine(buf.String())
	if !logged {
		t.Fatalf("no line announced the replaced root; logged: %s", buf.String())
	}
	if !strings.Contains(line, "level=ERROR") {
		t.Errorf("a root replacement must reach the operator, logged at: %s", line)
	}
	if !strings.Contains(line, caCertName) || !strings.Contains(line, "import") {
		t.Errorf("the line must name the certificate to re-import: %s", line)
	}
}

// rotationLine finds the line that reports a replaced root and reports whether it is there.
func rotationLine(logged string) (string, bool) {
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, "replacing the local CA") {
			return line, true
		}
	}
	return "", false
}

// --- CA --------------------------------------------------------------------

func TestEnsureCA_CreatesUsableCA(t *testing.T) {
	tlsDir := filepath.Join(t.TempDir(), "tls")
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	ca, err := ensureCA(tlsDir)
	if err != nil {
		t.Fatalf("ensureCA: %v", err)
	}

	cert := mustParseCert(t, ca.certPath)
	if !cert.IsCA {
		t.Error("CA certificate is missing IsCA")
	}
	if !cert.BasicConstraintsValid {
		t.Error("CA certificate has BasicConstraintsValid=false")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("CA certificate is missing the CertSign key usage")
	}
	if cert.KeyUsage&x509.KeyUsageCRLSign == 0 {
		t.Error("CA certificate is missing the CRLSign key usage")
	}
	if cert.KeyUsage&x509.KeyUsageDigitalSignature != 0 {
		t.Error("CA certificate may sign things other than certificates")
	}
	// MaxPathLen 0 encodes nothing unless MaxPathLenZero is set too.
	if cert.MaxPathLen != 0 || !cert.MaxPathLenZero {
		t.Errorf("CA certificate is not pinned to pathlen:0 (MaxPathLen=%d, MaxPathLenZero=%v)", cert.MaxPathLen, cert.MaxPathLenZero)
	}
	if got := cert.Subject.Organization; len(got) != 1 || got[0] != "MIKE" {
		t.Errorf("expected subject organization [MIKE], got %v", got)
	}
	if cert.Subject.CommonName != "MIKE Local CA" {
		t.Errorf("expected CA common name %q, got %q", "MIKE Local CA", cert.Subject.CommonName)
	}

	// Self-signed root: issuer == subject.
	if cert.Issuer.String() != cert.Subject.String() {
		t.Errorf("expected self-signed CA, issuer %q != subject %q", cert.Issuer, cert.Subject)
	}

	if _, err := os.ReadFile(filepath.Join(tlsDir, caKeyName)); err != nil {
		t.Errorf("CA key not written: %v", err)
	}
}

func TestEnsureCA_ReusesExistingCA(t *testing.T) {
	tlsDir := t.TempDir()

	first, err := ensureCA(tlsDir)
	if err != nil {
		t.Fatalf("first ensureCA: %v", err)
	}
	second, err := ensureCA(tlsDir)
	if err != nil {
		t.Fatalf("second ensureCA: %v", err)
	}

	if first.cert.SerialNumber.Cmp(second.cert.SerialNumber) != 0 {
		t.Error("existing CA was replaced")
	}
	if !first.key.Equal(second.key) {
		t.Error("existing CA key was replaced")
	}
}

// A key must end up owner-only whether it is being created or replacing a file that was not.
func TestKeyFilesAreOwnerOnly(t *testing.T) {
	t.Run("created", func(t *testing.T) {
		dataDir := t.TempDir()
		cfg, err := LoadWithCert(dataDir, "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for _, path := range []string{cfg.KeyFile, filepath.Join(dataDir, "tls", caKeyName)} {
			if perm := mustPerm(t, path); perm != 0o600 {
				t.Errorf("%s: expected permissions 0600, got %o", path, perm)
			}
		}
	})

	t.Run("replacing_a_file_that_was_not_owner_only", func(t *testing.T) {
		dataDir := t.TempDir()
		if _, err := LoadWithCert(dataDir, "", ""); err != nil {
			t.Fatalf("first load: %v", err)
		}

		keyPath := filepath.Join(dataDir, "tls", caKeyName)
		if err := os.Chmod(keyPath, 0o644); err != nil {
			t.Fatal(err)
		}
		// Replacing the certificate is what makes the key get written again, over a file
		// this package did not create.
		if err := os.WriteFile(filepath.Join(dataDir, "tls", caCertName), []byte("not a certificate"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadWithCert(dataDir, "", ""); err != nil {
			t.Fatalf("load after corrupting the CA: %v", err)
		}

		if perm := mustPerm(t, keyPath); perm != 0o600 {
			t.Errorf("a regenerated CA key kept the mode of the file it replaced: %o", perm)
		}
	})

	t.Run("a_link_is_not_a_target", func(t *testing.T) {
		dataDir := t.TempDir()
		if _, err := LoadWithCert(dataDir, "", ""); err != nil {
			t.Fatalf("first load: %v", err)
		}

		victim := filepath.Join(t.TempDir(), "someone-elses-file")
		if err := os.WriteFile(victim, []byte("do not clobber"), 0o600); err != nil {
			t.Fatal(err)
		}
		keyPath := filepath.Join(dataDir, "tls", caKeyName)
		if err := os.Remove(keyPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, keyPath); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dataDir, "tls", caCertName), []byte("not a certificate"), 0o644); err != nil {
			t.Fatal(err)
		}

		if _, err := LoadWithCert(dataDir, "", ""); err == nil {
			t.Error("writing a key through a symlink must be refused")
		}
		content, err := os.ReadFile(victim)
		if err != nil || string(content) != "do not clobber" {
			t.Errorf("the link target was written to: %q (err %v)", content, err)
		}
	})
}

func mustPerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("failed to stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

// --- leaf ------------------------------------------------------------------

func TestLoadWithCert_LeafVerifiesAgainstCA(t *testing.T) {
	dataDir := t.TempDir()
	cfg, err := LoadWithCert(dataDir, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ca := mustParseCert(t, cfg.CAFile)
	leaf := mustParseCert(t, cfg.CertFile)

	if leaf.IsCA {
		t.Error("leaf certificate must not be a CA")
	}
	if leaf.Issuer.String() != ca.Subject.String() {
		t.Errorf("leaf issuer %q does not match CA subject %q", leaf.Issuer, ca.Subject)
	}
	if got := leaf.Subject.Organization; len(got) != 1 || got[0] != "MIKE" {
		t.Errorf("expected leaf organization [MIKE], got %v", got)
	}

	serverAuth := false
	for _, eku := range leaf.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			serverAuth = true
		}
	}
	if !serverAuth {
		t.Error("leaf is missing the ServerAuth extended key usage")
	}

	// The whole point of the CA: a device that imported mike-ca.crt trusts the leaf.
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		DNSName:   "localhost",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("leaf does not verify against the CA for localhost: %v", err)
	}
}

func TestGenerateLeaf(t *testing.T) {
	dir := t.TempDir()
	ca, err := ensureCA(dir)
	if err != nil {
		t.Fatalf("ensureCA: %v", err)
	}

	certPath := filepath.Join(dir, "mike.crt")
	keyPath := filepath.Join(dir, "mike.key")
	dnsNames := []string{"localhost", "myhost", "myhost.local"}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.1.20")}

	if err := generateLeaf(certPath, keyPath, ca, dnsNames, ips); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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

	if info, err := os.Stat(keyPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("expected key file permissions 0600, got %o", info.Mode().Perm())
	}

	leaf := mustParseCert(t, certPath)
	if !sameNames(leaf.DNSNames, dnsNames) {
		t.Errorf("expected DNS names %v, got %v", dnsNames, leaf.DNSNames)
	}
	if !sameIPs(leaf.IPAddresses, ips) {
		t.Errorf("expected IPs %v, got %v", ips, leaf.IPAddresses)
	}
	if err := leaf.CheckSignatureFrom(ca.cert); err != nil {
		t.Errorf("leaf is not signed by the CA: %v", err)
	}
	if leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Error("leaf is missing the digitalSignature key usage, which TLS needs from an ECDSA server certificate")
	}
	if leaf.KeyUsage&x509.KeyUsageKeyEncipherment != 0 {
		t.Error("leaf carries keyEncipherment, which only an RSA key transport uses")
	}
}

// The root ends up in device trust stores, so it must not be able to authorise anything but a leaf.
// Signing an intermediate is never refused; the constraint holds where the chain is checked.
func TestLocalCARejectsAChain(t *testing.T) {
	ca, err := ensureCA(t.TempDir())
	if err != nil {
		t.Fatalf("ensureCA: %v", err)
	}

	interKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	interDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber:          big.NewInt(0x1234),
		Subject:               pkix.Name{CommonName: "an intermediate"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}, ca.cert, &interKey.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("the root refused to sign an intermediate at all: %v", err)
	}
	intermediate, err := x509.ParseCertificate(interDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	forgedDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber:          big.NewInt(0x9999),
		Subject:               pkix.Name{CommonName: "a forged server"},
		DNSNames:              []string{"localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}, intermediate, &leafKey.PublicKey, interKey)
	if err != nil {
		t.Fatal(err)
	}
	forged, err := x509.ParseCertificate(forgedDER)
	if err != nil {
		t.Fatal(err)
	}

	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	intermediates := x509.NewCertPool()
	intermediates.AddCert(intermediate)

	if _, err := forged.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: "localhost"}); err == nil {
		t.Fatal("a certificate two links under the local CA chains to it: the root carries no pathLenConstraint")
	}

	// The control: one link under the same root still chains, so the rejection above is the constraint.
	dir := t.TempDir()
	if err := generateLeaf(filepath.Join(dir, "mike.crt"), filepath.Join(dir, "mike.key"), ca, []string{"localhost"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := mustParseCert(t, filepath.Join(dir, "mike.crt")).Verify(x509.VerifyOptions{Roots: roots, DNSName: "localhost"}); err != nil {
		t.Errorf("the leaf this package issues does not chain to its own root: %v", err)
	}
}

func TestLeafSANsMatch(t *testing.T) {
	dir := t.TempDir()
	ca, err := ensureCA(dir)
	if err != nil {
		t.Fatalf("ensureCA: %v", err)
	}

	certPath := filepath.Join(dir, "mike.crt")
	keyPath := filepath.Join(dir, "mike.key")
	dnsNames := []string{"localhost", "myhost", "myhost.local"}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.1.20")}
	if err := generateLeaf(certPath, keyPath, ca, dnsNames, ips); err != nil {
		t.Fatalf("generateLeaf: %v", err)
	}

	tests := []struct {
		name     string
		dnsNames []string
		ips      []net.IP
		want     bool
	}{
		{"identical", dnsNames, ips, true},
		{"same set other order", []string{"myhost.local", "localhost", "myhost"}, []net.IP{net.ParseIP("192.168.1.20"), net.IPv4(127, 0, 0, 1)}, true},
		{"missing name", []string{"localhost", "myhost"}, ips, false},
		{"extra name", append(append([]string{}, dnsNames...), "extra"), ips, false},
		{"lost IP", dnsNames, []net.IP{net.ParseIP("127.0.0.1")}, false},
		{"gained IP", dnsNames, append(append([]net.IP{}, ips...), net.ParseIP("10.0.0.5")), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := leafSANsMatch(certPath, tc.dnsNames, tc.ips); got != tc.want {
				t.Errorf("leafSANsMatch() = %v, want %v", got, tc.want)
			}
		})
	}

	// A missing or unparseable certificate never matches.
	if leafSANsMatch(filepath.Join(dir, "nope.crt"), dnsNames, ips) {
		t.Error("missing cert file should never match")
	}
	garbage := filepath.Join(dir, "garbage.crt")
	if err := os.WriteFile(garbage, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if leafSANsMatch(garbage, dnsNames, ips) {
		t.Error("unparseable cert should never match")
	}
}

func TestLeafIsValid(t *testing.T) {
	dir := t.TempDir()
	ca, err := ensureCA(dir)
	if err != nil {
		t.Fatalf("ensureCA: %v", err)
	}

	certPath := filepath.Join(dir, "mike.crt")
	keyPath := filepath.Join(dir, "mike.key")
	dnsNames := []string{"localhost", "myhost"}
	ips := []net.IP{net.ParseIP("127.0.0.1")}
	if err := generateLeaf(certPath, keyPath, ca, dnsNames, ips); err != nil {
		t.Fatalf("generateLeaf: %v", err)
	}

	if !leafIsValid(certPath, keyPath, ca, dnsNames, ips) {
		t.Fatal("a leaf issued by this CA with matching names must be valid")
	}
	if leafIsValid(certPath, keyPath, ca, []string{"localhost"}, ips) {
		t.Error("leaf with different names must not be considered valid")
	}

	// A leaf signed by some other CA is not valid, even with matching names.
	otherCA, err := ensureCA(t.TempDir())
	if err != nil {
		t.Fatalf("ensureCA(other): %v", err)
	}
	if leafIsValid(certPath, keyPath, otherCA, dnsNames, ips) {
		t.Error("leaf signed by another CA must not be considered valid")
	}

	// A key that does not belong to the certificate is not valid either.
	otherKeyPath := filepath.Join(dir, "other.key")
	if err := generateLeaf(filepath.Join(dir, "other.crt"), otherKeyPath, ca, dnsNames, ips); err != nil {
		t.Fatalf("generateLeaf(other): %v", err)
	}
	if leafIsValid(certPath, otherKeyPath, ca, dnsNames, ips) {
		t.Error("mismatched cert/key pair must not be considered valid")
	}

	if leafIsValid(filepath.Join(dir, "missing.crt"), keyPath, ca, dnsNames, ips) {
		t.Error("missing files must not be considered valid")
	}
}

// --- names -----------------------------------------------------------------

func TestCollectLeafNames(t *testing.T) {
	dnsNames, ips := collectLeafNames()

	hostname, err := os.Hostname()
	if err != nil {
		t.Skipf("hostname unavailable: %v", err)
	}

	for _, want := range []string{"localhost", hostname, hostname + ".local"} {
		if !hasString(dnsNames, want) {
			t.Errorf("expected DNS name %q in %v", want, dnsNames)
		}
	}
	for _, want := range []string{"127.0.0.1", "::1"} {
		if !hasIP(ips, want) {
			t.Errorf("expected IP %q in %v", want, ips)
		}
	}

	if !sort.StringsAreSorted(dnsNames) {
		t.Errorf("DNS names are not sorted: %v", dnsNames)
	}
	seen := map[string]bool{}
	for _, n := range dnsNames {
		if seen[n] {
			t.Errorf("duplicate DNS name %q in %v", n, dnsNames)
		}
		seen[n] = true
	}
	seenIPs := map[string]bool{}
	for _, ip := range ips {
		if seenIPs[ip.String()] {
			t.Errorf("duplicate IP %q in %v", ip, ips)
		}
		seenIPs[ip.String()] = true
	}
}

func TestCollectLeafNames_IsDeterministic(t *testing.T) {
	dnsA, ipsA := collectLeafNames()
	dnsB, ipsB := collectLeafNames()

	if !sameNames(dnsA, dnsB) {
		t.Errorf("DNS names differ between calls: %v vs %v", dnsA, dnsB)
	}
	if !sameIPs(ipsA, ipsB) {
		t.Errorf("IPs differ between calls: %v vs %v", ipsA, ipsB)
	}
}

func TestCollectLeafNames_ExcludesNonUnicastAddresses(t *testing.T) {
	// Only real host identities belong in the certificate: never the unspecified
	// or multicast addresses that InterfaceAddrs can report.
	_, ips := collectLeafNames()
	for _, ip := range ips {
		if ip.IsUnspecified() {
			t.Errorf("unspecified address %q must not be a certificate SAN", ip)
		}
		if ip.IsMulticast() {
			t.Errorf("multicast address %q must not be a certificate SAN", ip)
		}
	}
}

// --- comparators -----------------------------------------------------------

func TestSameIPs_IgnoresIPv4Representation(t *testing.T) {
	// net.IP may hold an IPv4 address in 4- or 16-byte form; both are the same name.
	a := []net.IP{net.IPv4(127, 0, 0, 1)}
	b := []net.IP{net.ParseIP("127.0.0.1").To4()}
	if !sameIPs(a, b) {
		t.Errorf("expected %v and %v to be considered equal", a, b)
	}
}

// --- CertPEM ---------------------------------------------------------------

func TestCertPEM_ReturnsCertificate(t *testing.T) {
	cfg, err := LoadWithCert(t.TempDir(), "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pemBytes, err := CertPEM(cfg.CAFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("expected a CERTIFICATE PEM block, got %+v", block)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("served bytes are not a parseable certificate: %v", err)
	}
	if !cert.IsCA {
		t.Error("expected the CA certificate itself, got a non-CA certificate")
	}
}

// The reason CertPEM re-encodes instead of returning the file: a caller writes
// the result to an HTTP response, so key material must never come back out.
func TestCertPEM_ServesOnlyTheCertificate(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadWithCert(dir, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	keyPEM, err := os.ReadFile(filepath.Join(dir, "tls", caKeyName))
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		t.Fatal(err)
	}

	bundlePath := filepath.Join(dir, "bundle.pem")
	if err := os.WriteFile(bundlePath, append(append([]byte{}, certPEM...), keyPEM...), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := CertPEM(bundlePath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bytes.Contains(out, []byte("PRIVATE KEY")) {
		t.Error("private key material leaked out of CertPEM")
	}
	if !bytes.Equal(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: mustParseCert(t, cfg.CAFile).Raw})) {
		t.Error("expected exactly the certificate block, nothing else")
	}
}

func TestCertPEM_RefusesNonCertificate(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadWithCert(dir, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, path := range []string{
		filepath.Join(dir, "tls", caKeyName),   // a private key
		filepath.Join(dir, "tls", leafKeyName), // a private key
		filepath.Join(dir, "missing.crt"),      // absent file
	} {
		if out, err := CertPEM(path); err == nil {
			t.Errorf("expected an error for %s, got %d bytes", path, len(out))
		}
	}

	junkPath := filepath.Join(dir, "junk.crt")
	if err := os.WriteFile(junkPath, []byte("not a pem file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CertPEM(junkPath); err == nil {
		t.Error("expected an error for a file with no CERTIFICATE block")
	}

	// The real CA file must succeed, so the loop above cannot pass for the wrong reason.
	if _, err := CertPEM(cfg.CAFile); err != nil {
		t.Errorf("the CA file must be readable: %v", err)
	}
}
