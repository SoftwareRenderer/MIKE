package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Config holds TLS certificate paths for the server.
type Config struct {
	CertFile string
	KeyFile  string
}

// Load reads TLS configuration from environment variables.
//
// TLS is enabled by default with auto-generated self-signed certificates.
//
// Priority:
//  1. KANBAN_TLS_CERT + KANBAN_TLS_KEY — use user-provided cert/key files
//  2. Default — auto-generate a self-signed cert in dataDir/tls/
//  3. KANBAN_TLS=0 — explicitly disable TLS (plain HTTP)
//
// Returns nil only when TLS is explicitly disabled (KANBAN_TLS=0).
func Load(dataDir string) (*Config, error) {
	// Explicitly disable TLS
	if os.Getenv("KANBAN_TLS") == "0" {
		return nil, nil
	}

	certFile := os.Getenv("KANBAN_TLS_CERT")
	keyFile := os.Getenv("KANBAN_TLS_KEY")

	if certFile != "" && keyFile != "" {
		// User-provided cert and key files
		if _, err := os.Stat(certFile); err != nil {
			return nil, fmt.Errorf("tls: cert file not found: %s: %w", certFile, err)
		}
		if _, err := os.Stat(keyFile); err != nil {
			return nil, fmt.Errorf("tls: key file not found: %s: %w", keyFile, err)
		}
		log.Printf("TLS enabled using provided certificates (%s, %s)", certFile, keyFile)
		return &Config{CertFile: certFile, KeyFile: keyFile}, nil
	}

	// Default: auto-generate self-signed certificate
	tlsDir := filepath.Join(dataDir, "tls")
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		return nil, fmt.Errorf("tls: failed to create tls directory: %w", err)
	}

	certPath := filepath.Join(tlsDir, "kanban.crt")
	keyPath := filepath.Join(tlsDir, "kanban.key")

	// Only generate if files don't exist
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		if err := generateSelfSignedCert(certPath, keyPath); err != nil {
			return nil, fmt.Errorf("tls: failed to generate self-signed cert: %w", err)
		}
		log.Printf("TLS: generated self-signed certificate (%s)", certPath)
	} else {
		log.Printf("TLS: using existing self-signed certificate (%s)", certPath)
	}

	return &Config{CertFile: certPath, KeyFile: keyPath}, nil
}

// generateSelfSignedCert creates a self-signed ECDSA certificate valid for localhost.
func generateSelfSignedCert(certPath, keyPath string) error {
	// Generate ECDSA private key
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	// Certificate template
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("failed to generate serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"kanban"},
			CommonName:   "localhost",
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour), // valid for 1 year

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,

		DNSNames:  []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	// Self-sign the certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	// Write certificate file
	certOut, err := os.Create(certPath)
	if err != nil {
		return fmt.Errorf("failed to create cert file: %w", err)
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return fmt.Errorf("failed to write cert file: %w", err)
	}

	// Write key file
	keyOut, err := os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create key file: %w", err)
	}
	defer keyOut.Close()

	keyBytes, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		return fmt.Errorf("failed to write key file: %w", err)
	}

	return nil
}
