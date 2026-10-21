// Package tls provisions HTTPS material via a self-signed local CA and a
// server certificate covering localhost, the hostname, and host addresses.
// The CA key never leaves the data dir, and the CA is replaced only when
// it cannot be used.
package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	cryptotls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
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
	EnvTLSCert = "MIKE_TLS_CERT"
	EnvTLSKey  = "MIKE_TLS_KEY"
)

const (
	certOrganization = "MIKE"
	caCommonName     = "MIKE Local CA"
	leafCommonName   = "localhost"

	caValidFor   = 10 * 365 * 24 * time.Hour // the CA is imported once; make it last
	leafValidFor = 365 * 24 * time.Hour      // the leaf is cheap to replace

	// leafRenewalWindow determines how early a leaf is replaced on restart
	// to prevent expiration.
	leafRenewalWindow = 30 * 24 * time.Hour

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

// LoadWithCert loads provided certificates or generates a self-signed
// leaf from a local CA. Leaves are re-issued if names change or they near
// expiration. Returns a *Config (never nil).
func LoadWithCert(dataDir, certFile, keyFile string) (*Config, error) {
	if certFile != "" && keyFile != "" {
		// User-provided cert and key files
		if _, err := os.Stat(certFile); err != nil {
			return nil, fmt.Errorf("tls: cert file not found: %s: %w", certFile, err)
		}
		if _, err := os.Stat(keyFile); err != nil {
			return nil, fmt.Errorf("tls: key file not found: %s: %w", keyFile, err)
		}
		if _, err := cryptotls.LoadX509KeyPair(certFile, keyFile); err != nil {
			return nil, fmt.Errorf("tls: --tls-cert/--tls-key (or %s/%s) do not form a usable pair (%s, %s): %w", EnvTLSCert, EnvTLSKey, certFile, keyFile, err)
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
// Re-encoding ensures only the certificate is returned and validates the
// file content.
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

// ensureCA loads the local CA from tlsDir, creating and persisting it on first use.
// Usable CAs are never replaced to maintain trust on client devices; unusable ones are.
func ensureCA(tlsDir string) (*certificateAuthority, error) {
	certPath := filepath.Join(tlsDir, caCertName)
	keyPath := filepath.Join(tlsDir, caKeyName)

	if ca, err := loadCA(certPath, keyPath); err == nil {
		return ca, nil
	} else if leftBehind(certPath, keyPath) {
		logging.For(logging.TLS).Errorf("TLS: replacing the local CA (%s, %s): %v — devices that imported %s must import the new one",
			certPath, keyPath, err, certPath)
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
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		// MaxPathLenZero is what encodes the limit: MaxPathLen alone leaves the field out of the
		// certificate, and a missing pathLenConstraint means unlimited.
		MaxPathLen:     0,
		MaxPathLenZero: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create CA certificate: %w", err)
	}

	// Key first: the certificate is the file someone could import, so it is written last.
	if err := writeKeyPEM(keyPath, caKey); err != nil {
		return nil, err
	}
	if err := writeCertPEM(certPath, certDER); err != nil {
		return nil, err
	}

	caCert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("failed to parse generated CA certificate: %w", err)
	}

	return &certificateAuthority{cert: caCert, key: caKey, certPath: certPath}, nil
}

// leftBehind reports whether either path exists, used to detect if a
// first install is replacing an existing root.
func leftBehind(paths ...string) bool {
	for _, path := range paths {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}

// loadCA reads and validates a persisted CA certificate/key pair.
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
	if !caCert.IsCA || caCert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.New("not a certificate-issuing CA")
	}
	if now := time.Now(); now.Before(caCert.NotBefore) || now.After(caCert.NotAfter) {
		return nil, fmt.Errorf("CA certificate is outside its validity %s..%s at %s",
			caCert.NotBefore.UTC().Format(time.RFC3339), caCert.NotAfter.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
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

		// Use digitalSignature for P-256; keyEncipherment is for RSA.
		KeyUsage:              x509.KeyUsageDigitalSignature,
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

// leafIsValid returns true if the cert is valid, signed by ca, and
// not nearing expiration.
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
	if now := time.Now(); now.Before(leaf.NotBefore) || now.Add(leafRenewalWindow).After(leaf.NotAfter) {
		return false
	}
	return sanNamesMatch(leaf, dnsNames, ips)
}

// sanNamesMatch checks if certificate SANs exactly match the current
// host configuration.
func sanNamesMatch(leaf *x509.Certificate, dnsNames []string, ips []net.IP) bool {
	return sameNames(leaf.DNSNames, dnsNames) && sameIPs(leaf.IPAddresses, ips)
}

// collectLeafNames returns names for the server certificate: localhost,
// hostname, and all non-loopback addresses. Both slices are de-duplicated
// and sorted for stability.
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

// writePrivateFile ensures owner-only permissions regardless of file existence.
// A target that is not a regular file is refused.
func writePrivateFile(path string, content []byte) error {
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("failed to write key %s: not a regular file (mode %s)", path, fi.Mode())
		} else if fi.Mode().Perm()&0o077 != 0 {
			logging.For(logging.TLS).Warnf("TLS: tightening permissions on %s from %o to 600", path, fi.Mode().Perm())
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to write key %s: %w", path, err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("failed to write key %s: %w", path, err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("failed to set permissions on key %s: %w", path, err)
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return fmt.Errorf("failed to write key %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("failed to write key %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to write key %s: %w", path, err)
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
	return writePrivateFile(path, block)
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

// sameSet returns true if two slices contain the same distinct elements,
// ignoring order and duplicates.
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
