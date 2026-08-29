package tls

import (
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"sort"
	"testing"
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
	// A user-supplied certificate has no CA of ours attached.
	if cfg.CAFile != "" {
		t.Errorf("expected empty CAFile for user-provided cert, got %s", cfg.CAFile)
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

func TestLoadWithCert_RegeneratesUnusableCA(t *testing.T) {
	dataDir := t.TempDir()

	if _, err := LoadWithCert(dataDir, "", ""); err != nil {
		t.Fatalf("first load: %v", err)
	}

	caPath := filepath.Join(dataDir, "tls", caCertName)
	if err := os.WriteFile(caPath, []byte("not a certificate"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadWithCert(dataDir, "", "")
	if err != nil {
		t.Fatalf("load after corrupting the CA: %v", err)
	}

	ca := mustParseCert(t, caPath)
	if !ca.IsCA {
		t.Error("recovered CA is not a CA certificate")
	}
	leaf := mustParseCert(t, cfg.CertFile)
	if err := leaf.CheckSignatureFrom(ca); err != nil {
		t.Errorf("served leaf is not signed by the recovered CA: %v", err)
	}
}

func TestLoadDisabled(t *testing.T) {
	cfg := LoadDisabled()
	if cfg != nil {
		t.Fatalf("expected nil config, got %+v", cfg)
	}
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

func TestKeyFilesAreOwnerOnly(t *testing.T) {
	dataDir := t.TempDir()
	cfg, err := LoadWithCert(dataDir, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	keys := []string{cfg.KeyFile, filepath.Join(dataDir, "tls", caKeyName)}
	for _, path := range keys {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("failed to stat %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s: expected permissions 0600, got %o", path, info.Mode().Perm())
		}
	}
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
