// Package tls provisions HTTPS material: a self-signed local CA created once in the data
// directory, and a server certificate it signs whose SANs cover localhost, the hostname and
// the host's addresses. The CA key never leaves the data dir and is never rotated.
package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	cryptotls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"mike/pkg/logging"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// Config holds TLS certificate paths for the server.
type Config struct {
	CertFile string
	KeyFile  string
	CAFile   string
}

const (
	certOrganization = "MIKE"
	caCommonName     = "MIKE Local CA"
	leafCommonName   = "localhost"

	caValidFor   = 10 * 365 * 24 * time.Hour // the CA is imported once; make it last
	leafValidFor = 365 * 24 * time.Hour      // the leaf is cheap to replace

	caCertName   = "mike-ca.crt"
	caKeyName    = "mike-ca.key"
	leafCertName = "mike.crt"
	leafKeyName  = "mike.key"
)

// certificateAuthority is the local CA used to sign the server certificate.
type certificateAuthority struct {
	cert     *x509.Certificate
	key      *ecdsa.PrivateKey
	certPath string
}

// LoadWithCert creates a TLS config from explicit certificate paths, or, when either is empty,
// from a local CA (dataDir/tls/mike-ca.crt) that signs the server leaf. The leaf's SANs cover
// localhost, the hostname and the LAN addresses, and it is re-issued only when those names
// change; the CA is reused across restarts. Returns a *Config (never nil).
func LoadWithCert(dataDir, certFile, keyFile string) (*Config, error) {
	if certFile != "" && keyFile != "" {
		// User-provided cert and key files
		if _, err := os.Stat(certFile); err != nil {
			return nil, fmt.Errorf("tls: cert file not found: %s: %w", certFile, err)
		}
		if _, err := os.Stat(keyFile); err != nil {
			return nil, fmt.Errorf("tls: key file not found: %s: %w", keyFile, err)
		}
		logging.For(logging.TLS).Infof("TLS enabled using provided certificates (%s, %s)", certFile, keyFile)
		return &Config{CertFile: certFile, KeyFile: keyFile}, nil
	}

	// Default: local CA + CA-signed certificate
	tlsDir := filepath.Join(dataDir, "tls")
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		return nil, fmt.Errorf("tls: failed to create tls directory: %w", err)
	}

	ca, err := ensureCA(tlsDir)
	if err != nil {
		return nil, fmt.Errorf("tls: failed to prepare local CA: %w", err)
	}

	certPath := filepath.Join(tlsDir, leafCertName)
	keyPath := filepath.Join(tlsDir, leafKeyName)

	dnsNames, ips := collectLeafNames()

	if leafIsValid(certPath, keyPath, ca, dnsNames, ips) {
		logging.For(logging.TLS).Infof("TLS: using existing certificate (%s)", certPath)
	} else {
		if err := generateLeaf(certPath, keyPath, ca, dnsNames, ips); err != nil {
			return nil, fmt.Errorf("tls: failed to generate server certificate: %w", err)
		}
		logging.For(logging.TLS).Infof("TLS: generated certificate signed by %s (%s); import %s into a device's trust store to trust it", caCommonName, certPath, ca.certPath)
	}

	return &Config{CertFile: certPath, KeyFile: keyPath, CAFile: ca.certPath}, nil
}

// CertPEM returns the PEM encoding of a file's first CERTIFICATE block.
// Re-encoding rather than returning the file is the point: only the certificate
// goes out, and a file that is not a certificate is an error, never key material.
func CertPEM(path string) ([]byte, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("tls: failed to read certificate %s: %w", path, err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("tls: no CERTIFICATE PEM block in %s", path)
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return nil, fmt.Errorf("tls: invalid certificate in %s: %w", path, err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes}), nil
}

// ensureCA loads the local CA from tlsDir, creating and persisting it on first
// use. An existing, usable CA is never replaced: devices that already imported
// it would stop trusting the server otherwise.
func ensureCA(tlsDir string) (*certificateAuthority, error) {
	certPath := filepath.Join(tlsDir, caCertName)
	keyPath := filepath.Join(tlsDir, caKeyName)

	if ca, err := loadCA(certPath, keyPath); err == nil {
		return ca, nil
	} else if !os.IsNotExist(err) {
		// Files exist but are unusable (corrupt, rotated key, …) — regenerate.
		logging.For(logging.TLS).Infof("TLS: regenerating local CA (%v)", err)
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate CA key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{certOrganization},
			CommonName:   caCommonName,
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(caValidFor),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create CA certificate: %w", err)
	}

	if err := writeCertPEM(certPath, certDER); err != nil {
		return nil, err
	}
	if err := writeKeyPEM(keyPath, caKey); err != nil {
		return nil, err
	}

	caCert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("failed to parse generated CA certificate: %w", err)
	}

	return &certificateAuthority{cert: caCert, key: caKey, certPath: certPath}, nil
}

// loadCA reads and validates a persisted CA certificate/key pair.
// Returns an os.IsNotExist-compatible error when the files are absent.
func loadCA(certPath, keyPath string) (*certificateAuthority, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}

	caCert, err := parseCertificatePEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to parse CA certificate: %w", err)
	}
	caKey, err := parseKeyPEM(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to parse CA key: %w", err)
	}
	pub, ok := caCert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&caKey.PublicKey) {
		return nil, fmt.Errorf("CA key does not match CA certificate")
	}

	return &certificateAuthority{cert: caCert, key: caKey, certPath: certPath}, nil
}

// generateLeaf creates a server certificate for the given names, signed by ca.
func generateLeaf(certPath, keyPath string, ca *certificateAuthority, dnsNames []string, ips []net.IP) error {
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return err
	}

	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{certOrganization},
			CommonName:   leafCommonName,
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(leafValidFor),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,

		DNSNames:    dnsNames,
		IPAddresses: ips,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, ca.cert, &leafKey.PublicKey, ca.key)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	if err := writeCertPEM(certPath, certDER); err != nil {
		return err
	}
	return writeKeyPEM(keyPath, leafKey)
}

// leafIsValid reports whether the certificate/key pair at certPath/keyPath is
// still the right one to serve: it must parse, its key must match, it must be
// signed by ca, and its alternative names must match the ones we would issue
// today. Expiry is deliberately not part of this check — a leaf is only
// replaced when the host's names change, never silently on rotation.
func leafIsValid(certPath, keyPath string, ca *certificateAuthority, dnsNames []string, ips []net.IP) bool {
	pair, err := cryptotls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return false
	}
	leaf := pair.Leaf
	if leaf == nil {
		return false
	}
	if err := leaf.CheckSignatureFrom(ca.cert); err != nil {
		return false
	}
	return sanNamesMatch(leaf, dnsNames, ips)
}

// sanNamesMatch reports whether a leaf certificate carries exactly the given
// subject alternative names (order-independent, per kind). The leaf is compared
// against the names collectLeafNames would issue today, so any host rename or
// interface change reads as a mismatch and triggers a re-issue.
func sanNamesMatch(leaf *x509.Certificate, dnsNames []string, ips []net.IP) bool {
	return sameNames(leaf.DNSNames, dnsNames) && sameIPs(leaf.IPAddresses, ips)
}

// collectLeafNames returns the names the server certificate should be valid for:
// localhost, the machine hostname (plus its .local mDNS alias) and every
// non-loopback address of the host. Both slices are de-duplicated and sorted so
// that an unchanged host yields an identical set.
func collectLeafNames() (dnsNames []string, ips []net.IP) {
	nameSet := map[string]bool{"localhost": true}
	ipSet := map[string]net.IP{}

	addIP := func(ip net.IP) {
		if ip == nil {
			return
		}
		ipSet[ip.String()] = ip
	}

	addIP(net.IPv4(127, 0, 0, 1))
	addIP(net.ParseIP("::1"))

	if hostname, err := os.Hostname(); err == nil {
		if h := strings.TrimSuffix(hostname, "."); h != "" {
			nameSet[h] = true
			if !strings.HasSuffix(h, ".local") {
				nameSet[h+".local"] = true
			}
		}
	}

	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP == nil {
				continue
			}
			ip := ipnet.IP
			if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
				continue
			}
			addIP(ip)
		}
	}

	for name := range nameSet {
		dnsNames = append(dnsNames, name)
	}
	sort.Strings(dnsNames)

	for _, ip := range ipSet {
		ips = append(ips, ip)
	}
	slices.SortFunc(ips, func(a, b net.IP) int { return strings.Compare(a.String(), b.String()) })

	return dnsNames, ips
}

// randomSerial returns a random 128-bit certificate serial number.
func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("failed to generate serial number: %w", err)
	}
	return serial, nil
}

// writeCertPEM writes a DER certificate as PEM to path (0644).
func writeCertPEM(path string, der []byte) error {
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o644); err != nil {
		return fmt.Errorf("failed to write certificate %s: %w", path, err)
	}
	return nil
}

// writeKeyPEM writes an ECDSA key as PEM to path with owner-only permissions.
func writeKeyPEM(path string, key *ecdsa.PrivateKey) error {
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	if err := os.WriteFile(path, block, 0o600); err != nil {
		return fmt.Errorf("failed to write key %s: %w", path, err)
	}
	return nil
}

// parseCertificatePEM decodes the first CERTIFICATE block in pemBytes.
func parseCertificatePEM(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("no CERTIFICATE PEM block found")
	}
	return x509.ParseCertificate(block.Bytes)
}

// parseKeyPEM decodes a PEM-encoded ECDSA private key.
func parseKeyPEM(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	if block.Type == "PRIVATE KEY" {
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		ecKey, ok := parsed.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS8 key is not an ECDSA key")
		}
		return ecKey, nil
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

// sameSet reports whether a and b hold the same distinct elements, ignoring
// order and duplicates. key maps an element to its comparable identity, so each
// caller decides what "the same name" means (net.IP needs it to ignore the
// 4- vs 16-byte representation of IPv4).
func sameSet[T any](a, b []T, key func(T) string) bool {
	setA := make(map[string]bool, len(a))
	for _, v := range a {
		setA[key(v)] = true
	}
	setB := make(map[string]bool, len(b))
	for _, v := range b {
		setB[key(v)] = true
	}
	if len(setA) != len(setB) {
		return false
	}
	for v := range setA {
		if !setB[v] {
			return false
		}
	}
	return true
}

// sameNames compares two DNS name sets, ignoring order and duplicates.
func sameNames(a, b []string) bool {
	return sameSet(a, b, func(s string) string { return s })
}

// sameIPs compares two IP sets, ignoring order, duplicates and the 4- vs
// 16-byte representation of IPv4 addresses.
func sameIPs(a, b []net.IP) bool {
	return sameSet(a, b, net.IP.String)
}
