package server

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"kanban/backend/tls"
)

// defaultCertServer wires a test server the way main.go wires a real one without
// --tls-cert: a real CA and leaf in a data dir, plus the flag derived from them.
func defaultCertServer(t *testing.T) (*Server, *tls.Config) {
	t.Helper()
	s := testServer(t)
	cfg, err := tls.LoadWithCert(t.TempDir(), "", "")
	if err != nil {
		t.Fatalf("generate default certificates: %v", err)
	}
	s.tlsConfig = cfg
	s.configStore.SetTLSCAAvailable(cfg.CAFile != "")
	return s, cfg
}

func getTLSCA(t *testing.T, s *Server, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	req := testRequest(http.MethodGet, "/api/tls/ca", nil)
	if cookie != "" {
		withAuth(req, cookie)
	}
	return testResponse(t, s, req)
}

func TestTLSCADownloadServesCertificate(t *testing.T) {
	s, cfg := defaultCertServer(t)
	cookie := loginTest(t, s)

	rr := getTLSCA(t, s, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	if ct := rr.Header().Get("Content-Type"); ct != "application/x-x509-ca-cert" {
		t.Errorf("unexpected Content-Type %q", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); cd != `attachment; filename="mike-ca.crt"` {
		t.Errorf("unexpected Content-Disposition %q", cd)
	}

	body := rr.Body.Bytes()
	block, _ := pem.Decode(body)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("body is not a CERTIFICATE PEM block: %q", body)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("served body is not a parseable certificate: %v", err)
	}
	if !cert.IsCA {
		t.Error("served certificate is not a CA certificate")
	}

	// Must be the CA that signed the leaf, not something else from the data dir.
	onDisk, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	onDiskBlock, _ := pem.Decode(onDisk)
	if !bytes.Equal(block.Bytes, onDiskBlock.Bytes) {
		t.Error("served certificate is not the CA that signed the server certificate")
	}
	if bytes.Contains(body, []byte("PRIVATE KEY")) {
		t.Error("private key material leaked through the download route")
	}
}

// The CA is a public certificate, but the route is a link inside the signed-in
// UI, not a public endpoint.
func TestTLSCARequiresSession(t *testing.T) {
	s, _ := defaultCertServer(t)

	if rr := getTLSCA(t, s, ""); rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without a session, got %d: %s", rr.Code, rr.Body.String())
	}
	if rr := getTLSCA(t, s, "PHPSESSID=not-a-session"); rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with a bad session, got %d: %s", rr.Code, rr.Body.String())
	}
}

// --no-tls and a user-provided certificate both have no CA of ours.
func TestTLSCAUnavailableWithoutLocalCA(t *testing.T) {
	s := testServer(t)
	cookie := loginTest(t, s)

	cases := map[string]*tls.Config{
		"tls disabled":       nil,
		"custom certificate": {CertFile: "user.crt", KeyFile: "user.key"},
	}
	for name, cfg := range cases {
		s.tlsConfig = cfg
		if rr := getTLSCA(t, s, cookie); rr.Code != http.StatusNotFound {
			t.Errorf("%s: expected 404, got %d: %s", name, rr.Code, rr.Body.String())
		}
		s.configStore.SetTLSCAAvailable(cfg != nil && cfg.CAFile != "")
		if raw := getConfigBody(t, s, cookie); raw["tls_ca_available"] == true {
			t.Errorf("%s: /api/config still advertises a CA download", name)
		}
	}
}

func TestTLSCAOnlyServesGET(t *testing.T) {
	s, _ := defaultCertServer(t)
	cookie, csrf := loginTestCSRF(t, s)

	req := testRequest(http.MethodPost, "/api/tls/ca", nil)
	withAuthCSRF(req, cookie, csrf)
	if rr := testResponse(t, s, req); rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for POST, got %d: %s", rr.Code, rr.Body.String())
	}
}

// The flag drives the link, so it must follow the same condition as the 404.
func TestConfigAdvertisesTLSCA(t *testing.T) {
	s, _ := defaultCertServer(t)
	cookie := loginTest(t, s)

	if raw := getConfigBody(t, s, cookie); raw["tls_ca_available"] != true {
		t.Errorf("expected tls_ca_available true with the default certificate, got %v", raw["tls_ca_available"])
	}

	s.configStore.SetTLSCAAvailable(false)
	if raw := getConfigBody(t, s, cookie); raw["tls_ca_available"] != false {
		t.Errorf("expected tls_ca_available false after the flag is cleared, got %v", raw["tls_ca_available"])
	}
}

// getConfigBody reads /api/config as a map, to assert on one field.
func getConfigBody(t *testing.T, s *Server, cookie string) map[string]any {
	t.Helper()
	req := testRequest(http.MethodGet, "/api/config", nil)
	withAuth(req, cookie)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get config: %d %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return out
}
